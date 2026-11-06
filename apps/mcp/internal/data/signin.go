package data

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/apps/mcp/internal/messages"
	"github.com/percona/pds/apps/mcp/internal/oauth"
	"github.com/percona/pds/apps/mcp/internal/tokenstore"
)

// signInKey names the elicitation in a result that asks the client to open the sign in page.
const signInKey = "signin"

// announceTimeout is how long the sign in has to produce its address.
const announceTimeout = 10 * time.Second

// pending is a sign in waiting for the person to finish in the browser.
type pending struct {
	address string
	id      string
	cancel  context.CancelFunc
	done    chan struct{}
}

// signIn handles a tool call that needs the control plane when there is no sign in. The data server
// never opens a browser. A client that can show a URL to the person is asked to, through URL mode
// elicitation, and the person finishes in the browser. Any other client is told to run pds-mcp login.
func (s *Server) signIn(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	caps := req.ClientCapabilities()
	if caps == nil || caps.Elicitation == nil || caps.Elicitation.URL == nil {
		return nil, toolError(messages.DataNeedsLogin)
	}

	if answer, ok := req.Params.InputResponses[signInKey].(*sdk.ElicitResult); ok {
		if answer.Action != "accept" {
			s.cancelSignIn()

			return nil, toolError(messages.DataSignInDeclined)
		}

		return nil, toolError(messages.DataSignInWaiting)
	}

	p, err := s.beginSignIn(ctx, req.Session)
	if err != nil {
		return nil, toolError(messages.DataNeedsLogin)
	}

	return &sdk.CallToolResult{InputRequests: sdk.InputRequestMap{signInKey: &sdk.ElicitParams{
		Mode: "url", Message: messages.DataSignInPrompt, URL: p.address, ElicitationID: p.id,
	}}}, nil
}

// beginSignIn starts the loopback sign in and returns the address the person has to open, or the one
// already waiting. The sign in outlives the tool call: it ends when the person finishes, declines,
// or the time runs out.
func (s *Server) beginSignIn(ctx context.Context, session *sdk.ServerSession) (*pending, error) {
	// Only one sign in is started at a time. This lock is not the one the tool list uses, because
	// waiting for the address can take seconds.
	s.signMu.Lock()
	defer s.signMu.Unlock()
	if s.pending != nil {
		select {
		case <-s.pending.done:
		default:
			return s.pending, nil
		}
	}

	server, err := s.deps.Tokens.Server(ctx)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)

	s.mu.Lock()
	lifetime := s.lifetime
	s.mu.Unlock()
	loginCtx, cancel := context.WithCancel(lifetime)
	p := &pending{id: hex.EncodeToString(raw), cancel: cancel, done: make(chan struct{})}
	announced := make(chan string, 1)

	go func() {
		defer close(p.done)
		defer cancel()
		tokens, err := oauth.Login(loginCtx, oauth.LoginOptions{
			Server: server, HTTP: s.deps.HTTP, Scopes: signInScopes,
			// No Open: a program that a client starts over stdio never opens a browser itself.
			Announce: func(address string) { announced <- address },
		})
		if err != nil {
			s.deps.Log.Info("sign in did not finish")

			return
		}
		if err := s.deps.Logins.Save(server.Issuer, tokenstore.Login{RefreshToken: tokens.RefreshToken, Scope: tokens.Scope}); err != nil {
			s.deps.Log.Warn("sign in could not be saved")

			return
		}
		s.sync()
		// Only a client of the stateful era can be told; the next call finds the sign in either way.
		_ = session.NotifyElicitationComplete(context.WithoutCancel(loginCtx), &sdk.ElicitationCompleteParams{ElicitationID: p.id})
	}()

	select {
	case p.address = <-announced:
	case <-p.done:
		return nil, errors.New("the sign in did not start")
	case <-time.After(announceTimeout):
		cancel()

		return nil, errors.New("the sign in did not start")
	}
	s.pending = p

	return p, nil
}

// cancelSignIn ends a sign in the person declined.
func (s *Server) cancelSignIn() {
	s.signMu.Lock()
	defer s.signMu.Unlock()
	if s.pending != nil {
		s.pending.cancel()
		s.pending = nil
	}
}
