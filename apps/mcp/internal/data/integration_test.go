package data_test

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/percona/pds/apps/mcp/internal/data"
	"github.com/percona/pds/apps/mcp/internal/fakecp"
	"github.com/percona/pds/apps/mcp/internal/sqlrun"
)

// These tests drive the data server against a real MySQL and a real PostgreSQL, the way an agent
// does. They skip only when the service is absent: PDS_TEST_MYSQL_ADDR and PDS_TEST_POSTGRES_ADDR
// name them as host:port, with the password of the root and postgres users in
// PDS_TEST_MYSQL_PASSWORD and PDS_TEST_POSTGRES_PASSWORD, and a database named app.
func realDatabases(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	for name, v := range map[string]struct{ scheme, user, addr, pass string }{
		"mysql":      {"mysql", "root", "PDS_TEST_MYSQL_ADDR", "PDS_TEST_MYSQL_PASSWORD"},
		"postgresql": {"postgresql", "postgres", "PDS_TEST_POSTGRES_ADDR", "PDS_TEST_POSTGRES_PASSWORD"},
	} {
		addr := os.Getenv(v.addr)
		if addr == "" {
			t.Logf("%s is not running here: %s is not set", name, v.addr)

			continue
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := strconv.Atoi(port)
		found[name] = dbURI2(v.scheme, v.user, os.Getenv(v.pass), host, n, "/app")
	}
	if len(found) == 0 {
		t.Skip("no database service is running: set PDS_TEST_MYSQL_ADDR or PDS_TEST_POSTGRES_ADDR")
	}

	return found
}

func TestAnAgentCreatesATableReadsItBackAndCannotWriteThroughTheReadTool(t *testing.T) {
	for engine, uri := range realDatabases(t) {
		t.Run(engine, func(t *testing.T) {
			r := newRig(t, options{scopes: withWrites, real: true, database: fakecp.Database{URI: uri}})
			table := "agent_" + strconv.FormatInt(time.Now().UnixNano(), 36)

			if res := r.connect(); res.IsError {
				t.Fatalf("connect: %q", text(res))
			}
			if res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": "CREATE TABLE " + table + " (id int)"}); !res.IsError {
				t.Fatal("the read tool created a table")
			}
			if res := r.call("run_query", map[string]any{"id": databaseID, "sql": "CREATE TABLE " + table + " (id int, note varchar(20))"}); res.IsError {
				t.Fatalf("create: %q", text(res))
			}
			t.Cleanup(func() {
				entry, _ := r.conns.Get(r.cp.URL, databaseID)
				_, _ = sqlrun.Write(context.Background(), entry.Target, secretOf(uri), "DROP TABLE "+table,
					sqlrun.Limits{MaxRows: 1, MaxBytes: 100, Timeout: 5 * time.Second})
			})
			if res := r.call("run_query", map[string]any{"id": databaseID, "sql": "INSERT INTO " + table + " VALUES (1, 'one'), (2, 'two')"}); res.IsError {
				t.Fatalf("insert: %q", text(res))
			}

			res := r.call("run_read_query", map[string]any{"id": databaseID, "sql": "SELECT id, note FROM " + table + " ORDER BY id"})
			var got data.QueryResult
			r.structured(res, &got)
			if res.IsError || got.RowCount != 2 || got.Truncated || got.Columns[1] != "note" {
				t.Fatalf("read: %q", text(res))
			}

			if all := r.everything(); strings.Contains(all, secretOf(uri)) {
				t.Fatal("the database password reached a result, a log or a file")
			}
		})
	}
}

func TestAMissingDatabaseIsAnErrorAndStaysMissing(t *testing.T) {
	for engine, uri := range realDatabases(t) {
		t.Run(engine, func(t *testing.T) {
			r := newRig(t, options{scopes: withWrites, real: true, database: fakecp.Database{URI: uri}})
			missing := "nosuch_" + strconv.FormatInt(time.Now().UnixNano(), 36)

			res := r.call("connect_database", map[string]any{"id": databaseID, "database": missing})
			if !res.IsError || !strings.Contains(text(res), missing) || !strings.Contains(text(res), "Nothing was created") {
				t.Fatalf("result %q", text(res))
			}
			if len(r.vault.Accounts()) != 0 {
				t.Fatal("a password was stored")
			}
		})
	}
}
