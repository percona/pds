package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/store"
)

const (
	// maxChatTitle is how much of the first message becomes the title.
	maxChatTitle = 60
	// chatTakeBackBudget bounds taking an unanswered message back out. It is
	// its own budget because the turn's may be what just ran out.
	chatTakeBackBudget = 5 * time.Second
	// maxChatListPage bounds one page of the conversation list.
	maxChatListPage = 50
)

var (
	// ErrChatUnavailable means conversations cannot be read or written right
	// now, because the database has not connected or none is configured.
	ErrChatUnavailable = errors.New("chat conversations are not available")
	// ErrChatNotFound means the conversation is not there for this team.
	ErrChatNotFound = store.ErrChatNotFound
	// ErrChatCursor means a list cursor was not one the list handed out.
	ErrChatCursor = store.ErrChatCursor
	// ErrChatBusy means another turn on the conversation is still being
	// answered.
	ErrChatBusy = store.ErrChatBusy
	// ErrChatMismatch means a turn named a provider or model other than the
	// one the conversation started with.
	ErrChatMismatch = errors.New("the conversation uses another provider or model")
	// ErrChatStorage means the database is connected but a read or write of a
	// conversation failed.
	ErrChatStorage = errors.New("chat conversations could not be read or written")
	// ErrChatReplayPending means a retried first message is still being
	// answered in the conversation it opened. It is a kind of ErrChatBusy.
	ErrChatReplayPending = fmt.Errorf("%w: the retried message is still being answered", ErrChatBusy)
	// ErrChatUnsaved means the model answered but the answer could not be saved.
	ErrChatUnsaved = errors.New("the reply could not be saved")
)

// ChatStore keeps conversations for one team.
type ChatStore interface {
	StartChatConversation(ctx context.Context, conv store.ChatConversation, question store.ChatMessage) (store.ChatConversation, store.ChatMessage, error)
	ChatConversation(ctx context.Context, id string) (store.ChatConversation, error)
	ChatConversationByRequestKey(ctx context.Context, key string) (store.ChatConversation, error)
	ListChatConversations(ctx context.Context, cursor string, limit int) (store.ChatConversationPage, error)
	ChatMessages(ctx context.Context, id string) ([]store.ChatMessage, error)
	RecentChatMessages(ctx context.Context, id string, limit int) ([]store.ChatMessage, error)
	FirstChatMessages(ctx context.Context, id string, limit int) ([]store.ChatMessage, error)
	AppendChatMessage(ctx context.Context, id string, msg store.ChatMessage) (store.ChatMessage, error)
	AppendChatQuestion(ctx context.Context, id string, msg store.ChatMessage, since time.Time) (store.ChatMessage, error)
	DeleteChatMessage(ctx context.Context, id string, msg store.ChatMessage) error
	DeleteChatConversation(ctx context.Context, id string) error
	SetChatAgent(ctx context.Context, id, agentID string) error
}

// ChatStores returns the conversation store for the team a request carries,
// or nil when there is none to give.
type ChatStores func(ctx context.Context) ChatStore

// WithChatStore wires where conversations are kept.
func WithChatStore(stores ChatStores) ChatOption {
	return func(ch *Chat) {
		ch.stores = stores
	}
}

// TeamChatStores scopes the adapter database to the team on each request.
// fallback is the team a process that serves one Organization uses when a call
// has none.
func TeamChatStores(lazy *store.Lazy, fallback TeamFallback) ChatStores {
	if lazy == nil {
		return nil
	}

	return func(ctx context.Context) ChatStore {
		team := historyTeam(ctx, fallback)
		if team == 0 {
			return nil
		}

		return lazy.For(team)
	}
}

// ChatConversation is one saved conversation as the list shows it.
type ChatConversation struct {
	ID         string
	Title      string
	Provider   string
	Model      string
	DatabaseID string
	UpdatedAt  time.Time
}

// ChatConversationPage is one page of the list, newest first.
type ChatConversationPage struct {
	Conversations []ChatConversation
	Next          string
}

// ChatTurn is one saved message with the buttons its reply carried.
type ChatTurn struct {
	Role      string
	Content   string
	Actions   []ChatAction
	CreatedAt time.Time
}

// ChatThread is one conversation with every message in it. ContextWindow is
// how many of the newest messages the model is sent on the next turn.
type ChatThread struct {
	ChatConversation
	Messages      []ChatTurn
	ContextWindow int
}

// Conversations lists this team's conversations, newest first.
func (c *Chat) Conversations(ctx context.Context, cursor string, limit int) (ChatConversationPage, error) {
	chats, err := c.chatStore(ctx)
	if err != nil {
		return ChatConversationPage{}, err
	}
	if limit <= 0 || limit > maxChatListPage {
		limit = maxChatListPage
	}

	page, err := chats.ListChatConversations(ctx, cursor, limit)
	if err != nil {
		return ChatConversationPage{}, chatStoreError(err)
	}

	out := ChatConversationPage{
		Conversations: make([]ChatConversation, 0, len(page.Conversations)),
		Next:          page.Next,
	}
	for _, conv := range page.Conversations {
		out.Conversations = append(out.Conversations, toChatConversation(conv))
	}

	return out, nil
}

