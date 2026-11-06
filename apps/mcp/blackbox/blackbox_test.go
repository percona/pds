// Package blackbox tests the pds-mcp binary from outside: a person runs its commands, an agent
// talks to it over stdio, and a control plane built for the test answers.
package blackbox_test

import (
	"context"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/percona/pds/apps/mcp/internal/fakecp"
	"github.com/percona/pds/apps/mcp/internal/harness"
)

func browse(ctx context.Context, address string) error {
	_, err := fakecp.Browse(ctx, address)

	return err
}

func signedIn(t *testing.T, cp *fakecp.Server, args ...string) *harness.Machine {
	t.Helper()
	m := harness.NewMachine(t, cp.URL)
	res, err := m.Login(t.Context(), browse, args...)
	if err != nil || res.Code != 0 {
		t.Fatalf("login: %+v, %v", res, err)
	}

	return m
}

func toolNames(t *testing.T, s *harness.Session) []string {
	t.Helper()
	listed, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)

	return names
}

func TestAnAgentSeesTheToolsOfTheAccessThePersonAllowed(t *testing.T) {
	for access, want := range map[string]string{
		"read":   "list_databases",
		"change": "list_databases,scale_database",
	} {
		cp := fakecp.New(t)
		m := signedIn(t, cp, "--access", access)

		session, err := m.Connect(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", access, err)
		}
		if got := strings.Join(toolNames(t, session), ","); got != want {
			t.Errorf("--access %s: tools = %s, want %s", access, got, want)
		}
		_ = session.Close()
	}
}

func TestTheLoginFileIsPrivateAndHoldsNoAccessToken(t *testing.T) {
	cp := fakecp.New(t)
	m := signedIn(t, cp)

	info, err := os.Stat(m.LoginFile())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("login file: %v, %v", info, err)
	}
	dir, _ := os.Stat(m.Config + "/pds-mcp")
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %v", dir.Mode().Perm())
	}
	data, _ := os.ReadFile(m.LoginFile())
	for _, token := range cp.Tokens() {
		if strings.HasPrefix(token, "at-") && strings.Contains(string(data), token) {
			t.Error("an access token was written to the login file")
		}
	}
}

func TestEachRemovedSettingStopsTheBinaryAndNamesTheLoginCommand(t *testing.T) {
	cp := fakecp.New(t)
	m := harness.NewMachine(t, cp.URL)
	for _, name := range []string{
		"PDS_USERNAME", "PDS_PASSWORD", "PDS_OPERATOR_TOKEN", "PDS_ORGANIZATION", "PDS_TEAM", "PDS_ALLOW_WRITES", "PDS_ALLOW_DESTRUCTIVE",
	} {
		res := m.Run(t.Context(), nil, name+"=sentinel-1d4f")
		if res.Code == 0 || !strings.Contains(res.Stderr, name) || !strings.Contains(res.Stderr, "pds-mcp login") {
			t.Errorf("%s: %+v", name, res)
		}
		if res.Stdout != "" || strings.Contains(res.Stderr, "sentinel-1d4f") {
			t.Errorf("%s: stdout %q, stderr %q", name, res.Stdout, res.Stderr)
		}
	}
	if len(cp.Requests()) != 0 {
		t.Error("the control plane was contacted")
	}
}

func TestAnAgentStartedWithAnOldSettingNeverGetsASession(t *testing.T) {
	cp := fakecp.New(t)
	m := signedIn(t, cp)

	if _, err := m.Connect(t.Context(), "PDS_PASSWORD=sentinel-1d4f"); err == nil {
		t.Error("the bridge started with PDS_PASSWORD set")
	}
}

func TestWithoutALoginTheAgentIsToldToRunTheLoginCommand(t *testing.T) {
	cp := fakecp.New(t)
	m := harness.NewMachine(t, cp.URL)

	session, err := m.Connect(t.Context())
	if err == nil || !strings.Contains(err.Error(), "pds-mcp login") {
		t.Errorf("Connect error = %v, want one naming pds-mcp login", err)
	}
	if !strings.Contains(session.Stderr(), "pds-mcp login") {
		t.Errorf("stderr = %q, want the person told too", session.Stderr())
	}
}

