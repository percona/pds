package data_test

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/percona/pds/apps/mcp/internal/data"
	"github.com/percona/pds/apps/mcp/internal/fakecp"
	"github.com/percona/pds/apps/mcp/internal/messages"
)

// These tests drive the data server against a real MongoDB, the way an agent does. They skip only
// when the service is absent: PDS_TEST_MONGODB_ADDR names it as host:port, with the password of
// the root user, created in the admin database, in PDS_TEST_MONGODB_PASSWORD.
func realMongoDB(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("PDS_TEST_MONGODB_ADDR")
	if addr == "" {
		t.Skip("no MongoDB service is running: set PDS_TEST_MONGODB_ADDR")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(port)

	return dbURI2("mongodb", "root", os.Getenv("PDS_TEST_MONGODB_PASSWORD"), host, n, "/admin?ssl=false")
}

func TestAnAgentReadsAMongoDBDatabaseAndCannotWriteThroughTheReadTool(t *testing.T) {
	uri := realMongoDB(t)
	r := newRig(t, options{scopes: withWrites, real: true, database: fakecp.Database{URI: uri}})
	collection := "agent_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	copyOf := collection + "_copy"

	if res := r.call("connect_database", map[string]any{"id": databaseID, "database": "pds_test"}); res.IsError {
		t.Fatalf("connect: %q", text(res))
	}
	t.Cleanup(func() {
		for _, name := range []string{collection, copyOf} {
			r.call("run_query", map[string]any{"id": databaseID, "sql": `{"drop": "` + name + `"}`})
		}
	})

	insert := `{"insert": "` + collection + `", "documents": [{"n": 1, "word": "one"}, {"n": 2, "word": "two"}, {"n": 3, "word": "three"}]}`
	if res := r.call("run_query", map[string]any{"id": databaseID, "sql": insert}); res.IsError || !strings.Contains(text(res), `"n":3`) {
		t.Fatalf("insert: %q", text(res))
	}

	var listed data.QueryResult
	res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": `{"listCollections": 1, "nameOnly": true}`})
	r.structured(res, &listed)
	if res.IsError || !strings.Contains(text(res), collection) {
		t.Fatalf("listCollections: %q", text(res))
	}

	var found data.QueryResult
	res = r.call("run_read_query", map[string]any{"id": databaseID, "sql": `{"find": "` + collection + `", "filter": {"n": {"$gte": 2}}, "sort": {"n": 1}, "projection": {"_id": 0}}`})
	r.structured(res, &found)
	if res.IsError || found.RowCount != 2 || found.Truncated || !strings.Contains(text(res), `{"n":2,"word":"two"}`) {
		t.Fatalf("find: %q", text(res))
	}

	var limited data.QueryResult
	res = r.call("run_read_query", map[string]any{"id": databaseID, "sql": `{"find": "` + collection + `"}`, "maxRows": 2})
	r.structured(res, &limited)
	if res.IsError || limited.RowCount != 2 || !limited.Truncated {
		t.Fatalf("find with maxRows: %q", text(res))
	}

	res = r.call("run_read_query", map[string]any{"id": databaseID, "sql": `{"aggregate": "` + collection + `", "pipeline": [{"$group": {"_id": null, "total": {"$sum": "$n"}}}]}`})
	if res.IsError || !strings.Contains(text(res), `"total":6`) {
		t.Fatalf("aggregate: %q", text(res))
	}
	res = r.call("run_read_query", map[string]any{"id": databaseID, "sql": `{"count": "` + collection + `"}`})
	if res.IsError || !strings.Contains(text(res), `"n":3`) {
		t.Fatalf("count: %q", text(res))
	}

	for name, statement := range map[string]string{
		"insert":     `{"insert": "` + collection + `", "documents": [{"n": 4}]}`,
		"update":     `{"update": "` + collection + `", "updates": [{"q": {}, "u": {"$set": {"word": "x"}}, "multi": true}]}`,
		"delete":     `{"delete": "` + collection + `", "deletes": [{"q": {}, "limit": 0}]}`,
		"drop":       `{"drop": "` + collection + `"}`,
		"$out":       `{"aggregate": "` + collection + `", "pipeline": [{"$out": "` + copyOf + `"}]}`,
		"$merge":     `{"aggregate": "` + collection + `", "pipeline": [{"$merge": {"into": "` + copyOf + `"}}]}`,
		"$where":     `{"find": "` + collection + `", "filter": {"$where": "true"}}`,
		"$function":  `{"aggregate": "` + collection + `", "pipeline": [{"$addFields": {"x": {"$function": {"body": "function(){return 1}", "args": [], "lang": "js"}}}}]}`,
		"createUser": `{"createUser": "intruder", "pwd": "x", "roles": ["root"]}`,
	} {
		if res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": statement}); !res.IsError {
			t.Errorf("the read tool ran %s", name)
		}
	}

	// Nothing the refused commands tried is there: the data is as it was, and no copy exists.
	res = r.call("run_read_query", map[string]any{"id": databaseID, "sql": `{"find": "` + collection + `", "sort": {"n": 1}, "projection": {"_id": 0}}`})
	if res.IsError || !strings.Contains(text(res), `{"n":1,"word":"one"}`) || !strings.Contains(text(res), `{"n":3,"word":"three"}`) || strings.Contains(text(res), `"n":4`) {
		t.Fatalf("the data changed: %q", text(res))
	}
	res = r.call("run_read_query", map[string]any{"id": databaseID, "sql": `{"listCollections": 1, "nameOnly": true}`})
	if strings.Contains(text(res), copyOf) {
		t.Fatal("a refused $out created a collection")
	}
	res = r.call("run_read_query", map[string]any{"id": databaseID, "sql": `{"find": "system.users"}`})
	if !res.IsError || text(res) != messages.MongoSystemCollection {
		t.Fatalf("system.users: %q", text(res))
	}

	if all := r.everything(); strings.Contains(all, os.Getenv("PDS_TEST_MONGODB_PASSWORD")) {
		t.Fatal("the database password reached a result, a log or a file")
	}
}

