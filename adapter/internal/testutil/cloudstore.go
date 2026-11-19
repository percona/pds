package testutil

import (
	"context"
	"sync"
	"time"

	"github.com/percona/pds/adapter/internal/store"
)

// CloudStore keeps connections in memory with the semantics the service
// relies on: tokens by hash, one connection per (Team, account), the first
// Ready revoking the connection's tokens.
type CloudStore struct {
	mu          sync.Mutex
	Tokens      map[string]*store.CloudToken
	Connections map[string]*store.CloudConnection
	Reported    []store.CloudEvent
}

// NewCloudStore returns an empty CloudStore.
func NewCloudStore() *CloudStore {
	return &CloudStore{Tokens: map[string]*store.CloudToken{}, Connections: map[string]*store.CloudConnection{}}
}

// CreateToken stores a token.
func (f *CloudStore) CreateToken(_ context.Context, in store.NewCloudToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Tokens[in.Hash] = &store.CloudToken{Hash: in.Hash, TeamID: in.TeamID, AllowedRegions: in.AllowedRegions, ExpiresAt: in.ExpiresAt}

	return nil
}

// RevokeToken ends one token.
func (f *CloudStore) RevokeToken(_ context.Context, _ uint, hash string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if token, ok := f.Tokens[hash]; ok {
		token.RevokedAt = &now
	}

	return nil
}

// TokenTeam returns a live token.
func (f *CloudStore) TokenTeam(_ context.Context, hash string, now time.Time) (store.CloudToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	token, ok := f.Tokens[hash]
	if !ok || token.RevokedAt != nil || !token.ExpiresAt.After(now) {
		return store.CloudToken{}, store.ErrCloudTokenInvalid
	}

	return *token, nil
}

// Enroll finds or makes the Team's connection for the account.
func (f *CloudStore) Enroll(_ context.Context, in store.CloudEnrollment, now time.Time) (store.CloudConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	token, ok := f.Tokens[in.TokenHash]
	if !ok || token.RevokedAt != nil || !token.ExpiresAt.After(now) {
		return store.CloudConnection{}, store.ErrCloudTokenInvalid
	}
	for _, row := range f.Connections {
		if row.TeamID == token.TeamID && row.AccountID == in.AccountID {
			token.ConnectionID = row.ID

			return *row, nil
		}
	}
	regions := token.AllowedRegions
	if len(in.AllowedRegions) > 0 {
		regions = in.AllowedRegions
	}
	row := &store.CloudConnection{
		ID: in.NewID, TeamID: token.TeamID, AccountID: in.AccountID, Alias: in.Alias, ExternalID: in.NewExternalID,
		StackName: in.NewStackName, StackRegion: in.StackRegion, AllowedRegions: regions, State: store.CloudPending,
	}
	f.Connections[row.ID] = row
	token.ConnectionID = row.ID

	return *row, nil
}

// Get returns one of the Team's connections.
func (f *CloudStore) Get(_ context.Context, teamID uint, id string) (store.CloudConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.Connections[id]
	if !ok || row.TeamID != teamID {
		return store.CloudConnection{}, store.ErrCloudConnectionUnknown
	}

	return *row, nil
}

// List returns the Team's connections.
func (f *CloudStore) List(_ context.Context, teamID uint) ([]store.CloudConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.CloudConnection
	for _, row := range f.Connections {
		if row.TeamID == teamID {
			out = append(out, *row)
		}
	}

	return out, nil
}

// All returns every connection.
func (f *CloudStore) All(_ context.Context) ([]store.CloudConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.CloudConnection
	for _, row := range f.Connections {
		out = append(out, *row)
	}

	return out, nil
}

// SaveCheck records a check.
func (f *CloudStore) SaveCheck(_ context.Context, id string, in store.CloudCheck, now time.Time) (store.CloudConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.Connections[id]
	if !ok {
		return store.CloudConnection{}, store.ErrCloudConnectionUnknown
	}
	row.State, row.Reason, row.CheckedAt = in.State, in.Reason, &now
	if in.TemplateVersion > 0 {
		row.TemplateVersion = in.TemplateVersion
	}
	if in.State == store.CloudReady && row.ReadyAt == nil {
		row.ReadyAt = &now
		for _, token := range f.Tokens {
			if token.ConnectionID == id {
				token.RevokedAt = &now
			}
		}
	}

	return *row, nil
}

// Cancel drops a connection that was never Ready.
func (f *CloudStore) Cancel(_ context.Context, teamID uint, id string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.Connections[id]
	if !ok || row.TeamID != teamID {
		return store.ErrCloudConnectionUnknown
	}
	if row.ReadyAt != nil {
		return store.ErrCloudConnectionReady
	}
	delete(f.Connections, id)

	return nil
}

// RecordEvent keeps a reported step.
func (f *CloudStore) RecordEvent(_ context.Context, _ string, in store.CloudEvent, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Reported = append(f.Reported, in)

	return nil
}

// Events returns the reported steps.
func (f *CloudStore) Events(_ context.Context, _ uint, _ string) ([]store.CloudEvent, error) {
	return f.Reported, nil
}
