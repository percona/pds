// Package mongorun runs one MongoDB command, already checked by mongoguard, for the data server.
// Every command carries a time limit the server enforces, a command that answers with a cursor is
// asked for no more than the row limit allows, and the answer is cut by rows and bytes the way a
// SQL result is. Every command gets a connection of its own, so nothing one command sets reaches
// the next.
package mongorun

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/topology"

	"github.com/percona/pds/apps/mcp/internal/mongoguard"
	"github.com/percona/pds/apps/mcp/internal/sqlrun"
)

// backstop is how long after the command's own time limit the client gives up on a server that
// did not stop it.
const backstop = 2 * time.Second

const connectTimeout = 10 * time.Second

// defaultAuthSource is where the control plane creates the account.
const defaultAuthSource = "admin"

// Members lists where to connect, as host:port.
func Members(t sqlrun.Target) []string {
	if len(t.Members) > 0 {
		return t.Members
	}

	return []string{net.JoinHostPort(t.Host, strconv.Itoa(t.Port))}
}

// URI is the connection string for a target. It carries the password only when one is passed, so
// the same builder serves the connection and the .env file.
func URI(t sqlrun.Target, password string) string {
	u := url.URL{Scheme: "mongodb", Host: strings.Join(Members(t), ","), Path: "/" + t.Database}
	switch {
	case password != "":
		u.User = url.UserPassword(t.User, password)
	default:
		u.User = url.User(t.User)
	}
	q := url.Values{}
	if t.AuthSource != "" && t.AuthSource != t.Database {
		q.Set("authSource", t.AuthSource)
	}
	if t.Direct {
		q.Set("directConnection", "true")
	}
	if t.ReplicaSet != "" {
		q.Set("replicaSet", t.ReplicaSet)
	}
	q.Set("tls", strconv.FormatBool(t.TLS != sqlrun.TLSOff))
	u.RawQuery = q.Encode()

	return u.String()
}

// connect makes a client. The password is set on the credential and never put in a string.
func connect(t sqlrun.Target, password string) (*mongo.Client, error) {
	authSource := t.AuthSource
	if authSource == "" {
		authSource = defaultAuthSource
	}
	opts := options.Client().SetHosts(Members(t)).
		SetAuth(options.Credential{Username: t.User, Password: password, PasswordSet: true, AuthSource: authSource}).
		SetAppName("pds-mcp").SetConnectTimeout(connectTimeout).SetServerSelectionTimeout(connectTimeout)
	if t.ReplicaSet != "" {
		opts.SetReplicaSet(t.ReplicaSet)
	}
	if t.Direct {
		opts.SetDirect(true)
	}
	if cfg := tlsConfig(t); cfg != nil {
		opts.SetTLSConfig(cfg)
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, classify(err, password)
	}

	return client, nil
}

// tlsConfig is nil for a plain connection. The server name is left to the driver, which sets it
// for each member.
func tlsConfig(t sqlrun.Target) *tls.Config {
	switch t.TLS {
	case sqlrun.TLSOff:
		return nil
	case sqlrun.TLSRequire:
		return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // TLSRequire encrypts without checking the certificate, the way the SQL engines' require does; the credential asked for it and verify is the other mode.
	default:
		return &tls.Config{MinVersion: tls.VersionTLS12}
	}
}

func disconnect(ctx context.Context, client *mongo.Client) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = client.Disconnect(ctx)
}

// Probe connects and pings, to check that the members answer and the password is right.
func Probe(ctx context.Context, t sqlrun.Target, password string) error {
	client, err := connect(t, password)
	if err != nil {
		return err
	}
	defer disconnect(ctx, client)

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := client.Database(t.Database).RunCommand(ctx, bson.D{{Key: "ping", Value: 1}}).Err(); err != nil {
		return classify(err, password)
	}

	return nil
}

// Run sends one checked command and returns its documents, one row each. A command that answers
// with one document, such as a count or an insert, returns that document as the only row.
func Run(ctx context.Context, t sqlrun.Target, password string, cmd mongoguard.Command, lim sqlrun.Limits) (sqlrun.Result, error) {
	doc, err := decode(cmd.JSON)
	if err != nil {
		return sqlrun.Result{}, err
	}
	doc = prepare(doc, lim)

	client, err := connect(t, password)
	if err != nil {
		return sqlrun.Result{}, err
	}
	defer disconnect(ctx, client)

	ctx, cancel := context.WithTimeout(ctx, lim.Timeout+backstop)
	defer cancel()
	db := client.Database(cmd.Database)

	reply, err := db.RunCommand(ctx, doc).Raw()
	if err != nil {
		return sqlrun.Result{}, classify(err, password)
	}
	res, open, err := shape(reply, lim)
	if err != nil {
		return sqlrun.Result{}, classify(err, password)
	}
	if open.id != 0 {
		// A cursor with more in it is closed, or it would sit on the server until it times out.
		_ = db.RunCommand(ctx, bson.D{{Key: "killCursors", Value: open.collection}, {Key: "cursors", Value: bson.A{open.id}}}).Err()
	}

	return res, nil
}

// unreadable is the answer to a command whose values MongoDB's extended JSON cannot read.
func unreadable() error {
	return &sqlrun.QueryError{Message: "The command has a value that MongoDB extended JSON cannot read, such as an invalid $oid or $date."}
}

