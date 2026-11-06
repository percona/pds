package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// SessionResolver turns a session cookie into the identity behind it. The
// identity service satisfies it.
type SessionResolver interface {
	Resolve(ctx context.Context, cookie string) (auth.Identity, error)
	// Operator is who the hub operator token acts as.
	Operator(ctx context.Context) (auth.Identity, error)
}

// sessionAuthenticator requires a signed session cookie on every API route.
//
// It lives beside the handlers rather than in the auth package because
// refusing a request means rendering the adapter's own error shape, and that
// renderer is here. What the auth package owns is the cookie and the identity;
// what this owns is the HTTP answer.
type sessionAuthenticator struct {
	resolver SessionResolver
	// operatorToken is the way in for a caller that has no cookie to offer,
	// which is a script or curl. It is checked here rather than only deeper in
	// the stack because this middleware would otherwise refuse the request
	// before anything else could accept it.
	operatorToken string
}

// operatorSubject is who a request authenticated by the hub operator token
// acted as. It is deliberately not a username: the activity log should not
// record a script as the customer.
const operatorSubject = "operator"

// NewSessionAuthenticator returns the authenticator that requires a session,
// or the hub operator token from a caller that has none.
//
// It stands behind the same interface the pass-through stub does, so no route
// or handler moves when a deployment turns login on.
func NewSessionAuthenticator(resolver SessionResolver, operatorToken string) auth.Authenticator {
	return &sessionAuthenticator{resolver: resolver, operatorToken: operatorToken}
}

// Middleware attaches the identity behind a request and refuses anything that
// carries neither a live session nor the operator token.
func (a *sessionAuthenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, err := a.resolver.Resolve(r.Context(), sessionCookie(r))
		if err != nil {
			if !a.holdsOperatorToken(r) {
				Respond(w, r, nil, signInError(err))

				return
			}

			identity, err = a.operatorIdentity(r)
			if err != nil {
				Respond(w, r, nil, Internal(somethingWentWrong, err))

				return
			}
			if identity.Scope.OrganizationID == 0 {
				Respond(w, r, nil, Unauthorized("The operator token needs a control plane that serves one organization."))

				return
			}
		}

		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
	})
}

// holdsOperatorToken reports whether the caller sent the hub operator token.
// An empty configured token never matches, so an adapter that has none cannot
// be opened by sending none.
func (a *sessionAuthenticator) holdsOperatorToken(r *http.Request) bool {
	return auth.BearerMatches(a.operatorToken, r.Header.Get("Authorization"))
}

// operatorIdentity is the identity of a caller holding the hub operator token:
// an admin of the Organization a single-Organization deployment serves, or one
// with no Organization where many are served.
func (a *sessionAuthenticator) operatorIdentity(r *http.Request) (auth.Identity, error) {
	identity, err := a.resolver.Operator(r.Context())
	if err != nil {
		return auth.Identity{}, err
	}
	identity.Subject = operatorSubject

	return identity, nil
}

// loginRequest is the sign in form. The password is read here and goes no
// further than the service that hashes it: no handler downstream, no log line,
// and no response field carries it.
type loginRequest struct {
	Username string `json:"username" validate:"required"`
	Password string `json:"password" validate:"required"`
}

// identityResponse names who the caller is. The login, session and invite routes
// give back nothing else: the session itself rides in a cookie the browser
// cannot read.
//
// ControlPlaneVersion is the release this control plane was built from, for
// diagnosis by a signed-in caller.
type identityResponse struct {
	Username            string `json:"username"`
	ControlPlaneVersion string `json:"controlPlaneVersion,omitempty"`

	// User, Organization, Team and Role say who the session is in the
	// Organization model, for the console to show. They are what a person
	// reads, never what a decision keys on.
	User         string `json:"user,omitempty"`
	Organization string `json:"organization,omitempty"`
	Team         string `json:"team,omitempty"`
	Role         string `json:"role,omitempty"`

	// Organizations lists every Organization a person belongs to, for the
	// switcher. Only a person signed in through the identity provider has
	// any to switch between.
	Organizations []organizationResponse `json:"organizations,omitempty"`
}