func TestAnAggregateThatWritesRunsOnlyThroughTheWriteTool(t *testing.T) {
	uri := realMongoDB(t)
	r := newRig(t, options{scopes: withWrites, real: true, database: fakecp.Database{URI: uri}})
	collection := "agent_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	copyOf := collection + "_copy"
	r.call("connect_database", map[string]any{"id": databaseID, "database": "pds_test"})
	t.Cleanup(func() {
		for _, name := range []string{collection, copyOf} {
			r.call("run_query", map[string]any{"id": databaseID, "sql": `{"drop": "` + name + `"}`})
		}
	})
	r.call("run_query", map[string]any{"id": databaseID, "sql": `{"insert": "` + collection + `", "documents": [{"n": 1}, {"n": 2}]}`})

	if res := r.call("run_query", map[string]any{"id": databaseID, "sql": `{"aggregate": "` + collection + `", "pipeline": [{"$match": {}}, {"$out": "` + copyOf + `"}]}`}); res.IsError {
		t.Fatalf("$out: %q", text(res))
	}
	if res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": `{"count": "` + copyOf + `"}`}); res.IsError || !strings.Contains(text(res), `"n":2`) {
		t.Fatalf("the copy: %q", text(res))
	}
	if res := r.call("run_query", map[string]any{"id": databaseID, "sql": `{"aggregate": "` + collection + `", "pipeline": [{"$out": {"db": "other", "coll": "x"}}]}`}); !res.IsError {
		t.Fatal("an $out into another database ran")
	}
}

func TestAMongoDBCommandPastItsTimeLimitIsStoppedAndSaysSo(t *testing.T) {
	uri := realMongoDB(t)
	r := newRig(t, options{scopes: withWrites, real: true, database: fakecp.Database{URI: uri}})
	collection := "agent_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	r.call("connect_database", map[string]any{"id": databaseID, "database": "pds_test"})
	t.Cleanup(func() {
		r.call("run_query", map[string]any{"id": databaseID, "sql": `{"drop": "` + collection + `"}`})
	})

	var documents []string
	for i := range 4000 {
		documents = append(documents, `{"x": `+strconv.Itoa(i%50)+`}`)
	}
	r.call("run_query", map[string]any{"id": databaseID, "sql": `{"insert": "` + collection + `", "documents": [` + strings.Join(documents, ",") + `]}`})

	// A self join with no index compares every pair of documents, which takes far longer than the 1 ms the command allows itself.
	slow := `{"aggregate": "` + collection + `", "maxTimeMS": 1, "pipeline": [{"$lookup": {"from": "` + collection + `", "localField": "x", "foreignField": "x", "as": "m"}}, {"$count": "n"}]}`
	res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": slow})
	if !res.IsError || text(res) != messages.StatementTimedOut {
		t.Fatalf("result %q", text(res))
	}
}

func TestAMongoDBPasswordThatIsWrongIsRefusedAndNothingIsSaved(t *testing.T) {
	uri := realMongoDB(t)
	wrong := strings.Replace(uri, os.Getenv("PDS_TEST_MONGODB_PASSWORD"), "not-the-password", 1)
	r := newRig(t, options{scopes: withSecrets, real: true, database: fakecp.Database{URI: wrong}})

	if res := r.connect(); !res.IsError || text(res) != messages.CredentialRefused || len(r.vault.Accounts()) != 0 {
		t.Fatalf("result %q", text(res))
	}
}
