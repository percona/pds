package cli_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/percona/pds/apps/mcp/internal/cli"
	"github.com/percona/pds/apps/mcp/internal/fakecp"
	"github.com/percona/pds/apps/mcp/internal/tokenstore"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

type run struct {
	stdout, stderr *syncBuffer
	opened         chan string
	home           string
	env            map[string]string
	interactive    bool
}

func newRun(t *testing.T, cp *fakecp.Server) *run {
	t.Helper()
	home := t.TempDir()

	return &run{
		stdout: &syncBuffer{}, stderr: &syncBuffer{}, opened: make(chan string, 1), home: home,
		env: map[string]string{"PDS_URL": cp.URL, "XDG_CONFIG_HOME": home},
	}
}

func (r *run) do(ctx context.Context, args ...string) int {
	return cli.Run(ctx, cli.Env{
		Stdin: io.NopCloser(strings.NewReader("")), Stdout: nopCloser{r.stdout}, Stderr: r.stderr,
		Getenv:      func(name string) string { return r.env[name] },
		Version:     "1.2.3",
		Interactive: r.interactive,
		OpenBrowser: func(address string) error { r.opened <- address; return nil },
	}, args)
}

// browse answers the sign in the way a person in a browser would.
func (r *run) browse(t *testing.T) {
	t.Helper()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		select {
		case address := <-r.opened:
			// The test's own context ends as soon as the command returns, which can be
			// before the browser has read the page the command served, so the browser
			// outlives it and the test waits for it to finish.
			if _, err := fakecp.Browse(context.WithoutCancel(t.Context()), address); err != nil {
				t.Errorf("browse: %v", err)
			}
		case <-stop:
		}
	}()
	t.Cleanup(func() {
		close(stop)
		<-done
	})
}

func (r *run) loginFile() string { return filepath.Join(r.home, "pds-mcp", "credentials.json") }

func TestLoginOpensTheBrowserAndKeepsOnlyTheRefreshTokenInAPrivateFile(t *testing.T) {
	cp := fakecp.New(t)
	r := newRun(t, cp)
	r.browse(t)

	if code := r.do(t.Context(), "login", "--access", "change"); code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, r.stderr.String())
	}

	info, err := os.Stat(r.loginFile())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("login file: %v, %v", info, err)
	}
	dir, _ := os.Stat(filepath.Dir(r.loginFile()))
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %v", dir.Mode().Perm())
	}
	login, err := tokenstore.New(r.loginFile()).Load(cp.URL)
	if err != nil || login.RefreshToken == "" || !strings.Contains(login.Scope, "pds:change") {
		t.Errorf("stored login = %+v, %v", login, err)
	}
	raw, _ := os.ReadFile(r.loginFile())
	for _, token := range cp.Tokens() {
		if strings.HasPrefix(token, "at-") && strings.Contains(string(raw), token) {
			t.Error("an access token was written to disk")
		}
	}
	everything := r.stdout.String() + r.stderr.String()
	for _, token := range cp.Tokens() {
		if strings.Contains(everything, token) {
			t.Error("a token was printed")
		}
	}
	if !strings.Contains(r.stdout.String(), "Signed in") {
		t.Errorf("stdout = %q", r.stdout.String())
	}
}

func TestLoginAsksForTheAccessThePersonNamed(t *testing.T) {
	cases := map[string]string{
		"read": "pds:read offline_access", "change": "pds:read pds:change offline_access",
		"delete": "pds:read pds:change pds:delete offline_access",
	}
	for access, want := range cases {
		cp := fakecp.New(t)
		r := newRun(t, cp)
		var address string
		done := make(chan struct{})
		go func() {
			defer close(done)
			address = <-r.opened
			_, _ = fakecp.Browse(t.Context(), address)
		}()

		if code := r.do(t.Context(), "login", "--access", access); code != 0 {
			t.Fatalf("%s: exit code %d, stderr %q", access, code, r.stderr.String())
		}
		<-done
		u, _ := url.Parse(address)
		if got := u.Query().Get("scope"); got != want {
			t.Errorf("--access %s asked for %q, want %q", access, got, want)
		}
	}
}

