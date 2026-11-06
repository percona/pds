// Package sqlrun runs one SQL statement against a MySQL or PostgreSQL database for the data server.
// A read runs in a read only transaction that the database itself enforces, on a connection of its
// own, and is always rolled back. A write runs in a transaction that is committed when it
// succeeds. Results are bounded by rows, bytes and time.
package sqlrun

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// Engine is a database engine.
type Engine string

// The engines.
const (
	MySQL      Engine = "mysql"
	PostgreSQL Engine = "postgresql"
)

// TLSMode is how the connection is protected.
type TLSMode string

// The modes. TLSRequire encrypts and does not check the server's certificate, the way libpq's
// require does. TLSVerify checks the certificate and the host name against the system's roots.
// A connection that asks for either never falls back to plain text.
const (
	TLSOff     TLSMode = "off"
	TLSRequire TLSMode = "require"
	TLSVerify  TLSMode = "verify"
)

// Target is a database to connect to. The password is passed beside it, never inside it, so a
// Target can be stored, shown and logged.
type Target struct {
	Engine   Engine  `json:"engine"`
	Host     string  `json:"host"`
	Port     int     `json:"port"`
	User     string  `json:"user"`
	Database string  `json:"database"`
	TLS      TLSMode `json:"tls"`
}

// Limits bound one statement.
type Limits struct {
	MaxRows  int
	MaxBytes int
	Timeout  time.Duration
}

// Result is what a statement returned.
type Result struct {
	Columns      []string
	Rows         [][]any
	RowsAffected int64
	// Truncated is set when more rows or bytes were available than the limits allowed.
	Truncated bool
}

// Errors the data server turns into sentences. None of them carries a password or an address.
var (
	ErrNoDatabase  = errors.New("the database does not exist")
	ErrRefused     = errors.New("the database refused the user or the password")
	ErrUnreachable = errors.New("the database host could not be reached")
	ErrTLS         = errors.New("the database could not be reached over TLS")
	ErrTimeout     = errors.New("the statement took too long")
)

// QueryError is the database's own answer to a statement. Message is what the engine said, with
// the password taken out if it was in it.
type QueryError struct{ Message string }

func (e *QueryError) Error() string { return e.Message }

// backstop is how long after the statement's own timeout the client gives up on a database that
// did not stop it.
const backstop = 2 * time.Second

type mode int

const (
	readOnly mode = iota
	write
	writeRows
)

// Read runs one query in a read only transaction and rolls it back.
func Read(ctx context.Context, t Target, password, statement string, lim Limits) (Result, error) {
	return run(ctx, t, password, statement, readOnly, lim)
}

// Write runs one statement that answers with a count, and commits it.
func Write(ctx context.Context, t Target, password, statement string, lim Limits) (Result, error) {
	return run(ctx, t, password, statement, write, lim)
}

// WriteRows runs one statement that answers with rows, such as an INSERT with RETURNING, and
// commits it.
func WriteRows(ctx context.Context, t Target, password, statement string, lim Limits) (Result, error) {
	return run(ctx, t, password, statement, writeRows, lim)
}

// Probe connects, to check that the database exists and the password is right.
func Probe(ctx context.Context, t Target, password string) error {
	db, err := open(t, password, false, Limits{Timeout: 10 * time.Second})
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	return classifyConnect(db.PingContext(ctx))
}

func run(ctx context.Context, t Target, password, statement string, m mode, lim Limits) (Result, error) {
	db, err := open(t, password, m == readOnly, lim)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(ctx, lim.Timeout+backstop)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return Result{}, classifyConnect(err)
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: m == readOnly})
	if err != nil {
		return Result{}, classifyQuery(err, password)
	}
	// A transaction that is not committed is rolled back, so a read never keeps anything.
	defer func() { _ = tx.Rollback() }()

	if m == readOnly {
		// PostgreSQL lets a read only transaction switch itself to read write until its first
		// snapshot is taken. One query takes it, and from then on the mode is fixed.
		if _, err := tx.ExecContext(ctx, "SELECT 1"); err != nil {
			return Result{}, classifyQuery(err, password)
		}
	}

	var res Result
	if m == write {
		out, err := tx.ExecContext(ctx, statement)
		if err != nil {
			return Result{}, classifyQuery(err, password)
		}
		res.RowsAffected, _ = out.RowsAffected()
	} else {
		rows, err := tx.QueryContext(ctx, statement)
		if err != nil {
			return Result{}, classifyQuery(err, password)
		}
		res, err = collect(rows, lim)
		if err != nil {
			return Result{}, classifyQuery(err, password)
		}
	}

	if m != readOnly {
		if err := tx.Commit(); err != nil {
			return Result{}, classifyQuery(err, password)
		}
	}

	return res, nil
}

