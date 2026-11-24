package data_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/apps/mcp/internal/connections"
	"github.com/percona/pds/apps/mcp/internal/data"
	"github.com/percona/pds/apps/mcp/internal/fakecp"
	"github.com/percona/pds/apps/mcp/internal/mongoguard"
	"github.com/percona/pds/apps/mcp/internal/oauth"
	"github.com/percona/pds/apps/mcp/internal/sqlrun"
	"github.com/percona/pds/apps/mcp/internal/tokenstore"
	"github.com/percona/pds/apps/mcp/internal/vault"
)

// secret is the password every test database has. It is a sentinel: no output may carry it.
const secret = "sentinel-p@ss:w/rd#1"

const databaseID = "acme/pds.shop"

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// fakeRunner stands in for the database and records what it was asked.
type fakeRunner struct {
	mu       sync.Mutex
	calls    []string
	statemts []string
	targets  []sqlrun.Target
	limits   []sqlrun.Limits
	passes   []string

	probeErr error
	result   sqlrun.Result
	err      error
}

func (f *fakeRunner) record(kind string, t sqlrun.Target, password, statement string, lim sqlrun.Limits) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, kind)
	f.targets = append(f.targets, t)
	f.passes = append(f.passes, password)
	f.statemts = append(f.statemts, statement)
	f.limits = append(f.limits, lim)
}

func (f *fakeRunner) Read(_ context.Context, t sqlrun.Target, password, statement string, lim sqlrun.Limits) (sqlrun.Result, error) {
	f.record("read", t, password, statement, lim)

	return f.result, f.err
}

func (f *fakeRunner) Write(_ context.Context, t sqlrun.Target, password, statement string, lim sqlrun.Limits) (sqlrun.Result, error) {
	f.record("write", t, password, statement, lim)

	return f.result, f.err
}

func (f *fakeRunner) WriteRows(_ context.Context, t sqlrun.Target, password, statement string, lim sqlrun.Limits) (sqlrun.Result, error) {
	f.record("writeRows", t, password, statement, lim)

	return f.result, f.err
}

func (f *fakeRunner) Command(_ context.Context, t sqlrun.Target, password string, cmd mongoguard.Command, lim sqlrun.Limits) (sqlrun.Result, error) {
	f.record("command:"+cmd.Database, t, password, cmd.JSON, lim)

	return f.result, f.err
}

func (f *fakeRunner) Probe(_ context.Context, t sqlrun.Target, password string) error {
	f.record("probe", t, password, "", sqlrun.Limits{})

	return f.probeErr
}

func (f *fakeRunner) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.calls...)
}

type options struct {
	// scopes are what the person signed in with. Nil means no sign in has happened.
	scopes []string
	// urlElicitation makes the client declare URL mode elicitation.
	urlElicitation bool
	// formElicitation makes the client declare form mode elicitation, which write_env uses to confirm a password.
	formElicitation bool
	// elicitAction is what the person answers: accept, decline or cancel.
	elicitAction string
	// legacy makes the client speak the protocol before multi round trips.
	legacy    bool
	publicIP  func(context.Context) (string, error)
	database  fakecp.Database
	useRunner *fakeRunner
	allowed   []string
	// real runs SQL against real databases instead of the fake.
	real bool
}

type rig struct {
	t       *testing.T
	cp      *fakecp.Server
	logins  *tokenstore.Store
	vault   *vault.Memory
	conns   *connections.Store
	runner  *fakeRunner
	workDir string
	config  string
	logs    *syncBuffer
	session *sdk.ClientSession
	// asked receives the address of each URL mode elicitation the client was sent.
	asked chan string
	// changed receives a value for each tools/list_changed notification.
	changed chan struct{}
	all     []string
}

func dbURI(host string, port int, tail string) string {
	return dbURI2("mysql", "app", secret, host, port, tail)
}

func dbURI2(scheme, user, password, host string, port int, tail string) string {
	return scheme + "://" + user + ":" + url.QueryEscape(password) + "@" + host + ":" + itoa(port) + tail
}

// secretOf is the password in a connection string.
func secretOf(uri string) string {
	u, _ := url.Parse(uri)
	password, _ := u.User.Password()

	return password
}

func itoa(n int) string {
	b, _ := json.Marshal(n)

	return string(b)
}

