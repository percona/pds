package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/client/cursor"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
)

// chatRequest is one new message. The thread it continues is read back from
// the store, so the request carries no earlier turns.
type chatRequest struct {
	ConversationID string `json:"conversationId" validate:"omitempty,max=64"`
	DatabaseID     string `json:"databaseId" validate:"omitempty,max=253"`
	RequestKey     string `json:"requestKey" validate:"omitempty,max=64,alphanum"`
	Provider       string `json:"provider" validate:"required,oneof=anthropic cursor"`
	Model          string `json:"model" validate:"required,max=200"`
	Message        string `json:"message" validate:"required,max=8000"`
}

type chatResponse struct {
	ConversationID string               `json:"conversationId"`
	Text           string               `json:"text"`
	Actions        []service.ChatAction `json:"actions"`
}

// chatTextEvent is a piece of the answer's text. The pieces, in order, add up
// to the reply's text.
type chatTextEvent struct {
	Delta string `json:"delta"`
}

// chatApprovalRequest is the person's answer to a change the Chat asked about.
type chatApprovalRequest struct {
	Decision string `json:"decision" validate:"required,oneof=approve reject"`
	// Name is the database's name, typed. Only approving a delete reads it.
	Name string `json:"name" validate:"omitempty,max=253"`
}

type chatStartedEvent struct {
	ConversationID string `json:"conversationId"`
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	if s.chat == nil {
		Respond(w, r, nil, chatError(service.ErrAssistantUnavailable))

		return
	}

	chatReq := service.ChatRequest{
		ConversationID: req.ConversationID,
		DatabaseID:     req.DatabaseID,
		RequestKey:     req.RequestKey,
		Provider:       req.Provider,
		Model:          req.Model,
		Message:        req.Message,
	}

	if wantsEventStream(r) {
		s.streamChat(w, r, chatReq)

		return
	}

	reply, err := s.chat.Reply(r.Context(), chatReq)
	if err != nil {
		Respond(w, r, nil, chatError(err))

		return
	}

	Respond(w, r, toChatResponse(reply), nil)
}

func toChatResponse(reply *service.ChatReply) chatResponse {
	actions := reply.Actions
	if actions == nil {
		actions = []service.ChatAction{}
	}

	return chatResponse{ConversationID: reply.ConversationID, Text: reply.Text, Actions: actions}
}

// handleChatApproval answers a change the Chat is waiting on. The approval
// lives in the stream that asked, so one that is gone, answered or someone
// else's is a 404.
func (s *Server) handleChatApproval(w http.ResponseWriter, r *http.Request) {
	var req chatApprovalRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}
	if s.chat == nil {
		Respond(w, r, nil, chatError(service.ErrAssistantUnavailable))

		return
	}

	err := s.chat.Decide(r.Context(), chi.URLParam(r, "id"), service.ChatDecision{Approve: req.Decision == "approve", Name: req.Name})
	if err != nil {
		Respond(w, r, nil, chatError(err))

		return
	}

	Respond(w, r, nil, nil)
}

const defaultChatHeartbeat = 10 * time.Second

func wantsEventStream(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

// streamChat answers a turn as server-sent events: a progress event for each
// step, a text event for each piece of the answer as the model writes it, a
// comment on every heartbeat while the model works or a change waits for the
// person, an approval event for each change put to them with an approval_end
// event when it is settled, and one reply or error event at the end. The reply carries the whole text and the buttons, and
// replaces the text events: a refused answer is shown as a sentence instead of
// what was written before it. A conversation event names the conversation as soon
// as the message is saved, so a turn that fails still leaves the browser on
// the conversation that holds it. A proxy in front of the adapter gives up on a
// connection that carries no bytes, so the stream never goes quiet for long.
//
// The stream opens on the first event, which is the conversation event once
// the message is saved. A turn that fails before then, such as a provider with
// no key or a model that does not match the conversation, answers a plain JSON
// error with its status; one that fails after it answers an error event.
func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, req service.ChatRequest) {
	ctx := r.Context()
	stream := &chatStream{ctx: ctx, w: w, rc: http.NewResponseController(w)}

	heartbeat := s.chatHeartbeat
	if heartbeat <= 0 {
		heartbeat = defaultChatHeartbeat
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				stream.comment("ping")
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
	}()

	req.Progress = func(progress service.ChatProgress) {
		stream.event("progress", progress)
	}
	req.Text = func(delta string) {
		stream.event("text", chatTextEvent{Delta: delta})
	}
	req.Approval = func(approval service.ChatApproval) {
		stream.event("approval", approval)
	}
	req.Settled = func(end service.ChatApprovalEnd) {
		stream.event("approval_end", end)
	}
	req.Started = func(id string) {
		stream.event("conversation", chatStartedEvent{ConversationID: id})
	}

	reply, err := s.chat.Reply(ctx, req)
	if err != nil {
		// A retried first message still being answered: the conversation
		// event already named where it is, and pending tells the browser to
		// wait there rather than ask again.
		if errors.Is(err, service.ErrChatReplayPending) && stream.opened() {
			stream.event("pending", struct{}{})

			return
		}
		if !stream.opened() {
			Respond(w, r, nil, chatError(err))

			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		stream.event("error", logError(ctx, r, chatError(err)))

		return
	}

	stream.event("reply", toChatResponse(reply))
}

// chatStream writes server-sent events. Progress arrives on the turn's
// goroutine and heartbeats on their own, so every write takes the lock.
type chatStream struct {
	mu   sync.Mutex
	ctx  context.Context
	w    http.ResponseWriter
	rc   *http.ResponseController
	open bool
}

func (c *chatStream) opened() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.open
}

