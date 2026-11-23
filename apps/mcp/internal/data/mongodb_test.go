package data_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/apps/mcp/internal/data"
	"github.com/percona/pds/apps/mcp/internal/fakecp"
	"github.com/percona/pds/apps/mcp/internal/messages"
	"github.com/percona/pds/apps/mcp/internal/sqlrun"
	"github.com/percona/pds/apps/mcp/internal/vault"
)

// pdsMongoURI is the string the control plane gives for a MongoDB database: every member, the
// replica set, the auth database as the path, and plain transport.
func pdsMongoURI(tail string) string {
	return "mongodb://dba:" + escaped(secret) + "@pds-rs0-0.ns.svc:27017,pds-rs0-1.ns.svc:27017,pds-rs0-2.ns.svc:27017" + tail
}

func escaped(s string) string {
	return strings.NewReplacer("@", "%40", ":", "%3A", "/", "%2F", "#", "%23").Replace(s)
}

func mongoRig(t *testing.T, scopes []string, tail string) *rig {
	t.Helper()

	return newRig(t, options{scopes: scopes, database: fakecp.Database{URI: pdsMongoURI(tail)}})
}

func TestAnAgentConnectsWithTheStringTheControlPlaneGivesForMongoDB(t *testing.T) {
	r := mongoRig(t, withSecrets, "/admin?replicaSet=rs0&ssl=false")

	res := r.connect()
	if res.IsError {
		t.Fatalf("result %q", text(res))
	}
	var got data.ConnectResult
	r.structured(res, &got)
	if got.Engine != "mongodb" || got.Host != "pds-rs0-0.ns.svc" || got.Port != 27017 || got.User != "dba" || got.Database != "admin" || got.TLS != "off" || !got.Reachable {
		t.Fatalf("got %+v", got)
	}
	if len(got.Members) != 3 || got.Members[2] != "pds-rs0-2.ns.svc:27017" || got.ReplicaSet != "rs0" {
		t.Fatalf("members %v, replica set %q", got.Members, got.ReplicaSet)
	}

	entries := listed(r)
	if len(entries) != 1 || entries[0].Target.AuthSource != "admin" || entries[0].Target.Direct {
		t.Fatalf("saved %+v", entries)
	}
	if stored, err := r.vault.Get(vault.Account(r.cp.URL, databaseID)); err != nil || stored != secret {
		t.Fatalf("the credential store holds %q, %v", stored, err)
	}
	if calls := r.runner.Calls(); !slices.Equal(calls, []string{"probe"}) || r.runner.targets[0].Engine != sqlrun.MongoDB {
		t.Fatalf("database calls %v", calls)
	}
	r.call("list_connections", nil)
	if all := r.everything(); strings.Contains(all, secret) || strings.Contains(all, escaped(secret)) {
		t.Fatal("the password reached a result, a log or a file")
	}
}

func TestADirectConnectionAndAnotherDatabaseAreKept(t *testing.T) {
	r := newRig(t, options{scopes: withSecrets, database: fakecp.Database{URI: "mongodb://dba:" + escaped(secret) + "@127.0.0.1:27017/admin?directConnection=true&replicaSet=rs0&ssl=false"}})
	if res := r.call("connect_database", map[string]any{"id": databaseID, "database": "shop"}); res.IsError {
		t.Fatalf("result %q", text(res))
	}

	entries := listed(r)
	if len(entries) != 1 {
		t.Fatalf("saved %+v", entries)
	}
	got := entries[0].Target
	if !got.Direct || got.Database != "shop" || got.AuthSource != "admin" || got.Host != "127.0.0.1" || len(got.Members) != 1 {
		t.Fatalf("saved %+v", got)
	}
}

func TestTheTransportOfAMongoDBStringIsReadAsGivenAndNeverDowngraded(t *testing.T) {
	for tail, want := range map[string]sqlrun.TLSMode{
		"/admin?ssl=false":    sqlrun.TLSOff,
		"/admin?tls=false":    sqlrun.TLSOff,
		"/admin?ssl=true":     sqlrun.TLSVerify,
		"/admin?tls=true":     sqlrun.TLSVerify,
		"/admin":              sqlrun.TLSRequire,
		"/admin?ssl=whatever": sqlrun.TLSRequire,
	} {
		r := mongoRig(t, withSecrets, tail)
		r.connect()
		if got := listed(r); len(got) != 1 || got[0].Target.TLS != want {
			t.Errorf("%s: saved %+v, want %s", tail, got, want)
		}
	}
}