func TestARevokedSessionEndsWithAMessageNamingTheLoginCommand(t *testing.T) {
	cp := fakecp.New(t)
	m := signedIn(t, cp)
	session, err := m.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	cp.ExpireAccessTokens()
	cp.RejectRefreshes(1)
	_, err = session.ListTools(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "pds-mcp login") {
		t.Errorf("tools/list error = %v", err)
	}
}

func TestAControlPlaneThatServesNoMCPIsExplained(t *testing.T) {
	cp := fakecp.New(t)
	m := signedIn(t, cp)
	cp.Configure(func(c *fakecp.Config) { c.MCPOff = true })

	_, err := m.Connect(t.Context())
	if err == nil || !strings.Contains(err.Error(), "does not serve AI agents") {
		t.Errorf("Connect error = %v", err)
	}
}

func TestNoTokenReachesStdoutStderrOrAResult(t *testing.T) {
	cp := fakecp.New(t)
	m := harness.NewMachine(t, cp.URL)
	login, err := m.Login(t.Context(), browse, "--access", "change")
	if err != nil {
		t.Fatal(err)
	}

	session, err := m.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var seen strings.Builder
	for range 3 {
		cp.ExpireAccessTokens()
		listed, err := session.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range listed.Tools {
			seen.WriteString(tool.Name + tool.Description)
		}
	}
	_ = session.Close()

	logout := m.Run(t.Context(), []string{"logout"})
	everything := login.Stdout + login.Stderr + seen.String() + session.Stderr() + logout.Stdout + logout.Stderr
	tokens := cp.Tokens()
	if len(tokens) < 4 {
		t.Fatalf("only %d tokens were issued", len(tokens))
	}
	for _, token := range tokens {
		if strings.Contains(everything, token) {
			t.Errorf("a token reached the output: %s...", token[:6])
		}
	}
}

func TestTheBearerTokenGoesOnlyToTheControlPlane(t *testing.T) {
	cp := fakecp.New(t)
	m := signedIn(t, cp)
	session, err := m.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if _, err := session.ListTools(t.Context(), nil); err != nil {
		t.Fatal(err)
	}

	for _, req := range cp.Requests() {
		if req.Method != http.MethodPost || !strings.HasPrefix(req.Authorization, "Bearer at-") {
			t.Errorf("unexpected request %+v", req)
		}
		if req.UserAgent != "pds-mcp/"+harness.Version {
			t.Errorf("User-Agent = %q", req.UserAgent)
		}
	}
}

func TestAControlPlaneAddressWithoutHTTPSIsRefused(t *testing.T) {
	m := harness.NewMachine(t, "http://pds.example.com")

	for _, args := range [][]string{nil, {"login"}, {"logout"}} {
		res := m.Run(t.Context(), args)
		if res.Code != 2 || !strings.Contains(res.Stderr, "https") {
			t.Errorf("%v: %+v", args, res)
		}
	}
}

func TestVersionIsStamped(t *testing.T) {
	m := harness.NewMachine(t, "https://pds.example.com")

	if res := m.Run(t.Context(), []string{"version"}); res.Stdout != "pds-mcp "+harness.Version+"\n" {
		t.Errorf("stdout = %q", res.Stdout)
	}
}

func TestLogoutThenTheAgentNeedsALoginAgain(t *testing.T) {
	cp := fakecp.New(t)
	m := signedIn(t, cp)

	if res := m.Run(t.Context(), []string{"logout"}); res.Code != 0 {
		t.Fatalf("logout: %+v", res)
	}
	if _, err := m.Connect(t.Context()); err == nil || !strings.Contains(err.Error(), "pds-mcp login") {
		t.Errorf("Connect error = %v", err)
	}
	if len(cp.Revoked()) != 1 {
		t.Errorf("revoked = %v", cp.Revoked())
	}
}
