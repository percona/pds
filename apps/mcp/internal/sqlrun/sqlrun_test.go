package sqlrun_test

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/percona/pds/apps/mcp/internal/sqlrun"
)

// The tests that need a database run against a real MySQL and a real PostgreSQL, because what they
// prove is what the engines do. They skip only when the service is absent: the services are
// named by PDS_TEST_MYSQL_ADDR and PDS_TEST_POSTGRES_ADDR (host:port) with the password of the
// root and postgres users in PDS_TEST_MYSQL_PASSWORD and PDS_TEST_POSTGRES_PASSWORD, and a
// database named app.
type engineCase struct {
	name   string
	target sqlrun.Target
	secret string
	sleep  string
	series string
}

func engines(t *testing.T) []engineCase {
	t.Helper()
	var cases []engineCase
	for _, e := range []struct {
		name, addrVar, passVar, user string
		engine                       sqlrun.Engine
		sleep, series                string
	}{
		{"mysql", "PDS_TEST_MYSQL_ADDR", "PDS_TEST_MYSQL_PASSWORD", "root", sqlrun.MySQL, "SELECT COUNT(*) FROM information_schema.columns a, information_schema.columns b, information_schema.columns c, information_schema.columns d",
			"WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 50) SELECT i FROM n"},
		{"postgres", "PDS_TEST_POSTGRES_ADDR", "PDS_TEST_POSTGRES_PASSWORD", "postgres", sqlrun.PostgreSQL, "SELECT pg_sleep(10)",
			"SELECT generate_series(1, 50) AS i"},
	} {
		addr := os.Getenv(e.addrVar)
		if addr == "" {
			t.Logf("%s is not running here: %s is not set", e.name, e.addrVar)

			continue
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatalf("%s: %v", e.addrVar, err)
		}
		n, _ := strconv.Atoi(port)
		cases = append(cases, engineCase{
			name: e.name, secret: os.Getenv(e.passVar), sleep: e.sleep, series: e.series,
			target: sqlrun.Target{Engine: e.engine, Host: host, Port: n, User: e.user, Database: "app", TLS: sqlrun.TLSOff},
		})
	}
	if len(cases) == 0 {
		t.Skip("no database service is running: set PDS_TEST_MYSQL_ADDR or PDS_TEST_POSTGRES_ADDR")
	}

	return cases
}

var limits = sqlrun.Limits{MaxRows: 100, MaxBytes: 64 << 10, Timeout: 10 * time.Second}

func each(t *testing.T, fn func(t *testing.T, e engineCase, table string)) {
	t.Helper()
	for _, e := range engines(t) {
		t.Run(e.name, func(t *testing.T) {
			table := "t_" + strconv.FormatInt(time.Now().UnixNano(), 36)
			if _, err := sqlrun.Write(t.Context(), e.target, e.secret, "CREATE TABLE "+table+" (id int, note text)", limits); err != nil {
				t.Fatalf("create the table: %v", err)
			}
			t.Cleanup(func() {
				_, _ = sqlrun.Write(context.WithoutCancel(t.Context()), e.target, e.secret, "DROP TABLE "+table, limits)
			})
			fn(t, e, table)
		})
	}
}

func count(t *testing.T, e engineCase, table string) string {
	t.Helper()
	res, err := sqlrun.Read(t.Context(), e.target, e.secret, "SELECT count(*) FROM "+table, limits)
	if err != nil {
		t.Fatalf("count: %v", err)
	}

	return toString(res.Rows[0][0])
}

func toString(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return ""
	}
}

