// Package contract_test holds the checks every tool must keep, run against a real control plane
// through the built bridge, the way an agent meets it. The tools live in the control plane; these
// tests prove that the bridge passes them, and the safety rules inside them, through unchanged.
package contract_test

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/apps/mcp/internal/harness"
	"github.com/percona/pds/apps/mcp/internal/tokenstore"
)

var readTools = []string{
	"get_activity", "get_cloud_connection", "get_database", "get_database_alerts", "get_database_history", "get_database_usage",
	"get_environment", "get_environment_history", "get_metrics", "get_support_ticket", "list_activity", "list_alerts",
	"list_backups", "list_cloud_connections", "list_databases", "list_environments", "list_support_tickets", "wait_for_backup",
	"wait_for_database", "wait_for_environment",
}

var changeTools = []string{
	"backup_database", "create_database", "create_environment", "create_support_ticket",
	"reply_to_support_ticket", "restore_database", "scale_database", "set_backup_schedule",
	"create_managed_environment", "retry_environment_run", "start_cloud_connection", "upgrade_environment",
}

var deleteTools = []string{"delete_database", "delete_managed_environment", "rebuild_environment", "remove_cloud_connection"}

// tiers maps what the person allows on the consent screen to the flag that asks for it and the
// tools an agent then sees.
var tiers = []struct {
	name, consent, flag string
	tools               []string
}{
	{"read", "read", "read", readTools},
	{"change", "change", "change", join(readTools, changeTools)},
	{"change and delete", "change_delete", "delete", join(readTools, changeTools, deleteTools)},
}

func join(lists ...[]string) []string {
	var all []string
	for _, l := range lists {
		all = append(all, l...)
	}
	sort.Strings(all)

	return all
}

// signedIn signs the person in to a fresh computer at the given tier and starts the bridge.
func signedIn(t *testing.T, consent, flag string) (*harness.Machine, *harness.Session, harness.Result) {
	t.Helper()
	if realURL == "" {
		t.Skip("set PDS_REAL_URL, PDS_REAL_USERNAME and PDS_REAL_PASSWORD to run the contract tests against a control plane")
	}
	signInAsThePerson(t.Context(), t).allowAccess(t.Context(), "change_delete")

	m := harness.NewMachine(t, realURL)
	login, err := m.Login(t.Context(), browse(consent), "--access", flag)
	if err != nil || login.Code != 0 {
		t.Fatalf("login: %+v, %v", login, err)
	}
	session, err := m.Connect(t.Context())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return m, session, login
}

func names(t *testing.T, s *harness.Session) []string {
	t.Helper()
	listed, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tool := range listed.Tools {
		out = append(out, tool.Name)
	}
	sort.Strings(out)

	return out
}

func TestEachTierSeesExactlyItsTools(t *testing.T) {
	for _, tier := range tiers {
		t.Run(tier.name, func(t *testing.T) {
			_, session, _ := signedIn(t, tier.consent, tier.flag)

			got, want := names(t, session), tier.tools
			if strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("tools:\n got %v\nwant %v", got, want)
			}
		})
	}
}

