package api

import (
	"errors"
	"mime"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/oauth"
	"github.com/percona/pds/adapter/internal/service"
)

// consentRequest is what the consent screen sends. Request names the parked
// authorization request; the rest is the person's choice and is read only by
// approve. Where the person goes next is never part of it: it comes from the
// request the client's redirect address was checked against.
type consentRequest struct {
	Request        string `json:"request" validate:"required,max=256"`
	OrganizationID uint   `json:"organizationId"`
	TeamID         uint   `json:"teamId"`
	Access         string `json:"access"`
	Passwords      bool   `json:"passwords"`
	SQLWrites      bool   `json:"sqlWrites"`
}

type consentClient struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	Kind         string `json:"kind"`
	RedirectHost string `json:"redirectHost"`
	Loopback     bool   `json:"loopback"`
}

type consentTeam struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

type consentOrganization struct {
	ID        uint          `json:"id"`
	Name      string        `json:"name"`
	Role      string        `json:"role"`
	Teams     []consentTeam `json:"teams"`
	OwnTeamID uint          `json:"ownTeamId"`
	Access    string        `json:"access"`
	Passwords bool          `json:"passwords"`
	SQLWrites bool          `json:"sqlWrites"`
}

type consentResponse struct {
	Client        consentClient         `json:"client"`
	ExpiresAt     time.Time             `json:"expiresAt"`
	Organizations []consentOrganization `json:"organizations"`
}

type consentRedirect struct {
	RedirectURL string `json:"redirectUrl"`
}

// registerConsentRoutes mounts the calls the consent screen makes. They answer
// the console session and nothing else: the in process callers and the
// operator token that other routes accept are not a person at a browser, and
// a person is who consents. Every call is a POST so that no link can carry an
// approval.
func (s *Server) registerConsentRoutes(open chi.Router) {
	open.Group(func(c chi.Router) {
		c.Use(s.requireOAuth)

		c.Route("/agent-consent", func(consent chi.Router) {
			consent.Post("/lookup", s.consentCall(s.lookupConsent))
			consent.Post("/approve", s.consentCall(s.approveConsent))
			consent.Post("/deny", s.consentCall(s.denyConsent))
		})
	})
}

// consentCall runs one of the consent calls for a person with a console
// session. It refuses a request the browser itself marks as coming from
// another site, and a body that is not JSON, which a page on another site
// cannot send without the browser asking this server first.
func (s *Server) consentCall(
	call func(w http.ResponseWriter, r *http.Request, who auth.Identity, req consentRequest) error,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		noStore(w)

		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
			Respond(w, r, nil, Forbidden("This request did not come from the console."))

			return
		}
		if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
			Respond(w, r, nil, Validation("Send the request as JSON.").WithStatus(http.StatusUnsupportedMediaType))

			return
		}

		who, err := s.identity.Resolve(r.Context(), sessionCookie(r))
		if err != nil {
			Respond(w, r, nil, signInError(err))

			return
		}

		var req consentRequest
		if err := s.decode(r, &req); err != nil {
			Respond(w, r, nil, err)

			return
		}

		if err := call(w, r, who, req); err != nil {
			Respond(w, r, nil, consentError(err))
		}
	}
}

func (s *Server) lookupConsent(w http.ResponseWriter, r *http.Request, who auth.Identity, req consentRequest) error {
	screen, err := s.oauth.ConsentScreen(r.Context(), who, req.Request)
	if err != nil {
		return err
	}

	organizations := make([]consentOrganization, 0, len(screen.Organizations))
	for _, o := range screen.Organizations {
		teams := make([]consentTeam, 0, len(o.Teams))
		for _, t := range o.Teams {
			teams = append(teams, consentTeam{ID: t.ID, Name: t.Name})
		}
		organizations = append(organizations, consentOrganization{
			ID: o.ID, Name: o.Name, Role: o.Role, Teams: teams, OwnTeamID: o.OwnTeamID,
			Access: string(o.Access), Passwords: o.Passwords, SQLWrites: o.SQLWrites,
		})
	}

	Respond(w, r, consentResponse{
		Client: consentClient{
			Name: screen.ClientName, Host: screen.ClientHost, Kind: screen.ClientKind,
			RedirectHost: screen.RedirectHost, Loopback: screen.LoopbackRedirect,
		},
		ExpiresAt: screen.ExpiresAt, Organizations: organizations,
	}, nil)

	return nil
}

func (s *Server) approveConsent(w http.ResponseWriter, r *http.Request, who auth.Identity, req consentRequest) error {
	redirect, err := s.oauth.Approve(r.Context(), who, req.Request, service.Consent{
		OrganizationID: req.OrganizationID, TeamID: req.TeamID,
		Grants: oauth.Grants{Access: auth.Access(req.Access), Passwords: req.Passwords, SQLWrites: req.SQLWrites},
	})
	if err != nil {
		return err
	}

	Respond(w, r, consentRedirect{RedirectURL: redirect}, nil)

	return nil
}

func (s *Server) denyConsent(w http.ResponseWriter, r *http.Request, who auth.Identity, req consentRequest) error {
	redirect, err := s.oauth.Deny(r.Context(), who, req.Request)
	if err != nil {
		return err
	}

	Respond(w, r, consentRedirect{RedirectURL: redirect}, nil)

	return nil
}

// consentError renders what answering a consent request can refuse. A request
// that is not the person's reads the same as one that never existed, and an
// Organization or Team they cannot use is not found, as everywhere else.
func consentError(err error) *AppError {
	switch {
	case errors.Is(err, service.ErrAuthorizationGone):
		return NotFound("That sign in request has expired or was already answered. Start again from the app.")
	case errors.Is(err, service.ErrConsentTarget):
		return NotFound("That organization and team cannot be used for this sign in. Choose another.")
	case errors.Is(err, service.ErrConsentGrants):
		return Validation("Those permissions are more than this organization or this app allows. Choose less.")
	default:
		return Internal(somethingWentWrong, err)
	}
}
