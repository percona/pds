package oauth

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/percona/pds/apps/mcp/internal/tokenstore"
)

// DiscoveryTimeout is how long reading the control plane's sign in settings may take.
const DiscoveryTimeout = 15 * time.Second

// LazySource finds the control plane's sign in settings the first time a token is needed, not at
// start, so a program that starts while the control plane is unreachable still serves once it is
// back. A control plane that serves no MCP endpoint is reported the same way each time.
type LazySource struct {
	hc     *http.Client
	store  *tokenstore.Store
	origin string

	mu     sync.Mutex
	server Server
	source *Source
}

// NewLazySource returns the source for the control plane at origin.
func NewLazySource(hc *http.Client, store *tokenstore.Store, origin string) *LazySource {
	return &LazySource{hc: hc, store: store, origin: origin}
}

func (l *LazySource) get(ctx context.Context) (*Source, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.source != nil {
		return l.source, nil
	}
	ctx, cancel := context.WithTimeout(ctx, DiscoveryTimeout)
	defer cancel()

	server, err := Discover(ctx, l.hc, l.origin)
	if err != nil {
		return nil, err
	}
	l.server = server
	l.source = NewSource(server, l.hc, l.store)

	return l.source, nil
}

// Server returns the control plane's authorization server, as discovered.
func (l *LazySource) Server(ctx context.Context) (Server, error) {
	if _, err := l.get(ctx); err != nil {
		return Server{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.server, nil
}

// Token returns a valid access token for the saved sign in.
func (l *LazySource) Token(ctx context.Context) (string, error) {
	source, err := l.get(ctx)
	if err != nil {
		return "", err
	}

	return source.Token(ctx)
}

// Refresh answers a 401: rejected is the token the control plane refused.
func (l *LazySource) Refresh(ctx context.Context, rejected string) (string, error) {
	source, err := l.get(ctx)
	if err != nil {
		return "", err
	}

	return source.Refresh(ctx, rejected)
}

// Renew gets a new access token from the saved sign in, and with it the grants as they stand now.
func (l *LazySource) Renew(ctx context.Context) (string, error) {
	source, err := l.get(ctx)
	if err != nil {
		return "", err
	}

	return source.Renew(ctx)
}
