package data

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/apps/mcp/internal/connections"
	"github.com/percona/pds/apps/mcp/internal/messages"
	"github.com/percona/pds/apps/mcp/internal/oauth"
	"github.com/percona/pds/apps/mcp/internal/sqlrun"
	"github.com/percona/pds/apps/mcp/internal/tokenstore"
	"github.com/percona/pds/apps/mcp/internal/vault"
)

type connectInput struct {
	ID       string `json:"id" jsonschema:"Database id copied from list_databases, in the form environment/namespace.name."`
	Database string `json:"database,omitempty" jsonschema:"Name of the database to use on the server. Defaults to the one the credential names."`
}

// Connection describes a saved connection. It carries no password.
type Connection struct {
	ID       string `json:"id"`
	Engine   string `json:"engine"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Database string `json:"database"`
	TLS      string `json:"tls"`
	// Members and ReplicaSet are set for MongoDB, where Host and Port are the first member.
	Members    []string `json:"members,omitempty"`
	ReplicaSet string   `json:"replicaSet,omitempty"`
}

// ConnectResult is what connect_database reports: the connection, and whether it was reached.
type ConnectResult struct {
	Connection
	Reachable bool   `json:"reachable"`
	Note      string `json:"note,omitempty"`
}

func describe(id string, t sqlrun.Target) Connection {
	return Connection{
		ID: id, Engine: string(t.Engine), Host: t.Host, Port: t.Port, User: t.User, Database: t.Database, TLS: string(t.TLS),
		Members: t.Members, ReplicaSet: t.ReplicaSet,
	}
}

func (s *Server) connect(ctx context.Context, req *sdk.CallToolRequest, in connectInput) (*sdk.CallToolResult, ConnectResult, error) {
	if !idPattern.MatchString(in.ID) || (in.Database != "" && !databasePattern.MatchString(in.Database)) {
		return nil, ConnectResult{}, toolError(messages.BadInput)
	}

	token, res, err := s.accessToken(ctx, req)
	if res != nil || err != nil {
		return res, ConnectResult{}, err
	}
	uri, err := s.fetchCredential(ctx, token, in.ID)
	if err != nil {
		return nil, ConnectResult{}, err
	}
	target, password, err := parseCredential(uri, in.Database)
	if err != nil {
		return nil, ConnectResult{}, err
	}

	result := ConnectResult{Connection: describe(in.ID, target)}
	switch err := s.deps.Runner.Probe(ctx, target, password); {
	case err == nil:
		result.Reachable = true
	case errors.Is(err, sqlrun.ErrNoDatabase):
		return nil, ConnectResult{}, toolError(messages.DatabaseMissing(target.Database))
	case errors.Is(err, sqlrun.ErrRefused):
		return nil, ConnectResult{}, toolError(messages.CredentialRefused)
	case errors.Is(err, sqlrun.ErrTLS):
		return nil, ConnectResult{}, toolError(messages.TLSFailed)
	default:
		// A database this computer cannot reach yet is still saved: the person may only need to allow
		// this computer's address, or want the settings for an application that runs elsewhere.
		result.Note = messages.UnreachableHost
	}

	account := vault.Account(s.deps.Origin, in.ID)
	previous, hadPrevious := s.deps.Vault.Get(account)
	if err := s.deps.Vault.Set(account, password); err != nil {
		return nil, ConnectResult{}, toolError(messages.NoCredentialStore)
	}
	if err := s.deps.Connections.Put(s.deps.Origin, connections.Entry{ID: in.ID, Target: target}); err != nil {
		// A password with no connection to go with it is not left behind, and one that already
		// worked is put back.
		if hadPrevious == nil {
			_ = s.deps.Vault.Set(account, previous)
		} else {
			_ = s.deps.Vault.Delete(account)
		}

		return nil, ConnectResult{}, toolError(messages.DataUnreadable)
	}

	return nil, result, nil
}

// fetchCredential asks the control plane for the database's connection string. The refusals the
// control plane words for the customer are passed through as they are.
func (s *Server) fetchCredential(ctx context.Context, token, id string) (string, error) {
	env, name, _ := strings.Cut(id, "/")
	address := s.deps.Origin + "/api/databases/" + url.PathEscape(env) + "/" + url.PathEscape(name) + "/credential"

	status, body, err := s.get(ctx, address, token)
	if err == nil && status == http.StatusUnauthorized {
		var fresh string
		if fresh, err = s.deps.Tokens.Refresh(ctx, token); err != nil {
			if errors.Is(err, oauth.ErrSessionEnded) || errors.Is(err, tokenstore.ErrNoLogin) {
				return "", toolError(messages.SessionEnded)
			}

			return "", toolError(messages.Unreachable)
		}
		status, body, err = s.get(ctx, address, fresh)
	}
	if err != nil {
		return "", toolError(messages.Unreachable)
	}

	var answer struct {
		URI     string `json:"uri"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &answer)

	switch status {
	case http.StatusOK:
		if answer.URI == "" {
			return "", toolError(messages.BadCredential)
		}

		return answer.URI, nil
	case http.StatusUnauthorized:
		return "", toolError(messages.SessionEnded)
	}
	// The control plane words its refusals for customers, so a sentence it sent is passed on.
	if message := plain(answer.Message); message != "" && status >= http.StatusBadRequest {
		return "", toolError(message)
	}

	return "", toolError(messages.Refused)
}

