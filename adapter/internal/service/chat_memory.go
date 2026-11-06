package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/percona/pds/adapter/internal/store"
)

// MemoryChats keeps conversations in process memory, one set per Team. It
// has the same answers as the database store, so the chat routes can be
// exercised without a live PostgreSQL. Nothing survives a restart.
type MemoryChats struct {
	mu       sync.Mutex
	fallback TeamFallback
	teams    map[uint]*memoryChatTeam
}

type memoryChatTeam struct {
	conversations map[string]store.ChatConversation
	messages      map[string][]store.ChatMessage
}

// NewMemoryChats returns an empty set. fallback is the Team a call with no
// identity is recorded under.
func NewMemoryChats(fallback TeamFallback) *MemoryChats {
	return &MemoryChats{fallback: fallback, teams: map[uint]*memoryChatTeam{}}
}

// Stores scopes the set to the Team on each call.
func (m *MemoryChats) Stores() ChatStores {
	return func(ctx context.Context) ChatStore {
		team := historyTeam(ctx, m.fallback)
		if team == 0 {
			return nil
		}

		return &memoryChatStore{parent: m, team: team}
	}
}

type memoryChatStore struct {
	parent *MemoryChats
	team   uint
}

func (s *memoryChatStore) scope() *memoryChatTeam {
	t, ok := s.parent.teams[s.team]
	if !ok {
		t = &memoryChatTeam{
			conversations: map[string]store.ChatConversation{},
			messages:      map[string][]store.ChatMessage{},
		}
		s.parent.teams[s.team] = t
	}

	return t
}

func (s *memoryChatStore) CreateChatConversation(_ context.Context, conv store.ChatConversation) (store.ChatConversation, error) {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()

	if key := strings.TrimSpace(conv.RequestKey); key != "" {
		for _, existing := range s.scope().conversations {
			if existing.RequestKey == key {
				return store.ChatConversation{}, store.ErrChatRequestSeen
			}
		}
	}
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	conv.ID = hex.EncodeToString(raw)
	conv.CreatedAt = time.Now().UTC()
	conv.UpdatedAt = conv.CreatedAt
	s.scope().conversations[conv.ID] = conv

	return conv, nil
}

// StartChatConversation opens a conversation with its first question, and
// removes the conversation again when the question is refused, so none is
// left empty.
func (s *memoryChatStore) StartChatConversation(ctx context.Context, conv store.ChatConversation, question store.ChatMessage) (store.ChatConversation, store.ChatMessage, error) {
	created, err := s.CreateChatConversation(ctx, conv)
	if err != nil {
		return store.ChatConversation{}, store.ChatMessage{}, err
	}
	asked, err := s.AppendChatMessage(ctx, created.ID, question)
	if err != nil {
		_ = s.DeleteChatConversation(ctx, created.ID)

		return store.ChatConversation{}, store.ChatMessage{}, err
	}

	return created, asked, nil
}

func (s *memoryChatStore) ChatConversation(_ context.Context, id string) (store.ChatConversation, error) {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()

	conv, ok := s.scope().conversations[id]
	if !ok {
		return store.ChatConversation{}, store.ErrChatNotFound
	}

	return conv, nil
}

func (s *memoryChatStore) ChatConversationByRequestKey(_ context.Context, key string) (store.ChatConversation, error) {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()

	key = strings.TrimSpace(key)
	for _, conv := range s.scope().conversations {
		if key != "" && conv.RequestKey == key {
			return conv, nil
		}
	}

	return store.ChatConversation{}, store.ErrChatNotFound
}

// ListChatConversations pages by the id of the last conversation handed out.
func (s *memoryChatStore) ListChatConversations(_ context.Context, cursor string, limit int) (store.ChatConversationPage, error) {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()

	all := make([]store.ChatConversation, 0, len(s.scope().conversations))
	for _, conv := range s.scope().conversations {
		all = append(all, conv)
	}
	slices.SortFunc(all, func(a, b store.ChatConversation) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}

		return strings.Compare(b.ID, a.ID)
	})

	if cursor != "" {
		at := slices.IndexFunc(all, func(conv store.ChatConversation) bool { return conv.ID == cursor })
		if at < 0 {
			return store.ChatConversationPage{}, store.ErrChatCursor
		}
		all = all[at+1:]
	}

	page := store.ChatConversationPage{}
	if len(all) > limit {
		all = all[:limit]
		page.Next = all[len(all)-1].ID
	}
	page.Conversations = all

	return page, nil
}

