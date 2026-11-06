package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

const (
	// oidcFlowCookie carries a sign in across the trip to the issuer. It is
	// scoped to the OIDC routes so no other request sends it.
	oidcFlowCookie = "pds_oidc_flow"
	oidcPath       = "/api/auth/oidc"

	// consoleHome and consoleSignIn are where a finished sign in sends the
	// browser. The console is served under /app/, because the root of the host
	// is the public landing page, and the sign in page is a route of it.
	consoleHome   = "/app/"
	consoleSignIn = "/app/login"

	// The codes a failed sign in puts in the sign in page's address. The page
	// decides the words; the adapter never says why a sign in failed beyond
	// these two.
	errorNoMembership  = "no_membership"
	errorSignInFailure = "sign_in_failed"

	// The codes signup adds. Each says which gate refused and nothing about
	// the person's other records.
	errorEmailUnverified   = "email_unverified"
	errorSignupRateLimited = "signup_rate_limited"
	errorSignupUnavailable = "signup_unavailable"
	errorSignupLimit       = "signup_limit_reached"
	errorOrganizationName  = "organization_name_invalid"
)

// handleAuthMethods lists how this control plane lets people sign in, so the
// signed out page knows what to offer. The identity provider comes first.
func (s *Server) handleAuthMethods(w http.ResponseWriter, r *http.Request) {
	methods := []string{store.SignInPassword}
	if s.oidc != nil {
		methods = []string{store.SignInOIDC, store.SignInPassword}
	}

	Respond(w, r, methodsResponse{Methods: methods}, nil)
}

type methodsResponse struct {
	Methods []string `json:"methods"`
}

// handleOIDCStart sends the browser to the issuer, remembering the flow in a
// short lived signed cookie.
func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil || s.identity == nil {
		Respond(w, r, nil, NotFound("That sign in method is not available."))

		return
	}

	query := r.URL.Query()

	// The provider only picks which sign in the issuer shows. Anything the
	// console does not offer is not a method here.
	var connection string
	if provider := strings.TrimSpace(query.Get("provider")); provider != "" {
		var offered bool
		if connection, offered = auth.OIDCConnection(provider); !offered {
			Respond(w, r, nil, NotFound("That sign in method is not available."))

			return
		}
	}

	// The name is for an Organization this sign in might create, and is kept
	// across the trip so the callback does not depend on the browser sending
	// it again.
	var organization string
	if raw := strings.TrimSpace(query.Get("organization")); raw != "" {
		var err error
		if organization, err = service.OrganizationName(raw); err != nil {
			s.signInFailed(w, r, errorOrganizationName)

			return
		}
	}

	to, flow, err := s.oidc.Start(s.publicURL()+oidcPath+"/callback", connection)
	if err != nil {
		Respond(w, r, nil, Internal("Signing in did not finish.", err))

		return
	}

	// An invitation link carries its token through the sign in, so the claim
	// is made with the address the issuer verifies at the end of it.
	flow.Invite = strings.TrimSpace(query.Get("invite"))
	flow.Organization = organization

	sealed, err := s.identity.SealOIDCFlow(r.Context(), flow)
	if err != nil {
		Respond(w, r, nil, Internal("Signing in did not finish.", err))

		return
	}

	http.SetCookie(w, s.oidcFlowCookie(r, sealed, int(auth.OIDCFlowTTL/time.Second)))
	http.Redirect(w, r, to, http.StatusFound)
}