func newRig(t *testing.T, o options) *rig {
	t.Helper()
	cp := fakecp.New(t)
	if o.allowed != nil {
		cp.Configure(func(c *fakecp.Config) { c.Allowed = o.allowed })
	}
	if o.database.URI == "" {
		o.database.URI = dbURI("db.example.com", 3306, "/shop")
	}
	cp.AddDatabase(databaseID, o.database)

	config := t.TempDir()
	r := &rig{
		t: t, cp: cp, logins: tokenstore.New(filepath.Join(config, "pds-mcp", "credentials.json")),
		vault: vault.NewMemory(), conns: connections.New(filepath.Join(config, "pds-mcp", "connections.json")),
		runner: o.useRunner, workDir: t.TempDir(), config: config, logs: &syncBuffer{},
		asked: make(chan string, 4), changed: make(chan struct{}, 16),
	}
	if r.runner == nil {
		r.runner = &fakeRunner{}
	}
	if o.scopes != nil {
		fakecp.SignIn(t, cp, r.logins, o.scopes...)
	}
	publicIP := o.publicIP
	if publicIP == nil {
		publicIP = func(context.Context) (string, error) { return "203.0.113.7/32", nil }
	}

	var runner data.Runner = r.runner
	if o.real {
		runner = data.DatabaseRunner{}
	}
	hc := oauth.NewHTTPClient(cp.URL, "pds-mcp/test", 30*time.Second)
	srv := data.New(data.Deps{
		Origin: cp.URL, Version: "1.2.3", Logins: r.logins, Tokens: oauth.NewLazySource(hc, r.logins, cp.URL), HTTP: hc,
		Vault: r.vault, Connections: r.conns, Runner: runner, PublicIP: publicIP, WorkDir: r.workDir,
		Log: slog.New(slog.NewTextHandler(r.logs, nil)), SyncEvery: 25 * time.Millisecond,
	})

	copts := &sdk.ClientOptions{ToolListChangedHandler: func(context.Context, *sdk.ToolListChangedRequest) {
		select {
		case r.changed <- struct{}{}:
		default:
		}
	}}
	if o.urlElicitation || o.formElicitation {
		action := o.elicitAction
		if action == "" {
			action = "accept"
		}
		caps := &sdk.ElicitationCapabilities{}
		if o.urlElicitation {
			caps.URL = &sdk.URLElicitationCapabilities{}
		}
		if o.formElicitation {
			caps.Form = &sdk.FormElicitationCapabilities{}
		}
		copts.Capabilities = &sdk.ClientCapabilities{Elicitation: caps}
		copts.ElicitationHandler = func(_ context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
			r.asked <- req.Params.URL + req.Params.Message

			return &sdk.ElicitResult{Action: action}, nil
		}
	}

	ct, st := sdk.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, st) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	var sessionOptions *sdk.ClientSessionOptions
	if o.legacy {
		sessionOptions = &sdk.ClientSessionOptions{ProtocolVersion: "2025-11-25"}
	}
	session, err := sdk.NewClient(&sdk.Implementation{Name: "agent", Version: "1"}, copts).Connect(ctx, ct, sessionOptions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	r.session = session

	return r
}

// call runs a tool and returns its result, with every text it carries kept so a test can search
// everything the agent saw for a secret.
func (r *rig) call(name string, args map[string]any) *sdk.CallToolResult {
	r.t.Helper()
	res, err := r.session.CallTool(r.t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		r.t.Fatalf("%s: %v", name, err)
	}
	raw, _ := json.Marshal(res)
	r.all = append(r.all, string(raw))

	return res
}

func text(res *sdk.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}

	return strings.Join(parts, "\n")
}

func (r *rig) structured(res *sdk.CallToolResult, into any) {
	r.t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil || json.Unmarshal(raw, into) != nil {
		r.t.Fatalf("structured content %v: %v", res.StructuredContent, err)
	}
}

func (r *rig) toolNames() []string {
	r.t.Helper()
	var names []string
	for tool, err := range r.session.Tools(r.t.Context(), nil) {
		if err != nil {
			r.t.Fatal(err)
		}
		names = append(names, tool.Name)
	}

	return names
}

// everything is all the text the agent was shown, the logs and the files on disk.
func (r *rig) everything() string {
	r.t.Helper()
	parts := append([]string{r.logs.String()}, r.all...)
	_ = filepath.WalkDir(r.config, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			raw, _ := os.ReadFile(path)
			parts = append(parts, string(raw))
		}

		return nil
	})

	return strings.Join(parts, "\n")
}

func (r *rig) connect() *sdk.CallToolResult {
	r.t.Helper()

	return r.call("connect_database", map[string]any{"id": databaseID})
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var (
	readOnly    = []string{"pds:read", "offline_access"}
	withSecrets = []string{"pds:read", "pds:credentials", "offline_access"}
	withWrites  = []string{"pds:read", "pds:credentials", "pds:sql_write", "offline_access"}
)
