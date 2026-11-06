package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"
)

// IssuerClientID and IssuerClientSecret are the credentials the fake issuer
// accepts at its token endpoint.
const (
	IssuerClientID     = "pds-console"
	IssuerClientSecret = "console-secret"

	issuerKeyID = "fake-issuer-key"
)

// IssuerIdentity is who the fake issuer vouches for at its next sign in.
type IssuerIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

// FakeIssuer is an OpenID Connect issuer that signs in whoever Identity names,
// with no login page: its authorize endpoint answers with the code at once.
// It checks what a real issuer checks of a confidential client: the redirect
// URI, the client secret, and the PKCE verifier against the S256 challenge.
type FakeIssuer struct {
	URL string

	// Identity is who the next authorize call signs in.
	Identity IssuerIdentity

	mu         sync.Mutex
	key        *rsa.PrivateKey
	codes      map[string]issuedCode
	loggedOut  []url.Values
	authorized []url.Values
}

type issuedCode struct {
	nonce     string
	challenge string
	identity  IssuerIdentity
}

// NewFakeIssuer starts the issuer and stops it with the test.
func NewFakeIssuer(t *testing.T) *FakeIssuer {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate issuer key: %v", err)
	}

	issuer := &FakeIssuer{key: key, codes: map[string]issuedCode{}}
	keys := &oidctest.Server{PublicKeys: []oidctest.PublicKey{
		{PublicKey: key.Public(), KeyID: issuerKeyID, Algorithm: oidc.RS256},
	}}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", issuer.discovery)
	mux.HandleFunc("/authorize", issuer.authorize)
	mux.HandleFunc("/token", issuer.token)
	mux.HandleFunc("/logout", issuer.logout)
	mux.Handle("/keys", keys)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	issuer.URL = srv.URL

	return issuer
}

// AccessToken signs a JWT access token for a person, the way an issuer answers
// a request for an API. The control plane accepts none: a test uses it to show
// that a token the issuer really signed opens nothing. The test passes the
// audience and authorized party.
func (f *FakeIssuer) AccessToken(subject, audience, authorizedParty string, expires time.Time) string {
	raw, _ := json.Marshal(map[string]any{
		"iss": f.URL,
		"aud": audience,
		"azp": authorizedParty,
		"sub": subject,
		"exp": expires.Unix(),
		"iat": time.Now().Unix(),
	})

	return oidctest.SignIDToken(f.key, issuerKeyID, oidc.RS256, string(raw))
}

// LoggedOut returns the query of every call to the end session endpoint.
func (f *FakeIssuer) LoggedOut() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]url.Values(nil), f.loggedOut...)
}

// Authorizations returns the query of every call to the authorize endpoint.
func (f *FakeIssuer) Authorizations() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]url.Values(nil), f.authorized...)
}

func (f *FakeIssuer) discovery(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                f.URL,
		"authorization_endpoint":                f.URL + "/authorize",
		"token_endpoint":                        f.URL + "/token",
		"jwks_uri":                              f.URL + "/keys",
		"end_session_endpoint":                  f.URL + "/logout",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (f *FakeIssuer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if q.Get("client_id") != IssuerClientID || q.Get("response_type") != "code" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || err != nil {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)

		return
	}

	code := fmt.Sprintf("code-%d", time.Now().UnixNano())
	f.mu.Lock()
	f.codes[code] = issuedCode{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), identity: f.Identity}
	f.authorized = append(f.authorized, q)
	f.mu.Unlock()

	back := redirect.Query()
	back.Set("code", code)
	back.Set("state", q.Get("state"))
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (f *FakeIssuer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)

		return
	}

	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != IssuerClientID || secret != IssuerClientSecret {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))

		return
	}

	f.mu.Lock()
	issued, ok := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code"))
	f.mu.Unlock()

	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != issued.challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))

		return
	}

	now := time.Now()
	claims := map[string]any{
		"iss":            f.URL,
		"aud":            IssuerClientID,
		"sub":            issued.identity.Subject,
		"exp":            now.Add(time.Hour).Unix(),
		"iat":            now.Unix(),
		"nonce":          issued.nonce,
		"email":          issued.identity.Email,
		"email_verified": issued.identity.EmailVerified,
		"name":           issued.identity.Name,
	}
	raw, _ := json.Marshal(claims)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "access-" + strconv.FormatInt(now.UnixNano(), 10),
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     oidctest.SignIDToken(f.key, issuerKeyID, oidc.RS256, string(raw)),
	})
}

func (f *FakeIssuer) logout(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.loggedOut = append(f.loggedOut, r.URL.Query())
	f.mu.Unlock()

	if back := r.URL.Query().Get("post_logout_redirect_uri"); back != "" {
		http.Redirect(w, r, back, http.StatusFound)

		return
	}
	w.WriteHeader(http.StatusOK)
}
