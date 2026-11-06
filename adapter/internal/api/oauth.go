package api

import (
	"encoding/json"
	"errors"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/features"
	"github.com/percona/pds/adapter/internal/oauth"
	"github.com/percona/pds/adapter/internal/service"
)

const (
	// consoleConsent is where a validated authorization request is sent to be
	// approved. It is a console route, so signing in is the console's own gate.
	consoleConsent = "/app/authorize-agent"

	// maxOAuthBody caps a token, revocation or registration request.
	maxOAuthBody = 16 << 10
	// maxSignInReturn caps the authorization request carried through sign in.
	maxSignInReturn = 4096

	tokenRetryAfter = "60"
)

// requireOAuth answers 404 for every OAuth endpoint when the MCP surface is
// off or this control plane has no authorization server, so a deployment that
// does not serve agents shows nothing to find.
func (s *Server) requireOAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.oauth == nil || !s.features.Enabled(r.Context(), features.MCP) {
			Respond(w, r, nil, NotFound("That endpoint does not exist."))

			return
		}

		next.ServeHTTP(w, r)
	})
}

// oauthEndpoint serves one method of an OAuth endpoint and refuses the others.
func oauthEndpoint(method string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			Respond(w, r, nil, Validation("That method is not allowed on this endpoint.").
				WithStatus(http.StatusMethodNotAllowed))

			return
		}

		handler(w, r)
	}
}

// noStore keeps a response that carries or decides a credential out of every
// cache and out of the Referer a redirect target would see.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// serverMetadata is the RFC 8414 document.
type serverMetadata struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	RevocationEndpoint            string   `json:"revocation_endpoint"`
	ResponseTypesSupported        []string `json:"response_types_supported"`
	GrantTypesSupported           []string `json:"grant_types_supported"`
	TokenEndpointAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
	RevocationEndpointAuthMethods []string `json:"revocation_endpoint_auth_methods_supported"`
	CodeChallengeMethods          []string `json:"code_challenge_methods_supported"`
	ScopesSupported               []string `json:"scopes_supported"`
	ClientIDMetadataDocuments     bool     `json:"client_id_metadata_document_supported"`
	IssuerInResponses             bool     `json:"authorization_response_iss_parameter_supported"`
}

func (s *Server) handleOAuthMetadata(w http.ResponseWriter, r *http.Request) {
	issuer := s.oauth.Issuer()

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	Respond(w, r, serverMetadata{
		Issuer:                        issuer,
		AuthorizationEndpoint:         issuer + "/oauth/authorize",
		TokenEndpoint:                 issuer + "/oauth/token",
		RegistrationEndpoint:          issuer + "/oauth/register",
		RevocationEndpoint:            issuer + "/oauth/revoke",
		ResponseTypesSupported:        []string{"code"},
		GrantTypesSupported:           []string{"authorization_code", "refresh_token"},
		TokenEndpointAuthMethods:      []string{"none"},
		RevocationEndpointAuthMethods: []string{"none"},
		CodeChallengeMethods:          []string{"S256"},
		ScopesSupported:               oauth.AuthorizationServerScopes(),
		ClientIDMetadataDocuments:     true,
		IssuerInResponses:             true,
	}, nil)
}

// authorizeParams are the authorization request's query parameters. A name
// that appears twice is refused, because a client and a proxy could read
// different ones.
var authorizeParams = []string{
	"client_id", "redirect_uri", "response_type", "code_challenge", "code_challenge_method", "state", "scope", "resource",
}

func authorizationRequestFrom(query url.Values) (service.AuthorizationRequest, bool) {
	for _, name := range authorizeParams {
		if len(query[name]) > 1 {
			return service.AuthorizationRequest{}, false
		}
	}

	return service.AuthorizationRequest{
		ClientID: query.Get("client_id"), RedirectURI: query.Get("redirect_uri"), ResponseType: query.Get("response_type"),
		CodeChallenge: query.Get("code_challenge"), CodeChallengeMethod: query.Get("code_challenge_method"),
		State: query.Get("state"), Scope: query.Get("scope"), Resource: query.Get("resource"),
	}, true
}