// open makes a connection pool of one for one statement. Every statement gets a session of its
// own, so nothing a statement sets can reach the next one.
func open(t Target, password string, readOnly bool, lim Limits) (*sql.DB, error) {
	var db *sql.DB
	switch t.Engine {
	case MySQL:
		cfg := mysql.NewConfig()
		cfg.User, cfg.Passwd, cfg.Net = t.User, password, "tcp"
		cfg.Addr, cfg.DBName = net.JoinHostPort(t.Host, strconv.Itoa(t.Port)), t.Database
		cfg.Timeout = 10 * time.Second
		cfg.ParseTime = true
		cfg.TLS = tlsConfig(t)
		// A server in NO_BACKSLASH_ESCAPES or ANSI_QUOTES mode would read a backslash or a double
		// quote differently from the statement check, so the session takes those modes off.
		cfg.Params = map[string]string{"sql_mode": "REPLACE(REPLACE(@@sql_mode,'NO_BACKSLASH_ESCAPES',''),'ANSI_QUOTES','')"}
		if readOnly {
			cfg.Params["transaction_read_only"] = "1"
			cfg.Params["max_execution_time"] = strconv.FormatInt(lim.Timeout.Milliseconds(), 10)
		}
		connector, err := mysql.NewConnector(cfg)
		if err != nil {
			return nil, ErrUnreachable
		}
		db = sql.OpenDB(connector)
	case PostgreSQL:
		sslmode := map[TLSMode]string{TLSOff: "disable", TLSRequire: "require", TLSVerify: "verify-full"}[t.TLS]
		if sslmode == "" {
			sslmode = "verify-full"
		}
		u := url.URL{
			Scheme: "postgres", User: url.UserPassword(t.User, password),
			Host: net.JoinHostPort(t.Host, strconv.Itoa(t.Port)), Path: "/" + t.Database,
			RawQuery: url.Values{"sslmode": {sslmode}, "connect_timeout": {"10"}, "application_name": {"pds-mcp"}}.Encode(),
		}
		cfg, err := pgx.ParseConfig(u.String())
		if err != nil {
			return nil, ErrUnreachable
		}
		// The statement check reads a backslash in a plain string as itself, which is only what
		// the server does with this setting on.
		cfg.RuntimeParams["standard_conforming_strings"] = "on"
		if readOnly {
			cfg.RuntimeParams["default_transaction_read_only"] = "on"
		}
		cfg.RuntimeParams["statement_timeout"] = strconv.FormatInt(lim.Timeout.Milliseconds(), 10)
		cfg.RuntimeParams["idle_in_transaction_session_timeout"] = strconv.FormatInt((lim.Timeout + backstop).Milliseconds(), 10)
		db = stdlib.OpenDB(*cfg)
	default:
		return nil, ErrUnreachable
	}
	db.SetMaxOpenConns(1)

	return db, nil
}

