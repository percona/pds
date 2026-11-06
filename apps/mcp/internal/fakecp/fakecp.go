// Package fakecp is a control plane for tests: an OAuth authorization server for the PDS local
// client and a /mcp endpoint behind it, built the way the real one is (stateless streamable HTTP
// on the Go SDK). It lets the tests drive pds-mcp as an outside client would, without a hub.
package fakecp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// LocalClientID is the built in identity of the PDS local client.
const LocalClientID = "pds-mcp"

// Request is what the fake saw on /mcp.
type Request struct {
	Method        string
	Authorization string
	UserAgent     string
	Header        http.Header
	Status        int
}

type grant struct {
	challenge, redirect, scope string
}

type tokenRecord struct {
	scope   string
	expires time.Time
}

// Config is what a test can change about the fake. Changes go through Configure, because the
// handlers run on their own goroutines.
type Config struct {
	// Issuer is what the metadata reports as the issuer. It defaults to the server's address.
	Issuer string
	// MCPOff answers 404 on /mcp and its metadata, as a control plane with the feature off does.
	MCPOff bool
	// ExpiresIn is the lifetime reported for an access token. It defaults to an hour.
	ExpiresIn time.Duration
	// NoRefreshToken leaves the refresh token out of a token response.
	NoRefreshToken bool
	// OmitIss leaves the iss parameter out of the authorization response.
	OmitIss bool
	// StateOnReturn replaces the state in the authorization response when it is not empty.
	StateOnReturn string
	// Deny answers the authorization request with access_denied.
	Deny bool
	// ForbidScope makes every /mcp request answer 403 insufficient_scope for this scope.
	ForbidScope string
	// Mutate edits the authorization server metadata before it is served.
	Mutate func(meta map[string]any)
	// Allowed limits the scopes a sign in or a refresh may carry, as an organization's switches do. When
	// it is empty every scope that is asked for is granted.
	Allowed []string
}

// Server is the fake control plane.
type Server struct {
	*httptest.Server

	mu         sync.Mutex
	cfg        Config
	counter    int
	grants     map[string]grant
	access     map[string]tokenRecord
	refresh    map[string]string
	spent      map[string]bool
	rejectNext int
	requests   []Request
	forms      []url.Values
	revoked    []string

	databases      map[string]Database
	reads          []Request
	authorizations []url.Values
}

// New starts the fake. It is closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()

	s := &Server{
		cfg:     Config{ExpiresIn: time.Hour},
		grants:  map[string]grant{},
		access:  map[string]tokenRecord{},
		refresh: map[string]string{},
		spent:   map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", s.protectedResource)
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.metadata)
	mux.HandleFunc("/oauth/authorize", s.authorizeRequest)
	mux.HandleFunc("/oauth/token", s.token)
	mux.HandleFunc("/oauth/revoke", s.revoke)
	mux.Handle("/mcp", s.mcp())
	mux.HandleFunc("GET /api/databases/{env}/{id}/credential", s.credential)
	// The server is assigned before it starts, so a handler never reads it half set.
	s.Server = httptest.NewUnstartedServer(mux)
	s.Start()
	t.Cleanup(s.Close)

	return s
}

// Configure changes the fake's behavior. Call it before the code under test makes a request, or
// between requests.
func (s *Server) Configure(change func(*Config)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(&s.cfg)
}

func (s *Server) config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	if c.Issuer == "" {
		c.Issuer = s.URL
	}

	return c
}

func (s *Server) next(prefix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)

	return prefix + "-" + string(rune('a'+s.counter%26)) + hex.EncodeToString(raw)
}

// allow keeps the scopes the configuration lets through.
func (s *Server) allow(scope string) string {
	allowed := s.config().Allowed
	if len(allowed) == 0 {
		return scope
	}
	var kept []string
	for _, name := range strings.Fields(scope) {
		if name == "offline_access" || slices.Contains(allowed, name) {
			kept = append(kept, name)
		}
	}

	return strings.Join(kept, " ")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) protectedResource(w http.ResponseWriter, _ *http.Request) {
	c := s.config()
	if c.MCPOff {
		http.NotFound(w, nil)

		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.URL + "/mcp",
		"authorization_servers":    []string{c.Issuer},
		"scopes_supported":         []string{"pds:read", "pds:change", "pds:delete", "pds:credentials", "pds:sql_write"},
		"bearer_methods_supported": []string{"header"},
	})
}

