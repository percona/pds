package service_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/service"
)

// jwtExpiring builds a token shaped like the one Everest issues. Only the
// payload matters: the source reads exp to know when to mint again and never
// checks the signature.
func jwtExpiring(in time.Duration) string {
	payload, _ := json.Marshal(map[string]any{"exp": time.Now().Add(in).Unix()})

	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

type fakeAccounts struct {
	mu      sync.Mutex
	values  map[string]string
	err     error
	reads   int
	minted  int
	mintErr error
}

func (f *fakeAccounts) GetSecret(
	_ context.Context,
	_, _ string,
) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.err != nil {
		return nil, f.err
	}

	return f.values, nil
}

func (f *fakeAccounts) mint(context.Context, string, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.minted++
	if f.mintErr != nil {
		return "", f.mintErr
	}

	return jwtExpiring(24 * time.Hour), nil
}

func (f *fakeAccounts) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.reads, f.minted
}

func accountsSecret(password string) map[string]string {
	return map[string]string{
		"users.yaml": strings.Join([]string{
			"admin:",
			"  enabled: true",
			"  passwordHash: " + password,
			"  capabilities:",
			"    - login",
		}, "\n"),
	}
}

func newEverestToken(t *testing.T, accounts *fakeAccounts) *service.EverestToken {
	t.Helper()

	return service.NewEverestToken(accounts, accounts.mint, service.EverestTokenOptions{
		Namespace: "everest-system",
		Secret:    "everest-accounts",
	})
}

func TestEverestTokenMintsFromTheAdminAccount(t *testing.T) {
	accounts := &fakeAccounts{values: accountsSecret("s3cret")}
	source := newEverestToken(t, accounts)

	token, err := source.Token(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	reads, minted := accounts.counts()
	assert.Equal(t, 1, reads)
	assert.Equal(t, 1, minted)
}

// A token is good for a day, so asking for it on every request must not mean
// minting one on every request.
func TestEverestTokenIsMintedOnceWhileItLasts(t *testing.T) {
	accounts := &fakeAccounts{values: accountsSecret("s3cret")}
	source := newEverestToken(t, accounts)

	first, err := source.Token(context.Background())
	require.NoError(t, err)

	for range 20 {
		again, err := source.Token(context.Background())
		require.NoError(t, err)
		assert.Equal(t, first, again)
	}

	_, minted := accounts.counts()
	assert.Equal(t, 1, minted, "one mint covers every call while the token is good")
}

func TestEverestTokenMintsAgainNearExpiry(t *testing.T) {
	accounts := &fakeAccounts{values: accountsSecret("s3cret")}
	source := newEverestToken(t, accounts)
	// Inside the margin, so it counts as spent even though it has not expired.
	source.Seed(jwtExpiring(30 * time.Second))

	_, err := source.Token(context.Background())
	require.NoError(t, err)

	_, minted := accounts.counts()
	assert.Equal(t, 1, minted, "a token about to expire is replaced before it does")
}

func TestEverestTokenMintsAgainAfterInvalidate(t *testing.T) {
	accounts := &fakeAccounts{values: accountsSecret("s3cret")}
	source := newEverestToken(t, accounts)

	_, err := source.Token(context.Background())
	require.NoError(t, err)

	source.Invalidate()

	_, err = source.Token(context.Background())
	require.NoError(t, err)

	_, minted := accounts.counts()
	assert.Equal(t, 2, minted, "a refused token is not handed out a second time")
}

// Every request asks for the token, so a burst of them must not become a burst
// of logins against Everest.
func TestEverestTokenMintsOnceUnderConcurrentCallers(t *testing.T) {
	accounts := &fakeAccounts{values: accountsSecret("s3cret")}
	source := newEverestToken(t, accounts)

	var wg sync.WaitGroup
	for range 25 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := source.Token(context.Background())
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	_, minted := accounts.counts()
	assert.Equal(t, 1, minted)
}

func TestEverestTokenSaysWhenTheAccountsSecretIsMissing(t *testing.T) {
	accounts := &fakeAccounts{err: kube.ErrSecretNotFound}
	source := newEverestToken(t, accounts)

	_, err := source.Token(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, service.ErrEverestTokenUnavailable)
}

func TestEverestTokenSaysWhenTheSecretHasNoPassword(t *testing.T) {
	accounts := &fakeAccounts{values: map[string]string{"users.yaml": "admin:\n  enabled: true\n"}}
	source := newEverestToken(t, accounts)

	_, err := source.Token(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, service.ErrEverestTokenUnavailable)
}

// A laptop run has no Kubernetes to read, and a static token is how that has
// always worked.
func TestEverestTokenFallsBackToAStaticToken(t *testing.T) {
	source := service.NewEverestToken(nil, nil, service.EverestTokenOptions{
		Static: "pasted-token",
	})

	token, err := source.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "pasted-token", token)
}

