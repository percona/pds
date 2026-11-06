package bridge

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"golang.org/x/oauth2"

	"github.com/percona/pds/apps/mcp/internal/oauth"
)

type attemptsKey struct{}

// withAttempts counts the authorization failures one outgoing message meets. The first goes to
// the SDK, which asks the handler to refresh and sends the message again; a second means the new
// token was refused too, and ends the message with an error instead of breaking the connection.
func withAttempts(ctx context.Context) context.Context {
	return context.WithValue(ctx, attemptsKey{}, new(atomic.Int32))
}

// statuses turns what the SDK treats as a broken connection into a failure of one message. A
// control plane that answers 404 to every request is not one the connection should die over.
type statuses struct {
	base http.RoundTripper
	// version is the protocol version the agent negotiated, sent on every later request.
	version *atomic.Pointer[string]
	// client is the agent's own name, which the control plane cannot see over this connection.
	client *atomic.Pointer[string]
}

func (s *statuses) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if v := s.version.Load(); v != nil && req.Header.Get("MCP-Protocol-Version") == "" {
		req.Header.Set("MCP-Protocol-Version", *v)
	}
	if c := s.client.Load(); c != nil {
		req.Header.Set("X-PDS-MCP-Client", *c)
	}

	resp, err := s.base.RoundTrip(req)
	if err != nil || resp.StatusCode < 400 {
		return resp, err
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		if attempts, ok := req.Context().Value(attemptsKey{}).(*atomic.Int32); !ok || attempts.Add(1) == 1 {
			return resp, nil
		}
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return resp, nil
	case http.StatusNotFound:
		_ = resp.Body.Close()

		return nil, oauth.ErrNotServed
	default:
		// An error the control plane answered as JSON-RPC is the protocol's own, and passes.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		if msg, err := jsonrpc.DecodeMessage(body); err == nil {
			if response, ok := msg.(*jsonrpc.Response); ok && response.Error != nil {
				resp.Body = io.NopCloser(bytes.NewReader(body))

				return resp, nil
			}
		}

		return nil, &statusError{status: resp.StatusCode}
	}
	_ = resp.Body.Close()

	return nil, &statusError{status: resp.StatusCode}
}

// authorizer gives the SDK's streamable client its tokens and answers its authorization failures.
type authorizer struct {
	tokens Tokens
}

// TokenSource implements the SDK's OAuthHandler.
func (a *authorizer) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	return tokenSource{ctx: ctx, tokens: a.tokens}, nil
}

// tokenSource adapts the bridge's tokens to the form the SDK asks for. Its context is the
// connection's, so a request waiting for a token ends with the connection.
type tokenSource struct {
	ctx    context.Context
	tokens Tokens
}

func (t tokenSource) Token() (*oauth2.Token, error) {
	access, err := t.tokens.Token(t.ctx)
	if err != nil {
		return nil, err
	}

	return &oauth2.Token{AccessToken: access, TokenType: "Bearer"}, nil
}

// Authorize implements the SDK's OAuthHandler. It never opens a browser: a 401 is answered by one
// refresh, a 403 by an error that names the scope.
func (a *authorizer) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusForbidden {
		if challenge := resp.Header.Get("WWW-Authenticate"); strings.Contains(challenge, `error="insufficient_scope"`) {
			return &insufficientScopeError{scope: challengeParam(challenge, "scope")}
		}

		return &statusError{status: resp.StatusCode}
	}

	rejected := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	_, err := a.tokens.Refresh(ctx, rejected)

	return err
}

// challengeParam reads one quoted parameter of a WWW-Authenticate header.
func challengeParam(header, name string) string {
	_, rest, ok := strings.Cut(header, name+`="`)
	if !ok {
		return ""
	}
	value, _, _ := strings.Cut(rest, `"`)

	return value
}