func TestAMongoDBStringThatIsNotUsableIsRefusedWithoutRepeatingIt(t *testing.T) {
	for _, uri := range []string{
		"mongodb+srv://dba:x@srv.example.com/admin",
		"mongodb://dba:x@/admin",
		"mongodb://dba@h:27017/admin",
		"mongodb://dba:x@h:99999/admin",
		"mongodb://dba:x@h:27017,:27017/admin",
		"mongodb://dba:x@h:27017,h2:abc/admin",
		"mongodb://dba:x@h:27017/admin?replicaSet=a%20b",
		"mongodb://dba:x@h:27017/admin?authSource=a/b",
		"mongodb://dba:x@h:27017/admin?directConnection=maybe",
		"mongodb://dba:x@h:27017/a b",
	} {
		r := newRig(t, options{scopes: withSecrets, database: fakecp.Database{URI: uri}})
		res := r.connect()
		if !res.IsError || len(r.vault.Accounts()) != 0 {
			t.Errorf("%q: result %q", uri, text(res))
		}
		if strings.Contains(text(res), "27017") {
			t.Errorf("%q: the answer repeats the credential", uri)
		}
	}
}

func TestAMongoDBStringWithNoDatabaseAsksForOne(t *testing.T) {
	r := mongoRig(t, withSecrets, "?replicaSet=rs0&ssl=false")
	if res := r.connect(); !res.IsError || text(res) != messages.NameTheDatabase {
		t.Fatalf("result %q", text(res))
	}
}

func TestEveryMongoDBFailureOfTheDatabaseIsToldTheSameWayAsForTheSQLEngines(t *testing.T) {
	for err, want := range map[error]string{
		sqlrun.ErrRefused: messages.CredentialRefused, sqlrun.ErrTLS: messages.TLSFailed,
	} {
		r := mongoRig(t, withSecrets, "/admin?ssl=false")
		r.runner.probeErr = err
		if res := r.connect(); !res.IsError || text(res) != want || len(r.vault.Accounts()) != 0 {
			t.Errorf("%v: result %q", err, text(res))
		}
	}
	r := mongoRig(t, withSecrets, "/admin?ssl=false")
	r.runner.probeErr = sqlrun.ErrUnreachable
	var got data.ConnectResult
	r.structured(r.connect(), &got)
	if got.Reachable || got.Note != messages.UnreachableHost {
		t.Fatalf("got %+v", got)
	}
}

func TestAMongoDBReadRunsOneCheckedCommandOnTheConnectionsDatabase(t *testing.T) {
	r := mongoRig(t, withSecrets, "/admin?replicaSet=rs0&ssl=false")
	r.call("connect_database", map[string]any{"id": databaseID, "database": "shop"})
	r.runner.result = sqlrun.Result{Columns: []string{"document"}, Rows: [][]any{{json.RawMessage(`{"_id":1}`)}}, Truncated: true}

	res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": `{"find": "orders", "filter": {"a": 1}}`, "maxRows": 5000})
	if res.IsError {
		t.Fatalf("result %q", text(res))
	}
	var got data.QueryResult
	r.structured(res, &got)
	if got.RowCount != 1 || !got.Truncated || got.Columns[0] != "document" || !strings.Contains(text(res), `{"_id":1}`) {
		t.Fatalf("got %+v from %q", got, text(res))
	}

	r.runner.mu.Lock()
	defer r.runner.mu.Unlock()
	last := len(r.runner.calls) - 1
	if r.runner.calls[last] != "command:shop" || r.runner.statemts[last] != `{"find":"orders","filter":{"a":1}}` || r.runner.passes[last] != secret ||
		r.runner.limits[last].MaxRows != 1000 || r.runner.limits[last].MaxBytes == 0 || r.runner.limits[last].Timeout == 0 {
		t.Fatalf("ran as %s %s with %+v", r.runner.calls[last], r.runner.statemts[last], r.runner.limits[last])
	}
}

