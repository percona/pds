package api

import (
	"errors"
	"net/http"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

type createOrganizationRequest struct {
	Name string `json:"name" validate:"required"`
}

type switchOrganizationRequest struct {
	OrganizationID uint `json:"organizationId" validate:"required"`
}

// organizationsOf lists the Organizations behind a signed in person, marking
// the one the session acts in. A password session has none to list.
func (s *Server) organizationsOf(r *http.Request, identity auth.Identity) ([]organizationResponse, error) {
	if identity.Method != store.SignInOIDC {
		return nil, nil
	}

	rows, err := s.identity.Organizations(r.Context(), identity.Scope.UserID)
	if err != nil {
		return nil, err
	}

	out := make([]organizationResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, organizationResponse{
			ID: row.ID, Name: row.Name, Role: row.Role, Current: row.ID == identity.Scope.OrganizationID,
		})
	}

	return out, nil
}

// handleCreateOrganization makes another Organization for the signed in person
// and moves the session into it.
func (s *Server) handleCreateOrganization(w http.ResponseWriter, r *http.Request) {
	if s.identity == nil {
		Respond(w, r, nil, NotImplemented("Sign in is not configured on this control plane."))

		return
	}

	var req createOrganizationRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	err := s.identity.CreateOrganization(r.Context(), sessionCookie(r), req.Name, clientAddress(r))
	Respond(w, r, nil, organizationError(err))
}

// handleSwitchOrganization moves the session into another of the person's
// Organizations.
func (s *Server) handleSwitchOrganization(w http.ResponseWriter, r *http.Request) {
	if s.identity == nil {
		Respond(w, r, nil, NotImplemented("Sign in is not configured on this control plane."))

		return
	}

	var req switchOrganizationRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	err := s.identity.SwitchOrganization(r.Context(), sessionCookie(r), req.OrganizationID)
	Respond(w, r, nil, organizationError(err))
}

// organizationError renders what creating or switching an Organization can
// refuse. An Organization the person does not belong to is not found, whether
// or not it exists.
func organizationError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, service.ErrNotSignedIn):
		return Unauthorized("Sign in to continue.")
	case errors.Is(err, service.ErrNotOIDCSession):
		return Forbidden("Sign in through your identity provider to manage organizations.")
	case errors.Is(err, store.ErrSessionUnknown):
		return Unauthorized("Sign in to continue.")
	case errors.Is(err, service.ErrOrganizationsClosed):
		return Forbidden("Organizations cannot be created or switched on this control plane.")
	case errors.Is(err, service.ErrOrganizationName):
		return Validation("Give the organization a name of up to 200 characters.")
	case errors.Is(err, service.ErrSignUpRateLimited):
		return Validation("Too many organizations were created from this address. Try again in an hour.").
			WithStatus(http.StatusTooManyRequests).WithCause(err)
	case errors.Is(err, store.ErrCreationCap):
		return Conflict("You have created as many organizations as one person can. Contact Percona support.")
	case errors.Is(err, store.ErrOrganizationCeiling):
		return Conflict("New organizations cannot be created right now. Contact Percona support.")
	case errors.Is(err, store.ErrNoMembership):
		return NotFound("That organization does not exist.")
	default:
		return Internal("The organization did not change.", err)
	}
}
