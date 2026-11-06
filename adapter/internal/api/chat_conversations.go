package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/service"
)

type chatConversationResponse struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	DatabaseID string    `json:"databaseId,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type chatConversationsResponse struct {
	Conversations []chatConversationResponse `json:"conversations"`
	NextCursor    string                     `json:"nextCursor,omitempty"`
}

type chatTurnResponse struct {
	Role      string               `json:"role"`
	Content   string               `json:"content"`
	Actions   []service.ChatAction `json:"actions"`
	CreatedAt time.Time            `json:"createdAt"`
}

type chatThreadResponse struct {
	chatConversationResponse
	ContextWindow int                `json:"contextWindow"`
	Messages      []chatTurnResponse `json:"messages"`
}

func (s *Server) handleListChatConversations(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			Respond(w, r, nil, Validation("The limit must be a positive whole number."))

			return
		}
		limit = parsed
	}

	page, err := s.chat.Conversations(r.Context(), strings.TrimSpace(r.URL.Query().Get("cursor")), limit)
	if err != nil {
		Respond(w, r, nil, chatError(err))

		return
	}

	out := chatConversationsResponse{
		Conversations: make([]chatConversationResponse, 0, len(page.Conversations)),
		NextCursor:    page.Next,
	}
	for _, conv := range page.Conversations {
		out.Conversations = append(out.Conversations, toChatConversationResponse(conv))
	}

	Respond(w, r, out, nil)
}

func (s *Server) handleGetChatConversation(w http.ResponseWriter, r *http.Request) {
	thread, err := s.chat.Conversation(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Respond(w, r, nil, chatError(err))

		return
	}

	out := chatThreadResponse{
		chatConversationResponse: toChatConversationResponse(thread.ChatConversation),
		ContextWindow:            thread.ContextWindow,
		Messages:                 make([]chatTurnResponse, 0, len(thread.Messages)),
	}
	for _, turn := range thread.Messages {
		actions := turn.Actions
		if actions == nil {
			actions = []service.ChatAction{}
		}
		out.Messages = append(out.Messages, chatTurnResponse{
			Role:      turn.Role,
			Content:   turn.Content,
			Actions:   actions,
			CreatedAt: turn.CreatedAt,
		})
	}

	Respond(w, r, out, nil)
}

func (s *Server) handleDeleteChatConversation(w http.ResponseWriter, r *http.Request) {
	if err := s.chat.DeleteConversation(r.Context(), chi.URLParam(r, "id")); err != nil {
		Respond(w, r, nil, chatError(err))

		return
	}

	Respond(w, r, nil, nil)
}

func toChatConversationResponse(conv service.ChatConversation) chatConversationResponse {
	return chatConversationResponse{
		ID:         conv.ID,
		Title:      conv.Title,
		Provider:   conv.Provider,
		Model:      conv.Model,
		DatabaseID: conv.DatabaseID,
		UpdatedAt:  conv.UpdatedAt,
	}
}
