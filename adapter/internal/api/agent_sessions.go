package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
)

// maxAgentSessionID bounds the id read from the path. A session id is 32
// characters, so anything longer cannot name one.
const maxAgentSessionID = 64

// agentSessionResponse is one connected agent. It carries what a person needs
// to judge it: who signed it in, where it acts, what it may do and when it
// last connected. The client's name and host are what the client declared,
// and the console says so. It carries no token, hash or client id.
type agentSessionResponse struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Host       string    `json:"host"`
	Kind       string    `json:"kind"`
	Member     string    `json:"member"`
	Mine       bool      `json:"mine"`
	Team       string    `json:"team"`
	Access     string    `json:"access"`
	Passwords  bool      `json:"passwords"`
	SQLWrites  bool      `json:"sqlWrites"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// registerAgentSessionRoutes mounts the console's list of connected agents and
// the call that revokes one. They answer the console session cookie and
// nothing else, as the consent calls do, and 404 while the MCP surface is off.
func (s *Server) registerAgentSessionRoutes(open chi.Router) {
	open.Group(func(c chi.Router) {
		c.Use(s.requireOAuth)

		c.Route("/agent-sessions", func(sessions chi.Router) {
			sessions.Get("/", s.consoleSession(s.handleListAgentSessions))
			sessions.Delete("/{id}", s.consoleSession(s.handleRevokeAgentSession))
		})
	})
}

// consoleSession runs a call for a person with a console session. A call that
// changes anything must come from the console itself: the browser marks a
// request from another site, and a DELETE cannot be sent across sites without
// the browser asking this server first.
func (s *Server) consoleSession(call func(w http.ResponseWriter, r *http.Request, who auth.Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		noStore(w)

		if r.Method != http.MethodGet {
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
				Respond(w, r, nil, Forbidden("This request did not come from the console."))

				return
			}
		}

		who, err := s.identity.Resolve(r.Context(), sessionCookie(r))
		if err != nil {
			Respond(w, r, nil, signInError(err))

			return
		}

		call(w, r, who)
	}
}

func (s *Server) handleListAgentSessions(w http.ResponseWriter, r *http.Request, who auth.Identity) {
	views, err := s.oauth.AgentSessions(r.Context(), who.Scope)
	if err != nil {
		Respond(w, r, nil, Internal(somethingWentWrong, err))

		return
	}

	out := make([]agentSessionResponse, 0, len(views))
	for _, v := range views {
		out = append(out, agentSessionResponse{
			ID: v.ID, Name: v.ClientName, Host: v.ClientHost, Kind: v.ClientKind, Member: v.Member, Mine: v.Mine,
			Team: v.TeamName, Access: v.Access, Passwords: v.Passwords, SQLWrites: v.SQLWrites,
			CreatedAt: v.CreatedAt, LastUsedAt: v.LastUsedAt, ExpiresAt: v.ExpiresAt,
		})
	}

	Respond(w, r, map[string][]agentSessionResponse{"sessions": out}, nil)
}

func (s *Server) handleRevokeAgentSession(w http.ResponseWriter, r *http.Request, who auth.Identity) {
	id := chi.URLParam(r, "id")
	if len(id) > maxAgentSessionID {
		Respond(w, r, nil, NotFound(agentGone))

		return
	}

	err := s.oauth.RevokeAgentSession(auth.WithIdentity(r.Context(), who), who.Scope, id)
	switch {
	case errors.Is(err, service.ErrAgentSessionNotFound):
		Respond(w, r, nil, NotFound(agentGone))
	case err != nil:
		Respond(w, r, nil, Internal(somethingWentWrong, err))
	default:
		Respond(w, r, nil, nil)
	}
}

const agentGone = "That agent is not connected any more."