func (s *Server) get(ctx context.Context, address, token string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := s.deps.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))

	return resp.StatusCode, body, err
}

// plain keeps a sentence from the control plane to printable text of a sensible length, because it
// is repeated to an agent.
func plain(message string) string {
	var out []rune
	for _, r := range message {
		if unicode.IsPrint(r) {
			out = append(out, r)
		}
		if len(out) >= 500 {
			break
		}
	}

	return strings.TrimSpace(string(out))
}

// parseCredential reads a connection string into where to connect and the password, which is kept
// apart so nothing that describes the database can carry it. The string is never put in an error.
func parseCredential(uri, database string) (sqlrun.Target, string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Host == "" || u.User == nil {
		return sqlrun.Target{}, "", toolError(messages.BadCredential)
	}
	if u.Scheme == "mongodb" {
		return parseMongoDB(u, database)
	}
	var engine sqlrun.Engine
	port := 0
	switch u.Scheme {
	case "mysql":
		engine, port = sqlrun.MySQL, 3306
	case "postgres", "postgresql":
		engine, port = sqlrun.PostgreSQL, 5432
	default:
		return sqlrun.Target{}, "", toolError(messages.UnknownEngine)
	}
	password, hasPassword := u.User.Password()
	if !hasPassword || password == "" || u.User.Username() == "" || u.Hostname() == "" {
		return sqlrun.Target{}, "", toolError(messages.BadCredential)
	}
	if p := u.Port(); p != "" {
		if port, err = strconv.Atoi(p); err != nil || port < 1 || port > 65535 {
			return sqlrun.Target{}, "", toolError(messages.BadCredential)
		}
	}

	if database == "" {
		database = strings.TrimPrefix(u.Path, "/")
	}
	if database == "" && engine == sqlrun.PostgreSQL {
		database = "postgres"
	}
	if database == "" {
		return sqlrun.Target{}, "", toolError(messages.NameTheDatabase)
	}
	if !databasePattern.MatchString(database) {
		return sqlrun.Target{}, "", toolError(messages.BadCredential)
	}

	return sqlrun.Target{
		Engine: engine, Host: u.Hostname(), Port: port, User: u.User.Username(), Database: database,
		TLS: tlsMode(u.Query(), u.Hostname(), "sslmode", "ssl-mode", "tls"),
	}, password, nil
}

var (
	hostPattern       = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	replicaSetPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,63}$`)
)

// defaultMongoPort is where a member without a port is reached.
const defaultMongoPort = "27017"

// parseMongoDB reads a MongoDB connection string: every member, the replica set, whether to talk
// to one member alone, the auth database, and how the connection is protected. The path is the
// database the user lives in, which is also the database to use unless one was passed. Other
// options are not passed on. The string is never put in an error.
func parseMongoDB(u *url.URL, database string) (sqlrun.Target, string, error) {
	bad := func() (sqlrun.Target, string, error) { return sqlrun.Target{}, "", toolError(messages.BadCredential) }

	password, hasPassword := u.User.Password()
	if !hasPassword || password == "" || u.User.Username() == "" {
		return bad()
	}
	var members []string
	for _, member := range strings.Split(u.Host, ",") {
		host, port, err := net.SplitHostPort(member)
		if err != nil {
			host, port = strings.Trim(member, "[]"), defaultMongoPort
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || (net.ParseIP(host) == nil && !hostPattern.MatchString(host)) {
			return bad()
		}
		members = append(members, net.JoinHostPort(host, port))
	}
	host, port, _ := net.SplitHostPort(members[0])
	first, _ := strconv.Atoi(port)

	q := u.Query()
	direct := false
	if v := q.Get("directConnection"); v != "" {
		var err error
		if direct, err = strconv.ParseBool(v); err != nil {
			return bad()
		}
	}
	replicaSet := q.Get("replicaSet")
	path := strings.TrimPrefix(u.Path, "/")
	authSource := firstOf(q.Get("authSource"), path, "admin")
	if (replicaSet != "" && !replicaSetPattern.MatchString(replicaSet)) || !databasePattern.MatchString(authSource) {
		return bad()
	}

	if database == "" {
		database = path
	}
	if database == "" {
		return sqlrun.Target{}, "", toolError(messages.NameTheDatabase)
	}
	if !databasePattern.MatchString(database) {
		return bad()
	}

	return sqlrun.Target{
		Engine: sqlrun.MongoDB, Host: host, Port: first, User: u.User.Username(), Database: database,
		TLS: tlsMode(q, host, "ssl", "tls"), Members: members, ReplicaSet: replicaSet, Direct: direct, AuthSource: authSource,
	}, password, nil
}

// tlsMode is how the credential says the connection is protected. A connection string that says
// nothing is encrypted unless the database is on this computer, and a value that is not understood
// is read as the stricter of the choices. The stricter reading can only fail, never fall back to plain text.
func tlsMode(q url.Values, host string, keys ...string) sqlrun.TLSMode {
	var values []string
	for _, key := range keys {
		values = append(values, q.Get(key))
	}
	switch strings.ToLower(firstOf(values...)) {
	case "disable", "disabled", "false":
		return sqlrun.TLSOff
	case "verify-ca", "verify-full", "verify_ca", "verify_identity", "true":
		return sqlrun.TLSVerify
	case "":
		if isLoopback(host) {
			return sqlrun.TLSOff
		}
	}

	return sqlrun.TLSRequire
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}
