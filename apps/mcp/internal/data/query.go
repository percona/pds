package data

import (
	"context"
	"errors"
	"slices"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/apps/mcp/internal/connections"
	"github.com/percona/pds/apps/mcp/internal/messages"
	"github.com/percona/pds/apps/mcp/internal/oauth"
	"github.com/percona/pds/apps/mcp/internal/sqlguard"
	"github.com/percona/pds/apps/mcp/internal/sqlrun"
	"github.com/percona/pds/apps/mcp/internal/tokenstore"
	"github.com/percona/pds/apps/mcp/internal/vault"
)

type readInput struct {
	ID      string `json:"id" jsonschema:"Database id of a connected database."`
	SQL     string `json:"sql" jsonschema:"One read only SQL statement."`
	MaxRows int    `json:"maxRows,omitempty" jsonschema:"Most rows to return. Defaults to 100, at most 1000."`
}

type writeInput struct {
	ID  string `json:"id" jsonschema:"Database id of a connected database."`
	SQL string `json:"sql" jsonschema:"One SQL statement."`
}

// QueryResult is the rows a statement returned.
type QueryResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	RowCount  int      `json:"rowCount"`
	Truncated bool     `json:"truncated"`
}

// WriteResult is what a statement that changes data did.
type WriteResult struct {
	// RowsAffected is set for a statement that returns no rows. A statement that returns rows, such as
	// one with RETURNING, reports them instead, and the database does not also give a count.
	RowsAffected *int64   `json:"rowsAffected,omitempty"`
	Columns      []string `json:"columns,omitempty"`
	Rows         [][]any  `json:"rows,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
}

func dialect(e sqlrun.Engine) sqlguard.Dialect {
	if e == sqlrun.PostgreSQL {
		return sqlguard.Postgres
	}

	return sqlguard.MySQL
}

// target finds the saved connection and its password.
func (s *Server) target(id string) (sqlrun.Target, string, error) {
	if !idPattern.MatchString(id) {
		return sqlrun.Target{}, "", toolError(messages.BadInput)
	}
	entry, err := s.deps.Connections.Get(s.deps.Origin, id)
	switch {
	case errors.Is(err, connections.ErrNotFound):
		return sqlrun.Target{}, "", toolError(messages.NoConnection)
	case err != nil:
		return sqlrun.Target{}, "", toolError(messages.DataUnreadable)
	}

	password, err := s.deps.Vault.Get(vault.Account(s.deps.Origin, id))
	switch {
	case errors.Is(err, vault.ErrNotFound):
		return sqlrun.Target{}, "", toolError(messages.PasswordMissing)
	case err != nil:
		return sqlrun.Target{}, "", toolError(messages.NoCredentialStore)
	}

	return entry.Target, password, nil
}

// failure turns what the database answered into a sentence. An engine's own message is passed on,
// because it is what the agent needs to correct a statement, and nothing else from an error is.
func failure(err error) error {
	var query *sqlrun.QueryError
	switch {
	case errors.As(err, &query):
		return toolError(query.Message)
	case errors.Is(err, sqlrun.ErrTimeout):
		return toolError(messages.StatementTimedOut)
	case errors.Is(err, sqlrun.ErrRefused):
		return toolError(messages.StoredRefused)
	case errors.Is(err, sqlrun.ErrUnreachable):
		return toolError(messages.UnreachableHost)
	case errors.Is(err, sqlrun.ErrTLS):
		return toolError(messages.TLSFailed)
	default:
		return toolError(messages.QueryFailed)
	}
}

func (s *Server) read(ctx context.Context, _ *sdk.CallToolRequest, in readInput) (*sdk.CallToolResult, QueryResult, error) {
	target, password, err := s.target(in.ID)
	if err != nil {
		return nil, QueryResult{}, err
	}
	if _, err := sqlguard.Check(in.SQL, dialect(target.Engine), sqlguard.ReadOnly); err != nil {
		return nil, QueryResult{}, toolError(err.Error())
	}

	rows := min(max(in.MaxRows, 0), maxRows)
	if rows == 0 {
		rows = defaultRows
	}
	res, err := s.deps.Runner.Read(ctx, target, password, in.SQL, sqlrun.Limits{MaxRows: rows, MaxBytes: maxBytes, Timeout: queryTimeout})
	if err != nil {
		return nil, QueryResult{}, failure(err)
	}

	return nil, QueryResult{Columns: res.Columns, Rows: nonNil(res.Rows), RowCount: len(res.Rows), Truncated: res.Truncated}, nil
}

func nonNil(rows [][]any) [][]any {
	if rows == nil {
		return [][]any{}
	}

	return rows
}

// holdsSQLWrite reports whether the sign in allows SQL writes right now. The access token is
// renewed first on every write, so a grant the Organization withdrew is seen at once and not
// trusted for as long as the saved scopes say.
func (s *Server) holdsSQLWrite(ctx context.Context) error {
	if _, err := s.deps.Tokens.Renew(ctx); err != nil {
		switch {
		case errors.Is(err, tokenstore.ErrNoLogin):
			s.sync()

			return toolError(messages.NotSignedIn)
		case errors.Is(err, oauth.ErrSessionEnded):
			s.sync()

			return toolError(messages.SessionEnded)
		case errors.Is(err, oauth.ErrNotServed):
			return toolError(messages.NotServed)
		default:
			return toolError(messages.Unreachable)
		}
	}
	if !slices.Contains(s.grants(), scopeSQLWrite) {
		s.sync()

		return toolError(messages.NoSQLWrite)
	}

	return nil
}

func (s *Server) write(ctx context.Context, _ *sdk.CallToolRequest, in writeInput) (*sdk.CallToolResult, WriteResult, error) {
	if err := s.holdsSQLWrite(ctx); err != nil {
		return nil, WriteResult{}, err
	}
	target, password, err := s.target(in.ID)
	if err != nil {
		return nil, WriteResult{}, err
	}
	kind, err := sqlguard.Check(in.SQL, dialect(target.Engine), sqlguard.ReadWrite)
	if err != nil {
		return nil, WriteResult{}, toolError(err.Error())
	}

	lim := sqlrun.Limits{MaxRows: defaultRows, MaxBytes: maxBytes, Timeout: queryTimeout}
	if kind == sqlguard.ReturnsRows {
		res, err := s.deps.Runner.WriteRows(ctx, target, password, in.SQL, lim)
		if err != nil {
			return nil, WriteResult{}, failure(err)
		}

		return nil, WriteResult{Columns: res.Columns, Rows: nonNil(res.Rows), Truncated: res.Truncated}, nil
	}
	res, err := s.deps.Runner.Write(ctx, target, password, in.SQL, lim)
	if err != nil {
		return nil, WriteResult{}, failure(err)
	}

	return nil, WriteResult{RowsAffected: &res.RowsAffected}, nil
}
