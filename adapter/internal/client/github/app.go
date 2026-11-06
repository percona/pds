package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	// jwtLifetime stays under the ten minutes GitHub allows for an App JWT.
	jwtLifetime = 9 * time.Minute
	// jwtBackdate leaves room for this host's clock running ahead of GitHub's.
	jwtBackdate = time.Minute
	// refreshMargin replaces an installation token while it still has time
	// to finish a multi-call operation.
	refreshMargin = time.Minute
)

// appAuth exchanges a GitHub App's private key for short-lived installation
// tokens and keeps the current one until it is about to expire.
type appAuth struct {
	appID          string
	installationID string
	key            *rsa.PrivateKey
	now            func() time.Time

	mu       sync.Mutex
	token    string
	expires  time.Time
	inFlight *exchangeCall
	// refused is the last cached token a refresh replaced and refreshErr is how
	// the latest exchange ended when it failed. A request that was sent with
	// refused and is refused only after that exchange failed shares the failure
	// instead of starting another. A successful exchange clears both.
	refused    string
	refreshErr error
}

// exchangeCall is one token exchange that callers can wait for. err is set
// before done closes.
type exchangeCall struct {
	done chan struct{}
	err  error
}

// ParsePrivateKey reads the RSA private key GitHub issues for an App, in
// PKCS#1 or PKCS#8 PEM form. The error never carries the key material.
func ParsePrivateKey(raw []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(bytes.TrimSpace(raw))
	if block == nil {
		return nil, errors.New("github app private key is not PEM encoded")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("github app private key is not a PKCS#1 or PKCS#8 key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github app private key is not an RSA key")
	}

	return key, nil
}

// current returns a usable installation token. When the cached one is missing
// or inside the refresh margin, the first caller runs the exchange and every
// caller that arrives while it runs waits for it, so concurrent callers share
// one request. A waiter gets the exchange's failure instead of repeating it,
// so an outage costs one request rather than one per caller, and it stops
// waiting when its own context ends. A call that starts after the failure
// exchanges again, and so does a waiter whose exchange failed only because the
// caller running it was canceled.
//
// stale is a token a request was just refused with. It is dropped in the same
// step that finds or starts the exchange, so callers whose requests were
// refused together share one exchange, and one whose refusal arrives after a
// failed exchange shares that failure. A token another caller already replaced
// is kept.
func (a *appAuth) current(ctx context.Context, c *Client, stale string) (string, error) {
	a.mu.Lock()
	cached := a.token
	if stale != "" && a.token == stale {
		a.token = ""
	}
	for {
		if a.token != "" && a.now().Add(refreshMargin).Before(a.expires) {
			token := a.token
			a.mu.Unlock()

			return token, nil
		}
		call := a.inFlight
		if call == nil && stale != "" && stale == a.refused && a.refreshErr != nil {
			err := a.refreshErr
			a.mu.Unlock()

			return "", err
		}
		if call == nil {
			break
		}
		a.mu.Unlock()
		select {
		case <-call.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if call.err != nil && !errors.Is(call.err, context.Canceled) && !errors.Is(call.err, context.DeadlineExceeded) {
			return "", call.err
		}
		a.mu.Lock()
	}
	call := &exchangeCall{done: make(chan struct{})}
	a.inFlight = call
	a.mu.Unlock()

	token, expires, err := a.exchange(ctx, c)

	a.mu.Lock()
	switch {
	case err == nil:
		a.token, a.expires = token, expires
		a.refused, a.refreshErr = "", nil
	case !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded):
		if cached != "" {
			a.refused = cached
		}
		a.refreshErr = err
	}
	call.err = err
	a.inFlight = nil
	close(call.done)
	a.mu.Unlock()

	return token, err
}

func (a *appAuth) exchange(ctx context.Context, c *Client) (string, time.Time, error) {
	jwt, err := a.signJWT()
	if err != nil {
		return "", time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/app/installations/"+url.PathEscape(a.installationID)+"/access_tokens", nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+jwt)
	res, err := c.http.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("github installation token: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("github installation token: %w", err)
	}
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("github installation token: HTTP %d: %s", res.StatusCode, truncate(raw, 200))
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Token == "" || out.ExpiresAt.IsZero() {
		return "", time.Time{}, errors.New("github installation token: response has no token or expiry")
	}
	return out.Token, out.ExpiresAt, nil
}

func (a *appAuth) signJWT() (string, error) {
	now := a.now()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss": a.appID,
		"iat": now.Add(-jwtBackdate).Unix(),
		"exp": now.Add(jwtLifetime).Unix(),
	})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign github app jwt: %w", err)
	}

	return signing + "." + enc.EncodeToString(sig), nil
}
