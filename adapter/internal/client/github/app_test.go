package github_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ghclient "github.com/percona/pds/adapter/internal/client/github"
)

const (
	testAppID          = "12345"
	testInstallationID = "67890"
)

// fakeApp serves the installation token exchange and one API route. Each
// exchange mints a distinct token that expires an hour after the fake clock.
type fakeApp struct {
	t       *testing.T
	key     *rsa.PrivateKey
	clock   *fakeClock
	srv     *httptest.Server
	mu      sync.Mutex
	issued  int
	valid   map[string]bool
	jwts    []string
	apiAuth []string
	// rejectNext makes the next exchange answer with this status.
	rejectNext int
	// refuseIssued makes every token the fake mints fail on the API.
	refuseIssued bool
	// rejectAll makes every exchange answer with this status.
	rejectAll int
	// gate, when set, holds each exchange open until it is closed.
	gate chan struct{}
	// apiGate, when set, holds each API request until a value is sent on it or
	// it is closed, so a test chooses when a request's answer arrives.
	apiGate chan struct{}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newFakeApp(t *testing.T) *fakeApp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	f := &fakeApp{
		t:     t,
		key:   key,
		clock: &fakeClock{now: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)},
		valid: map[string]bool{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)

	return f
}

func (f *fakeApp) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if r.Method == http.MethodPost && r.URL.Path == "/app/installations/"+testInstallationID+"/access_tokens" {
		f.jwts = append(f.jwts, bearer)
		if gate := f.gate; gate != nil {
			f.mu.Unlock()
			<-gate
			f.mu.Lock()
		}
		if f.rejectAll != 0 {
			w.WriteHeader(f.rejectAll)

			return
		}
		if f.rejectNext != 0 {
			status := f.rejectNext
			f.rejectNext = 0
			w.WriteHeader(status)

			return
		}
		f.issued++
		token := fmt.Sprintf("ghs_%d", f.issued)
		f.valid[token] = !f.refuseIssued
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token":      token,
			"expires_at": f.clock.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})

		return
	}
	f.apiAuth = append(f.apiAuth, bearer)
	if gate := f.apiGate; gate != nil {
		f.mu.Unlock()
		<-gate
		f.mu.Lock()
	}
	if !f.valid[bearer] {
		w.WriteHeader(http.StatusUnauthorized)

		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{}`))
}

func (f *fakeApp) client() *ghclient.Client {
	return ghclient.NewApp("percona-cloud", testAppID, testInstallationID, f.key).
		WithBaseURL(f.srv.URL).
		WithClock(f.clock.Now)
}

func (f *fakeApp) exchanges() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.jwts)
}

func TestAppExchangeSignsAnRS256JWTWithTheAppClaims(t *testing.T) {
	f := newFakeApp(t)

	_, err := f.client().HasRepo(context.Background(), "acme")
	require.NoError(t, err)

	require.Len(t, f.jwts, 1)
	parts := strings.Split(f.jwts[0], ".")
	require.Len(t, parts, 3)

	var header map[string]string
	decodeSegment(t, parts[0], &header)
	assert.Equal(t, "RS256", header["alg"])
	assert.Equal(t, "JWT", header["typ"])

	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	decodeSegment(t, parts[1], &claims)
	now := f.clock.Now().Unix()
	assert.Equal(t, testAppID, claims.Iss)
	assert.LessOrEqual(t, claims.Iat, now, "iat leaves room for clock drift")
	assert.Greater(t, claims.Exp, now)
	assert.LessOrEqual(t, claims.Exp-now, int64(600), "GitHub refuses a JWT valid for more than ten minutes")

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	require.NoError(t, rsa.VerifyPKCS1v15(&f.key.PublicKey, crypto.SHA256, digest[:], sig))
}

func TestAppCallsUseTheInstallationTokenAndCacheIt(t *testing.T) {
	f := newFakeApp(t)
	c := f.client()

	for range 3 {
		_, err := c.HasRepo(context.Background(), "acme")
		require.NoError(t, err)
	}

	assert.Equal(t, 1, f.exchanges())
	assert.Equal(t, []string{"ghs_1", "ghs_1", "ghs_1"}, f.apiAuth)
}

func TestAppRefreshesBeforeTheTokenExpires(t *testing.T) {
	f := newFakeApp(t)
	c := f.client()

	_, err := c.HasRepo(context.Background(), "acme")
	require.NoError(t, err)

	f.clock.Advance(30 * time.Minute)
	_, err = c.HasRepo(context.Background(), "acme")
	require.NoError(t, err)
	assert.Equal(t, 1, f.exchanges(), "a token with time left is reused")

	f.clock.Advance(29*time.Minute + 30*time.Second)
	_, err = c.HasRepo(context.Background(), "acme")
	require.NoError(t, err)
	assert.Equal(t, 2, f.exchanges(), "a token inside the refresh margin is replaced")
	assert.Equal(t, "ghs_2", f.apiAuth[len(f.apiAuth)-1])
}

func TestAppRefreshesOnceAfterA401(t *testing.T) {
	f := newFakeApp(t)
	c := f.client()

	_, err := c.HasRepo(context.Background(), "acme")
	require.NoError(t, err)

	f.mu.Lock()
	f.valid["ghs_1"] = false
	f.mu.Unlock()

	status, err := c.HasRepo(context.Background(), "acme")
	require.NoError(t, err)
	assert.True(t, status)
	assert.Equal(t, 2, f.exchanges())
	assert.Equal(t, []string{"ghs_1", "ghs_1", "ghs_2"}, f.apiAuth)
}

func TestAppDoesNotLoopWhenAFreshTokenIsRefused(t *testing.T) {
	f := newFakeApp(t)
	c := f.client()
	_, err := c.HasRepo(context.Background(), "acme")
	require.NoError(t, err)

	f.mu.Lock()
	f.refuseIssued = true
	f.valid["ghs_1"] = false
	f.mu.Unlock()

	_, err = c.HasRepo(context.Background(), "acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")
	assert.Equal(t, 2, f.exchanges(), "one refresh, then the 401 is reported")
}

func TestAppConcurrentCallersShareOneExchange(t *testing.T) {
	f := newFakeApp(t)
	c := f.client()

	var wg sync.WaitGroup
	var failed atomic.Int32
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.HasRepo(context.Background(), "acme"); err != nil {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Zero(t, failed.Load())
	assert.Equal(t, 1, f.exchanges())
}

func TestAppConcurrentCallersShareAFailedExchangeAndALaterCallRetries(t *testing.T) {
	f := newFakeApp(t)
	f.rejectAll = http.StatusServiceUnavailable
	f.gate = make(chan struct{})
	c := f.client()

	const callers = 20
	var started sync.WaitGroup
	var done sync.WaitGroup
	var failed atomic.Int32
	started.Add(callers)
	for range callers {
		done.Add(1)
		go func() {
			defer done.Done()
			started.Done()
			if _, err := c.HasRepo(context.Background(), "acme"); err != nil {
				failed.Add(1)
			}
		}()
	}
	started.Wait()
	require.Eventually(t, func() bool { return f.exchanges() == 1 }, 5*time.Second, time.Millisecond)
	waitForBlockedAppCallers(t, callers-1)
	close(f.gate)
	done.Wait()

	assert.Equal(t, int32(callers), failed.Load())
	assert.Equal(t, 1, f.exchanges())

	f.mu.Lock()
	f.rejectAll = 0
	f.mu.Unlock()
	_, err := c.HasRepo(context.Background(), "acme")
	require.NoError(t, err)
	assert.Equal(t, 2, f.exchanges())
}

// waitForBlockedAppCallers waits until n goroutines are parked waiting for the
// exchange in flight, which is how a test knows its callers queued behind it
// rather than racing it. The caller running the exchange is not counted.
func waitForBlockedAppCallers(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		buf := make([]byte, 1<<20)
		for {
			size := runtime.Stack(buf, true)
			if size < len(buf) {
				buf = buf[:size]

				break
			}
			buf = make([]byte, 2*len(buf))
		}
		blocked := 0
		for _, stack := range strings.Split(string(buf), "\n\n") {
			if strings.Contains(stack, "(*appAuth).current") && !strings.Contains(stack, "(*appAuth).exchange") &&
				strings.Contains(stack, "[select") {
				blocked++
			}
		}

		return blocked == n
	}, 5*time.Second, time.Millisecond)
}

func TestAppConcurrent401sShareAFailedRefreshAndALaterCallRetries(t *testing.T) {
	f := newFakeApp(t)
	c := f.client()
	_, err := c.HasRepo(context.Background(), "acme")
	require.NoError(t, err)
	require.Equal(t, 1, f.exchanges())

	// The cached token is revoked while the token endpoint is down.
	f.mu.Lock()
	f.valid["ghs_1"] = false
	f.rejectAll = http.StatusServiceUnavailable
	f.gate = make(chan struct{})
	f.mu.Unlock()

	const callers = 20
	var done sync.WaitGroup
	var failed atomic.Int32
	for range callers {
		done.Add(1)
		go func() {
			defer done.Done()
			if _, err := c.HasRepo(context.Background(), "acme"); err != nil {
				failed.Add(1)
			}
		}()
	}
	require.Eventually(t, func() bool { return f.exchanges() == 2 }, 5*time.Second, time.Millisecond)
	waitForBlockedAppCallers(t, callers-1)
	close(f.gate)
	done.Wait()

	assert.Equal(t, int32(callers), failed.Load())
	assert.Equal(t, 2, f.exchanges())

	f.mu.Lock()
	f.rejectAll = 0
	f.mu.Unlock()
	_, err = c.HasRepo(context.Background(), "acme")
	require.NoError(t, err)
	assert.Equal(t, 3, f.exchanges())
}

func TestAppLate401sShareAFailedRefreshAndALaterCallRetries(t *testing.T) {
	f := newFakeApp(t)
	c := f.client()
	_, err := c.HasRepo(context.Background(), "acme")
	require.NoError(t, err)
	require.Equal(t, 1, f.exchanges())

	// The cached token is revoked while the token endpoint is down, and each
	// request's 401 arrives on its own schedule.
	f.mu.Lock()
	f.valid["ghs_1"] = false
	f.rejectAll = http.StatusServiceUnavailable
	f.apiGate = make(chan struct{})
	f.mu.Unlock()

	const callers = 10
	var done sync.WaitGroup
	var failed atomic.Int32
	for range callers {
		done.Add(1)
		go func() {
			defer done.Done()
			if _, err := c.HasRepo(context.Background(), "acme"); err != nil {
				failed.Add(1)
			}
		}()
	}
	// Every request has sent ghs_1 and is waiting for its answer.
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()

		return len(f.apiAuth) == 1+callers
	}, 5*time.Second, time.Millisecond)

	// One answer arrives and its refresh fails before any other answer does.
	f.apiGate <- struct{}{}
	require.Eventually(t, func() bool { return failed.Load() == 1 }, 5*time.Second, time.Millisecond)
	require.Equal(t, 2, f.exchanges())
	close(f.apiGate)
	done.Wait()

	assert.Equal(t, int32(callers), failed.Load())
	assert.Equal(t, 2, f.exchanges())

	f.mu.Lock()
	f.rejectAll = 0
	f.apiGate = nil
	f.mu.Unlock()
	_, err = c.HasRepo(context.Background(), "acme")
	require.NoError(t, err)
	assert.Equal(t, 3, f.exchanges())
}

func TestAppWaiterGivesUpWhenItsOwnContextEndsWhileAnExchangeIsInFlight(t *testing.T) {
	f := newFakeApp(t)
	f.gate = make(chan struct{})
	var release sync.Once
	releaseGate := func() { release.Do(func() { close(f.gate) }) }
	t.Cleanup(releaseGate)
	c := f.client()

	leader := make(chan error, 1)
	go func() {
		_, err := c.HasRepo(context.Background(), "acme")
		leader <- err
	}()
	require.Eventually(t, func() bool { return f.exchanges() == 1 }, 5*time.Second, time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	waiter := make(chan error, 1)
	go func() {
		_, err := c.HasRepo(ctx, "acme")
		waiter <- err
	}()
	select {
	case err := <-waiter:
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(2 * time.Second):
		t.Error("a waiter stayed blocked behind the exchange after its own context ended")
	}

	releaseGate()
	require.NoError(t, <-leader)
	assert.Equal(t, 1, f.exchanges())
}

func TestAppExchangeFailureIsReportedWithoutSecrets(t *testing.T) {
	f := newFakeApp(t)
	f.rejectNext = http.StatusUnauthorized

	_, err := f.client().HasRepo(context.Background(), "acme")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "installation token")
	assert.Contains(t, err.Error(), "HTTP 401")
	assert.NotContains(t, err.Error(), f.jwts[0])
}

func TestTokenReturnsTheInstallationTokenForApps(t *testing.T) {
	f := newFakeApp(t)

	got, err := f.client().Token(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "ghs_1", got)
}

func TestTokenReturnsTheStaticTokenWithoutAnApp(t *testing.T) {
	got, err := ghclient.New("percona-cloud", "ghp_static").Token(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "ghp_static", got)
}

func TestParsePrivateKeyAcceptsPKCS1AndPKCS8(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	for name, in := range map[string][]byte{"pkcs1": pkcs1, "pkcs8": pkcs8} {
		got, err := ghclient.ParsePrivateKey(in)
		require.NoError(t, err, name)
		assert.True(t, key.Equal(got), name)
	}
}

func TestParsePrivateKeyRefusesGarbageWithoutEchoingIt(t *testing.T) {
	_, err := ghclient.ParsePrivateKey([]byte("not-a-key-sentinel"))

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "not-a-key-sentinel")
}

func decodeSegment(t *testing.T, seg string, into any) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, into))
}

func TestAppConcurrentCallersShareOneRefreshAfterExpiryAndAfterA401(t *testing.T) {
	cases := map[string]func(f *fakeApp){
		"expired": func(f *fakeApp) { f.clock.Advance(2 * time.Hour) },
		"revoked": func(f *fakeApp) {
			f.mu.Lock()
			f.valid["ghs_1"] = false
			f.mu.Unlock()
		},
	}
	for name, stale := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeApp(t)
			c := f.client()
			_, err := c.HasRepo(context.Background(), "acme")
			require.NoError(t, err)
			stale(f)

			var wg sync.WaitGroup
			var failed atomic.Int32
			for range 20 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := c.HasRepo(context.Background(), "acme"); err != nil {
						failed.Add(1)
					}
				}()
			}
			wg.Wait()

			assert.Zero(t, failed.Load())
			assert.Equal(t, 2, f.exchanges(), "the first exchange plus one shared refresh")
		})
	}
}

func TestAppReplaysAWriteBodyAfterA401(t *testing.T) {
	f := newFakeApp(t)
	var puts []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			puts = append(puts, r.Header.Get("Authorization")+" "+string(raw))
			mu.Unlock()
			if r.Header.Get("Authorization") == "Bearer ghs_1" {
				w.WriteHeader(http.StatusUnauthorized)

				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		default:
			f.serve(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := ghclient.NewApp("percona-cloud", testAppID, testInstallationID, f.key).
		WithBaseURL(srv.URL).WithClock(f.clock.Now)

	require.NoError(t, c.PutFile(context.Background(), "acme", "a/b.yaml", "add b", []byte("kind: x")))

	require.Len(t, puts, 2)
	first := strings.SplitN(puts[0], " ", 3)
	second := strings.SplitN(puts[1], " ", 3)
	assert.Equal(t, "ghs_2", second[1])
	assert.Equal(t, first[2], second[2], "the retry carries the same body")
}

func TestAppRefusesAnExchangeWithoutAnExpiry(t *testing.T) {
	f := newFakeApp(t)
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_x"}`))
	})

	_, err := f.client().Token(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "installation token")
}
