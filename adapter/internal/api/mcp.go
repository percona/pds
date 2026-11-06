package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/adapter/internal/oauth"
	"github.com/percona/pds/adapter/internal/service"
)

const (
	mcpPath            = "/mcp"
	protectedResource  = "/.well-known/oauth-protected-resource"
	protectedMCPSuffix = protectedResource + mcpPath
)

// mcpAgentKey carries the caller a request's token resolved to, from the
// bearer check to the handler that builds the server for it.
type mcpAgentKey struct{}

// newMCPHandler serves the registry over streamable HTTP. It is stateless: no
// session outlives a request, so each request is judged by its own token, a
// revoked token stops working on the next call, and a pod restart or another
// replica loses nothing. The server a request meets is built for the caller
// its token resolved to, so it lists only the tools that caller's tier allows.
func (s *Server) newMCPHandler() http.Handler {
	return sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		agent, ok := r.Context().Value(mcpAgentKey{}).(service.AgentCaller)
		if !ok {
			return nil
		}

		return s.mcp.Server(agent.Caller)
	}, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: s.logger})
}

// requireMCP answers 404 unless the MCP surface is on and this control plane
// has both a registry and an authorization server, so a deployment that does
// not serve agents shows nothing to find.
func (s *Server) requireMCP(next http.Handler) http.Handler {
	return s.requireOAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.mcp == nil {
			Respond(w, r, nil, NotFound("That endpoint does not exist."))

			return
		}

		next.ServeHTTP(w, r)
	}))
}

// registerMCPRoutes mounts /mcp and the metadata that points clients at the
// authorization server.
func (s *Server) registerMCPRoutes() {
	handler := s.newMCPHandler()

	s.router.Group(func(r chi.Router) {
		r.Use(s.requireMCP)

		r.HandleFunc(protectedResource, oauthEndpoint(http.MethodGet, s.handleProtectedResource))
		r.HandleFunc(protectedMCPSuffix, oauthEndpoint(http.MethodGet, s.handleProtectedResource))
		r.HandleFunc(mcpPath, s.serveMCP(handler))
		r.HandleFunc(mcpPath+"/", s.serveMCP(handler))
	})
}

// protectedResourceMetadata is the RFC 9728 document.
type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}

func (s *Server) handleProtectedResource(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	Respond(w, r, protectedResourceMetadata{
		Resource:               s.oauth.Resource(),
		AuthorizationServers:   []string{s.oauth.Issuer()},
		ScopesSupported:        oauth.ResourceScopes(),
		BearerMethodsSupported: []string{"header"},
	}, nil)
}

// serveMCP checks where a request came from and who it is for, then hands it to
// the protocol handler. Nothing but a bearer access token is read: a session
// cookie, a query parameter and the operator token are all ignored.
func (s *Server) serveMCP(protocol http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		noStore(w)

		// A browser always sends its page's origin on a cross-origin request.
		// An agent that is not a browser sends none, so one that does is a page
		// reaching for the user's token, and this endpoint is not for pages.
		if origin := r.Header.Get("Origin"); origin != "" && origin != s.oauth.Issuer() {
			Respond(w, r, nil, Forbidden("That origin may not use this endpoint."))

			return
		}

		raw, bearer := agentBearer(r)
		if !bearer {
			s.challenge(w, r, "")

			return
		}

		agent, err := s.oauth.Authenticate(r.Context(), raw, r.URL.Path)
		switch {
		case err == nil:
		case errors.Is(err, service.ErrTokenInvalid), errors.Is(err, service.ErrTokenAudience), errors.Is(err, service.ErrTokenScope):
			s.challenge(w, r, "invalid_token")

			return
		default:
			Respond(w, r, nil, Internal("Signing in did not finish.", err))

			return
		}

		// The agent never sees, and the tools never act on, the request's own
		// credentials: only the caller the token resolved to.
		r.Header.Del("Authorization")
		r.Header.Del("Cookie")
		r = r.WithContext(context.WithValue(r.Context(), mcpAgentKey{}, agent))

		protocol.ServeHTTP(w, r)
	}
}

// challenge answers 401 with the pointer to the protected resource metadata an
// agent reads to find where to sign in. A request with no token carries no
// error code, as RFC 6750 asks, so a client reads it as "sign in" and not as a
// failure of a token it holds.
func (s *Server) challenge(w http.ResponseWriter, r *http.Request, code string) {
	var params []string
	if code != "" {
		params = append(params, fmt.Sprintf("error=%q", code))
	}
	params = append(params,
		fmt.Sprintf("resource_metadata=%q", s.oauth.Issuer()+protectedMCPSuffix),
		fmt.Sprintf("scope=%q", oauth.ScopeRead),
	)
	w.Header().Set("WWW-Authenticate", "Bearer "+strings.Join(params, ", "))
	Respond(w, r, nil, Unauthorized("Sign in to continue."))
}

// agentBearer reads the Authorization header's bearer token.
func agentBearer(r *http.Request) (string, bool) {
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return "", false
	}

	return fields[1], true
}
