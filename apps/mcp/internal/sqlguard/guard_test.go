package sqlguard_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/percona/pds/apps/mcp/internal/messages"
	"github.com/percona/pds/apps/mcp/internal/sqlguard"
)

type tc struct {
	name    string
	sql     string
	refused string
	rows    bool
}

func run(t *testing.T, d sqlguard.Dialect, mode sqlguard.Mode, cases []tc) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, err := sqlguard.Check(c.sql, d, mode)
			if c.refused == "" {
				if err != nil {
					t.Fatalf("refused %q: %v", c.sql, err)
				}
				if (kind == sqlguard.ReturnsRows) != c.rows {
					t.Fatalf("kind %v for %q, want rows=%v", kind, c.sql, c.rows)
				}

				return
			}
			var refusal *sqlguard.Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("%q was accepted, want refused with %q", c.sql, c.refused)
			}
			if refusal.Reason != c.refused {
				t.Fatalf("%q refused with %q, want %q", c.sql, refusal.Reason, c.refused)
			}
		})
	}
}

func TestReadModeAcceptsPlainQueries(t *testing.T) {
	ok := []tc{
		{name: "select", sql: "SELECT 1", rows: true},
		{name: "trailing semicolon", sql: "select * from t;  ", rows: true},
		{name: "semicolon in a string", sql: "select * from t where a = 'x;y'", rows: true},
		{name: "keyword in a string", sql: "select 'INSERT INTO x; DROP'", rows: true},
		{name: "quoted identifier named update", sql: `select "update" from t`, rows: true},
		{name: "column that contains a keyword", sql: "select update_time, deleted_at from t", rows: true},
		{name: "line comment first", sql: "-- hello\nSELECT 1", rows: true},
		{name: "block comment first", sql: "/* hello */ SELECT 1", rows: true},
		{name: "with", sql: "WITH a AS (SELECT 1) SELECT * FROM a", rows: true},
		{name: "parenthesized union", sql: "(SELECT 1) UNION (SELECT 2)", rows: true},
		{name: "show", sql: "SHOW TABLES", rows: true},
		{name: "explain", sql: "EXPLAIN SELECT 1", rows: true},
		{name: "values", sql: "VALUES (1)", rows: true},
		{name: "table", sql: "TABLE t", rows: true},
		{name: "describe", sql: "DESCRIBE t", rows: true},
	}
	run(t, sqlguard.MySQL, sqlguard.ReadOnly, ok)
	run(t, sqlguard.Postgres, sqlguard.ReadOnly, ok[:len(ok)-1])
}