func TestListDatabasesRunsOnAdmin(t *testing.T) {
	r := mongoRig(t, withSecrets, "/shop?ssl=false")
	r.connect()
	r.call("run_read_query", map[string]any{"id": databaseID, "sql": `{"listDatabases": 1}`})
	if calls := r.runner.Calls(); calls[len(calls)-1] != "command:admin" {
		t.Fatalf("calls %v", calls)
	}
}

func TestTheReadToolNeverSendsAMongoDBWriteOrAnythingOffTheList(t *testing.T) {
	r := mongoRig(t, withWrites, "/admin?ssl=false")
	r.connect()
	before := len(r.runner.Calls())

	for sql, want := range map[string]string{
		`{"insert": "c", "documents": [{"a": 1}]}`:                         messages.MongoNotRead,
		`{"update": "c", "updates": [{"q": {}, "u": {"$set": {"a": 1}}}]}`: messages.MongoNotRead,
		`{"delete": "c", "deletes": [{"q": {}, "limit": 0}]}`:              messages.MongoNotRead,
		`{"drop": "c"}`: messages.MongoNotRead,
		`{"aggregate": "c", "pipeline": [{"$match": {}}, {"$out": "z"}], "cursor": {}}`:   messages.MongoWriteStage,
		`{"aggregate": "c", "pipeline": [{"$merge": {"into": "z"}}], "cursor": {}}`:       messages.MongoWriteStage,
		`{"find": "c", "filter": {"$where": "sleep(5000)"}}`:                              messages.MongoJavaScript,
		`{"mapReduce": "c", "map": "function(){}", "reduce": "function(){}", "out": "z"}`: messages.MongoJavaScript,
		`{"createUser": "x", "pwd": "y", "roles": ["root"]}`:                              messages.MongoAdmin,
		`{"dropDatabase": 1}`:         messages.MongoAdmin,
		`{"find": "c"} {"drop": "c"}`: messages.MongoNotJSON,
		`SELECT 1`:                    messages.MongoNotJSON,
		``:                            messages.MongoEmpty,
	} {
		res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": sql})
		if !res.IsError || text(res) != want {
			t.Errorf("%s: result %q, want %q", sql, text(res), want)
		}
	}
	for _, sql := range []string{`{"ping": 1}`, `{"serverStatus": 1}`, `{"explain": {"find": "c"}}`} {
		if res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": sql}); !res.IsError || !strings.Contains(text(res), "does not run") && text(res) != messages.MongoAdmin {
			t.Errorf("%s: result %q", sql, text(res))
		}
	}
	if len(r.runner.Calls()) != before {
		t.Fatal("a command reached the database")
	}
}

func TestTheWriteToolNeedsTheGrantForMongoDBAndRefusesTheSameListsWithIt(t *testing.T) {
	denied := mongoRig(t, withSecrets, "/admin?ssl=false")
	denied.connect()
	if res, err := denied.session.CallTool(t.Context(), &sdk.CallToolParams{Name: "run_query", Arguments: map[string]any{"id": databaseID, "sql": `{"insert": "c", "documents": [{"a": 1}]}`}}); err == nil && !res.IsError {
		t.Fatal("run_query ran without the grant")
	}
	if len(denied.runner.Calls()) != 1 {
		t.Fatal("a write reached the database without the grant")
	}

	r := mongoRig(t, withWrites, "/admin?ssl=false")
	r.call("connect_database", map[string]any{"id": databaseID, "database": "shop"})
	r.runner.result = sqlrun.Result{Columns: []string{"document"}, Rows: [][]any{{json.RawMessage(`{"n":1,"ok":1}`)}}}
	res := r.call("run_query", map[string]any{"id": databaseID, "sql": `{"insert": "c", "documents": [{"a": 1}]}`})
	if res.IsError || !strings.Contains(text(res), `{"n":1,"ok":1}`) || strings.Contains(text(res), "rowsAffected") {
		t.Fatalf("result %q", text(res))
	}
	if calls := r.runner.Calls(); calls[len(calls)-1] != "command:shop" {
		t.Fatalf("calls %v", calls)
	}

	before := len(r.runner.Calls())
	for _, sql := range []string{
		`{"find": "c", "filter": {"$where": "1"}}`, `{"createUser": "x"}`, `{"dropDatabase": 1}`, `{"somethingNew": 1}`,
		`{"drop": "c", "find": "c"}`, `{"insert": "system.users", "documents": [{}]}`, `{"insert": "c", "documents": [], "$db": "admin"}`,
	} {
		if res := r.call("run_query", map[string]any{"id": databaseID, "sql": sql}); !res.IsError {
			t.Errorf("%s was accepted", sql)
		}
	}
	if len(r.runner.Calls()) != before {
		t.Fatal("a command reached the database")
	}
}