// decode reads the checked JSON as MongoDB extended JSON, keeping the order of the keys.
func decode(text string) (bson.D, error) {
	var doc bson.D
	if err := bson.UnmarshalExtJSON([]byte(text), false, &doc); err != nil || len(doc) == 0 {
		return nil, unreadable()
	}

	return doc, nil
}

// cursorCommands answer with a cursor and take its batch size in a cursor document. find takes it
// as a field of its own.
var cursorCommands = map[string]bool{"aggregate": true, "listCollections": true, "listIndexes": true}

// prepare puts the limits on the command: the server's time limit, which an agent can lower and
// not raise, and for a cursor the first batch that holds one document more than the row limit,
// which is how a truncated result is told from a complete one.
func prepare(doc bson.D, lim sqlrun.Limits) bson.D {
	name := doc[0].Key
	limit := lim.Timeout.Milliseconds()
	out := make(bson.D, 0, len(doc)+2)
	for i, e := range doc {
		if i > 0 {
			switch {
			case e.Key == "maxTimeMS":
				if v, ok := millis(e.Value); ok && v > 0 && v < limit {
					limit = v
				}

				continue
			case e.Key == "batchSize" && name == "find", e.Key == "cursor" && cursorCommands[name]:
				continue
			}
		}
		out = append(out, e)
	}

	batch := int32(lim.MaxRows + 1) //nolint:gosec // MaxRows is at most 1000.
	switch {
	case name == "find":
		out = append(out, bson.E{Key: "batchSize", Value: batch})
	case cursorCommands[name]:
		out = append(out, bson.E{Key: "cursor", Value: bson.D{{Key: "batchSize", Value: batch}}})
	}

	return append(out, bson.E{Key: "maxTimeMS", Value: limit})
}

func millis(v any) (int64, bool) {
	switch v := v.(type) {
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), true
	}

	return 0, false
}

// openCursor is a cursor the server still holds after the first batch.
type openCursor struct {
	id         int64
	collection string
}

// shape turns the server's answer into rows. A cursor answer has one row for each document of its
// first batch, and an answer without one is a single row.
func shape(reply bson.Raw, lim sqlrun.Limits) (sqlrun.Result, openCursor, error) {
	res := sqlrun.Result{Columns: []string{"document"}}
	budget := lim.MaxBytes

	add := func(doc any) bool {
		text, err := bson.MarshalExtJSON(doc, false, false)
		if err != nil || len(res.Rows) >= lim.MaxRows || len(text) > budget {
			res.Truncated = true

			return false
		}
		budget -= len(text)
		res.Rows = append(res.Rows, []any{json.RawMessage(text)})

		return true
	}

	cursorValue, err := reply.LookupErr("cursor")
	cursor, isCursor := cursorValue.DocumentOK()
	if err != nil || !isCursor {
		var doc bson.D
		if err := bson.Unmarshal(reply, &doc); err != nil {
			return sqlrun.Result{}, openCursor{}, err
		}
		kept := doc[:0]
		for _, e := range doc {
			if e.Key != "$clusterTime" && e.Key != "operationTime" {
				kept = append(kept, e)
			}
		}
		add(kept)

		return res, openCursor{}, nil
	}

	batch, _ := cursor.Lookup("firstBatch").ArrayOK()
	values, err := batch.Values()
	if err != nil {
		return sqlrun.Result{}, openCursor{}, err
	}
	for _, v := range values {
		doc, ok := v.DocumentOK()
		if !ok || !add(doc) {
			res.Truncated = true

			break
		}
	}

	var open openCursor
	if id, ok := cursor.Lookup("id").AsInt64OK(); ok && id != 0 {
		ns, _ := cursor.Lookup("ns").StringValueOK()
		_, open.collection, _ = strings.Cut(ns, ".")
		open.id = id
		res.Truncated = true
	}

	return res, open, nil
}

// errCommandFailed is every other failure, which the data server words for the agent.
var errCommandFailed = errors.New("the command did not complete")

// classify says why a command failed, in the errors the data server turns into sentences. What
// the server said about a command is passed on with the password taken out, and nothing else of a
// driver error is.
func classify(err error, password string) error {
	var selection topology.ServerSelectionError
	var command mongo.CommandError

	switch {
	case errors.As(err, &selection):
		return classifyConnect(err)
	case errors.As(err, &command) && command.Code == 18:
		return sqlrun.ErrRefused
	case errors.Is(err, context.DeadlineExceeded), mongo.IsTimeout(err):
		return sqlrun.ErrTimeout
	case errors.As(err, &command):
		return &sqlrun.QueryError{Message: sqlrun.Scrub(command.Message, password)}
	default:
		return errCommandFailed
	}
}

// classifyConnect says why no member could be used. The driver reports the last error of each
// member inside one selection error, so the reason is read from its text.
func classifyConnect(err error) error {
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "authenticationfailed"), strings.Contains(text, "authentication failed"), strings.Contains(text, "auth error"):
		return sqlrun.ErrRefused
	case strings.Contains(text, "tls:"), strings.Contains(text, "x509"), strings.Contains(text, "certificate"), strings.Contains(text, "tls handshake"):
		return sqlrun.ErrTLS
	}

	return sqlrun.ErrUnreachable
}
