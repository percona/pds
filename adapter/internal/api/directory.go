package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

type teamResponse struct {
	Name string `json:"name"`
}

type memberResponse struct {
	ID    uint   `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	Team  string `json:"team"`
	Role  string `json:"role"`
}

type teamRequest struct {
	Name string `json:"name" validate:"required"`
}

type organizationRequest struct {
	Name string `json:"name" validate:"required"`
}

// aiSettings is what the Organization lets agents and the Chat do. A request
// that leaves the tier out is refused, so a client cannot clear it by omission.
type aiSettings struct {
	Access    string `json:"access"`
	Passwords bool   `json:"passwords"`
	SQLWrites bool   `json:"sqlWrites"`
}

// memberRequest changes a User. A field left out is left alone.
type memberRequest struct {
	Team *string `json:"team"`
	Role *string `json:"role"`
}

// organizationOf is the Organization the admin acts in, from the session.
func (s *Server) organizationOf(w http.ResponseWriter, r *http.Request) (uint, bool) {
	return organizationScope(w, r, s.directory != nil)
}

// organizationScope answers 501 when the service behind the route is absent,
// and 404 when the session names no Organization.
func organizationScope(w http.ResponseWriter, r *http.Request, available bool) (uint, bool) {
	if !available {
		Respond(w, r, nil, NotImplemented("Sign in is not configured on this control plane."))

		return 0, false
	}

	scope, ok := auth.ScopeFromContext(r.Context())
	if !ok || scope.OrganizationID == 0 {
		Respond(w, r, nil, NotFound("There is no organization to manage here."))

		return 0, false
	}

	return scope.OrganizationID, true
}

func (s *Server) handleRenameOrganization(w http.ResponseWriter, r *http.Request) {
	org, ok := s.organizationOf(w, r)
	if !ok {
		return
	}

	var req organizationRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	Respond(w, r, nil, directoryError(s.directory.RenameOrganization(r.Context(), org, req.Name)))
}

// handleGetAI answers every member of the Organization.
func (s *Server) handleGetAI(w http.ResponseWriter, r *http.Request) {
	org, ok := s.organizationOf(w, r)
	if !ok {
		return
	}

	settings, err := s.directory.AI(r.Context(), org)
	if err != nil {
		Respond(w, r, nil, directoryError(err))

		return
	}
	Respond(w, r, aiSettings{Access: settings.Access, Passwords: settings.Passwords, SQLWrites: settings.SQLWrites}, nil)
}

func (s *Server) handleSetAI(w http.ResponseWriter, r *http.Request) {
	org, ok := s.organizationOf(w, r)
	if !ok {
		return
	}

	var req aiSettings
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	err := s.directory.SetAI(r.Context(), org, store.AISettings{Access: req.Access, Passwords: req.Passwords, SQLWrites: req.SQLWrites})
	if err != nil {
		Respond(w, r, nil, directoryError(err))

		return
	}
	Respond(w, r, req, nil)
}

func (s *Server) handleListTeams(w http.ResponseWriter, r *http.Request) {
	org, ok := s.organizationOf(w, r)
	if !ok {
		return
	}

	teams, err := s.directory.Teams(r.Context(), org)
	out := make([]teamResponse, 0, len(teams))
	for _, team := range teams {
		out = append(out, teamResponse{Name: team.Name})
	}
	Respond(w, r, map[string][]teamResponse{"teams": out}, directoryError(err))
}

func (s *Server) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	org, ok := s.organizationOf(w, r)
	if !ok {
		return
	}

	var req teamRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	team, err := s.directory.CreateTeam(r.Context(), org, req.Name)
	if err != nil {
		Respond(w, r, nil, directoryError(err))

		return
	}
	RespondWithStatus(w, r, http.StatusCreated, teamResponse{Name: team.Name}, nil)
}

func (s *Server) handleRenameTeam(w http.ResponseWriter, r *http.Request) {
	org, ok := s.organizationOf(w, r)
	if !ok {
		return
	}

	var req teamRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	Respond(w, r, nil, directoryError(s.directory.RenameTeam(r.Context(), org, chi.URLParam(r, "name"), req.Name)))
}

func (s *Server) handleDeleteTeam(w http.ResponseWriter, r *http.Request) {
	org, ok := s.organizationOf(w, r)
	if !ok {
		return
	}

	Respond(w, r, nil, directoryError(s.directory.DeleteTeam(r.Context(), org, chi.URLParam(r, "name"))))
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	org, ok := s.organizationOf(w, r)
	if !ok {
		return
	}

	members, err := s.directory.Members(r.Context(), org)
	out := make([]memberResponse, 0, len(members))
	for _, member := range members {
		out = append(out, memberResponse{
			ID: member.UserID, Name: member.Name, Email: member.Email, Team: member.TeamName, Role: member.Role,
		})
	}
	Respond(w, r, map[string][]memberResponse{"users": out}, directoryError(err))
}

func (s *Server) handleUpdateMember(w http.ResponseWriter, r *http.Request) {
	org, ok := s.organizationOf(w, r)
	if !ok {
		return
	}

	var req memberRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}
	id, ok := userID(w, r)
	if !ok {
		return
	}

	change := service.MemberChange{Team: req.Team, Role: req.Role}
	Respond(w, r, nil, directoryError(s.directory.UpdateMember(r.Context(), org, id, change)))
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	org, ok := s.organizationOf(w, r)
	if !ok {
		return
	}
	id, ok := userID(w, r)
	if !ok {
		return
	}

	Respond(w, r, nil, directoryError(s.directory.RemoveMember(r.Context(), org, id)))
}

// userID reads the User in the path. One that is not a number cannot name a
// User of this Organization, so it is not found.
func userID(w http.ResponseWriter, r *http.Request) (uint, bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		Respond(w, r, nil, NotFound("That user does not exist."))

		return 0, false
	}

	return uint(id), true
}

// directoryError renders what changing Teams and Users can refuse. A User or
// Team of another Organization is not found, whether or not it exists.
func directoryError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, service.ErrTeamName):
		return Validation("Give the team a name of up to 128 characters.")
	case errors.Is(err, service.ErrTeamNameReserved):
		return Validation("The name all is reserved for viewing every team. Choose another name.")
	case errors.Is(err, service.ErrAIAccess):
		return Validation("Choose read, change, or change and delete.")
	case errors.Is(err, service.ErrOrganizationName):
		return Validation("Give the organization a name of up to 200 characters.")
	case errors.Is(err, store.ErrOrganizationUnknown):
		return NotFound("There is no organization to manage here.")
	case errors.Is(err, service.ErrMemberChange):
		return Validation("Name a team, a role, or both.")
	case errors.Is(err, store.ErrRoleUnknown):
		return Validation("A role is either user or admin.")
	case errors.Is(err, store.ErrTeamExists):
		return Conflict("A team with that name already exists.")
	case errors.Is(err, store.ErrTeamHasUsers):
		return Conflict("That team still has users. Move or remove them first.")
	case errors.Is(err, store.ErrTeamHasEnvironments):
		return Conflict("That team still has environments. Delete them first.")
	case errors.Is(err, store.ErrLastAdmin):
		return Conflict("An organization needs at least one admin.")
	case errors.Is(err, store.ErrTeamUnknown):
		return NotFound("That team does not exist.")
	case errors.Is(err, store.ErrNoMembership):
		return NotFound("That user does not exist.")
	default:
		return Internal("The change did not finish. Try again.", err)
	}
}