func (c *chatStream) event(name string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		log.FromContext(c.ctx).ErrorContext(c.ctx, "could not encode chat event",
			slog.String("event", name), slog.String("cause", err.Error()))

		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.start()
	_, _ = fmt.Fprintf(c.w, "event: %s\ndata: %s\n\n", name, body)
	_ = c.rc.Flush()
}

// comment writes an SSE comment, which clients ignore, once the stream is open.
func (c *chatStream) comment(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.open {
		return
	}
	_, _ = fmt.Fprintf(c.w, ": %s\n\n", text)
	_ = c.rc.Flush()
}

func (c *chatStream) start() {
	if c.open {
		return
	}
	c.open = true
	header := c.w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	// nginx, in the UI pod and in ingress-nginx, buffers a response unless told
	// otherwise, which would hold every event until the turn ends.
	header.Set("X-Accel-Buffering", "no")
	c.w.WriteHeader(http.StatusOK)
}

func chatError(err error) *AppError {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, service.ErrChatUnsaved):
		return Downstream("The reply could not be saved. Try again in a moment.", err)

	case errors.Is(err, service.ErrChatUnavailable):
		// 503 rather than 502: nothing downstream failed, the database that
		// holds conversations has not connected yet.
		return Downstream("Saved chats are not available yet. Try again in a moment.", err).
			WithStatus(http.StatusServiceUnavailable)

	case errors.Is(err, service.ErrChatStorage):
		return Downstream("Saved chats are not working right now. Try again in a moment.", err)

	case errors.Is(err, service.ErrChatNotFound):
		return NotFound("That conversation does not exist. It may have been deleted.").WithCause(err)

	case errors.Is(err, service.ErrChatBusy):
		return Conflict("This chat is still answering. Try again when the reply lands.").WithCause(err)

	case errors.Is(err, service.ErrChatMismatch):
		return Validation("This conversation uses another provider or model. Start a new chat to switch.").WithCause(err)

	case errors.Is(err, service.ErrApprovalNotFound):
		return NotFound("That approval expired or was already answered. Ask again if you still want the change.").WithCause(err)

	case errors.Is(err, service.ErrApprovalName):
		return Validation("That does not match. Type the database name exactly to confirm.").WithCause(err)

	case errors.Is(err, service.ErrChatCursor):
		return Validation("That page of conversations is not valid. Reload the list.").WithCause(err)

	case errors.Is(err, service.ErrInvalidChat):
		return Validation("That chat request is not valid.").WithCause(err)

	case errors.Is(err, service.ErrUnknownProvider):
		return Validation("Provider must be anthropic or cursor.").WithCause(err)

	case errors.Is(err, service.ErrAssistantUnavailable):
		return NotImplemented("Chat needs an Anthropic key, which is not configured.").
			WithCause(err)

	case errors.Is(err, service.ErrCursorUnavailable):
		return NotImplemented("Chat needs a Cursor key, which is not configured.").
			WithCause(err)

	case errors.Is(err, claude.ErrUnauthorized), errors.Is(err, cursor.ErrUnauthorized):
		return Downstream("The assistant could not authenticate. Check the key in Connections.", err)

	default:
		return Downstream("The assistant did not answer. Try again in a moment.", err)
	}
}
