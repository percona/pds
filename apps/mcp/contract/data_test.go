package contract_test

import (
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/apps/mcp/internal/harness"
)

// The data server's own tools run on the person's computer. What a real control plane adds is the
// credential route, so these tests check that it refuses an agent whose sign in has no passwords
// grant, in the control plane's own words, and that the write tool is not offered.
func TestTheDataServerIsRefusedPasswordsWithoutTheGrantAndOffersNoWriteTool(t *testing.T) {
	if realURL == "" {
		t.Skip("set PDS_REAL_URL, PDS_REAL_USERNAME and PDS_REAL_PASSWORD to run the contract tests against a control plane")
	}
	signInAsThePerson(t.Context(), t).allowAccess(t.Context(), "read")

	m := harness.NewMachine(t, realURL)
	if login, err := m.Login(t.Context(), browse("read")); err != nil || login.Code != 0 {
		t.Fatalf("login: %+v, %v", login, err)
	}
	session, err := m.ConnectData(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	var names []string
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	for _, want := range []string{"connect_database", "run_read_query", "list_connections", "get_my_ip", "write_env"} {
		if !slices.Contains(names, want) {
			t.Errorf("the data server does not list %s: %v", want, names)
		}
	}
	if slices.Contains(names, "run_query") {
		t.Error("the write tool is listed without the grant")
	}

	res, err := session.CallTool(t.Context(), &sdk.CallToolParams{
		Name: "connect_database", Arguments: map[string]any{"id": "contract-env/pds.contract-db"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			said = append(said, tc.Text)
		}
	}
	if !res.IsError || !strings.Contains(strings.Join(said, " "), "database passwords") {
		t.Fatalf("answer %v, error %v: want the control plane's refusal", said, res.IsError)
	}
}
