package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
)

const somethingWentWrong = "Something went wrong on our side. Try again."

const (
	// teamParam names the Team a request acts in. Absent means the caller's own.
	teamParam = "team"

	// allTeams asks a list for every Team of the caller's Organization.
	allTeams = "all"
)

// listsAcrossTeams are the routes that answer team=all. Any other route
// refuses it, because a handler that ignored it would quietly answer for one
// Team while the caller believed they were reading all of them.
var listsAcrossTeams = map[string]bool{
	"/api/environments": true,
	"/api/databases":    true,
	"/api/activity":     true,
}

type everyTeamKey struct{}

// actingTeam moves the request into the Team named by ?team=. A User may only
// name their own Team, and any other answers 404 whether or not it exists. An
// admin may name any Team of their Organization, and a Team of another
// Organization is not found. With no team named the request stays in the
// caller's own Team.
func (s *Server) actingTeam(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope, ok := auth.ScopeFromContext(r.Context())
		name := strings.TrimSpace(r.URL.Query().Get(teamParam))
		if !ok || name == "" {
			next.ServeHTTP(w, r)

			return
		}

		if name == allTeams {
			switch {
			case teamPinned(r.Context()):
				Respond(w, r, nil, Forbidden("This agent works in the team it signed in for only."))
			case !scope.Admin():
				Respond(w, r, nil, Forbidden("Only an organization admin can list every team."))
			case !listsAcrossTeams[strings.TrimSuffix(r.URL.Path, "/")] || r.Method != http.MethodGet:
				Respond(w, r, nil, Validation("Listing every team is only available on lists."))
			default:
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), everyTeamKey{}, true)))
			}

			return
		}

		team, err := s.team(r.Context(), scope, name)
		if err != nil {
			Respond(w, r, nil, err)

			return
		}

		next.ServeHTTP(w, r.WithContext(auth.WithActingTeam(r.Context(), team)))
	})
}

// team resolves a Team of the caller's Organization by name. A User reaches
// their own Team only.
func (s *Server) team(ctx context.Context, scope auth.Scope, name string) (auth.Team, *AppError) {
	notFound := NotFound("That team does not exist.")
	if s.identity == nil {
		return auth.Team{}, notFound
	}

	team, err := s.identity.Team(ctx, scope.OrganizationID, name)
	if err != nil {
		if errors.Is(err, service.ErrTeamUnknown) {
			return auth.Team{}, notFound
		}

		return auth.Team{}, Internal(somethingWentWrong, err)
	}
	if !mayActAcrossTeams(ctx, scope) && team.ID != scope.Team.ID {
		return auth.Team{}, notFound
	}

	return team, nil
}

// teamsOfList is the Teams a list reads: every Team of the Organization when
// the request asked for them all, and nil otherwise, which means the request's
// own acting Team.
func (s *Server) teamsOfList(r *http.Request) ([]auth.Team, *AppError) {
	if all, _ := r.Context().Value(everyTeamKey{}).(bool); !all {
		return nil, nil
	}

	scope, _ := auth.ScopeFromContext(r.Context())
	teams, err := s.identity.Teams(r.Context(), scope.OrganizationID)
	if err != nil {
		return nil, Internal(somethingWentWrong, err)
	}

	return teams, nil
}

// objectTeam finds the Team an environment belongs to when the request did not
// name one. The caller's Team is tried first. An environment that lives in
// another Team of the Organization is that Team's object: an admin acts in it,
// and anybody else is told it does not exist. An environment found nowhere
// is left to the route, which answers for an unknown name as it always has.
func (s *Server) objectTeam(param string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope, ok := auth.ScopeFromContext(r.Context())
			name := strings.TrimSpace(chi.URLParam(r, param))
			if !ok || name == "" || r.URL.Query().Get(teamParam) != "" || s.hasEnvironment(r.Context(), name) {
				next.ServeHTTP(w, r)

				return
			}

			teams, err := s.identity.Teams(r.Context(), scope.OrganizationID)
			if err != nil {
				Respond(w, r, nil, Internal(somethingWentWrong, err))

				return
			}
			for _, team := range teams {
				if team.ID == scope.Team.ID {
					continue
				}
				acting := auth.WithActingTeam(r.Context(), team)
				if !s.hasEnvironment(acting, name) {
					continue
				}
				if !mayActAcrossTeams(r.Context(), scope) {
					Respond(w, r, nil, NotFound("That environment does not exist."))

					return
				}

				next.ServeHTTP(w, r.WithContext(acting))

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// hasEnvironment reports whether the Team the context acts in has connected
// the environment.
func (s *Server) hasEnvironment(ctx context.Context, name string) bool {
	if s.databases == nil {
		return false
	}
	hubSvc, err := s.databases.Hub(ctx)
	if err != nil || hubSvc == nil {
		return false
	}
	for _, environment := range hubSvc.Environments() {
		if environment.Name == name {
			return true
		}
	}

	return false
}

// requireAdmin guards the routes only an Organization admin may use. A User
// gets 403: the route exists, and the answer says it is a permission.
//
// A caller with no Organization is a route mounted outside the session door by
// mistake, or the pass-through stub. A login passes as it did before
// Organizations, and a bare caller needs the operator token. The token itself
// never arrives here without an Organization: the door refuses it where many
// are served.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if scope, ok := auth.ScopeFromContext(r.Context()); ok {
			if !scope.Admin() {
				Respond(w, r, nil, Forbidden("Only an organization admin can do that."))

				return
			}

			next.ServeHTTP(w, r)

			return
		}

		s.requireCaller(next).ServeHTTP(w, r)
	})
}

// eachTeam runs read once per Team a list covers: the Team the request acts
// in, or every Team of the Organization when it asked for all of them. all is
// true in the second case, where each row should say which Team it is in.
func (s *Server) eachTeam(r *http.Request, read func(ctx context.Context, team auth.Team, all bool) *AppError) *AppError {
	teams, appErr := s.teamsOfList(r)
	if appErr != nil {
		return appErr
	}
	if teams == nil {
		return read(r.Context(), auth.Team{}, false)
	}
	for _, team := range teams {
		if appErr := read(auth.WithActingTeam(r.Context(), team), team, true); appErr != nil {
			return appErr
		}
	}

	return nil
}
