package blackbox_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/apps/mcp/internal/fakecp"
	"github.com/percona/pds/apps/mcp/internal/harness"
	"github.com/percona/pds/apps/mcp/internal/messages"
)

func callData(t *testing.T, s interface {
	CallTool(context.Context, *sdk.CallToolParams) (*sdk.CallToolResult, error)
}, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}

	return strings.Join(parts, "\n"), res.IsError
}

func TestTheDataServerListsTheWriteToolOnlyForASignInThatHoldsTheGrant(t *testing.T) {
	for name, tc := range map[string]struct {
		flags []string
		write bool
	}{
		"read":      {nil, false},
		"passwords": {[]string{"--passwords"}, false},
		"sql write": {[]string{"--sql-write"}, true},
	} {
		cp := fakecp.New(t)
		m := signedIn(t, cp, tc.flags...)

		session, err := m.ConnectData(t.Context(), nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := toolNames(t, session)
		if slices.Contains(got, "run_query") != tc.write || !slices.Contains(got, "run_read_query") || !slices.Contains(got, "connect_database") {
			t.Errorf("%s: tools = %v", name, got)
		}
		_ = session.Close()
	}
}

func TestTheDataServerNeverOpensABrowserAndAsksTheClientInstead(t *testing.T) {
	cp := fakecp.New(t)
	m := harnessMachine(t, cp)
	marker := openerTrap(t, m)

	// A client that cannot show a URL is told to run the command.
	plain, err := m.ConnectData(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if text, isError := callData(t, plain, "connect_database", map[string]any{"id": "acme/pds.shop"}); !isError || text != messages.DataNeedsLogin {
		t.Fatalf("answer %q", text)
	}
	_ = plain.Close()

	// A client that can is handed the address, and the person finishes in the browser.
	asked := make(chan string, 1)
	session, err := m.ConnectData(t.Context(), &sdk.ClientOptions{
		Capabilities: &sdk.ClientCapabilities{Elicitation: &sdk.ElicitationCapabilities{URL: &sdk.URLElicitationCapabilities{}}},
		ElicitationHandler: func(_ context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
			asked <- req.Params.URL

			return &sdk.ElicitResult{Action: "accept"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	if text, _ := callData(t, session, "connect_database", map[string]any{"id": "acme/pds.shop"}); !strings.Contains(text, messages.DataSignInWaiting) {
		t.Fatalf("answer %q", text)
	}
	address := <-asked
	if !strings.HasPrefix(address, cp.URL+"/oauth/authorize?") {
		t.Fatalf("asked to open %q", address)
	}
	if _, err := fakecp.Browse(t.Context(), address); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the sign in to be saved", func() bool { _, err := os.Stat(m.LoginFile()); return err == nil })

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the data server started a browser")
	}
}

func TestADatabasePasswordIsRefusedWithoutThePasswordsGrantAndNothingIsSaved(t *testing.T) {
	cp := fakecp.New(t)
	cp.AddDatabase("acme/pds.shop", fakecp.Database{URI: "mysql://app:secret-value@db.example.com:3306/shop"})
	cp.AddDatabase("acme/pds.private", fakecp.Database{Private: true})

	m := signedIn(t, cp)
	session, err := m.ConnectData(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	if text, isError := callData(t, session, "connect_database", map[string]any{"id": "acme/pds.shop"}); !isError || text != fakecp.PasswordsNotAllowed {
		t.Fatalf("answer %q", text)
	}
	if _, err := os.Stat(filepath.Join(m.Config, "pds-mcp", "connections.json")); err == nil {
		t.Fatal("a connection was saved")
	}
	if strings.Contains(session.Stderr(), "secret-value") {
		t.Fatal("the password is in the log")
	}
}

func TestAPrivateDatabasePassesTheControlPlanesSentenceThroughTheBinary(t *testing.T) {
	cp := fakecp.New(t)
	cp.AddDatabase("acme/pds.private", fakecp.Database{Private: true})

	m := signedIn(t, cp, "--passwords")
	session, err := m.ConnectData(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	if text, isError := callData(t, session, "connect_database", map[string]any{"id": "acme/pds.private"}); !isError || text != fakecp.NoPasswordHeld {
		t.Fatalf("answer %q", text)
	}
}

func harnessMachine(t *testing.T, cp *fakecp.Server) *harness.Machine {
	t.Helper()

	return harness.NewMachine(t, cp.URL)
}

// openerTrap puts programs named open and xdg-open first on the machine's PATH. Each leaves a file
// behind when it runs, so a test can tell that something tried to start a browser.
func openerTrap(t *testing.T, m *harness.Machine) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "browser-was-started")
	script := "#!/bin/sh\necho \"$@\" > " + marker + "\n"
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil { //nolint:gosec // a test program that has to be executable.
			t.Fatal(err)
		}
	}
	m.Env = append(m.Env, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return marker
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