type organizationResponse struct {
	ID      uint   `json:"id"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Current bool   `json:"current"`
}

// claimRequest is the invite claim: the token from the link and the password
// the customer chose.
//
// The token carries no required tag on purpose. A missing one and a mangled
// one are the same thing to the customer, and both deserve the answer the
// preview route already gives: that link is not valid.
type claimRequest struct {
	Token    string `json:"token"`
	Password string `json:"password" validate:"required"`
	// Login is the name a person invited to an Organization signs in with
	// when no identity provider does it for them. A link from Percona names
	// the login it sets a password for, so it ignores this.
	Login string `json:"login"`
}

// usernameOf is the name the console shows for the caller. A password login is
// known by its username, which is the subject of its session; a person an
// identity provider vouches for by the name or address on file.
func usernameOf(identity auth.Identity) string {
	if identity.Method == store.SignInPassword {
		return identity.Subject
	}

	return identity.Scope.UserName
}

// handleLogin checks a username and password and sets the session cookie.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.identity == nil {
		Respond(w, r, nil, NotImplemented("Sign in is not configured on this control plane."))

		return
	}

	var req loginRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	username := req.Username

	session, err := s.identity.Login(r.Context(), username, req.Password)
	if err != nil {
		Respond(w, r, nil, signInError(err))

		return
	}

	http.SetCookie(w, s.newSessionCookie(r, session))
	Respond(w, r, identityResponse{Username: username}, nil)
}

// handleLogout ends the session and tells the browser to drop the cookie.
//
// It answers the same way whether there was a session or not. A second click,
// or a click after the session already expired, still leaves the customer
// signed out, which is the only useful meaning of the button.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.identity != nil {
		if err := s.identity.Logout(r.Context(), sessionCookie(r)); err != nil {
			Respond(w, r, nil, Internal("Signing out did not finish.", err))

			return
		}
	}

	http.SetCookie(w, s.clearedSessionCookie(r))
	Respond(w, r, nil, nil)
}

// handleAuthSession reports who the caller is signed in as. The UI asks this
// on load to decide between the app and the login page.
func (s *Server) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	if s.identity == nil {
		Respond(w, r, nil, NotImplemented("Sign in is not configured on this control plane."))

		return
	}

	identity, err := s.identity.Resolve(r.Context(), sessionCookie(r))
	if err != nil {
		Respond(w, r, nil, signInError(err))

		return
	}

	organizations, err := s.organizationsOf(r, identity)
	if err != nil {
		Respond(w, r, nil, Internal("Signing in did not finish.", err))

		return
	}

	Respond(w, r, identityResponse{
		Username:            usernameOf(identity),
		ControlPlaneVersion: s.version,
		User:                identity.Scope.UserName,
		Organization:        identity.Scope.OrganizationName,
		Team:                identity.Scope.Team.Name,
		Role:                identity.Scope.Role,
		Organizations:       organizations,
	}, nil)
}

// handleInvitePreview names the login an invite link belongs to, so the claim
// page can say whose password it is setting. It does not spend the link:
// opening the page twice has to be allowed.
func (s *Server) handleInvitePreview(w http.ResponseWriter, r *http.Request) {
	if s.identity == nil {
		Respond(w, r, nil, NotImplemented("Sign in is not configured on this control plane."))

		return
	}

	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if s.invitations != nil {
		preview, err := s.invitations.Preview(r.Context(), token)
		if err == nil {
			Respond(w, r, invitePreviewResponse{
				Organization: preview.Organization, Team: preview.Team, Role: preview.Role,
				Email: preview.Email, AnyEmail: preview.AnyEmail,
			}, nil)

			return
		}
		if !errors.Is(err, store.ErrInviteUnknown) {
			Respond(w, r, nil, invitationLinkError(err))

			return
		}
	}

	username, err := s.identity.InviteLogin(r.Context(), token)
	if err != nil {
		Respond(w, r, nil, inviteError(err))

		return
	}

	Respond(w, r, identityResponse{Username: username}, nil)
}

// handleClaimInvite spends the link and sets the password the customer chose.
// A leftover session in this browser is ended, so the sign in page cannot
// bounce them back in as whoever was already here.
func (s *Server) handleClaimInvite(w http.ResponseWriter, r *http.Request) {
	if s.identity == nil {
		Respond(w, r, nil, NotImplemented("Sign in is not configured on this control plane."))

		return
	}

	var req claimRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	username, ok := s.claimLink(w, r, req)
	if !ok {
		return
	}

	// This browser may already be signed in as somebody else. The password
	// is set; the leftover cookie is what would send them back into that
	// other login instead of the sign in form. A logout failure must not
	// fail the claim: the invite is already spent.
	if err := s.identity.Logout(r.Context(), sessionCookie(r)); err != nil {
		s.logger.ErrorContext(r.Context(), "claim could not end the leftover session", "err", err)
	}
	http.SetCookie(w, s.clearedSessionCookie(r))
	Respond(w, r, identityResponse{Username: username}, nil)
}

// claimLink spends the link and returns the login it was for. A link an admin
// issued adds a password login to their Organization; any other is Percona's
// and sets its login's password. It answers the request itself on failure.
func (s *Server) claimLink(w http.ResponseWriter, r *http.Request, req claimRequest) (string, bool) {
	if s.invitations != nil {
		login, err := s.invitations.ClaimPassword(r.Context(), req.Token, req.Login, req.Password)
		if err == nil {
			return login, true
		}
		if !errors.Is(err, store.ErrInviteUnknown) {
			Respond(w, r, nil, invitationLinkError(err))

			return "", false
		}
	}

	username, err := s.identity.Claim(r.Context(), req.Token, req.Password)
	if err != nil {
		Respond(w, r, nil, inviteError(err))

		return "", false
	}

	return username, true
}

// sessionCookie returns the raw cookie value, or an empty string.
func sessionCookie(r *http.Request) string {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return ""
	}

	return cookie.Value
}

// newSessionCookie builds the cookie that carries a session.
//
// HttpOnly keeps a script on the page from reading it. SameSite lax keeps a
// cross site form post from carrying it, which is what stands in for a CSRF
// token here: every write is a same site request from the SPA.
func (s *Server) newSessionCookie(r *http.Request, session service.SignedSession) *http.Cookie {
	return &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    session.Value,
		Path:     "/",
		Expires:  session.ExpiresAt,
		HttpOnly: true,
		Secure:   s.secureCookies(r),
		SameSite: http.SameSiteLaxMode,
	}
}

// clearedSessionCookie is the cookie that tells the browser to forget the
// session. The attributes have to match the one that was set, or the browser
// keeps the original alongside it.
func (s *Server) clearedSessionCookie(r *http.Request) *http.Cookie {
	return &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secureCookies(r),
		SameSite: http.SameSiteLaxMode,
	}
}

// secureCookies reports whether the session cookie may carry the Secure flag.
//
// The configured public URL is the reliable signal, because behind an ingress
// the adapter itself only ever sees plain HTTP. The request is the fallback so
// a deployment with no public URL set still marks the cookie on a TLS
// connection, and a laptop run over HTTP still gets a cookie the browser will
// send.
func (s *Server) secureCookies(r *http.Request) bool {
	if strings.HasPrefix(strings.ToLower(s.cfg.HubPublicURL), "https://") {
		return true
	}
	if r.TLS != nil {
		return true
	}

	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// signInError renders a refused sign in. Every reason answers 401 with the
// same sentence: which usernames are real is not something the login form
// should be able to tell anybody.
func signInError(err error) *AppError {
	switch {
	case errors.Is(err, service.ErrSignInRefused):
		return Unauthorized("That username and password do not match.")
	case errors.Is(err, service.ErrNotSignedIn):
		return Unauthorized("Sign in to continue.")
	default:
		return Internal("Signing in did not finish.", err)
	}
}

// inviteError renders a link that cannot be used. A link that was never issued
// is not found; one that is spent or out of time clashes with the state the
// row is already in.
func inviteError(err error) *AppError {
	switch {
	case errors.Is(err, store.ErrInviteUnknown):
		return NotFound("That invite link is not valid.")
	case errors.Is(err, store.ErrInviteSpent):
		return Conflict("That invite link has already been used. Ask Percona for a new one.")
	case errors.Is(err, store.ErrInviteExpired):
		return Conflict("That invite link has expired. Ask Percona for a new one.")
	case errors.Is(err, service.ErrPasswordRefused):
		// The rule that was broken is in the wrapped error, and a password too
		// long for bcrypt to read is a real case: telling that customer to use
		// more characters would be advice that cannot work.
		return Validation("Choose a password of 12 to 72 characters.")
	case errors.Is(err, store.ErrLoginUnknown):
		return NotFound("That invite link is not valid.")
	default:
		return Internal("Setting the password did not finish.", err)
	}
}

// inProcessAuthenticator lets a request this process built for itself act as
// the caller it carries. Every other request meets the session authenticator.
//
// The caller travels as a context value, which no HTTP request can set: a
// header, cookie or query parameter naming a caller is only data to the
// session authenticator behind it.
type inProcessAuthenticator struct {
	session auth.Authenticator
}

// Middleware routes a request that carries an in-process caller past the
// session check, with the identity the caller was authenticated as.
func (a inProcessAuthenticator) Middleware(next http.Handler) http.Handler {
	session := a.session.Middleware(next)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, ok := auth.CallerFromContext(r.Context())
		if !ok {
			session.ServeHTTP(w, r)

			return
		}

		// The agent a caller names wins over the one its identity carries.
		identity := caller.Identity
		if caller.Agent != "" {
			identity.Client.Agent = caller.Agent
		}

		// A caller with no Organization was not authenticated by anything, so
		// it is not let through as an identity with no scope. An agent name
		// the activity log could not keep is refused for the same reason: the
		// caller was built wrong, and whoever builds one shortens or cleans a
		// client's own name first.
		if identity.Scope.OrganizationID == 0 || (identity.Client.Agent != "" && !auth.ValidAgent(identity.Client.Agent)) {
			Respond(w, r, nil, Unauthorized("Sign in to continue."))

			return
		}

		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
	})
}