func (s *Server) metadata(w http.ResponseWriter, _ *http.Request) {
	c := s.config()
	if c.MCPOff {
		http.NotFound(w, nil)

		return
	}
	meta := map[string]any{
		"issuer":                                         c.Issuer,
		"authorization_endpoint":                         s.URL + "/oauth/authorize",
		"token_endpoint":                                 s.URL + "/oauth/token",
		"revocation_endpoint":                            s.URL + "/oauth/revoke",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"code_challenge_methods_supported":               []string{"S256"},
		"scopes_supported":                               []string{"pds:read", "pds:change", "pds:delete", "pds:credentials", "pds:sql_write", "offline_access"},
		"authorization_response_iss_parameter_supported": true,
	}
	if c.Mutate != nil {
		c.Mutate(meta)
	}
	writeJSON(w, http.StatusOK, meta)
}

func (s *Server) authorizeRequest(w http.ResponseWriter, r *http.Request) {
	c := s.config()
	q := r.URL.Query()
	s.mu.Lock()
	s.authorizations = append(s.authorizations, q)
	s.mu.Unlock()
	redirect := q.Get("redirect_uri")
	bad := q.Get("client_id") != LocalClientID || q.Get("response_type") != "code" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" ||
		q.Get("resource") != s.URL+"/mcp" || !strings.HasPrefix(redirect, "http://127.0.0.1:") ||
		!strings.HasSuffix(redirect, "/callback")
	if bad {
		http.Error(w, "bad authorization request", http.StatusBadRequest)

		return
	}

	back, _ := url.Parse(redirect)
	params := back.Query()
	state := q.Get("state")
	if c.StateOnReturn != "" {
		state = c.StateOnReturn
	}
	params.Set("state", state)
	if !c.OmitIss {
		params.Set("iss", c.Issuer)
	}
	if c.Deny {
		params.Set("error", "access_denied")
	} else {
		code := s.next("code")
		scope := s.allow(q.Get("scope"))
		s.mu.Lock()
		s.grants[code] = grant{challenge: q.Get("code_challenge"), redirect: redirect, scope: scope}
		s.mu.Unlock()
		params.Set("code", code)
	}
	back.RawQuery = params.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func oauthError(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code, "error_description": "fake"})
}

func (s *Server) issue(scope string) map[string]any {
	c := s.config()
	access := s.next("at")
	s.mu.Lock()
	s.access[access] = tokenRecord{scope: scope, expires: time.Now().Add(c.ExpiresIn)}
	s.mu.Unlock()

	body := map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(c.ExpiresIn.Seconds()), "scope": scope,
	}
	if !c.NoRefreshToken {
		refresh := s.next("rt")
		s.mu.Lock()
		s.refresh[refresh] = scope
		s.mu.Unlock()
		body["refresh_token"] = refresh
	}

	return body
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.Method != http.MethodPost {
		oauthError(w, "invalid_request")

		return
	}
	s.mu.Lock()
	s.forms = append(s.forms, r.PostForm)
	s.mu.Unlock()

	form := r.PostForm
	if form.Get("client_id") != LocalClientID || form.Get("resource") != s.URL+"/mcp" {
		oauthError(w, "invalid_request")

		return
	}

	switch form.Get("grant_type") {
	case "authorization_code":
		s.mu.Lock()
		g, ok := s.grants[form.Get("code")]
		delete(s.grants, form.Get("code"))
		s.mu.Unlock()
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		if !ok || g.redirect != form.Get("redirect_uri") ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			oauthError(w, "invalid_grant")

			return
		}
		writeJSON(w, http.StatusOK, s.issue(g.scope))
	case "refresh_token":
		token := form.Get("refresh_token")
		s.mu.Lock()
		scope, known := s.refresh[token]
		spent := s.spent[token]
		reject := s.rejectNext > 0
		if reject {
			s.rejectNext--
		}
		if known && !spent && !reject {
			s.spent[token] = true
		}
		s.mu.Unlock()
		if !known || spent || reject {
			oauthError(w, "invalid_grant")

			return
		}
		writeJSON(w, http.StatusOK, s.issue(s.allow(scope)))
	default:
		oauthError(w, "unsupported_grant_type")
	}
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	s.revoked = append(s.revoked, r.PostForm.Get("token"))
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// RejectRefreshes makes the next n refresh requests answer invalid_grant, as a revoked session does.
func (s *Server) RejectRefreshes(n int) {
	s.mu.Lock()
	s.rejectNext = n
	s.mu.Unlock()
}