func TestLoginAsksForReadOnlyUnlessToldOtherwise(t *testing.T) {
	cp := fakecp.New(t)
	r := newRun(t, cp)
	var address string
	done := make(chan struct{})
	go func() {
		defer close(done)
		address = <-r.opened
		_, _ = fakecp.Browse(t.Context(), address)
	}()

	if code := r.do(t.Context(), "login"); code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, r.stderr.String())
	}
	<-done
	u, _ := url.Parse(address)
	if got := u.Query().Get("scope"); got != "pds:read offline_access" {
		t.Errorf("scope = %q, want read only", got)
	}
}

func TestLoginWithNoBrowserPrintsTheAddressAndOpensNothing(t *testing.T) {
	cp := fakecp.New(t)
	r := newRun(t, cp)
	done := make(chan int, 1)
	go func() { done <- r.do(t.Context(), "login", "--no-browser") }()

	var address string
	for address == "" {
		for _, field := range strings.Fields(r.stdout.String()) {
			if strings.HasPrefix(field, cp.URL+"/oauth/authorize") {
				address = field
			}
		}
	}
	if status, err := fakecp.Browse(t.Context(), address); err != nil || status != http.StatusOK {
		t.Fatalf("browse: %d, %v", status, err)
	}
	if code := <-done; code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, r.stderr.String())
	}
	select {
	case <-r.opened:
		t.Error("a browser was opened")
	default:
	}
}

func TestADeclinedLoginSaysSoAndSavesNothing(t *testing.T) {
	cp := fakecp.New(t)
	cp.Configure(func(c *fakecp.Config) { c.Deny = true })
	r := newRun(t, cp)
	r.browse(t)

	if code := r.do(t.Context(), "login"); code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if !strings.Contains(r.stderr.String(), "declined") || !strings.Contains(r.stderr.String(), "pds-mcp login") {
		t.Errorf("stderr = %q", r.stderr.String())
	}
	if _, err := os.Stat(r.loginFile()); err == nil {
		t.Error("a login file was written")
	}
}

func TestLoginExplainsAControlPlaneThatServesNoMCP(t *testing.T) {
	cp := fakecp.New(t)
	cp.Configure(func(c *fakecp.Config) { c.MCPOff = true })
	r := newRun(t, cp)

	if code := r.do(t.Context(), "login"); code != 1 || !strings.Contains(r.stderr.String(), "does not serve AI agents") {
		t.Errorf("exit code %d, stderr %q", code, r.stderr.String())
	}
}

func TestLogoutRemovesTheLoginAndEndsTheSession(t *testing.T) {
	cp := fakecp.New(t)
	r := newRun(t, cp)
	r.browse(t)
	if code := r.do(t.Context(), "login"); code != 0 {
		t.Fatal(r.stderr.String())
	}
	saved, _ := tokenstore.New(r.loginFile()).Load(cp.URL)

	if code := r.do(t.Context(), "logout"); code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, r.stderr.String())
	}
	if _, err := os.Stat(r.loginFile()); err == nil {
		t.Error("the login file is still there")
	}
	if revoked := cp.Revoked(); len(revoked) != 1 || revoked[0] != saved.RefreshToken {
		t.Errorf("revoked = %v", revoked)
	}
	if strings.Contains(r.stdout.String()+r.stderr.String(), saved.RefreshToken) {
		t.Error("the refresh token was printed")
	}
}

func TestLogoutWithoutALoginSaysThereIsNone(t *testing.T) {
	cp := fakecp.New(t)
	r := newRun(t, cp)

	if code := r.do(t.Context(), "logout"); code != 0 || !strings.Contains(r.stdout.String(), "No sign in is saved") {
		t.Errorf("exit code %d, stdout %q", code, r.stdout.String())
	}
}

func TestEachRemovedSettingStopsEveryCommandThatSignsIn(t *testing.T) {
	for _, name := range []string{
		"PDS_USERNAME", "PDS_PASSWORD", "PDS_OPERATOR_TOKEN", "PDS_ORGANIZATION", "PDS_TEAM", "PDS_ALLOW_WRITES", "PDS_ALLOW_DESTRUCTIVE",
	} {
		for _, args := range [][]string{nil, {"login"}, {"logout"}} {
			cp := fakecp.New(t)
			r := newRun(t, cp)
			r.env[name] = "sentinel-secret-value"

			code := r.do(t.Context(), args...)
			if code != 2 {
				t.Errorf("%s with %v: exit code %d, want 2", name, args, code)
			}
			if !strings.Contains(r.stderr.String(), name) || !strings.Contains(r.stderr.String(), "pds-mcp login") {
				t.Errorf("%s with %v: stderr = %q", name, args, r.stderr.String())
			}
			if strings.Contains(r.stdout.String()+r.stderr.String(), "sentinel-secret-value") {
				t.Errorf("%s: the value was printed", name)
			}
			if r.stdout.String() != "" {
				t.Errorf("%s with %v: stdout = %q, want nothing", name, args, r.stdout.String())
			}
			if len(cp.Requests()) != 0 || len(cp.Forms()) != 0 {
				t.Errorf("%s with %v: the control plane was contacted", name, args)
			}
		}
	}
}