// Conversation returns one conversation and every message in it.
func (c *Chat) Conversation(ctx context.Context, id string) (ChatThread, error) {
	chats, err := c.chatStore(ctx)
	if err != nil {
		return ChatThread{}, err
	}

	conv, err := chats.ChatConversation(ctx, id)
	if err != nil {
		return ChatThread{}, chatStoreError(err)
	}
	messages, err := chats.ChatMessages(ctx, id)
	if err != nil {
		return ChatThread{}, chatStoreError(err)
	}

	thread := ChatThread{
		ChatConversation: toChatConversation(conv),
		Messages:         make([]ChatTurn, 0, len(messages)),
		ContextWindow:    maxChatMessages,
	}
	for _, msg := range messages {
		thread.Messages = append(thread.Messages, ChatTurn{
			Role:      msg.Role,
			Content:   msg.Content,
			Actions:   decodeChatActions(msg.Actions),
			CreatedAt: msg.CreatedAt,
		})
	}

	return thread, nil
}

// DeleteConversation removes one conversation and its messages, and asks
// Cursor to delete the agent that held it. The conversation is gone whether or
// not Cursor answers.
func (c *Chat) DeleteConversation(ctx context.Context, id string) error {
	chats, err := c.chatStore(ctx)
	if err != nil {
		return err
	}
	conv, err := chats.ChatConversation(ctx, id)
	if err != nil {
		return chatStoreError(err)
	}
	if err := chats.DeleteChatConversation(ctx, id); err != nil {
		return chatStoreError(err)
	}
	if conv.AgentID != "" {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cursorAgentBudget)
		defer cancel()
		c.deleteAgent(ctx, conv.AgentID)
	}

	return nil
}

func (c *Chat) chatStore(ctx context.Context) (ChatStore, error) {
	if c == nil || c.stores == nil {
		return nil, fmt.Errorf("%w: no database is configured", ErrChatUnavailable)
	}
	chats := c.stores(ctx)
	if chats == nil {
		return nil, fmt.Errorf("%w: the request carries no team", ErrChatUnavailable)
	}

	return chats, nil
}

// continueConversation loads the conversation a message continues and checks
// the message may go in it. A first message has no id and gets back an empty
// conversation. The history is read later, once the question is in, so it is
// the thread as this turn will see it.
func continueConversation(ctx context.Context, chats ChatStore, req ChatRequest) (store.ChatConversation, error) {
	id := strings.TrimSpace(req.ConversationID)
	if id == "" {
		return store.ChatConversation{}, nil
	}

	conv, err := chats.ChatConversation(ctx, id)
	if err != nil {
		return store.ChatConversation{}, chatStoreError(err)
	}
	if !runsOn(conv, req) {
		return store.ChatConversation{}, ErrChatMismatch
	}

	return conv, nil
}

// runsOn reports whether req names the provider and model conv runs on. A
// conversation saved on a deprecated model runs on the default, so that is the
// model its next turn names.
func runsOn(conv store.ChatConversation, req ChatRequest) bool {
	return conv.Provider == strings.TrimSpace(req.Provider) &&
		currentModel(conv.Provider, conv.Model) == strings.TrimSpace(req.Model)
}

// turnWindow reads what the model is sent for the question just appended.
//
// It runs after the append, not before: once the question is in, no other
// question can join the conversation until this one is answered, so the read
// holds every reply that landed first and nothing from a later turn. Only the
// newest maxChatMessages are read, however long the thread has grown.
func turnWindow(ctx context.Context, chats ChatStore, id string, asked store.ChatMessage, started bool) ([]ChatMessage, error) {
	if started {
		return chatWindow([]store.ChatMessage{asked}), nil
	}

	recent, err := chats.RecentChatMessages(ctx, id, maxChatMessages)
	if err != nil {
		return nil, chatStoreError(err)
	}

	return chatWindow(recent), nil
}

// startConversation opens the conversation a first message starts, with the
// provider and model fixed from here on, and saves the message in it.
func startConversation(ctx context.Context, chats ChatStore, req ChatRequest, question store.ChatMessage) (store.ChatConversation, store.ChatMessage, error) {
	subject, _ := auth.SubjectFromContext(ctx)
	conv, asked, err := chats.StartChatConversation(ctx, store.ChatConversation{
		Subject:    subject,
		Title:      chatTitle(Sanitize(req.Message)),
		Provider:   strings.TrimSpace(req.Provider),
		Model:      strings.TrimSpace(req.Model),
		DatabaseID: strings.TrimSpace(req.DatabaseID),
		RequestKey: strings.TrimSpace(req.RequestKey),
	}, question)
	if err != nil {
		return store.ChatConversation{}, store.ChatMessage{}, chatStoreError(err)
	}

	return conv, asked, nil
}

