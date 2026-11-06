package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrEverestTokenUnavailable means the adapter has no way to authenticate
// against Everest. It is a deployment fault rather than a transient one: the
// admin account cannot be read, or the instance refused to issue a session.
var ErrEverestTokenUnavailable = errors.New("everest token is not available")

// DefaultEverestAccountsSecret holds the Everest admin account. Everest writes
// it on first install and keeps it.
const DefaultEverestAccountsSecret = "everest-accounts"

// everestAccountsKey is the entry in that Secret describing the accounts.
const everestAccountsKey = "users.yaml"

// everestTokenMargin is how long before expiry a token is treated as spent.
// Everest issues a token good for a day, so a few minutes costs nothing and
// leaves room for a slow call to finish on a token that was valid when it
// started.
const everestTokenMargin = 5 * time.Minute

// EverestSessionFunc trades an account password for a bearer token.
type EverestSessionFunc func(ctx context.Context, username, password string) (string, error)

// SecretReader is the read half of SecretStore. The token source only ever
// reads the admin account, and asking for the write half would be asking for
// permission it does not need.
type SecretReader interface {
	GetSecret(ctx context.Context, namespace, name string) (map[string]string, error)
}

// EverestTokenOptions configures an EverestToken.
type EverestTokenOptions struct {
	// Namespace holds the accounts Secret.
	Namespace string
	// Secret names it. Empty means DefaultEverestAccountsSecret.
	Secret string
	// Static is the token from configuration. It is used first, so an environment
	// that pastes one keeps working, and is dropped once Everest refuses it.
	Static string
	// Username is the account to log in as. Empty means admin.
	Username string
}

// EverestToken supplies the bearer token the Everest client sends, minting a
// new one whenever the last has expired or been refused.
//
// Everest issues a short-lived access token, so a token read once at startup
// stops working partway through the adapter's own uptime. Holding the
// credential instead of the token is what
// lets the adapter outlive the session, without a scheduled restart and
// without a window where every database route answers 502.
//
// The token is kept in memory only. Each replica mints its own, which needs no
// write access anywhere and cannot race another pod over a shared Secret.
type EverestToken struct {
	store   SecretReader
	session EverestSessionFunc
	opts    EverestTokenOptions

	// mu serialises minting, so a burst of requests arriving on an expired
	// token causes one login rather than one per request.
	mu      sync.Mutex
	token   string
	expires time.Time
	// staticSpent stops the configured token being offered again. Any refusal
	// sets it, not only a refusal of that token: the configured one is offered
	// before the first mint, so by the time a minted token can be refused this
	// is already set and setting it again changes nothing.
	staticSpent bool
}

// NewEverestToken builds a token source. A nil store or session means only the
// static token is available, which is how a laptop run works.
func NewEverestToken(store SecretReader, session EverestSessionFunc, opts EverestTokenOptions) *EverestToken {
	if strings.TrimSpace(opts.Secret) == "" {
		opts.Secret = DefaultEverestAccountsSecret
	}
	if strings.TrimSpace(opts.Username) == "" {
		opts.Username = "admin"
	}

	return &EverestToken{store: store, session: session, opts: opts}
}

// Seed sets the cached token. It exists for tests that need a source holding a
// token with a known expiry.
func (t *EverestToken) Seed(token string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.token = token
	t.expires = everestTokenExpiry(token)
}

// Invalidate drops the cached token. The client calls it when Everest refuses
// one, so the next call mints rather than sending the same token again.
func (t *EverestToken) Invalidate() {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.token = ""
	t.expires = time.Time{}
	t.staticSpent = true
}

// Bearer is Token in the shape the Everest client asks for. refresh means the
// token it last handed over was refused, so that one is dropped before a
// replacement is minted.
func (t *EverestToken) Bearer(ctx context.Context, refresh bool) (string, error) {
	if t == nil {
		return "", ErrEverestTokenUnavailable
	}

	if refresh {
		t.Invalidate()
	}

	return t.Token(ctx)
}

// Token returns a bearer token, minting one if what it holds is spent.
func (t *EverestToken) Token(ctx context.Context) (string, error) {
	if t == nil {
		return "", ErrEverestTokenUnavailable
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token != "" && time.Now().Add(everestTokenMargin).Before(t.expires) {
		return t.token, nil
	}

	// The configured token comes first so an environment that pastes one behaves as
	// it always did.
	if !t.staticSpent && strings.TrimSpace(t.opts.Static) != "" {
		return t.opts.Static, nil
	}

	// Nothing configured and nothing to log in with is not a failure: it is a
	// development Everest that wants no token, and the client sends no
	// Authorization header when it has none. Only a source that could have
	// minted and did not reports one.
	if t.store == nil || t.session == nil {
		return "", nil
	}

	token, err := t.mint(ctx)
	if err != nil {
		return "", err
	}

	t.token = token
	t.expires = everestTokenExpiry(token)

	return token, nil
}

// mint reads the admin password and trades it for a token. The caller holds mu
// and has already established there is an account to read.
func (t *EverestToken) mint(ctx context.Context) (string, error) {
	values, err := t.store.GetSecret(ctx, t.opts.Namespace, t.opts.Secret)
	if err != nil {
		return "", fmt.Errorf("%w: read %s: %w", ErrEverestTokenUnavailable, t.opts.Secret, err)
	}

	password := everestAdminPassword(values[everestAccountsKey])
	if password == "" {
		return "", fmt.Errorf("%w: %s has no %s password",
			ErrEverestTokenUnavailable, t.opts.Secret, t.opts.Username)
	}

	token, err := t.session(ctx, t.opts.Username, password)
	if err != nil {
		// The password is in scope here, so the reason is reported without the
		// values it was derived from.
		return "", fmt.Errorf("%w: %w", ErrEverestTokenUnavailable, err)
	}

	return token, nil
}

// everestAdminPassword reads the password out of the accounts document.
//
// Everest stores the initial admin password in the passwordHash field and
// leaves it as the plain password until somebody rotates it, which is what
// `everestctl accounts initial-admin-password` reads. One field is not worth a
// YAML parser, and this is the same line the install job matches.
func everestAdminPassword(document string) string {
	for _, line := range strings.Split(document, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "passwordHash:" {
			return fields[1]
		}
	}

	return ""
}

// everestTokenExpiry reads exp out of a JWT payload.
//
// The signature is not checked, because this is not a trust decision: the
// instance validates the token, and this only decides when to ask for the next
// one. A token it cannot read counts as expired, so a shape it does not
// recognise costs an extra mint rather than a failed request.
func everestTokenExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}

	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}

	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}
	}

	return time.Unix(claims.Exp, 0)
}