func TestVersionNeedsNoSettings(t *testing.T) {
	cp := fakecp.New(t)
	r := newRun(t, cp)
	r.env["PDS_PASSWORD"] = "x"

	if code := r.do(t.Context(), "version"); code != 0 || r.stdout.String() != "pds-mcp 1.2.3\n" {
		t.Errorf("exit code %d, stdout %q", code, r.stdout.String())
	}
}

func TestTypedAtATerminalTheBridgeExplainsInsteadOfWaiting(t *testing.T) {
	cp := fakecp.New(t)
	r := newRun(t, cp)
	r.interactive = true

	if code := r.do(t.Context()); code != 2 || !strings.Contains(r.stderr.String(), "Usage: pds-mcp") {
		t.Errorf("exit code %d, stderr %q", code, r.stderr.String())
	}
}

func TestUnknownCommandsAndFlagsExplainTheUsage(t *testing.T) {
	cp := fakecp.New(t)
	for _, args := range [][]string{{"frobnicate"}, {"login", "--access", "root"}, {"login", "--bogus"}, {"logout", "now"}} {
		r := newRun(t, cp)
		if code := r.do(t.Context(), args...); code != 2 || r.stdout.String() != "" || !strings.Contains(r.stderr.String(), "Usage:") {
			t.Errorf("%v: exit code %d, stdout %q, stderr %q", args, code, r.stdout.String(), r.stderr.String())
		}
	}
}

func TestLoginAsksForPasswordsAndSQLWritesWhenToldAndSQLWritesNeedPasswords(t *testing.T) {
	cases := map[string][]string{
		"passwords": {"--passwords"}, "sql": {"--sql-write"}, "both": {"--passwords", "--sql-write"},
	}
	want := map[string]string{
		"passwords": "pds:read pds:credentials offline_access",
		"sql":       "pds:read pds:credentials pds:sql_write offline_access",
		"both":      "pds:read pds:credentials pds:sql_write offline_access",
	}
	for name, flags := range cases {
		cp := fakecp.New(t)
		r := newRun(t, cp)
		r.browse(t)

		if code := r.do(t.Context(), append([]string{"login"}, flags...)...); code != 0 {
			t.Fatalf("%s: exit code %d, stderr %q", name, code, r.stderr.String())
		}
		if got := cp.Authorizations()[0].Get("scope"); got != want[name] {
			t.Errorf("%s: asked for %q, want %q", name, got, want[name])
		}
	}
}

func TestLoginSaysWhenTheOrganizationDoesNotAllowWhatWasAskedFor(t *testing.T) {
	cp := fakecp.New(t)
	cp.Configure(func(c *fakecp.Config) { c.Allowed = []string{"pds:read"} })
	r := newRun(t, cp)
	r.browse(t)

	if code := r.do(t.Context(), "login", "--passwords", "--sql-write"); code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, r.stderr.String())
	}
	out := r.stdout.String()
	for _, want := range []string{"database passwords", "SQL writes", "organization admin"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout %q does not say %q", out, want)
		}
	}
	login, err := tokenstore.New(r.loginFile()).Load(cp.URL)
	if err != nil || strings.Contains(login.Scope, "pds:credentials") {
		t.Errorf("stored login %+v, %v", login, err)
	}
}

func TestDataTakesNoArgumentsAndNeedsNoTerminal(t *testing.T) {
	cp := fakecp.New(t)
	r := newRun(t, cp)

	if code := r.do(t.Context(), "data", "--bogus"); code != 2 || !strings.Contains(r.stderr.String(), "pds-mcp data") {
		t.Errorf("exit code %d, stderr %q", code, r.stderr.String())
	}
	r = newRun(t, cp)
	r.interactive = true
	if code := r.do(t.Context(), "data"); code != 2 || !strings.Contains(r.stderr.String(), "pds-mcp data") {
		t.Errorf("at a terminal: exit code %d, stderr %q", code, r.stderr.String())
	}
}