// handleOAuthAuthorize validates an authorization request and hands the person
// to the consent screen. A person with no console session is sent to sign in
// and comes back to this same request; the client's document is fetched only
// once they have, so nobody who is not signed in can make this server fetch an
// address.
func (s *Server) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	noStore(w)

	req, ok := authorizationRequestFrom(r.URL.Query())
	if !ok {
		oauthErrorPage(w, http.StatusBadRequest, "The request repeats a parameter.")

		return
	}

	who, err := s.identity.Resolve(r.Context(), sessionCookie(r))
	switch {
	case errors.Is(err, service.ErrNotSignedIn):
		s.sendToSignIn(w, r, req)

		return
	case err != nil:
		s.logger.ErrorContext(r.Context(), "authorize could not read the session", "err", err)
		oauthErrorPage(w, http.StatusInternalServerError, "Something went wrong on our side. Try again.")

		return
	}

	token, aerr := s.oauth.StartAuthorization(r.Context(), who, req)
	if aerr != nil {
		s.refuseAuthorization(w, r, aerr)

		return
	}

	http.Redirect(w, r, consoleConsent+"?request="+url.QueryEscape(token), http.StatusFound)
}

// sendToSignIn redirects to the console sign in, carrying this request.
func (s *Server) sendToSignIn(w http.ResponseWriter, r *http.Request, req service.AuthorizationRequest) {
	if aerr := s.oauth.CheckAuthorization(req); aerr != nil {
		s.refuseAuthorization(w, r, aerr)

		return
	}

	next := "/oauth/authorize?" + r.URL.RawQuery
	if len(next) > maxSignInReturn {
		oauthErrorPage(w, http.StatusBadRequest, "The request is too long.")

		return
	}

	http.Redirect(w, r, consoleSignIn+"?next="+url.QueryEscape(next), http.StatusFound)
}

// refuseAuthorization answers a refused request: to the client's redirect
// address when it was proven to be theirs, and with an error page otherwise.
func (s *Server) refuseAuthorization(w http.ResponseWriter, r *http.Request, aerr *service.AuthorizationError) {
	if aerr.RedirectURI != "" {
		http.Redirect(w, r, s.oauth.ErrorRedirect(aerr), http.StatusFound)

		return
	}

	status := http.StatusBadRequest
	switch aerr.Code {
	case "temporarily_unavailable":
		status = http.StatusTooManyRequests
	case "server_error":
		status = http.StatusInternalServerError
	}
	oauthErrorPage(w, status, aerr.Description)
}

// oauthErrorPage tells the person the request cannot continue. It names no
// value from the request, so it cannot be used to show somebody a page of an
// attacker's making, and it never redirects.
func oauthErrorPage(w http.ResponseWriter, status int, message string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)

	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8">`+
		`<meta name="viewport" content="width=device-width, initial-scale=1">`+
		`<title>This request cannot continue</title>`+
		`<style>body{font:16px/1.5 system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1rem;color:#1b1b1f;background:#fff}`+
		`@media (prefers-color-scheme:dark){body{color:#ececf1;background:#17171c}}</style></head><body>`+
		`<h1>This request cannot continue</h1><p>`+html.EscapeString(message)+`</p>`+
		`<p>Go back to the app that sent you here and try again.</p></body></html>`)
}

// oauthJSON writes a token or registration answer.
func oauthJSON(w http.ResponseWriter, status int, body any) {
	noStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// oauthFailure writes an OAuth error. Its cause goes to the log; the client
// sees the code and a sentence.
func (s *Server) oauthFailure(w http.ResponseWriter, r *http.Request, err *service.OAuthError) {
	if err.Cause != nil {
		s.logger.ErrorContext(r.Context(), "oauth request failed", "code", err.Code, "err", err.Cause)
	}

	status := http.StatusBadRequest
	switch err.Code {
	case "server_error":
		status = http.StatusInternalServerError
	case "temporarily_unavailable":
		status = http.StatusTooManyRequests
		w.Header().Set("Retry-After", tokenRetryAfter)
	case "invalid_client":
		if r.URL.Path == "/oauth/token" && (r.Header.Get("Authorization") != "" || r.PostForm.Has("client_secret")) {
			status = http.StatusUnauthorized
			w.Header().Set("WWW-Authenticate", `Basic realm="oauth", error="invalid_client"`)
		}
	}

	oauthJSON(w, status, map[string]string{"error": err.Code, "error_description": err.Description})
}

// tokenFormParams are the parameters a token or revocation request may carry
// once each.
var tokenFormParams = []string{
	"grant_type", "code", "redirect_uri", "client_id", "code_verifier", "resource", "refresh_token", "scope",
	"token", "token_type_hint", "client_secret",
}

// readForm reads a form body, and nothing from the query string: a credential
// in a URL ends up in logs. A repeated parameter is refused.
func (s *Server) readForm(w http.ResponseWriter, r *http.Request) *service.OAuthError {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/x-www-form-urlencoded" {
		return &service.OAuthError{Code: "invalid_request", Description: "Send the request as application/x-www-form-urlencoded."}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthBody)
	if err := r.ParseForm(); err != nil {
		return &service.OAuthError{Code: "invalid_request", Description: "The request body could not be read."}
	}
	for _, name := range tokenFormParams {
		if len(r.PostForm[name]) > 1 {
			return &service.OAuthError{Code: "invalid_request", Description: "A parameter was repeated."}
		}
	}

	return nil
}

