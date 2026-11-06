package testutil

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/percona/pds/adapter/internal/store"
)

// MemoryOAuthStore keeps the authorization server's rows in maps. The rules
// worth testing are the service's; this only has to hold rows and keep the
// two atomic steps (use a code, retire a refresh token) atomic.
type MemoryOAuthStore struct {
	mu       sync.Mutex
	clients  map[string]store.OAuthClient
	requests map[string]store.OAuthRequest
	codes    map[string]store.OAuthCode
	sessions map[string]store.AgentSession
	tokens   map[string]store.OAuthToken
	purges   int
}

func NewMemoryOAuthStore() *MemoryOAuthStore {
	return &MemoryOAuthStore{
		clients:  map[string]store.OAuthClient{},
		requests: map[string]store.OAuthRequest{},
		codes:    map[string]store.OAuthCode{},
		sessions: map[string]store.AgentSession{},
		tokens:   map[string]store.OAuthToken{},
	}
}

func (m *MemoryOAuthStore) CreateClient(_ context.Context, c store.OAuthClient) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[c.ID] = c

	return nil
}

func (m *MemoryOAuthStore) Client(_ context.Context, id string) (store.OAuthClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.clients[id]
	if !ok {
		return store.OAuthClient{}, store.ErrOAuthClientUnknown
	}

	return c, nil
}

func (m *MemoryOAuthStore) UnusedClients(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, c := range m.clients {
		if !c.Granted {
			n++
		}
	}

	return n, nil
}

func (m *MemoryOAuthStore) MarkClientUsed(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.clients[id]
	c.Granted = true
	m.clients[id] = c

	return nil
}

func (m *MemoryOAuthStore) PurgeClients(_ context.Context, cutoff time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, c := range m.clients {
		if !c.Granted && c.CreatedAt.Before(cutoff) {
			delete(m.clients, id)
		}
	}

	return nil
}

func (m *MemoryOAuthStore) CreateRequest(_ context.Context, r store.OAuthRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[r.Hash] = r

	return nil
}

func (m *MemoryOAuthStore) Request(_ context.Context, hash string, now time.Time) (store.OAuthRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.requests[hash]
	if !ok || !now.Before(r.ExpiresAt) {
		return store.OAuthRequest{}, store.ErrOAuthRequestUnknown
	}

	return r, nil
}

func (m *MemoryOAuthStore) TakeRequest(_ context.Context, hash string, now time.Time) (store.OAuthRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.requests[hash]
	if !ok || !now.Before(r.ExpiresAt) {
		return store.OAuthRequest{}, store.ErrOAuthRequestUnknown
	}
	delete(m.requests, hash)

	return r, nil
}

func (m *MemoryOAuthStore) CreateCode(_ context.Context, c store.OAuthCode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes[c.Hash] = c

	return nil
}

func (m *MemoryOAuthStore) UseCode(_ context.Context, hash string, now time.Time) (store.OAuthCode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.codes[hash]
	if !ok || !now.Before(c.ExpiresAt) {
		return store.OAuthCode{}, store.ErrOAuthCodeUnknown
	}
	if c.UsedAt != nil {
		return c, store.ErrOAuthCodeUsed
	}
	used := now
	c.UsedAt = &used
	m.codes[hash] = c

	return c, nil
}

func (m *MemoryOAuthStore) CreateAgentSession(_ context.Context, s store.AgentSession, tokens []store.OAuthToken, codeHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.ID] = s
	for _, t := range tokens {
		m.tokens[t.Hash] = t
	}
	if c, ok := m.codes[codeHash]; ok {
		c.SessionID = s.ID
		m.codes[codeHash] = c
	}

	return nil
}

func (m *MemoryOAuthStore) TokenSession(_ context.Context, hash string) (store.OAuthToken, store.AgentSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[hash]
	if !ok {
		return store.OAuthToken{}, store.AgentSession{}, store.ErrOAuthTokenUnknown
	}
	s, ok := m.sessions[t.SessionID]
	if !ok {
		return store.OAuthToken{}, store.AgentSession{}, store.ErrOAuthTokenUnknown
	}

	return t, s, nil
}

func (m *MemoryOAuthStore) RotateRefresh(_ context.Context, hash string, now time.Time, tokens []store.OAuthToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.tokens[hash]
	if !ok || old.Kind != store.TokenRefresh {
		return store.ErrOAuthTokenUnknown
	}
	if _, ok := m.sessions[old.SessionID]; !ok {
		return store.ErrAgentSessionUnknown
	}
	if old.RotatedAt != nil {
		return store.ErrOAuthTokenReused
	}
	old.RotatedAt = &now
	m.tokens[hash] = old
	for _, t := range tokens {
		m.tokens[t.Hash] = t
	}
	s := m.sessions[old.SessionID]
	s.LastUsedAt = now
	m.sessions[old.SessionID] = s

	return nil
}

func (m *MemoryOAuthStore) TouchAgentSession(_ context.Context, id string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.LastUsedAt = now
		m.sessions[id] = s
	}

	return nil
}

func (m *MemoryOAuthStore) AgentSessions(_ context.Context, organizationID, userID uint, now time.Time) ([]store.AgentSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []store.AgentSession
	for _, s := range m.sessions {
		if s.OrganizationID == organizationID && (userID == 0 || s.UserID == userID) && s.ExpiresAt.After(now) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastUsedAt.Equal(out[j].LastUsedAt) {
			return out[i].LastUsedAt.After(out[j].LastUsedAt)
		}

		return out[i].ID < out[j].ID
	})

	return out, nil
}

// PutSession replaces a session row, for a test that needs it aged.
func (m *MemoryOAuthStore) PutSession(s store.AgentSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.ID] = s
}

func (m *MemoryOAuthStore) DeleteAgentSession(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
	for h, t := range m.tokens {
		if t.SessionID == id {
			delete(m.tokens, h)
		}
	}

	return nil
}

func (m *MemoryOAuthStore) PurgeOAuth(context.Context, time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purges++

	return nil
}

// Purges is how many times expired state was purged.
func (m *MemoryOAuthStore) Purges() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.purges
}

func (m *MemoryOAuthStore) SessionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.sessions)
}

func (m *MemoryOAuthStore) OnlySession() store.AgentSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		return s
	}

	return store.AgentSession{}
}
