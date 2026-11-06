package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/features"
	"github.com/percona/pds/adapter/internal/oauth"
	"github.com/percona/pds/adapter/internal/service"
)

// passwordsNotAllowed is what an agent is told when its sign in does not carry
// the passwords permission today, whether it never did or the Organization has
// since turned the switch off.
const passwordsNotAllowed = "This agent is not allowed to read database passwords. Ask an organization admin to allow them, then sign in again."

// teamPinnedKey marks a request whose caller is bound to the Team it signed in
// for. An agent's token is approved for one Team, so even an Organization admin
// behind it never moves to another.
type teamPinnedKey struct{}

func teamPinned(ctx context.Context) bool {
	pinned, _ := ctx.Value(teamPinnedKey{}).(bool)

	return pinned
}

// mayActAcrossTeams reports whether the scope may act in a Team other than its
// own: an Organization admin, unless the request is pinned to one Team.
func mayActAcrossTeams(ctx context.Context, scope auth.Scope) bool {
	return scope.Admin() && !teamPinned(ctx)
}

// agentCredentialAuthenticator lets an agent's access token reach the database
// credential route, the one API route besides /mcp a token is bound to. Every
// other route meets the session authenticator, which never reads a token.
//
// A token is judged afresh on every call: Authenticate reads the session, the
// person's membership and the Organization's switch as they stand, so a session
// revoked or a switch turned off an instant ago refuses this request.
type agentCredentialAuthenticator struct {
	server  *Server
	session auth.Authenticator
}

func (a agentCredentialAuthenticator) Middleware(next http.Handler) http.Handler {
	session := a.session.Middleware(next)
	s := a.server

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !oauth.CredentialRoute(r.URL.Path) {
			session.ServeHTTP(w, r)

			return
		}
		noStore(w)

		// A request this process built for itself, such as a tool call or the
		// Chat, carries a caller no HTTP request can. No tool reads a password,
		// and a future one must not be able to by reaching this route.
		if _, inProcess := auth.CallerFromContext(r.Context()); inProcess {
			Respond(w, r, nil, Forbidden("Database passwords are not available to this tool. Copy the connection string in the console."))

			return
		}

		raw, bearer := agentBearer(r)
		if !bearer || s.oauth == nil || !s.features.Enabled(r.Context(), features.MCP) ||
			auth.BearerMatches(s.cfg.HubOperatorToken, r.Header.Get("Authorization")) {
			session.ServeHTTP(w, r)

			return
		}

		agent, err := s.oauth.Authenticate(r.Context(), raw, r.URL.Path)
		switch {
		case err == nil:
		case errors.Is(err, service.ErrTokenScope):
			Respond(w, r, nil, Forbidden(passwordsNotAllowed))

			return
		case errors.Is(err, service.ErrTokenInvalid), errors.Is(err, service.ErrTokenAudience):
			s.challenge(w, r, "invalid_token")

			return
		default:
			Respond(w, r, nil, Internal("Signing in did not finish.", err))

			return
		}

		identity := agent.Caller.Identity
		if identity.Scope.OrganizationID == 0 || !auth.ValidAgent(identity.Client.Agent) {
			s.challenge(w, r, "invalid_token")

			return
		}

		ctx := context.WithValue(auth.WithIdentity(r.Context(), identity), teamPinnedKey{}, true)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