// handleOAuthToken is the token endpoint: authorization codes and refresh
// tokens, for public clients.
func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if err := s.readForm(w, r); err != nil {
		s.oauthFailure(w, r, err)

		return
	}
	if r.Header.Get("Authorization") != "" || r.PostForm.Has("client_secret") {
		s.oauthFailure(w, r, &service.OAuthError{Code: "invalid_client", Description: "Only public clients are supported: send no client credentials."})

		return
	}

	form, from := r.PostForm, clientAddress(r)
	var (
		response service.TokenResponse
		err      *service.OAuthError
	)
	switch form.Get("grant_type") {
	case "authorization_code":
		response, err = s.oauth.ExchangeCode(r.Context(), from, service.TokenRequest{
			ClientID: form.Get("client_id"), Code: form.Get("code"), RedirectURI: form.Get("redirect_uri"),
			CodeVerifier: form.Get("code_verifier"), Resource: form.Get("resource"),
		})
	case "refresh_token":
		response, err = s.oauth.Refresh(r.Context(), from, service.RefreshRequest{
			ClientID: form.Get("client_id"), RefreshToken: form.Get("refresh_token"),
			Resource: form.Get("resource"), Scope: form.Get("scope"),
		})
	case "":
		err = &service.OAuthError{Code: "invalid_request", Description: "The grant_type parameter is required."}
	default:
		err = &service.OAuthError{Code: "unsupported_grant_type", Description: "Use authorization_code or refresh_token."}
	}
	if err != nil {
		s.oauthFailure(w, r, err)

		return
	}

	oauthJSON(w, http.StatusOK, map[string]any{
		"access_token": response.AccessToken, "token_type": response.TokenType, "expires_in": response.ExpiresIn,
		"refresh_token": response.RefreshToken, "scope": response.Scope,
	})
}

// handleOAuthRevoke ends the session a token belongs to (RFC 7009).
func (s *Server) handleOAuthRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.readForm(w, r); err != nil {
		s.oauthFailure(w, r, err)

		return
	}

	if err := s.oauth.Revoke(r.Context(), clientAddress(r), service.RevokeRequest{
		Token: r.PostForm.Get("token"), ClientID: r.PostForm.Get("client_id"),
	}); err != nil {
		s.oauthFailure(w, r, err)

		return
	}

	noStore(w)
	w.WriteHeader(http.StatusOK)
}

// registrationRequest is the RFC 7591 client metadata this server reads.
type registrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

// handleOAuthRegister is Dynamic Client Registration for public clients.
func (s *Server) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	var req registrationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOAuthBody)).Decode(&req); err != nil {
		s.oauthFailure(w, r, &service.OAuthError{Code: "invalid_client_metadata", Description: "The request body is not valid client metadata JSON."})

		return
	}

	client, err := s.oauth.Register(r.Context(), clientAddress(r), service.ClientRegistration{
		ClientName: req.ClientName, RedirectURIs: req.RedirectURIs, TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
		GrantTypes: req.GrantTypes, ResponseTypes: req.ResponseTypes,
	})
	if err != nil {
		s.oauthFailure(w, r, err)

		return
	}

	oauthJSON(w, http.StatusCreated, map[string]any{
		"client_id": client.ClientID, "client_id_issued_at": client.IssuedAt.Unix(), "client_name": client.ClientName,
		"redirect_uris": client.RedirectURIs, "grant_types": []string{"authorization_code", "refresh_token"},
		"response_types": []string{"code"}, "token_endpoint_auth_method": "none",
	})
}

// registerOAuthRoutes mounts the authorization server. Every route answers 404
// while the MCP surface is off.
func (s *Server) registerOAuthRoutes() {
	s.router.Group(func(r chi.Router) {
		r.Use(s.requireOAuth)

		r.Get("/.well-known/oauth-authorization-server", s.handleOAuthMetadata)
		r.Route("/oauth", func(o chi.Router) {
			o.HandleFunc("/authorize", oauthEndpoint(http.MethodGet, s.handleOAuthAuthorize))
			o.HandleFunc("/token", oauthEndpoint(http.MethodPost, s.handleOAuthToken))
			o.HandleFunc("/register", oauthEndpoint(http.MethodPost, s.handleOAuthRegister))
			o.HandleFunc("/revoke", oauthEndpoint(http.MethodPost, s.handleOAuthRevoke))
		})
	})
}