func TestAReadOnlyTransactionRefusesEveryWriteTheEngineSees(t *testing.T) {
	each(t, func(t *testing.T, e engineCase, table string) {
		statements := []string{
			"INSERT INTO " + table + " VALUES (1, 'a')",
			"CREATE TABLE " + table + "_x (id int)",
			"WITH x AS (SELECT 1) INSERT INTO " + table + " SELECT * FROM x",
			"SET TRANSACTION READ WRITE",
			"INSERT INTO " + table + " VALUES (2, 'b'); INSERT INTO " + table + " VALUES (3, 'c')",
		}
		if e.name == "postgres" {
			statements = append(statements, "COMMIT; INSERT INTO "+table+" VALUES (4, 'd')")
		}
		for _, statement := range statements {
			_, err := sqlrun.Read(t.Context(), e.target, e.secret, statement, limits)
			var failed *sqlrun.QueryError
			if !errors.As(err, &failed) {
				t.Errorf("%q: error %v, want the engine's refusal", statement, err)
			}
		}
		if got := count(t, e, table); got != "0" {
			t.Fatalf("%s row(s) were written through a read only transaction", got)
		}
	})
}

func TestACommitInsideAReadCannotMakeTheNextStatementWrite(t *testing.T) {
	each(t, func(t *testing.T, e engineCase, table string) {
		// Each read is its own session, so what one statement does to its session is gone for the next.
		_, _ = sqlrun.Read(t.Context(), e.target, e.secret, "COMMIT", limits)
		_, _ = sqlrun.Read(t.Context(), e.target, e.secret, "SET SESSION transaction_read_only = off", limits)
		if _, err := sqlrun.Read(t.Context(), e.target, e.secret, "INSERT INTO "+table+" VALUES (1, 'a')", limits); err == nil {
			t.Fatal("a write was accepted after a read changed its session")
		}
		if got := count(t, e, table); got != "0" {
			t.Fatalf("rows were written: %s", got)
		}
	})
}

func TestAWriteIsCommittedAndCounted(t *testing.T) {
	each(t, func(t *testing.T, e engineCase, table string) {
		res, err := sqlrun.Write(t.Context(), e.target, e.secret,
			"INSERT INTO "+table+" VALUES (1, 'a'), (2, 'b')", limits)
		if err != nil {
			t.Fatal(err)
		}
		if res.RowsAffected != 2 {
			t.Fatalf("rows affected %d, want 2", res.RowsAffected)
		}
		if got := count(t, e, table); got != "2" {
			t.Fatalf("count %s after the write, want 2", got)
		}
	})
}

func TestAWriteThatFailsLeavesNothingBehind(t *testing.T) {
	each(t, func(t *testing.T, e engineCase, table string) {
		_, err := sqlrun.Write(t.Context(), e.target, e.secret, "INSERT INTO "+table+" VALUES ('not a number', 'a')", limits)
		var failed *sqlrun.QueryError
		if !errors.As(err, &failed) {
			t.Fatalf("error %v, want the engine's refusal", err)
		}
		if got := count(t, e, table); got != "0" {
			t.Fatalf("count %s", got)
		}
	})
}

