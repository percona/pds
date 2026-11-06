package store

import (
	"context"
	"time"
)

// LazyOAuth is the authorization server's state, resolved when the database
// connects. It has the same surface as OAuth, so the server can be wired at
// start; a call that arrives first reports that the database is not connected.
type LazyOAuth struct {
	starter *Starter
}

// NewLazyOAuth returns the authorization server's state, resolved on first use.
func NewLazyOAuth(starter *Starter) *LazyOAuth { return &LazyOAuth{starter: starter} }

func (l *LazyOAuth) resolve() (*OAuth, error) {
	if l == nil || l.starter == nil {
		return nil, ErrNotConnected
	}
	db := l.starter.Store()
	if db == nil {
		return nil, ErrNotConnected
	}

	return db.OAuth(), nil
}

// CreateClient stores a registration.
func (l *LazyOAuth) CreateClient(ctx context.Context, client OAuthClient) error {
	o, err := l.resolve()
	if err != nil {
		return err
	}

	return o.CreateClient(ctx, client)
}

// Client returns a registered client.
func (l *LazyOAuth) Client(ctx context.Context, id string) (OAuthClient, error) {
	o, err := l.resolve()
	if err != nil {
		return OAuthClient{}, err
	}

	return o.Client(ctx, id)
}

// UnusedClients counts the registrations that never produced a session.
func (l *LazyOAuth) UnusedClients(ctx context.Context) (int64, error) {
	o, err := l.resolve()
	if err != nil {
		return 0, err
	}

	return o.UnusedClients(ctx)
}

// MarkClientUsed keeps a registration from being purged.
func (l *LazyOAuth) MarkClientUsed(ctx context.Context, id string) error {
	o, err := l.resolve()
	if err != nil {
		return err
	}

	return o.MarkClientUsed(ctx, id)
}

// PurgeClients drops registrations that never produced a session.
func (l *LazyOAuth) PurgeClients(ctx context.Context, cutoff time.Time) error {
	o, err := l.resolve()
	if err != nil {
		return err
	}

	return o.PurgeClients(ctx, cutoff)
}

// CreateRequest parks an authorization request.
func (l *LazyOAuth) CreateRequest(ctx context.Context, row OAuthRequest) error {
	o, err := l.resolve()
	if err != nil {
		return err
	}

	return o.CreateRequest(ctx, row)
}

// Request reads a parked request.
func (l *LazyOAuth) Request(ctx context.Context, hash string, now time.Time) (OAuthRequest, error) {
	o, err := l.resolve()
	if err != nil {
		return OAuthRequest{}, err
	}

	return o.Request(ctx, hash, now)
}

// TakeRequest removes a parked request and returns it.
func (l *LazyOAuth) TakeRequest(ctx context.Context, hash string, now time.Time) (OAuthRequest, error) {
	o, err := l.resolve()
	if err != nil {
		return OAuthRequest{}, err
	}

	return o.TakeRequest(ctx, hash, now)
}

// CreateCode stores an authorization code.
func (l *LazyOAuth) CreateCode(ctx context.Context, code OAuthCode) error {
	o, err := l.resolve()
	if err != nil {
		return err
	}

	return o.CreateCode(ctx, code)
}

// UseCode marks a code used and returns it.
func (l *LazyOAuth) UseCode(ctx context.Context, hash string, now time.Time) (OAuthCode, error) {
	o, err := l.resolve()
	if err != nil {
		return OAuthCode{}, err
	}

	return o.UseCode(ctx, hash, now)
}

// CreateAgentSession stores a session and its first tokens.
func (l *LazyOAuth) CreateAgentSession(ctx context.Context, session AgentSession, tokens []OAuthToken, codeHash string) error {
	o, err := l.resolve()
	if err != nil {
		return err
	}

	return o.CreateAgentSession(ctx, session, tokens, codeHash)
}

// TokenSession returns a token and its session.
func (l *LazyOAuth) TokenSession(ctx context.Context, hash string) (OAuthToken, AgentSession, error) {
	o, err := l.resolve()
	if err != nil {
		return OAuthToken{}, AgentSession{}, err
	}

	return o.TokenSession(ctx, hash)
}

// RotateRefresh retires a refresh token and stores its replacements.
func (l *LazyOAuth) RotateRefresh(ctx context.Context, hash string, now time.Time, tokens []OAuthToken) error {
	o, err := l.resolve()
	if err != nil {
		return err
	}

	return o.RotateRefresh(ctx, hash, now, tokens)
}

// TouchAgentSession records that a session was used.
func (l *LazyOAuth) TouchAgentSession(ctx context.Context, id string, now time.Time) error {
	o, err := l.resolve()
	if err != nil {
		return err
	}

	return o.TouchAgentSession(ctx, id, now)
}

// AgentSessions lists an Organization's sessions that have not expired.
func (l *LazyOAuth) AgentSessions(ctx context.Context, organizationID, userID uint, now time.Time) ([]AgentSession, error) {
	o, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return o.AgentSessions(ctx, organizationID, userID, now)
}

// DeleteAgentSession ends a session and its tokens.
func (l *LazyOAuth) DeleteAgentSession(ctx context.Context, id string) error {
	o, err := l.resolve()
	if err != nil {
		return err
	}

	return o.DeleteAgentSession(ctx, id)
}

// PurgeOAuth drops what has expired.
func (l *LazyOAuth) PurgeOAuth(ctx context.Context, now time.Time) error {
	o, err := l.resolve()
	if err != nil {
		return err
	}

	return o.PurgeOAuth(ctx, now)
}
