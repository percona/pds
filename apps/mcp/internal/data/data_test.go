package data_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/apps/mcp/internal/connections"
	"github.com/percona/pds/apps/mcp/internal/fakecp"
	"github.com/percona/pds/apps/mcp/internal/messages"
	"github.com/percona/pds/apps/mcp/internal/sqlrun"
	"github.com/percona/pds/apps/mcp/internal/vault"
)

type connectResult struct {
	ID        string `json:"id"`
	Engine    string `json:"engine"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	User      string `json:"user"`
	Database  string `json:"database"`
	TLS       string `json:"tls"`
	Reachable bool   `json:"reachable"`
	Note      string `json:"note"`
}

func TestAClientThatCanOpenAURLIsAskedToSignInAndThenTheCallWorks(t *testing.T) {
	for name, legacy := range map[string]bool{"2026-07-28": false, "2025-11-25": true} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, options{urlElicitation: true, legacy: legacy})

			res := r.connect()
			if got := text(res); !strings.Contains(got, messages.DataSignInWaiting) {
				t.Fatalf("result %q (error %v), want the waiting sentence", got, res.IsError)
			}
			address := strings.TrimSuffix(<-r.asked, messages.DataSignInPrompt)
			if !strings.HasPrefix(address, r.cp.URL+"/oauth/authorize?") || !strings.Contains(address, "pds%3Acredentials") {
				t.Fatalf("the client was asked to open %q", address)
			}
			if _, err := r.logins.Load(r.cp.URL); err == nil {
				t.Fatal("signed in before the person finished in the browser")
			}

			if status, err := fakecp.Browse(t.Context(), address); err != nil || status != 200 {
				t.Fatalf("browse: %d %v", status, err)
			}
			waitFor(t, "the sign in to be saved", func() bool { _, err := r.logins.Load(r.cp.URL); return err == nil })

			var got connectResult
			res = r.connect()
			r.structured(res, &got)
			if res.IsError || got.Host != "db.example.com" {
				t.Fatalf("result %q", text(res))
			}
		})
	}
}

func TestAClientThatCannotOpenAURLIsToldToRunLogin(t *testing.T) {
	r := newRig(t, options{})

	res := r.connect()
	if !res.IsError || text(res) != messages.DataNeedsLogin {
		t.Fatalf("result %q, want %q", text(res), messages.DataNeedsLogin)
	}
	if len(r.cp.Authorizations()) != 0 {
		t.Fatal("a sign in was started for a client that cannot show it")
	}
}

func TestDecliningTheSignInEndsItAndLeavesNothingWaiting(t *testing.T) {
	r := newRig(t, options{urlElicitation: true, elicitAction: "decline"})

	res := r.connect()
	if !res.IsError || text(res) != messages.DataSignInDeclined {
		t.Fatalf("result %q", text(res))
	}
	if _, err := r.logins.Load(r.cp.URL); err == nil {
		t.Fatal("a sign in was saved")
	}
}

func TestConnectKeepsThePasswordInTheCredentialStoreAndNowhereElse(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets, database: fakecp.Database{URI: dbURI("db.example.com", 3306, "/shop?tls=skip-verify")}})

	res := r.connect()
	var got connectResult
	r.structured(res, &got)
	if res.IsError {
		t.Fatalf("result %q", text(res))
	}
	want := connectResult{ID: databaseID, Engine: "mysql", Host: "db.example.com", Port: 3306, User: "app", Database: "shop", TLS: "require", Reachable: true}
	got.Note = ""
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	account := vault.Account(r.cp.URL, databaseID)
	if stored, err := r.vault.Get(account); err != nil || stored != secret {
		t.Fatalf("the credential store holds %q, %v; want the password", stored, err)
	}
	if accounts := r.vault.Accounts(); len(accounts) != 1 || accounts[0] != account {
		t.Fatalf("accounts %v", accounts)
	}
	if calls := r.runner.Calls(); !slices.Equal(calls, []string{"probe"}) {
		t.Fatalf("database calls %v", calls)
	}

	r.call("list_connections", nil)
	r.call("get_my_ip", nil)
	r.toolNames()
	if all := r.everything(); strings.Contains(all, secret) || strings.Contains(all, "p%40ss") {
		t.Fatal("the password reached a result, a log or a file")
	}

	reads := r.cp.CredentialReads()
	if len(reads) != 1 || !strings.HasPrefix(reads[0].Authorization, "Bearer ") || !strings.HasPrefix(reads[0].UserAgent, "pds-mcp/") {
		t.Fatalf("credential reads %+v", reads)
	}
}

func TestConnectWithoutThePasswordsPermissionPassesTheControlPlanesSentenceThrough(t *testing.T) {
	r := newRig(t, options{scopes: readOnly})

	res := r.connect()
	if !res.IsError || text(res) != fakecp.PasswordsNotAllowed {
		t.Fatalf("result %q, want the control plane's sentence", text(res))
	}
	if len(r.vault.Accounts()) != 0 || len(r.runner.Calls()) != 0 {
		t.Fatal("something was stored or contacted")
	}
}

func TestAPrivateDatabasePassesTheControlPlanesSentenceThrough(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets, database: fakecp.Database{Private: true}})

	res := r.connect()
	if !res.IsError || text(res) != fakecp.NoPasswordHeld {
		t.Fatalf("result %q, want the control plane's sentence", text(res))
	}
	if len(r.vault.Accounts()) != 0 {
		t.Fatal("a password was stored")
	}
}

func TestAMissingDatabaseIsAnErrorThatNamesItAndNothingIsCreatedOrSaved(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	r.runner.probeErr = sqlrun.ErrNoDatabase

	res := r.call("connect_database", map[string]any{"id": databaseID, "database": "nosuch"})
	if !res.IsError || text(res) != messages.DatabaseMissing("nosuch") {
		t.Fatalf("result %q", text(res))
	}
	if calls := r.runner.Calls(); !slices.Equal(calls, []string{"probe"}) {
		t.Fatalf("database calls %v: a missing database must only be probed", calls)
	}
	if len(r.vault.Accounts()) != 0 || len(listed(r)) != 0 {
		t.Fatal("something was saved")
	}
}

func TestADatabaseThatRefusesTheCredentialIsNotSaved(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	r.runner.probeErr = sqlrun.ErrRefused

	if res := r.connect(); !res.IsError || text(res) != messages.CredentialRefused {
		t.Fatalf("result %q", text(res))
	}
	if len(r.vault.Accounts()) != 0 {
		t.Fatal("a password was stored")
	}
}

func TestADatabaseThatCannotBeReachedIsSavedWithAnAdvisoryNote(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	r.runner.probeErr = sqlrun.ErrUnreachable

	res := r.connect()
	var got connectResult
	r.structured(res, &got)
	if res.IsError || got.Reachable || !strings.Contains(got.Note, "get_my_ip") {
		t.Fatalf("result %q", text(res))
	}
	if len(r.vault.Accounts()) != 1 {
		t.Fatal("the connection was not saved for later")
	}
}

func TestACredentialThatNeedsTLSIsNeverDowngraded(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets, database: fakecp.Database{URI: "postgresql://app:x@db.example.com:5432/shop?sslmode=verify-full"}})
	r.connect()
	if got := listed(r); len(got) != 1 || got[0].Target.TLS != sqlrun.TLSVerify || got[0].Target.Engine != sqlrun.PostgreSQL {
		t.Fatalf("saved %+v", got)
	}

	for uri, want := range map[string]sqlrun.TLSMode{
		"postgresql://app:x@db.example.com/shop?sslmode=prefer":      sqlrun.TLSRequire,
		"postgresql://app:x@db.example.com/shop":                     sqlrun.TLSRequire,
		"postgresql://app:x@127.0.0.1/shop":                          sqlrun.TLSOff,
		"postgresql://app:x@db.example.com/shop?sslmode=disable":     sqlrun.TLSOff,
		"mysql://app:x@db.example.com/shop?tls=true":                 sqlrun.TLSVerify,
		"mysql://app:x@db.example.com/shop?ssl-mode=VERIFY_IDENTITY": sqlrun.TLSVerify,
		"mysql://app:x@db.example.com/shop?ssl-mode=REQUIRED":        sqlrun.TLSRequire,
		"mysql://app:x@db.example.com/shop":                          sqlrun.TLSRequire,
	} {
		r := newRig(t, options{scopes: withSecrets, database: fakecp.Database{URI: uri}})
		r.connect()
		if got := listed(r); len(got) != 1 || got[0].Target.TLS != want {
			t.Errorf("%s: saved %+v, want %s", uri, got, want)
		}
	}
}

func TestAComputerWithNoCredentialStoreSavesNothing(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	r.vault.Err = vault.ErrUnavailable

	if res := r.connect(); !res.IsError || text(res) != messages.NoCredentialStore {
		t.Fatalf("result %q", text(res))
	}
	if len(listed(r)) != 0 {
		t.Fatal("a connection was saved without its password")
	}
	if _, err := os.Stat(filepath.Join(r.config, "pds-mcp", "connections.json")); err == nil {
		t.Fatal("a connections file was written")
	}
}

func TestACredentialThatIsNotADatabaseIsRefused(t *testing.T) {
	for _, uri := range []string{"http://app:x@db.example.com/shop", "mysql://db.example.com/shop", "mysql://app@db.example.com/shop", "not a uri", "oracle://a:b@h/db"} {
		r := newRig(t, options{scopes: withSecrets, database: fakecp.Database{URI: uri}})
		res := r.connect()
		if !res.IsError || len(r.vault.Accounts()) != 0 {
			t.Errorf("%q: result %q", uri, text(res))
		}
		if strings.Contains(text(res), "db.example.com") {
			t.Errorf("%q: the answer repeats the credential", uri)
		}
	}
}

func TestInputThatIsNotADatabaseIdNeverReachesTheControlPlane(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	for _, id := range []string{"../environments/x", "acme", "acme/pds.shop/credential", "ACME/pds.shop", "acme/pds.shop?x=1", ""} {
		if res := r.call("connect_database", map[string]any{"id": id}); !res.IsError {
			t.Errorf("%q was accepted", id)
		}
	}
	if len(r.cp.CredentialReads()) != 0 {
		t.Fatal("the control plane was asked")
	}
}

func TestAReadRunsAsAQueryAndIsBounded(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	r.connect()
	r.runner.result = sqlrun.Result{Columns: []string{"n"}, Rows: [][]any{{int64(1)}}, Truncated: true}

	res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": "SELECT 1", "maxRows": 5000})
	if res.IsError {
		t.Fatalf("result %q", text(res))
	}
	var got struct {
		Columns   []string `json:"columns"`
		Rows      [][]any  `json:"rows"`
		Truncated bool     `json:"truncated"`
	}
	r.structured(res, &got)
	if !got.Truncated || len(got.Rows) != 1 || got.Columns[0] != "n" {
		t.Fatalf("got %+v", got)
	}

	r.runner.mu.Lock()
	defer r.runner.mu.Unlock()
	last := len(r.runner.calls) - 1
	if r.runner.calls[last] != "read" || r.runner.passes[last] != secret || r.runner.limits[last].MaxRows != 1000 ||
		r.runner.limits[last].MaxBytes == 0 || r.runner.limits[last].Timeout == 0 {
		t.Fatalf("the read ran as %s with limits %+v", r.runner.calls[last], r.runner.limits[last])
	}
}

func TestAReadThatIsNotAQueryNeverReachesTheDatabase(t *testing.T) {
	r := newRig(t, options{scopes: withWrites})
	r.connect()
	before := len(r.runner.Calls())

	for _, statement := range []string{
		"SELECT 1; DROP TABLE t", "COMMIT", "ROLLBACK", "SET TRANSACTION READ WRITE", "START TRANSACTION READ WRITE",
		"INSERT INTO t VALUES (1)", "SELECT * INTO OUTFILE '/tmp/x' FROM t", "SELECT LOAD_FILE('/etc/passwd')",
		"COPY t TO PROGRAM 'id'", "SELECT pg_read_file('/etc/passwd')", "WITH d AS (DELETE FROM t RETURNING 1) SELECT 1", "",
	} {
		if res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": statement}); !res.IsError {
			t.Errorf("%q was accepted", statement)
		}
	}
	if after := len(r.runner.Calls()); after != before {
		t.Fatalf("%d statement(s) reached the database", after-before)
	}
}

func TestADatabaseThatWasNeverConnectedIsNotQueried(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})

	if res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": "SELECT 1"}); !res.IsError || text(res) != messages.NoConnection {
		t.Fatalf("result %q", text(res))
	}
	if len(r.runner.Calls()) != 0 {
		t.Fatal("the database was contacted")
	}
}

func TestAnEngineErrorIsPassedOnAndAStoredPasswordIsNotRepeated(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	r.connect()

	r.runner.err = &sqlrun.QueryError{Message: "syntax error near FROM"}
	if res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": "SELECT FROM"}); !res.IsError || text(res) != "syntax error near FROM" {
		t.Fatalf("result %q", text(res))
	}
	for err, want := range map[error]string{
		sqlrun.ErrTimeout: messages.StatementTimedOut, sqlrun.ErrRefused: messages.StoredRefused,
		sqlrun.ErrUnreachable: messages.UnreachableHost, sqlrun.ErrTLS: messages.TLSFailed,
		errors.New("dial tcp db.example.com:3306 password=" + secret): messages.QueryFailed,
	} {
		r.runner.err = err
		if res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": "SELECT 1"}); !res.IsError || text(res) != want {
			t.Errorf("%v: result %q, want %q", err, text(res), want)
		}
	}
	if strings.Contains(strings.Join(r.all, ""), secret) {
		t.Fatal("the password reached a result")
	}
}

func TestRunQueryIsListedOnlyWithTheSQLWriteGrantAndEnforcedWhenCalledAnyway(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	r.connect()

	if slices.Contains(r.toolNames(), "run_query") {
		t.Fatal("run_query is listed without the grant")
	}
	res, err := r.session.CallTool(t.Context(), &sdk.CallToolParams{Name: "run_query", Arguments: map[string]any{"id": databaseID, "sql": "CREATE TABLE t (id int)"}})
	if err == nil && !res.IsError {
		t.Fatal("run_query ran without the grant")
	}
	if slices.Contains(r.runner.Calls(), "write") {
		t.Fatal("a write reached the database")
	}
}

func TestRunQueryAppearsAfterASignInThatGrantsItAndTheClientIsTold(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	r.connect()
	if slices.Contains(r.toolNames(), "run_query") {
		t.Fatal("run_query is listed without the grant")
	}

	fakecp.SignIn(t, r.cp, r.logins, withWrites...)
	<-r.changed
	waitFor(t, "run_query to be listed", func() bool { return slices.Contains(r.toolNames(), "run_query") })

	r.runner.result = sqlrun.Result{RowsAffected: 1}
	res := r.call("run_query", map[string]any{"id": databaseID, "sql": "INSERT INTO t VALUES (1)"})
	var got struct {
		RowsAffected int64 `json:"rowsAffected"`
	}
	r.structured(res, &got)
	if res.IsError || got.RowsAffected != 1 {
		t.Fatalf("result %q", text(res))
	}
	if calls := r.runner.Calls(); calls[len(calls)-1] != "write" {
		t.Fatalf("calls %v", calls)
	}
}

func TestAGrantTheOrganizationHasWithdrawnIsNotUsedAndTheToolDisappears(t *testing.T) {
	r := newRig(t, options{scopes: withWrites})
	r.connect()
	if !slices.Contains(r.toolNames(), "run_query") {
		t.Fatal("run_query is not listed with the grant")
	}

	// The organization turns SQL writes off: the next refresh carries less.
	r.cp.Configure(func(c *fakecp.Config) { c.Allowed = []string{"pds:read", "pds:credentials"} })
	r.cp.ExpireAccessTokens()

	before := len(r.runner.Calls())
	res := r.call("run_query", map[string]any{"id": databaseID, "sql": "INSERT INTO t VALUES (1)"})
	if !res.IsError || text(res) != messages.NoSQLWrite {
		t.Fatalf("result %q", text(res))
	}
	if len(r.runner.Calls()) != before {
		t.Fatal("a write reached the database")
	}
	waitFor(t, "run_query to leave the list", func() bool { return !slices.Contains(r.toolNames(), "run_query") })
}

func TestRunQueryRunsOneStatementAndNeverTransactionControl(t *testing.T) {
	r := newRig(t, options{scopes: withWrites})
	r.connect()
	before := len(r.runner.Calls())

	for _, statement := range []string{"INSERT INTO t VALUES (1); DROP TABLE t", "COMMIT", "BEGIN", "SET search_path = x", "COPY t FROM PROGRAM 'id'", "SELECT pg_read_file('/etc/passwd')"} {
		if res := r.call("run_query", map[string]any{"id": databaseID, "sql": statement}); !res.IsError {
			t.Errorf("%q was accepted", statement)
		}
	}
	if len(r.runner.Calls()) != before {
		t.Fatal("a statement reached the database")
	}

	r.runner.result = sqlrun.Result{Columns: []string{"id"}, Rows: [][]any{{int64(7)}}}
	r.call("run_query", map[string]any{"id": databaseID, "sql": "INSERT INTO t VALUES (7) RETURNING id"})
	if calls := r.runner.Calls(); calls[len(calls)-1] != "writeRows" {
		t.Fatalf("a statement that returns rows ran as %v", calls)
	}
}

func TestListConnectionsHasNamesAndNoSecrets(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	r.connect()

	res := r.call("list_connections", nil)
	var got struct {
		Connections []connectResult `json:"connections"`
	}
	r.structured(res, &got)
	if len(got.Connections) != 1 || got.Connections[0].ID != databaseID || got.Connections[0].User != "app" {
		t.Fatalf("got %+v", got)
	}
	if strings.Contains(text(res), secret) {
		t.Fatal("the password is in the list")
	}
	if strings.Contains(text(res), "reachable") {
		t.Fatal("the list says whether a database was reached, which it never checked")
	}
}

func TestGetMyIPReturnsASlash32AndFailsClosed(t *testing.T) {
	r := newRig(t, options{})
	res := r.call("get_my_ip", nil)
	var got struct {
		Address string `json:"address"`
	}
	r.structured(res, &got)
	if res.IsError || got.Address != "203.0.113.7/32" {
		t.Fatalf("result %q", text(res))
	}

	failing := newRig(t, options{publicIP: func(context.Context) (string, error) { return "", errors.New("proxy page <html> 10.0.0.1") }})
	if res := failing.call("get_my_ip", nil); !res.IsError || text(res) != messages.NoPublicAddress {
		t.Fatalf("result %q", text(res))
	}
}

func TestWriteEnvWritesAPrivateFileAndNeverAPasswordUnlessAsked(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	r.connect()
	path := filepath.Join(r.workDir, ".env")

	res := r.call("write_env", map[string]any{"id": databaseID})
	if res.IsError {
		t.Fatalf("result %q", text(res))
	}
	raw, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 || strings.Contains(string(raw), secret) || !strings.Contains(string(raw), "DB_HOST=db.example.com\n") ||
		!strings.Contains(string(raw), "DB_NAME=shop\n") || strings.Contains(string(raw), "PASSWORD") {
		t.Fatalf("mode %v, file %q", info.Mode().Perm(), raw)
	}
	if !strings.Contains(text(res), messages.EnvNote) {
		t.Fatalf("result %q does not say not to commit the file", text(res))
	}

	confirming := newRig(t, options{scopes: withSecrets, formElicitation: true})
	confirming.connect()
	res = confirming.call("write_env", map[string]any{"id": databaseID, "includePassword": true, "prefix": "SHOP"})
	raw, _ = os.ReadFile(filepath.Join(confirming.workDir, ".env"))
	if res.IsError || !strings.Contains(string(raw), "SHOP_PASSWORD='"+secret+"'") {
		t.Fatalf("result %q, file %q", text(res), raw)
	}
	for _, shown := range append(confirming.all, text(res)) {
		if strings.Contains(shown, secret) {
			t.Fatal("the password was echoed")
		}
	}
}

func TestWriteEnvSaysWhatItLeftAloneAndRefusesOtherFiles(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	r.connect()
	path := filepath.Join(r.workDir, ".env")
	if err := os.WriteFile(path, []byte("DB_HOST=mine.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := r.call("write_env", map[string]any{"id": databaseID})
	var got struct {
		Written []string `json:"written"`
		Skipped []string `json:"skipped"`
	}
	r.structured(res, &got)
	if raw, _ := os.ReadFile(path); !strings.HasPrefix(string(raw), "DB_HOST=mine.example.com\n") || !slices.Equal(got.Skipped, []string{"DB_HOST"}) {
		t.Fatalf("file %q, skipped %v", raw, got.Skipped)
	}

	for _, bad := range []string{"../.env", "/etc/passwd", ".bashrc", "id_rsa", ".ssh/authorized_keys"} {
		if res := r.call("write_env", map[string]any{"id": databaseID, "path": bad}); !res.IsError {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestTheAccessAdvicePromptSaysWhichAddressesToAllow(t *testing.T) {
	r := newRig(t, options{})

	prompt, err := r.session.GetPrompt(t.Context(), &sdk.GetPromptParams{Name: "database_access_advice"})
	if err != nil {
		t.Fatal(err)
	}
	advice := prompt.Messages[0].Content.(*sdk.TextContent).Text
	for _, want := range []string{"same VPC", "get_my_ip", "/32", "Never allow 0.0.0.0/0"} {
		if !strings.Contains(advice, want) {
			t.Errorf("the advice does not say %q", want)
		}
	}
}

func TestEverythingTheDataServerSaysFollowsTheCustomerCopyRules(t *testing.T) {
	r := newRig(t, options{scopes: withWrites})
	var shown []string
	for tool, err := range r.session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(tool)
		shown = append(shown, string(raw))
	}
	for prompt, err := range r.session.Prompts(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		shown = append(shown, prompt.Name+" "+prompt.Description)
	}
	shown = append(shown, r.session.InitializeResult().Instructions)
	for _, sentence := range shown {
		for _, rule := range messages.Banned {
			if rule.MatchString(sentence) {
				t.Errorf("%q matches %s", sentence, rule)
			}
		}
	}
	if len(shown) < 7 {
		t.Fatalf("only %d texts were checked", len(shown))
	}
}

func TestAPasswordIsWrittenToAnEnvFileOnlyWhenAClientThatCanAskTheUserConfirms(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for action, written := range map[string]bool{"accept": true, "decline": false, "cancel": false} {
			r := newRig(t, options{scopes: withSecrets, formElicitation: true, elicitAction: action, legacy: legacy})
			r.connect()

			res := r.call("write_env", map[string]any{"id": databaseID, "includePassword": true, "prefix": "SHOP"})
			raw, _ := os.ReadFile(filepath.Join(r.workDir, ".env"))
			if has := strings.Contains(string(raw), "SHOP_PASSWORD"); has != written || res.IsError == written {
				t.Errorf("legacy %v, %s: password written %v, error %v, result %q", legacy, action, has, res.IsError, text(res))
			}
			if !written && (len(raw) != 0 || text(res) != messages.EnvNotConfirmed) {
				t.Errorf("legacy %v, %s: file %q, result %q", legacy, action, raw, text(res))
			}
			// The user is told which file and which name, so a confirmation cannot be reused for another.
			if asked := <-r.asked; !strings.Contains(asked, "plain text") || !strings.Contains(asked, ".env") || !strings.Contains(asked, "SHOP_PASSWORD") {
				t.Errorf("legacy %v, %s: the user was asked %q", legacy, action, asked)
			}
		}
	}
}

func TestAPasswordOrAnOverwriteIsRefusedWhenTheClientCannotAskTheUser(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	r.connect()
	path := filepath.Join(r.workDir, ".env")
	if err := os.WriteFile(path, []byte("DB_HOST=mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range []map[string]any{
		{"id": databaseID, "includePassword": true}, {"id": databaseID, "overwrite": true},
	} {
		if res := r.call("write_env", args); !res.IsError || text(res) != messages.EnvNeedsConfirmation {
			t.Errorf("%v: result %q", args, text(res))
		}
	}
	if raw, _ := os.ReadFile(path); string(raw) != "DB_HOST=mine\n" {
		t.Fatalf("the file changed: %q", raw)
	}
}

func TestSettingsWithoutAPasswordAreWrittenWithoutAsking(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets, formElicitation: true, elicitAction: "decline"})
	r.connect()

	if res := r.call("write_env", map[string]any{"id": databaseID}); res.IsError {
		t.Fatalf("result %q", text(res))
	}
	select {
	case asked := <-r.asked:
		t.Fatalf("the user was asked %q", asked)
	default:
	}
}

func TestAnyRefusalTheControlPlaneWordsIsPassedThrough(t *testing.T) {
	for _, status := range []int{400, 409, 502, 503} {
		r := newRig(t, options{scopes: withSecrets, database: fakecp.Database{Fail: status}})
		if res := r.connect(); !res.IsError || text(res) != fakecp.NotAvailable {
			t.Errorf("status %d: result %q", status, text(res))
		}
	}
}

func TestAnEndedSessionIsToldApartFromATroubledConnection(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	r.connect()
	r.cp.ExpireAccessTokens()
	r.cp.RejectRefreshes(1)

	// The cached token is not known to be bad, so the route answers 401 and the refresh is refused.
	if res := r.connect(); !res.IsError || text(res) != messages.SessionEnded {
		t.Fatalf("result %q", text(res))
	}
}

func TestListConnectionsOverAnUnreadableFileSaysSo(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	path := filepath.Join(r.config, "pds-mcp", "connections.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if res := r.call("list_connections", nil); !res.IsError || text(res) != messages.DataUnreadable {
		t.Fatalf("result %q", text(res))
	}
}

func TestReconnectingKeepsTheWorkingPasswordWhenTheConnectionCannotBeSaved(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets})
	r.connect()
	account := vault.Account(r.cp.URL, databaseID)
	if err := os.WriteFile(filepath.Join(r.config, "pds-mcp", "connections.json"), []byte(`{"version": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r.cp.AddDatabase(databaseID, fakecp.Database{URI: dbURI2("mysql", "app", "another-password", "db.example.com", 3306, "/shop")})

	if res := r.connect(); !res.IsError {
		t.Fatalf("result %q", text(res))
	}
	if got, err := r.vault.Get(account); err != nil || got != secret {
		t.Fatalf("the vault holds %q, %v; want the password that worked", got, err)
	}
}

func TestAWriteThatReturnsRowsDoesNotClaimACount(t *testing.T) {
	r := newRig(t, options{scopes: withWrites})
	r.connect()
	r.runner.result = sqlrun.Result{Columns: []string{"id"}, Rows: [][]any{{int64(7)}}}

	res := r.call("run_query", map[string]any{"id": databaseID, "sql": "INSERT INTO t VALUES (7) RETURNING id"})
	if res.IsError || strings.Contains(text(res), "rowsAffected") || !strings.Contains(text(res), `"rows":[[7]]`) {
		t.Fatalf("result %q", text(res))
	}
}

func TestSigningOutRemovesTheWriteToolAtOnce(t *testing.T) {
	r := newRig(t, options{scopes: withWrites})
	if !slices.Contains(r.toolNames(), "run_query") {
		t.Fatal("run_query is not listed with the grant")
	}
	if err := r.logins.Delete(r.cp.URL); err != nil {
		t.Fatal(err)
	}

	if slices.Contains(r.toolNames(), "run_query") {
		t.Fatal("run_query is still listed after the sign in was removed")
	}
}

func listed(r *rig) []connections.Entry {
	r.t.Helper()
	entries, err := r.conns.List(r.cp.URL)
	if err != nil {
		r.t.Fatal(err)
	}

	return entries
}