func TestReadModeRefusesWhatCouldChangeThings(t *testing.T) {
	cases := []tc{
		{name: "empty", sql: "  ", refused: messages.SQLEmpty},
		{name: "only a comment", sql: "-- nothing\n", refused: messages.SQLEmpty},
		{name: "second statement", sql: "SELECT 1; DROP TABLE t", refused: messages.SQLMultiple},
		{name: "second statement after a comment", sql: "SELECT 1 /* ; */ ; /* x */ SELECT 2", refused: messages.SQLMultiple},
		{name: "two semicolons", sql: "SELECT 1;;", refused: messages.SQLMultiple},
		{name: "commit", sql: "COMMIT", refused: messages.SQLNotRead},
		{name: "rollback", sql: "ROLLBACK", refused: messages.SQLNotRead},
		{name: "begin", sql: "BEGIN", refused: messages.SQLNotRead},
		{name: "start transaction", sql: "START TRANSACTION READ WRITE", refused: messages.SQLNotRead},
		{name: "set transaction", sql: "SET TRANSACTION READ WRITE", refused: messages.SQLNotRead},
		{name: "set session", sql: "SET SESSION transaction_read_only = 0", refused: messages.SQLNotRead},
		{name: "insert", sql: "INSERT INTO t VALUES (1)", refused: messages.SQLNotRead},
		{name: "delete", sql: "DELETE FROM t", refused: messages.SQLNotRead},
		{name: "drop", sql: "DROP TABLE t", refused: messages.SQLNotRead},
		{name: "call", sql: "CALL p()", refused: messages.SQLNotRead},
		{name: "copy", sql: "COPY t TO PROGRAM 'id'", refused: messages.SQLNotRead},
		{name: "data modifying cte", sql: "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", refused: messages.SQLForbidden},
		{name: "explain analyze delete", sql: "EXPLAIN ANALYZE DELETE FROM t", refused: messages.SQLForbidden},
		{name: "locking read", sql: "SELECT * FROM t FOR UPDATE", refused: messages.SQLForbidden},
		{name: "into outfile", sql: "SELECT * INTO OUTFILE '/tmp/x' FROM t", refused: messages.SQLForbidden},
		{name: "into table", sql: "SELECT * INTO copy_of_t FROM t", refused: messages.SQLForbidden},
		{name: "load_file", sql: "SELECT LOAD_FILE('/etc/passwd')", refused: messages.SQLForbidden},
		{name: "pg_read_file", sql: "SELECT pg_read_file('/etc/passwd')", refused: messages.SQLForbidden},
		{name: "pg_ls_dir", sql: "SELECT pg_ls_dir('/')", refused: messages.SQLForbidden},
		{name: "large object import", sql: "SELECT lo_import('/etc/passwd')", refused: messages.SQLForbidden},
		{name: "dblink", sql: "SELECT dblink_exec('x', 'y')", refused: messages.SQLForbidden},
		{name: "keyword hidden after a comment", sql: "SELECT 1 /* x */ INTO /* y */ OUTFILE '/x'", refused: messages.SQLForbidden},
		{name: "executable comment", sql: "SELECT 1 /*! ; DROP TABLE t */", refused: messages.SQLForbidden},
		{name: "mariadb executable comment", sql: "SELECT 1 /*M! ; DROP TABLE t */", refused: messages.SQLForbidden},
	}
	run(t, sqlguard.MySQL, sqlguard.ReadOnly, cases)
	// The two executable comment cases are MySQL only: Postgres reads them as plain comments.
	run(t, sqlguard.Postgres, sqlguard.ReadOnly, cases[:len(cases)-2])
}

func TestMySQLQuotingCannotHideAStatement(t *testing.T) {
	run(t, sqlguard.MySQL, sqlguard.ReadOnly, []tc{
		{name: "dash dash without a space is not a comment", sql: "SELECT 1 --1;DROP TABLE t", refused: messages.SQLMultiple},
		{name: "dash dash with a space is a comment", sql: "SELECT 1 -- ; DROP TABLE t", rows: true},
		{name: "hash comment", sql: "SELECT 1 # ; DROP TABLE t", rows: true},
		{name: "backslash escapes the quote", sql: `SELECT 'a\'; DROP TABLE t; --'`, rows: true},
		{name: "doubled quote", sql: "SELECT 'a''; DROP TABLE t; --'", rows: true},
		{name: "backtick identifier", sql: "SELECT `a;b` FROM t", rows: true},
		{name: "double quoted string", sql: `SELECT "a;b"`, rows: true},
	})
}

func TestPostgresQuotingCannotHideAStatement(t *testing.T) {
	run(t, sqlguard.Postgres, sqlguard.ReadOnly, []tc{
		{name: "dollar quoted text", sql: "SELECT $q$ ; DROP TABLE t $q$", rows: true},
		{name: "dollar quote closes", sql: "SELECT $q$x$q$; DROP TABLE t", refused: messages.SQLMultiple},
		{name: "backslash is not an escape", sql: `SELECT 'a\'; DROP TABLE t; --'`, refused: messages.SQLMultiple},
		{name: "escape string", sql: `SELECT E'a\'; DROP TABLE t; --'`, rows: true},
		{name: "nested comment", sql: "SELECT 1 /* a /* b */ ; DROP TABLE t */", rows: true},
		{name: "positional parameter", sql: "SELECT $1", rows: true},
	})
}