// handleOIDCCallback finishes a sign in. Every failure goes back to the sign
// in page with a code, never to an API error: the browser is mid navigation
// and a JSON body would be the last thing it shows.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil || s.identity == nil {
		Respond(w, r, nil, NotFound("That sign in method is not available."))

		return
	}

	// The flow is single use whatever happens next.
	http.SetCookie(w, s.oidcFlowCookie(r, "", -1))

	flow, ok, err := s.identity.OpenOIDCFlow(r.Context(), oidcFlowValue(r))
	if err != nil {
		s.logger.ErrorContext(r.Context(), "oidc sign in could not open the flow", "err", err)
		s.signInFailed(w, r, errorSignInFailure)

		return
	}

	query := r.URL.Query()
	if !ok || query.Get("error") != "" || query.Get("state") != flow.State || query.Get("code") == "" {
		s.signInFailed(w, r, errorSignInFailure)

		return
	}

	claims, err := s.oidc.Complete(r.Context(), s.publicURL()+oidcPath+"/callback", query.Get("code"), flow)
	if err != nil {
		s.logger.WarnContext(r.Context(), "oidc sign in was refused by the issuer", "err", err)
		s.signInFailed(w, r, errorSignInFailure)

		return
	}

	session, err := s.identity.SignInOIDC(r.Context(), s.oidc.Issuer(), claims, clientAddress(r), flow.Invite, flow.Organization)
	if err != nil {
		if code, masked := inviteRefusal(err); code != "" {
			http.Redirect(w, r, signInFailedAt(code, masked), http.StatusFound)

			return
		}
		if code := signUpRefusal(err); code != "" {
			s.signInFailed(w, r, code)

			return
		}
		s.logger.ErrorContext(r.Context(), "oidc sign in could not open a session", "err", err)
		s.signInFailed(w, r, errorSignInFailure)

		return
	}

	http.SetCookie(w, s.newSessionCookie(r, session))
	http.Redirect(w, r, consoleHome, http.StatusFound)
}

// handleOIDCLogout ends the session and, for one the issuer opened, the
// issuer's own, so signing out really signs out. It is a POST so a link on
// another site cannot sign anybody out, and it answers 303 so the browser
// follows with a GET: the issuer's page has to load as a navigation.
func (s *Server) handleOIDCLogout(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil || s.identity == nil {
		Respond(w, r, nil, NotFound("That sign in method is not available."))

		return
	}

	identity, _ := s.identity.Resolve(r.Context(), sessionCookie(r))
	if err := s.identity.Logout(r.Context(), sessionCookie(r)); err != nil {
		Respond(w, r, nil, Internal("Signing out did not finish.", err))

		return
	}
	http.SetCookie(w, s.clearedSessionCookie(r))

	home := s.publicURL() + "/"
	if identity.Method == store.SignInOIDC {
		if to := s.oidc.LogoutURL(home); to != "" {
			http.Redirect(w, r, to, http.StatusSeeOther)

			return
		}
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// signUpRefusal is the sign in page code for a sign in refused as a limit,
// or empty when it failed for another reason.
func signUpRefusal(err error) string {
	switch {
	case errors.Is(err, store.ErrNoMembership):
		return errorNoMembership
	case errors.Is(err, service.ErrSignUpUnverified):
		return errorEmailUnverified
	case errors.Is(err, service.ErrSignUpRateLimited):
		return errorSignupRateLimited
	case errors.Is(err, store.ErrOrganizationCeiling):
		return errorSignupUnavailable
	case errors.Is(err, store.ErrCreationCap):
		return errorSignupLimit
	case errors.Is(err, service.ErrOrganizationName):
		return errorOrganizationName
	default:
		return ""
	}
}

func (s *Server) signInFailed(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, consoleSignIn+"?error="+code, http.StatusFound)
}

// publicURL is the address customers reach this control plane at, with no
// trailing slash. It is the configured one and never the request's Host, which
// a caller chooses and which would become the redirect target.
func (s *Server) publicURL() string {
	return strings.TrimRight(strings.TrimSpace(s.cfg.HubPublicURL), "/")
}

func (s *Server) oidcFlowCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	cookie := &http.Cookie{
		Name:     oidcFlowCookie,
		Value:    value,
		Path:     oidcPath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.secureCookies(r),
		// Lax is what lets the issuer's redirect back carry it.
		SameSite: http.SameSiteLaxMode,
	}
	if maxAge < 0 {
		cookie.Expires = time.Unix(0, 0)
	}

	return cookie
}

func oidcFlowValue(r *http.Request) string {
	cookie, err := r.Cookie(oidcFlowCookie)
	if err != nil {
		return ""
	}

	return cookie.Value
}