// replayRequest answers a first message that already opened a conversation,
// which is what a retry after a dropped connection is. It never asks the
// model: when the reply is saved it is returned as it was, and while the
// question is still being answered the caller is told which conversation to
// wait on.
//
// resume reports a question saved before staleBefore that never got its
// reply: the turn that asked it is gone, so the caller runs this message as
// a normal turn on the conversation, which replaces the orphaned question. A
// zero staleBefore never resumes.
func replayRequest(ctx context.Context, chats ChatStore, req ChatRequest, opened store.ChatConversation, staleBefore time.Time) (reply *ChatReply, resume bool, err error) {
	if !runsOn(opened, req) {
		return nil, false, ErrChatMismatch
	}

	// The key names the message that opened the conversation, so the reply
	// to hand back is the one right after it, however far the thread has gone
	// since, for example from another tab that found it in the list.
	opening, err := chats.FirstChatMessages(ctx, opened.ID, 2)
	if err != nil {
		return nil, false, chatStoreError(err)
	}
	if len(opening) == 2 && opening[0].Role == "user" && opening[1].Role == "assistant" {
		return &ChatReply{
			ConversationID: opened.ID,
			Text:           opening[1].Content,
			Actions:        decodeChatActions(opening[1].Actions),
		}, false, nil
	}

	// The question, or the conversation when the question never went in.
	asked := opened.CreatedAt
	if len(opening) > 0 {
		asked = opening[0].CreatedAt
	}
	if !staleBefore.IsZero() && asked.Before(staleBefore) {
		return nil, true, nil
	}

	if req.Started != nil {
		req.Started(opened.ID)
	}

	return nil, false, ErrChatReplayPending
}

// takeBack removes a message the model never answered, so the thread does not
// keep a question with no reply and the next turn does not send it twice. A
// conversation the message had just started goes with it.
func takeBack(ctx context.Context, chats ChatStore, id string, asked store.ChatMessage, started bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chatTakeBackBudget)
	defer cancel()

	var err error
	if started {
		err = chats.DeleteChatConversation(ctx, id)
	} else {
		err = chats.DeleteChatMessage(ctx, id, asked)
	}
	if err != nil {
		log.FromContext(ctx).WarnContext(ctx, "an unanswered chat message stayed in its conversation",
			slog.String("cause", log.RedactText(err.Error())))
	}
}

// chatWindow is the part of the thread the model is sent: the newest
// maxChatMessages, starting on a user turn because a thread may not open with
// the assistant. Everything stays stored and shown; only this is sent.
func chatWindow(messages []store.ChatMessage) []ChatMessage {
	if len(messages) > maxChatMessages {
		messages = messages[len(messages)-maxChatMessages:]
	}
	for len(messages) > 0 && messages[0].Role != "user" {
		messages = messages[1:]
	}

	out := make([]ChatMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, ChatMessage{Role: msg.Role, Content: msg.Content})
	}

	return out
}

// chatTitle is the first message on one line, cut to maxChatTitle characters.
func chatTitle(message string) string {
	title := strings.Join(strings.Fields(message), " ")
	if utf8.RuneCountInString(title) <= maxChatTitle {
		return title
	}

	runes := []rune(title)

	return strings.TrimSpace(string(runes[:maxChatTitle])) + "..."
}

func encodeChatActions(actions []ChatAction) string {
	if actions == nil {
		actions = []ChatAction{}
	}
	body, _ := json.Marshal(actions)

	return string(body)
}

// decodeChatActions reads back what encodeChatActions wrote. A value that does
// not parse shows the message without buttons rather than hiding the message.
func decodeChatActions(raw string) []ChatAction {
	out := []ChatAction{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []ChatAction{}
	}

	return out
}

func toChatConversation(conv store.ChatConversation) ChatConversation {
	return ChatConversation{
		ID:         conv.ID,
		Title:      conv.Title,
		Provider:   conv.Provider,
		Model:      currentModel(conv.Provider, conv.Model),
		DatabaseID: conv.DatabaseID,
		UpdatedAt:  conv.UpdatedAt,
	}
}

// chatStoreError sorts a store failure into what the caller can act on: a
// conversation that is not there, a database that has not connected yet, or a
// read or write that failed. The last is kept apart from a model failure so it
// is never reported as the assistant not answering.
func chatStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrChatNotFound), errors.Is(err, store.ErrChatCursor), errors.Is(err, store.ErrChatBusy):
		return err
	case errors.Is(err, store.ErrNotConnected):
		return fmt.Errorf("%w: %w", ErrChatUnavailable, err)
	default:
		return fmt.Errorf("%w: %w", ErrChatStorage, err)
	}
}