// The static token is a bootstrap, so once it is spent the source moves on to
// minting rather than handing back something Everest already refused.
func TestEverestTokenLeavesASpentStaticTokenBehind(t *testing.T) {
	accounts := &fakeAccounts{values: accountsSecret("s3cret")}
	source := service.NewEverestToken(accounts, accounts.mint, service.EverestTokenOptions{
		Namespace: "everest-system",
		Secret:    "everest-accounts",
		Static:    "pasted-token",
	})

	first, err := source.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "pasted-token", first)

	source.Invalidate()

	second, err := source.Token(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, "pasted-token", second)
}

func TestEverestTokenReportsAFailedMint(t *testing.T) {
	accounts := &fakeAccounts{
		values:  accountsSecret("s3cret"),
		mintErr: errors.New("everest: 503 Service Unavailable"),
	}
	source := newEverestToken(t, accounts)

	_, err := source.Token(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, service.ErrEverestTokenUnavailable)
}

// A token and the password it came from are both secrets, so neither belongs in
// anything a reader of the logs can see.
func TestEverestTokenKeepsSecretsOutOfItsErrors(t *testing.T) {
	accounts := &fakeAccounts{
		values:  accountsSecret("s3cret-password"),
		mintErr: errors.New("everest: 503 Service Unavailable"),
	}
	source := newEverestToken(t, accounts)

	_, err := source.Token(context.Background())
	require.Error(t, err)
	assert.NotContains(t, fmt.Sprint(err), "s3cret-password")
}

// A development Everest wants no token at all, and the adapter sends no
// Authorization header when it has none. With nothing to mint from and nothing
// configured, that is the answer rather than a failure.
func TestEverestTokenIsUnauthenticatedWithNothingToMintFrom(t *testing.T) {
	source := service.NewEverestToken(nil, nil, service.EverestTokenOptions{})

	token, err := source.Token(context.Background())
	require.NoError(t, err, "no token is a valid state, not an error")
	assert.Empty(t, token)
}

// Having an admin account and failing to use it is a different thing, and that
// one is reported.
func TestEverestTokenStillReportsAFailedMintWhenItCouldTry(t *testing.T) {
	accounts := &fakeAccounts{err: kube.ErrSecretNotFound}
	source := newEverestToken(t, accounts)

	_, err := source.Token(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, service.ErrEverestTokenUnavailable)
}

// Token tolerates a nil receiver, so the two calls the client makes through it
// have to as well. Bearer taking a lock on nil would be a panic in the request
// path rather than the error the rest of this type returns.
func TestEverestTokenOnANilReceiverDoesNotPanic(t *testing.T) {
	var source *service.EverestToken

	assert.NotPanics(t, func() { source.Invalidate() })

	_, err := source.Token(context.Background())
	assert.ErrorIs(t, err, service.ErrEverestTokenUnavailable)

	_, err = source.Bearer(context.Background(), false)
	assert.ErrorIs(t, err, service.ErrEverestTokenUnavailable)

	_, err = source.Bearer(context.Background(), true)
	assert.ErrorIs(t, err, service.ErrEverestTokenUnavailable,
		"a refresh takes the same path as the rest")
}
