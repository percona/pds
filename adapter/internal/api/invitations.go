package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// The codes a refused invitation puts in the sign in page's address. The page
// decides the words.
const (
	errorInviteInvalid     = "invite_invalid"
	errorInviteSpent       = "invite_spent"
	errorInviteExpired     = "invite_expired"
	errorInviteMismatch    = "invite_email_mismatch"
	errorInviteUnverified  = "invite_email_unverified"
	errorInviteAlreadyHere = "invite_already_member"
)

type invitationRequest struct {
	Email    string `json:"email"`
	AnyEmail bool   `json:"anyEmail"`
	Team     string `json:"team" validate:"required"`
	Role     string `json:"role" validate:"required"`
}

type invitationResponse struct {
	ID        uint      `json:"id"`
	Email     string    `json:"email,omitempty"`
	AnyEmail  bool      `json:"anyEmail"`
	Team      string    `json:"team"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
}

// createdInvitationResponse is the one answer that carries the link. The store
// keeps only its hash, so it cannot be read again.
type createdInvitationResponse struct {
	invitationResponse
	Link    string `json:"link"`
	Emailed bool   `json:"emailed"`
}

func invitationBody(view store.InvitationView) invitationResponse {
	return invitationResponse{
		ID: view.ID, Email: view.Email, AnyEmail: view.AnyEmail, Team: view.TeamName, Role: view.Role,
		ExpiresAt: view.ExpiresAt, CreatedAt: view.CreatedAt,
	}
}

func (s *Server) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	org, ok := organizationScope(w, r, s.invitations != nil)
	if !ok {
		return
	}

	pending, err := s.invitations.Pending(r.Context(), org)
	out := make([]invitationResponse, 0, len(pending))
	for _, view := range pending {
		out = append(out, invitationBody(view))
	}
	Respond(w, r, map[string][]invitationResponse{"invitations": out}, invitationError(err))
}

func (s *Server) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	org, ok := organizationScope(w, r, s.invitations != nil)
	if !ok {
		return
	}

	var req invitationRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	result, err := s.invitations.Invite(r.Context(), org, service.Invite{
		Email: req.Email, AnyEmail: req.AnyEmail, Team: req.Team, Role: req.Role,
	})
	s.respondInvitation(w, r, result, err)
}

func (s *Server) handleResendInvitation(w http.ResponseWriter, r *http.Request) {
	org, ok := organizationScope(w, r, s.invitations != nil)
	if !ok {
		return
	}
	id, ok := invitationID(w, r)
	if !ok {
		return
	}

	result, err := s.invitations.Resend(r.Context(), org, id)
	s.respondInvitation(w, r, result, err)
}

func (s *Server) respondInvitation(w http.ResponseWriter, r *http.Request, result service.InviteResult, err error) {
	// The link is a credential until it is claimed.
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		Respond(w, r, nil, invitationError(err))

		return
	}

	RespondWithStatus(w, r, http.StatusCreated, createdInvitationResponse{
		invitationResponse: invitationBody(result.Invitation), Link: result.Link, Emailed: result.Emailed,
	}, nil)
}

func (s *Server) handleRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	org, ok := organizationScope(w, r, s.invitations != nil)
	if !ok {
		return
	}
	id, ok := invitationID(w, r)
	if !ok {
		return
	}

	Respond(w, r, nil, invitationError(s.invitations.Revoke(r.Context(), org, id)))
}

// invitationID reads the invitation in the path. One that is not a number
// cannot name an invitation of this Organization, so it is not found.
func invitationID(w http.ResponseWriter, r *http.Request) (uint, bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		Respond(w, r, nil, NotFound("That invitation does not exist."))

		return 0, false
	}

	return uint(id), true
}

// invitationError renders what inviting can refuse, falling back to what
// changing Teams and Users can.
func invitationError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, service.ErrInviteEmail):
		return Validation("Give one email address, or choose any email.")
	case errors.Is(err, store.ErrAnyEmailRole):
		return Validation("An invitation to any email can only give the user role.")
	case errors.Is(err, store.ErrInvitationPending):
		return Conflict("That address already has a pending invitation. Resend or revoke it.")
	case errors.Is(err, store.ErrAlreadyMember):
		return Conflict("That person already belongs to this organization.")
	case errors.Is(err, store.ErrInvitationUnknown):
		return NotFound("That invitation does not exist.")
	default:
		return directoryError(err)
	}
}

// invitePreviewResponse says what an invitation link is for. The address is
// masked, so a forwarded link does not reveal who it was meant for.
type invitePreviewResponse struct {
	Organization string `json:"organization"`
	Team         string `json:"team"`
	Role         string `json:"role"`
	Email        string `json:"email,omitempty"`
	AnyEmail     bool   `json:"anyEmail"`
}

// invitationLinkError renders an invitation link that cannot be used, in the
// words for a person invited by an admin rather than by Percona.
func invitationLinkError(err error) *AppError {
	switch {
	case errors.Is(err, store.ErrInviteUnknown):
		return NotFound("That invite link is not valid.")
	case errors.Is(err, store.ErrInviteSpent):
		return Conflict("That invite link has already been used. Ask your organization admin for a new one.")
	case errors.Is(err, store.ErrInviteExpired):
		return Conflict("That invite link has expired. Ask your organization admin for a new one.")
	case errors.Is(err, service.ErrPasswordClaimClosed):
		return Conflict("Accept this invitation by signing in with the account it was sent to.")
	case errors.Is(err, store.ErrLoginTaken):
		return Conflict("That login is already taken. Choose another.")
	case errors.Is(err, service.ErrLoginInvalid):
		return Validation("A login starts with a letter and holds only lower case letters, digits and hyphens.")
	case errors.Is(err, store.ErrAlreadyMember):
		return Conflict("You already belong to this organization.")
	default:
		return inviteError(err)
	}
}

// inviteRefusal is the sign in page code for an invitation that could not be
// claimed and the masked address to show beside it, or empty when the failure
// was something else.
func inviteRefusal(err error) (code, masked string) {
	var refusal *service.InviteRefusal
	if errors.As(err, &refusal) {
		masked = refusal.Masked
	}

	switch {
	case errors.Is(err, service.ErrInviteEmailMismatch):
		return errorInviteMismatch, masked
	case errors.Is(err, service.ErrInviteEmailUnverified):
		return errorInviteUnverified, masked
	case errors.Is(err, store.ErrInviteUnknown):
		return errorInviteInvalid, ""
	case errors.Is(err, store.ErrInviteSpent):
		return errorInviteSpent, ""
	case errors.Is(err, store.ErrInviteExpired):
		return errorInviteExpired, ""
	case errors.Is(err, store.ErrAlreadyMember):
		return errorInviteAlreadyHere, ""
	default:
		return "", ""
	}
}

func signInFailedAt(code, masked string) string {
	query := url.Values{"error": {code}}
	if masked != "" {
		query.Set("invited", masked)
	}

	return consoleSignIn + "?" + query.Encode()
}