func TestWriteModeAllowsOneStatementThatIsNotTransactionControl(t *testing.T) {
	cases := []tc{
		{name: "create table", sql: "CREATE TABLE t (id int)"},
		{name: "insert", sql: "INSERT INTO t VALUES (1)"},
		{name: "insert select", sql: "INSERT INTO t SELECT * FROM u"},
		{name: "select", sql: "SELECT 1", rows: true},
		{name: "second statement", sql: "INSERT INTO t VALUES (1); DROP TABLE t", refused: messages.SQLMultiple},
		{name: "commit", sql: "COMMIT", refused: messages.SQLNotRead},
		{name: "begin", sql: "BEGIN", refused: messages.SQLNotRead},
		{name: "set", sql: "SET search_path = x", refused: messages.SQLNotRead},
		{name: "copy", sql: "COPY t FROM PROGRAM 'id'", refused: messages.SQLNotRead},
		{name: "into outfile", sql: "SELECT * INTO OUTFILE '/tmp/x' FROM t", refused: messages.SQLForbidden},
		{name: "load_file", sql: "INSERT INTO t SELECT LOAD_FILE('/etc/passwd')", refused: messages.SQLForbidden},
		{name: "pg_read_file", sql: "INSERT INTO t SELECT pg_read_file('/etc/passwd')", refused: messages.SQLForbidden},
	}
	run(t, sqlguard.MySQL, sqlguard.ReadWrite, cases)
	run(t, sqlguard.Postgres, sqlguard.ReadWrite, cases)

	run(t, sqlguard.Postgres, sqlguard.ReadWrite, []tc{
		{name: "returning", sql: "INSERT INTO t VALUES (1) RETURNING id", rows: true},
	})
}

func TestStatementsAreBounded(t *testing.T) {
	long := "SELECT '" + strings.Repeat("a", sqlguard.MaxLength) + "'"
	run(t, sqlguard.MySQL, sqlguard.ReadOnly, []tc{{name: "too long", sql: long, refused: messages.SQLTooLong}})
}

func TestAFunctionNameInQuotesOrAStringIsStillRefused(t *testing.T) {
	run(t, sqlguard.Postgres, sqlguard.ReadOnly, []tc{
		{name: "quoted function", sql: `SELECT "pg_read_file"('/etc/passwd')`, refused: messages.SQLForbidden},
		{name: "quoted after a schema", sql: `SELECT pg_catalog."pg_ls_dir"('/')`, refused: messages.SQLForbidden},
		{name: "quoted with upper case", sql: `SELECT "PG_READ_FILE"('/etc/passwd')`, refused: messages.SQLForbidden},
		{name: "unicode escape in an identifier", sql: `SELECT U&"\0070g_read_file"('/etc/passwd')`, refused: messages.SQLForbidden},
		{name: "unicode escape in a string", sql: `SELECT U&'\0041'`, refused: messages.SQLForbidden},
		{name: "sql held in a string", sql: `SELECT query_to_xml('select pg_read_file(''/etc/passwd'')', true, true, '')`, refused: messages.SQLForbidden},
		{name: "table to xml", sql: `SELECT table_to_xml('t', true, true, '')`, refused: messages.SQLForbidden},
		{name: "cursor to xml", sql: `SELECT cursor_to_xml('c', 1, true, true, '')`, refused: messages.SQLForbidden},
		{name: "cancel a session", sql: `SELECT pg_cancel_backend(1)`, refused: messages.SQLForbidden},
		{name: "a quoted column is still fine", sql: `SELECT "Name" FROM "Users"`, rows: true},
	})
	run(t, sqlguard.MySQL, sqlguard.ReadOnly, []tc{
		{name: "backtick function", sql: "SELECT `load_file`('/etc/passwd')", refused: messages.SQLForbidden},
		{name: "a backtick column is still fine", sql: "SELECT `name` FROM `users`", rows: true},
	})
}