// ExpireAccessTokens makes every access token issued so far answer 401.
func (s *Server) ExpireAccessTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, record := range s.access {
		record.expires = time.Now().Add(-time.Minute)
		s.access[token] = record
	}
}

// Revoked lists the tokens the revocation endpoint was given.
func (s *Server) Revoked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.revoked)
}

// Forms lists the token endpoint requests, in order.
func (s *Server) Forms() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.forms)
}

// Requests lists what /mcp received.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.requests)
}

// Tokens lists every access and refresh token the fake has issued, so a test can look for them in
// output that must not carry them.
func (s *Server) Tokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []string
	for token := range s.access {
		all = append(all, token)
	}
	for token := range s.refresh {
		all = append(all, token)
	}

	return all
}

// scopeOf resolves a bearer token to its scope, and reports whether it is good.
func (s *Server) scopeOf(authorization string) (string, bool) {
	token, ok := strings.CutPrefix(authorization, "Bearer ")
	if !ok {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.access[token]
	if !ok || time.Now().After(record.expires) {
		return "", false
	}

	return record.scope, true
}

type noInput struct{}

type scaleInput struct {
	ID       string `json:"id"`
	Replicas int    `json:"replicas"`
}

type text struct {
	Text string `json:"text"`
}

func (s *Server) server(scope string) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "pds", Version: "test"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "list_databases", Description: "List the databases."},
		func(context.Context, *sdk.CallToolRequest, noInput) (*sdk.CallToolResult, text, error) {
			return nil, text{Text: "no databases"}, nil
		})
	if strings.Contains(scope, "pds:change") {
		sdk.AddTool(server, &sdk.Tool{Name: "scale_database", Description: "Scale a database."},
			func(_ context.Context, _ *sdk.CallToolRequest, in scaleInput) (*sdk.CallToolResult, text, error) {
				return nil, text{Text: "scaled " + in.ID}, nil
			})
	}

	return server
}

func (s *Server) mcp() http.Handler {
	protocol := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		scope, _ := s.scopeOf(r.Header.Get("Authorization"))

		return s.server(scope)
	}, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record := Request{
			Method: r.Method, Authorization: r.Header.Get("Authorization"),
			UserAgent: r.Header.Get("User-Agent"), Header: r.Header.Clone(),
		}
		defer func() {
			s.mu.Lock()
			s.requests = append(s.requests, record)
			s.mu.Unlock()
		}()

		status := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() { record.Status = status.status }()

		c := s.config()
		if c.MCPOff {
			http.NotFound(status, r)

			return
		}
		if _, ok := s.scopeOf(r.Header.Get("Authorization")); !ok {
			status.Header().Set("WWW-Authenticate",
				`Bearer error="invalid_token", resource_metadata="`+s.URL+`/.well-known/oauth-protected-resource/mcp"`)
			http.Error(status, "Sign in to continue.", http.StatusUnauthorized)

			return
		}
		if c.ForbidScope != "" {
			status.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+c.ForbidScope+`"`)
			http.Error(status, "Not allowed.", http.StatusForbidden)

			return
		}
		protocol.ServeHTTP(status, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Browse plays the person's browser: it opens the authorization address, takes the redirect the
// fake answers with, and follows it to the loopback listener the command is waiting on. It
// returns the status the listener answered, or 0 when there was none.
func Browse(ctx context.Context, authorizeURL string) (int, error) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authorizeURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	location := resp.Header.Get("Location")
	if location == "" {
		return resp.StatusCode, nil
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return 0, err
	}
	resp, err = client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()

	return resp.StatusCode, nil
}

// Authorizations lists the authorization requests the fake received, as their query parameters.
func (s *Server) Authorizations() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.authorizations)
}