func TestAMongoDBCommandThatRanPastTheTimeLimitSaysSo(t *testing.T) {
	r := mongoRig(t, withSecrets, "/admin?ssl=false")
	r.connect()
	r.runner.err = sqlrun.ErrTimeout
	if res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": `{"find": "c"}`}); !res.IsError || text(res) != messages.StatementTimedOut {
		t.Fatalf("result %q", text(res))
	}
	r.runner.err = errors.New("dial tcp pds-rs0-0.ns.svc:27017: password " + secret)
	if res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": `{"find": "c"}`}); !res.IsError || text(res) != messages.QueryFailed {
		t.Fatalf("result %q", text(res))
	}
}

func TestWriteEnvWritesAMongoDBConnectionStringWithoutThePasswordUnlessAsked(t *testing.T) {
	r := mongoRig(t, withSecrets, "/admin?replicaSet=rs0&ssl=false")
	r.connect()
	res := r.call("write_env", map[string]any{"id": databaseID})
	if res.IsError {
		t.Fatalf("result %q", text(res))
	}
	raw, _ := os.ReadFile(filepath.Join(r.workDir, ".env"))
	file := string(raw)
	want := "DB_URL='mongodb://dba@pds-rs0-0.ns.svc:27017,pds-rs0-1.ns.svc:27017,pds-rs0-2.ns.svc:27017/admin?replicaSet=rs0&tls=false'\n"
	if !strings.Contains(file, want) || strings.Contains(file, secret) || strings.Contains(file, escaped(secret)) || strings.Contains(file, "PASSWORD") {
		t.Fatalf("file %q", file)
	}
	if !strings.Contains(text(res), `"passwordWritten":false`) {
		t.Fatalf("result %q", text(res))
	}

	confirming := newRig(t, options{scopes: withSecrets, formElicitation: true, database: fakecp.Database{URI: pdsMongoURI("/admin?replicaSet=rs0&ssl=false")}})
	confirming.connect()
	res = confirming.call("write_env", map[string]any{"id": databaseID, "includePassword": true, "prefix": "MONGO"})
	raw, _ = os.ReadFile(filepath.Join(confirming.workDir, ".env"))
	if res.IsError || !strings.Contains(string(raw), "MONGO_URL=") || !strings.Contains(string(raw), "dba:"+escaped(secret)+"@pds-rs0-0") {
		t.Fatalf("result %q, file %q", text(res), raw)
	}
	if !strings.Contains(text(res), `"passwordWritten":true`) {
		t.Fatalf("result %q", text(res))
	}
	for _, shown := range append(confirming.all, text(res)) {
		if strings.Contains(shown, secret) || strings.Contains(shown, escaped(secret)) {
			t.Fatal("the password was echoed")
		}
	}

	refused := mongoRig(t, withSecrets, "/admin?replicaSet=rs0&ssl=false")
	refused.connect()
	if res := refused.call("write_env", map[string]any{"id": databaseID, "includePassword": true}); !res.IsError || text(res) != messages.EnvNeedsConfirmation {
		t.Fatalf("result %q", text(res))
	}
}

func TestTheToolDescriptionsNameMongoDBWhereTheyListEngines(t *testing.T) {
	r := mongoRig(t, withWrites, "/admin")
	found := 0
	for tool, err := range r.session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		switch tool.Name {
		case "connect_database", "run_read_query", "run_query", "write_env":
			found++
			if !strings.Contains(tool.Description, "MongoDB") {
				t.Errorf("%s does not name MongoDB: %q", tool.Name, tool.Description)
			}
		}
	}
	if found != 4 {
		t.Fatalf("found %d tools", found)
	}
}