func (s *memoryChatStore) ChatMessages(_ context.Context, id string) ([]store.ChatMessage, error) {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()

	if _, ok := s.scope().conversations[id]; !ok {
		return nil, store.ErrChatNotFound
	}

	return slices.Clone(s.scope().messages[id]), nil
}

func (s *memoryChatStore) FirstChatMessages(ctx context.Context, id string, limit int) ([]store.ChatMessage, error) {
	all, err := s.ChatMessages(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(all) > limit {
		all = all[:limit]
	}

	return all, nil
}

func (s *memoryChatStore) RecentChatMessages(ctx context.Context, id string, limit int) ([]store.ChatMessage, error) {
	all, err := s.ChatMessages(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(all) > limit {
		all = all[len(all)-limit:]
	}

	return all, nil
}

func (s *memoryChatStore) AppendChatMessage(ctx context.Context, id string, msg store.ChatMessage) (store.ChatMessage, error) {
	return s.AppendChatQuestion(ctx, id, msg, time.Time{})
}

// AppendChatQuestion refuses a message while the newest one is a user message
// saved after since. A zero since appends unconditionally.
func (s *memoryChatStore) AppendChatQuestion(_ context.Context, id string, msg store.ChatMessage, since time.Time) (store.ChatMessage, error) {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()

	t := s.scope()
	conv, ok := t.conversations[id]
	if !ok {
		return store.ChatMessage{}, store.ErrChatNotFound
	}
	if existing := t.messages[id]; !since.IsZero() && len(existing) > 0 {
		if last := existing[len(existing)-1]; last.Role == "user" {
			if last.CreatedAt.After(since) {
				return store.ChatMessage{}, store.ErrChatBusy
			}
			t.messages[id] = existing[:len(existing)-1]
		}
	}

	msg.Sequence = 1
	if existing := t.messages[id]; len(existing) > 0 {
		msg.Sequence = existing[len(existing)-1].Sequence + 1
	}
	msg.CreatedAt = time.Now().UTC()
	t.messages[id] = append(t.messages[id], msg)
	conv.UpdatedAt = msg.CreatedAt
	t.conversations[id] = conv

	return msg, nil
}

func (s *memoryChatStore) DeleteChatMessage(_ context.Context, id string, target store.ChatMessage) error {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()

	t := s.scope()
	messages, ok := t.messages[id]
	if _, found := t.conversations[id]; !found || !ok {
		return store.ErrChatNotFound
	}
	at := slices.IndexFunc(messages, func(msg store.ChatMessage) bool {
		return msg.Sequence == target.Sequence && msg.CreatedAt.Equal(target.CreatedAt)
	})
	if at < 0 {
		return store.ErrChatNotFound
	}
	t.messages[id] = slices.Delete(messages, at, at+1)
	conv := t.conversations[id]
	conv.UpdatedAt = conv.CreatedAt
	if remaining := t.messages[id]; len(remaining) > 0 {
		conv.UpdatedAt = remaining[len(remaining)-1].CreatedAt
	}
	t.conversations[id] = conv

	return nil
}

func (s *memoryChatStore) DeleteChatConversation(_ context.Context, id string) error {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()

	t := s.scope()
	if _, ok := t.conversations[id]; !ok {
		return store.ErrChatNotFound
	}
	delete(t.conversations, id)
	delete(t.messages, id)

	return nil
}

func (s *memoryChatStore) SetChatAgent(_ context.Context, id, agentID string) error {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()

	t := s.scope()
	conv, ok := t.conversations[id]
	if !ok {
		return store.ErrChatNotFound
	}
	conv.AgentID = strings.TrimSpace(agentID)
	t.conversations[id] = conv

	return nil
}