func tlsConfig(t Target) *tls.Config {
	switch t.TLS {
	case TLSOff:
		return nil
	case TLSRequire:
		return &tls.Config{ServerName: t.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // TLSRequire encrypts without checking the certificate, the way libpq's require does; the credential asked for it and verify is the other mode.
	default:
		return &tls.Config{ServerName: t.Host, MinVersion: tls.VersionTLS12}
	}
}

func collect(rows *sql.Rows, lim Limits) (Result, error) {
	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil {
		return Result{}, fmt.Errorf("read the columns: %w", err)
	}
	types, _ := rows.ColumnTypes()
	res := Result{Columns: columns}
	budget := lim.MaxBytes

	for rows.Next() {
		if len(res.Rows) >= lim.MaxRows || budget <= 0 {
			res.Truncated = true

			break
		}
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return Result{}, fmt.Errorf("read a row: %w", err)
		}
		row := make([]any, len(columns))
		for i, value := range values {
			var binary bool
			if i < len(types) {
				binary = isBinary(types[i].DatabaseTypeName())
			}
			cell, cut := readable(value, binary, &budget)
			row[i] = cell
			res.Truncated = res.Truncated || cut
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("read the rows: %w", err)
	}

	return res, nil
}

func isBinary(typeName string) bool {
	switch strings.ToUpper(typeName) {
	case "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "BYTEA":
		return true
	}

	return false
}

// readable turns a value the driver returned into one a JSON result can hold, and spends the byte
// budget on it. A text value longer than what is left is cut at a character, and cut reports it.
func readable(value any, binary bool, budget *int) (cell any, cut bool) {
	var text string
	switch v := value.(type) {
	case nil:
		return nil, false
	case []byte:
		if binary || !utf8.Valid(v) {
			text = "base64:" + base64.StdEncoding.EncodeToString(v)
		} else {
			text = string(v)
		}
	case string:
		text = v
	case time.Time:
		text = v.Format(time.RFC3339Nano)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			text = strconv.FormatFloat(v, 'g', -1, 64)
		} else {
			*budget -= 8

			return v, false
		}
	case int64, bool:
		*budget -= 8

		return v, false
	default:
		text = fmt.Sprint(v)
	}

	if len(text) > *budget {
		end := max(*budget, 0)
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		text, cut = text[:end], true
	}
	*budget -= len(text)

	return text, cut
}

// classifyConnect says why a connection could not be made.
func classifyConnect(err error) error {
	if err == nil {
		return nil
	}
	var mysqlErr *mysql.MySQLError
	var pgErr *pgconn.PgError
	var certErr *tls.CertificateVerificationError
	var netErr net.Error

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ErrUnreachable
	case errors.As(err, &mysqlErr):
		switch mysqlErr.Number {
		case 1049:
			return ErrNoDatabase
		case 1044, 1045, 1130, 1698:
			return ErrRefused
		}
	case errors.As(err, &pgErr):
		switch pgErr.Code {
		case "3D000":
			return ErrNoDatabase
		case "28P01":
			return ErrRefused
		case "28000":
			// A server that has no rule for this address says so in the same class as a wrong user.
			if strings.Contains(pgErr.Message, "pg_hba.conf") {
				return ErrUnreachable
			}

			return ErrRefused
		}
	case errors.Is(err, mysql.ErrNoTLS), errors.As(err, &certErr), isTLSFailure(err):
		return ErrTLS
	case errors.Is(err, syscall.ECONNREFUSED), errors.As(err, &netErr):
		return ErrUnreachable
	}

	return ErrUnreachable
}

func isTLSFailure(err error) bool {
	text := strings.ToLower(err.Error())

	return strings.Contains(text, "tls") || strings.Contains(text, "ssl is not enabled")
}

// classifyQuery turns what a statement failed with into an error the data server can pass on.
func classifyQuery(err error, password string) error {
	var mysqlErr *mysql.MySQLError
	var pgErr *pgconn.PgError

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ErrTimeout
	case errors.As(err, &mysqlErr):
		if mysqlErr.Number == 3024 || mysqlErr.Number == 1317 {
			return ErrTimeout
		}

		return &QueryError{Message: scrub(mysqlErr.Message, password)}
	case errors.As(err, &pgErr):
		if pgErr.Code == "57014" {
			return ErrTimeout
		}

		return &QueryError{Message: scrub(pgErr.Message, password)}
	default:
		return &QueryError{Message: "The database did not complete the statement."}
	}
}

// scrub takes the password out of a message the database wrote, in case the statement or an error
// path put it there.
func scrub(message, password string) string {
	if password != "" {
		message = strings.ReplaceAll(message, password, "[removed]")
	}

	return message
}