func TestResultsAreBoundedByRowsAndBytes(t *testing.T) {
	each(t, func(t *testing.T, e engineCase, _ string) {
		res, err := sqlrun.Read(t.Context(), e.target, e.secret, e.series, sqlrun.Limits{MaxRows: 10, MaxBytes: 64 << 10, Timeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != 10 || !res.Truncated {
			t.Fatalf("%d rows, truncated %v, want 10 and true", len(res.Rows), res.Truncated)
		}

		res, err = sqlrun.Read(t.Context(), e.target, e.secret, "SELECT repeat('x', 5000)", sqlrun.Limits{MaxRows: 10, MaxBytes: 1000, Timeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		cell, _ := res.Rows[0][0].(string)
		if !res.Truncated || len(cell) > 1000 {
			t.Fatalf("a cell of %d bytes, truncated %v, want at most 1000 and true", len(cell), res.Truncated)
		}
	})
}

func TestAStatementThatRunsTooLongIsStopped(t *testing.T) {
	each(t, func(t *testing.T, e engineCase, _ string) {
		start := time.Now()
		_, err := sqlrun.Read(t.Context(), e.target, e.secret, e.sleep, sqlrun.Limits{MaxRows: 1, MaxBytes: 1000, Timeout: 500 * time.Millisecond})
		if !errors.Is(err, sqlrun.ErrTimeout) {
			t.Fatalf("error %v, want a timeout", err)
		}
		if time.Since(start) > 8*time.Second {
			t.Fatalf("took %s to stop", time.Since(start))
		}
	})
}

func TestAMissingDatabaseIsAnErrorAndNothingIsCreated(t *testing.T) {
	for _, e := range engines(t) {
		t.Run(e.name, func(t *testing.T) {
			missing := e.target
			missing.Database = "pds_missing_" + strconv.FormatInt(time.Now().UnixNano(), 36)
			if err := sqlrun.Probe(t.Context(), missing, e.secret); !errors.Is(err, sqlrun.ErrNoDatabase) {
				t.Fatalf("error %v, want a missing database", err)
			}
			list := "SELECT datname FROM pg_database"
			if e.name == "mysql" {
				list = "SELECT schema_name FROM information_schema.schemata"
			}
			res, err := sqlrun.Read(t.Context(), e.target, e.secret, list, limits)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range res.Rows {
				if toString(row[0]) == missing.Database {
					t.Fatal("the missing database was created")
				}
			}
		})
	}
}

func TestAWrongPasswordIsRefusedWithoutRepeatingIt(t *testing.T) {
	for _, e := range engines(t) {
		t.Run(e.name, func(t *testing.T) {
			sentinel := "sentinel-wrong-password-4821"
			err := sqlrun.Probe(t.Context(), e.target, sentinel)
			if !errors.Is(err, sqlrun.ErrRefused) {
				t.Fatalf("error %v, want a refusal", err)
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Fatal("the error repeats the password")
			}
		})
	}
}

func TestAHostThatIsNotThereIsUnreachable(t *testing.T) {
	for _, e := range engines(t) {
		t.Run(e.name, func(t *testing.T) {
			gone := e.target
			gone.Port = 1
			if err := sqlrun.Probe(t.Context(), gone, e.secret); !errors.Is(err, sqlrun.ErrUnreachable) {
				t.Fatalf("error %v, want unreachable", err)
			}
		})
	}
}

func TestRequiredTLSIsNeverDowngraded(t *testing.T) {
	for _, e := range engines(t) {
		t.Run(e.name, func(t *testing.T) {
			secure := e.target
			secure.TLS = sqlrun.TLSRequire
			res, err := sqlrun.Read(t.Context(), secure, e.secret, tlsCipher(e.name), limits)
			if e.name == "postgres" {
				// The test server does not offer TLS: a required connection fails and does not fall back.
				if !errors.Is(err, sqlrun.ErrTLS) {
					t.Fatalf("error %v, want a TLS refusal", err)
				}

				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cipher := toString(res.Rows[0][len(res.Rows[0])-1]); cipher == "" {
				t.Fatal("the connection asked for TLS and runs in the clear")
			}
		})
	}
}

func tlsCipher(engine string) string {
	if engine == "mysql" {
		return "SHOW SESSION STATUS LIKE 'Ssl_cipher'"
	}

	return "SELECT 1"
}

func TestValuesAreReadableAsJSON(t *testing.T) {
	for _, e := range engines(t) {
		t.Run(e.name, func(t *testing.T) {
			res, err := sqlrun.Read(t.Context(), e.target, e.secret, "SELECT 1 AS n, NULL AS nothing, 'text' AS s, 1.5 AS f", limits)
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Columns; len(got) != 4 || got[0] != "n" || got[3] != "f" {
				t.Fatalf("columns %v", got)
			}
			row := res.Rows[0]
			if row[1] != nil || row[2] != "text" {
				t.Fatalf("row %v", row)
			}
		})
	}
}
