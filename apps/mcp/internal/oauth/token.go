package oauth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/percona/pds/apps/mcp/internal/tokenstore"
)

var (
	// ErrSessionEnded means the control plane refused the refresh token: the person signed out of
	// it, an administrator ended it, or it expired.
	ErrSessionEnded = errors.New("the control plane ended this sign in")
	// ErrNoRefreshToken means the control plane gave an access token and no way to renew it.
	ErrNoRefreshToken = errors.New("the control plane did not allow this sign in to be kept")
)

// expiryMargin is how long before it expires an access token stops being used, so a token never
// runs out between being checked and being read.
const expiryMargin = 30 * time.Second

// Tokens is what the token endpoint gave. The access token is held in memory only.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	Scope        string
	Expires      time.Time
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

// exchange posts a token request. The answer's refusal is reduced to a sentinel; its body is never
// put in an error, because it can carry a token.
func exchange(ctx context.Context, hc *http.Client, server Server, form url.Values, now time.Time) (Tokens, error) {
	form.Set("client_id", LocalClientID)
	form.Set("resource", server.Resource)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, fmt.Errorf("build the token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("reach the control plane's token endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readBody(resp.Body)
	if err != nil {
		return Tokens{}, err
	}
	var answer tokenResponse
	if err := json.Unmarshal(body, &answer); err != nil {
		return Tokens{}, fmt.Errorf("the token endpoint answered with status %d and no JSON", resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		if answer.Error == "invalid_grant" {
			return Tokens{}, ErrSessionEnded
		}

		return Tokens{}, fmt.Errorf("the token endpoint refused the request with status %d", resp.StatusCode)
	}
	if answer.AccessToken == "" || !strings.EqualFold(answer.TokenType, "Bearer") {
		return Tokens{}, errors.New("the token endpoint gave no bearer access token")
	}
	lifetime := time.Duration(answer.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = 5 * time.Minute
	}

	return Tokens{
		AccessToken: answer.AccessToken, RefreshToken: answer.RefreshToken, Scope: answer.Scope,
		Expires: now.Add(lifetime),
	}, nil
}

// Source hands out access tokens for one control plane. It renews them with the refresh token in
// the store, keeps the rotated successor before the new access token is used, and holds the
// access token in memory only.
type Source struct {
	server Server
	hc     *http.Client
	store  *tokenstore.Store
	now    func() time.Time

	mu      sync.Mutex
	access  string
	expires time.Time
}

// NewSource returns the source for a control plane.
func NewSource(server Server, hc *http.Client, store *tokenstore.Store) *Source {
	return &Source{server: server, hc: hc, store: store, now: time.Now}
}

// Token returns a valid access token, renewing it when it is missing or about to expire.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.usable() {
		return s.access, nil
	}

	return s.renew(ctx)
}

// Refresh answers a 401: rejected is the token the control plane refused. When another request has
// already replaced it, the newer one is returned and no second refresh is spent.
func (s *Source) Refresh(ctx context.Context, rejected string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.usable() && subtle.ConstantTimeCompare([]byte(s.access), []byte(rejected)) != 1 {
		return s.access, nil
	}

	return s.renew(ctx)
}

// Renew spends the refresh token now, whatever the access token's age, so what the control plane
// grants is read as it stands and not as it was when the access token was issued.
func (s *Source) Renew(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.renew(ctx)
}

func (s *Source) usable() bool {
	return s.access != "" && s.now().Add(expiryMargin).Before(s.expires)
}

func (s *Source) renew(ctx context.Context) (string, error) {
	var fresh Tokens
	err := s.store.Update(s.server.Issuer, func(current tokenstore.Login) (tokenstore.Login, error) {
		tokens, err := exchange(ctx, s.hc, s.server, url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {current.RefreshToken},
		}, s.now())
		if err != nil {
			return tokenstore.Login{}, err
		}
		fresh = tokens

		next := current
		if tokens.RefreshToken != "" {
			next.RefreshToken = tokens.RefreshToken
		}
		if tokens.Scope != "" {
			next.Scope = tokens.Scope
		}

		return next, nil
	})
	if err != nil {
		s.access = ""

		return "", err
	}

	s.access, s.expires = fresh.AccessToken, fresh.Expires

	return s.access, nil
}

// Revoke asks the control plane to end the session a refresh token belongs to. A control plane
// with no revocation endpoint has nothing to ask.
func Revoke(ctx context.Context, server Server, hc *http.Client, refreshToken string) error {
	if server.RevocationEndpoint == "" {
		return nil
	}
	form := url.Values{"token": {refreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {LocalClientID}}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.RevocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build the revocation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("reach the control plane's revocation endpoint: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the revocation endpoint answered with status %d", resp.StatusCode)
	}

	return nil
}