func TestNoToolReturnsOrTakesACredential(t *testing.T) {
	_, session, _ := signedIn(t, "change_delete", "delete")
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	forbidden := regexp.MustCompile(`(?i)password|credential|secret|token|private.?key`)
	for _, tool := range listed.Tools {
		if forbidden.MatchString(tool.Name) {
			t.Errorf("the tool name %q names a credential", tool.Name)
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		_ = json.Unmarshal(raw, &schema)
		for property := range schema.Properties {
			if forbidden.MatchString(property) {
				t.Errorf("%s takes %q", tool.Name, property)
			}
		}
	}
}

// The customer copy rules of the repository's AGENTS.md, applied to every string an agent reads.
var bannedCopy = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bPMM\b`), regexp.MustCompile(`(?i)\bFlux\b`), regexp.MustCompile(`(?i)\bthe hub\b`),
	regexp.MustCompile(`(?i)\bthe adapter\b`), regexp.MustCompile(`(?i)\bspoke\b`),
	regexp.MustCompile(`(?i)(^|[^a-z])clusters?\b`), regexp.MustCompile(`\x{2014}|\x{2013}`),
}

// infrastructure is what the rules allow: the Kubernetes under an environment.
var infrastructure = regexp.MustCompile(`(?i)\b(kubernetes|eks) clusters?\b|\bin-cluster\b`)

func TestEveryStringAnAgentReadsFollowsTheCustomerCopyRules(t *testing.T) {
	_, session, _ := signedIn(t, "change_delete", "delete")
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tool := range listed.Tools {
		raw, _ := json.Marshal(tool)
		var document any
		_ = json.Unmarshal(raw, &document)
		texts := append(stringsUnder(document, "description", "title"), tool.Name)
		texts = append(texts, propertyKeys(document)...)
		for _, text := range texts {
			text = infrastructure.ReplaceAllString(text, "")
			for _, rule := range bannedCopy {
				if rule.MatchString(text) {
					t.Errorf("%s: %q matches %s", tool.Name, text, rule)
				}
			}
		}
	}
}

// propertyKeys collects the names of every schema property, at any depth. A property is named by a
// key of a "properties" object, which is never a keyword of the schema itself.
func propertyKeys(node any) []string {
	var found []string
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if props, ok := v.(map[string]any); ok && k == "properties" {
				for name := range props {
					found = append(found, name)
				}
			}
			found = append(found, propertyKeys(v)...)
		}
	case []any:
		for _, v := range n {
			found = append(found, propertyKeys(v)...)
		}
	}

	return found
}

// stringsUnder collects the string values held under the given keys, at any depth.
func stringsUnder(node any, keys ...string) []string {
	var found []string
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if s, ok := v.(string); ok {
				for _, key := range keys {
					if k == key {
						found = append(found, s)
					}
				}
				continue
			}
			found = append(found, stringsUnder(v, keys...)...)
		}
	case []any:
		for _, v := range n {
			found = append(found, stringsUnder(v, keys...)...)
		}
	}

	return found
}

func call(t *testing.T, s *harness.Session, name string, args map[string]any) (string, map[string]any, bool) {
	t.Helper()
	result, err := s.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var text strings.Builder
	for _, c := range result.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(text.String()), &body)

	return text.String(), body, result.IsError
}

func TestAHostileIdIsRefusedBeforeItGoesAnywhere(t *testing.T) {
	_, session, _ := signedIn(t, "change_delete", "delete")

	for _, id := range []string{
		"../session", "a/../b", "env/ns.%2e%2e", "env/ns.name?x=1", "env/ns.name#frag", "env/ns.name\n",
		"env/ns.name/extra", "http://evil.example/x", "", " ", strings.Repeat("a", 5000), "env/ns.na me",
	} {
		for _, tool := range []string{"get_database", "list_backups", "scale_database", "delete_database"} {
			args := map[string]any{"id": id}
			if tool == "scale_database" {
				args["members"] = 3
			}
			text, body, isError := call(t, session, tool, args)
			if !isError || body["kind"] != "invalid_input" {
				t.Errorf("%s with id %q answered %q, want an invalid_input error", tool, id, text)
			}
		}
	}
}

func TestADatabaseThatDoesNotExistIsNotFound(t *testing.T) {
	_, session, _ := signedIn(t, "change_delete", "delete")

	text, body, isError := call(t, session, "list_backups", map[string]any{"id": "edge-1/pds.missing"})
	if !isError || body["kind"] != "not_found" {
		t.Errorf("answered %q", text)
	}
}

func TestAnOpenRangeNeedsAnAcknowledgement(t *testing.T) {
	_, session, _ := signedIn(t, "change_delete", "delete")
	base := map[string]any{"name": "contract-check", "environment": "edge-1", "profile": "dev", "access": "public"}

	text, body, isError := call(t, session, "create_database", base)
	if !isError || body["kind"] != "invalid_input" || !strings.Contains(text, "sourceRanges") {
		t.Errorf("a public database with no ranges answered %q", text)
	}

	open := map[string]any{"sourceRanges": []string{"0.0.0.0/0"}}
	for k, v := range base {
		open[k] = v
	}
	text, body, isError = call(t, session, "create_database", open)
	if !isError || body["kind"] != "confirmation_required" {
		t.Errorf("the whole internet with no acknowledgement answered %q", text)
	}
}

func TestAChangeToolIsRefusedAtTheReadTier(t *testing.T) {
	_, session, _ := signedIn(t, "read", "read")

	text, body, isError := call(t, session, "scale_database", map[string]any{"id": "edge-1/pds.db", "members": 3})
	if !isError || body["kind"] != "not_allowed" {
		t.Errorf("answered %q, want a not_allowed error", text)
	}
}

func TestNoSecretReachesAnOutput(t *testing.T) {
	m, session, login := signedIn(t, "change_delete", "delete")

	var seen strings.Builder
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"list_environments", nil}, {"list_databases", nil}, {"list_activity", nil}, {"list_alerts", nil},
		{"list_support_tickets", nil}, {"get_database", map[string]any{"id": "edge-1/pds.missing"}},
		{"get_database", map[string]any{"id": "../session"}},
	} {
		text, _, _ := call(t, session, c.tool, c.args)
		seen.WriteString(text)
	}
	listed, _ := session.ListTools(t.Context(), nil)
	raw, _ := json.Marshal(listed)
	seen.Write(raw)
	_ = session.Close()

	data, err := os.ReadFile(m.LoginFile())
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Logins map[string]tokenstore.Login
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	secrets := []string{realPassword}
	for _, l := range file.Logins {
		secrets = append(secrets, l.RefreshToken)
	}

	everything := seen.String() + session.Stderr() + login.Stdout + login.Stderr
	for _, secret := range secrets {
		if secret == "" || len(secret) < 8 {
			t.Fatalf("the test has no secret to look for")
		}
		if strings.Contains(everything, secret) {
			t.Errorf("a secret of %d characters reached an output", len(secret))
		}
	}
}

func TestAfterLogoutTheAgentIsToldToSignInAgain(t *testing.T) {
	m, session, _ := signedIn(t, "change", "change")
	_ = session.Close()

	if res := m.Run(context.Background(), []string{"logout"}); res.Code != 0 {
		t.Fatalf("logout: %+v", res)
	}
	if _, err := m.Connect(t.Context()); err == nil || !strings.Contains(err.Error(), "pds-mcp login") {
		t.Errorf("Connect error = %v, want one naming pds-mcp login", err)
	}
}
