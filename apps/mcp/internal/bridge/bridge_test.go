package bridge_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/apps/mcp/internal/bridge"
	"github.com/percona/pds/apps/mcp/internal/fakecp"
	"github.com/percona/pds/apps/mcp/internal/oauth"
	"github.com/percona/pds/apps/mcp/internal/tokenstore"
)

// syncBuffer is a buffer two goroutines can write.
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

type rig struct {
	cp      *fakecp.Server
	stdout  *syncBuffer
	stderr  *syncBuffer
	session *sdk.ClientSession
	done    chan error
}

// signIn runs the browser flow against the fake and leaves the refresh token in a new store.
func signIn(t *testing.T, cp *fakecp.Server, scopes ...string) (*oauth.Source, *tokenstore.Store) {
	t.Helper()
	store := tokenstore.New(filepath.Join(t.TempDir(), "credentials.json"))
	fakecp.SignIn(t, cp, store, scopes...)

	hc := oauth.NewHTTPClient(cp.URL, "test", time.Minute)
	server, err := oauth.Discover(t.Context(), hc, cp.URL)
	if err != nil {
		t.Fatal(err)
	}

	return oauth.NewSource(server, hc, store), store
}

// start runs the bridge between a client of the test's making and the fake, over pipes, the way an
// agent runs it over the process's stdin and stdout.
func start(t *testing.T, cp *fakecp.Server, source bridge.Tokens, endpoint string, handlers *sdk.ClientOptions) *rig {
	t.Helper()

	downIn, clientOut := io.Pipe()
	clientIn, downOut := io.Pipe()
	stdout, stderr := &syncBuffer{}, &syncBuffer{}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- bridge.Run(ctx, bridge.Options{
			Endpoint: endpoint, Tokens: source, Version: "1.2.3",
			HTTP:  oauth.NewHTTPClient(cp.URL, "pds-mcp/1.2.3", 0),
			Stdin: downIn, Stdout: struct {
				io.Writer
				io.Closer
			}{io.MultiWriter(downOut, stdout), downOut},
			Log: slog.New(slog.NewTextHandler(stderr, nil)),
		})
	}()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-agent", Version: "9.9"}, handlers)
	session, err := client.Connect(ctx, &sdk.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	r := &rig{cp: cp, stdout: stdout, stderr: stderr, session: session, done: done}
	if err != nil {
		cancel()
		t.Fatalf("connect through the bridge: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		cancel()
		<-done
	})

	return r
}

func textOf(t *testing.T, result *sdk.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range result.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}

	return b.String()
}

func TestToolsListedThroughTheBridgeAreTheOnesTheControlPlaneListsForTheSameToken(t *testing.T) {
	cp := fakecp.New(t)
	source, _ := signIn(t, cp, "pds:read", "pds:change", "offline_access")
	r := start(t, cp, source, cp.URL+"/mcp", nil)

	through, err := r.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	token, err := source.Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	direct, err := sdk.NewClient(&sdk.Implementation{Name: "direct", Version: "1"}, nil).Connect(t.Context(), &sdk.StreamableClientTransport{
		Endpoint: cp.URL + "/mcp",
		HTTPClient: &http.Client{Transport: roundTripper(func(req *http.Request) (*http.Response, error) {
			req = req.Clone(req.Context())
			req.Header.Set("Authorization", "Bearer "+token)

			return http.DefaultTransport.RoundTrip(req)
		})},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = direct.Close() }()
	want, err := direct.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	a, _ := json.Marshal(through.Tools)
	b, _ := json.Marshal(want.Tools)
	if len(through.Tools) != 2 || string(a) != string(b) {
		t.Errorf("through the bridge:\n%s\ndirect:\n%s", a, b)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestACallIsForwardedAndAnswered(t *testing.T) {
	cp := fakecp.New(t)
	source, _ := signIn(t, cp, "pds:read", "pds:change", "offline_access")
	r := start(t, cp, source, cp.URL+"/mcp", nil)

	result, err := r.session.CallTool(t.Context(), &sdk.CallToolParams{Name: "scale_database", Arguments: map[string]any{"id": "db-1", "replicas": 3}})
	if err != nil || result.IsError || !strings.Contains(textOf(t, result), "scaled db-1") {
		t.Fatalf("result = %+v, %v", result, err)
	}
}

// raw is an agent that speaks JSON lines to the bridge by hand, so a test can send exactly the
// messages of one protocol era.
type raw struct {
	t     *testing.T
	in    io.Writer
	lines chan string
}

func startRaw(t *testing.T, cp *fakecp.Server, source bridge.Tokens) *raw {
	t.Helper()
	downIn, clientOut := io.Pipe()
	clientIn, downOut := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		_ = bridge.Run(ctx, bridge.Options{
			Endpoint: cp.URL + "/mcp", Tokens: source, Version: "1.2.3", HTTP: oauth.NewHTTPClient(cp.URL, "pds-mcp/1.2.3", 0),
			Stdin: downIn, Stdout: downOut, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()

	r := &raw{t: t, in: clientOut, lines: make(chan string, 16)}
	go func() {
		scanner := bufio.NewScanner(clientIn)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			r.lines <- scanner.Text()
		}
	}()

	return r
}

func (r *raw) send(line string) {
	r.t.Helper()
	if _, err := io.WriteString(r.in, line+"\n"); err != nil {
		r.t.Fatal(err)
	}
}

func (r *raw) answer() string {
	r.t.Helper()
	select {
	case line := <-r.lines:
		return line
	case <-time.After(10 * time.Second):
		r.t.Fatal("no answer from the bridge")

		return ""
	}
}

func TestTheBridgeNamesItselfAndPassesOnTheAgentsNameInTheOlderProtocolEra(t *testing.T) {
	cp := fakecp.New(t)
	source, _ := signIn(t, cp, "pds:read", "offline_access")
	agent := startRaw(t, cp, source)

	agent.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test-agent","version":"9.9"}}}`)
	var initialized struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(agent.answer()), &initialized); err != nil || initialized.Result.ProtocolVersion == "" {
		t.Fatalf("initialize answer: %+v, %v", initialized, err)
	}
	agent.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	agent.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if line := agent.answer(); !strings.Contains(line, "list_databases") {
		t.Fatalf("tools/list answer = %s", line)
	}

	var withVersion int
	for _, req := range cp.Requests() {
		if req.UserAgent != "pds-mcp/1.2.3" {
			t.Errorf("User-Agent = %q", req.UserAgent)
		}
		if got := req.Header.Get("X-PDS-MCP-Client"); got != "test-agent/9.9" {
			t.Errorf("X-PDS-MCP-Client = %q, want test-agent/9.9", got)
		}
		if req.Header.Get("MCP-Protocol-Version") == initialized.Result.ProtocolVersion {
			withVersion++
		}
	}
	if withVersion == 0 {
		t.Errorf("no request carried the negotiated version %q: %+v", initialized.Result.ProtocolVersion, cp.Requests())
	}
}

func TestA401RefreshesOnceAndRetriesOnce(t *testing.T) {
	cp := fakecp.New(t)
	source, _ := signIn(t, cp, "pds:read", "pds:change", "offline_access")
	r := start(t, cp, source, cp.URL+"/mcp", nil)
	if _, err := r.session.ListTools(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	refreshesBefore, requestsBefore := len(cp.Forms()), len(cp.Requests())

	cp.ExpireAccessTokens()
	result, err := r.session.CallTool(t.Context(), &sdk.CallToolParams{Name: "list_databases"})
	if err != nil || result.IsError {
		t.Fatalf("result = %+v, %v", result, err)
	}

	if got := len(cp.Forms()) - refreshesBefore; got != 1 {
		t.Errorf("%d token requests for one expired token, want 1", got)
	}
	var statuses []int
	for _, req := range cp.Requests()[requestsBefore:] {
		statuses = append(statuses, req.Status)
	}
	if len(statuses) != 2 || statuses[0] != http.StatusUnauthorized || statuses[1] != http.StatusOK {
		t.Errorf("statuses of the call = %v, want 401 then 200", statuses)
	}
}

func TestARefusedRefreshTellsTheAgentToAskThePersonToRunLoginAndTheBridgeKeepsRunning(t *testing.T) {
	cp := fakecp.New(t)
	source, _ := signIn(t, cp, "pds:read", "pds:change", "offline_access")
	r := start(t, cp, source, cp.URL+"/mcp", nil)
	if _, err := r.session.ListTools(t.Context(), nil); err != nil {
		t.Fatal(err)
	}

	cp.ExpireAccessTokens()
	cp.RejectRefreshes(1)
	result, err := r.session.CallTool(t.Context(), &sdk.CallToolParams{Name: "list_databases"})
	if err != nil {
		t.Fatalf("a tool call failed at the protocol level: %v", err)
	}
	if !result.IsError || !strings.Contains(textOf(t, result), "pds-mcp login") {
		t.Errorf("result = %+v, want a tool error naming pds-mcp login", result)
	}

	// Another method gets a protocol error with the same words.
	cp.ExpireAccessTokens()
	cp.RejectRefreshes(1)
	if _, err := r.session.ListTools(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "pds-mcp login") {
		t.Errorf("tools/list error = %v, want one naming pds-mcp login", err)
	}

	// The person signed in again (here: the control plane accepts the refresh), and the same
	// bridge carries on.
	result, err = r.session.CallTool(t.Context(), &sdk.CallToolParams{Name: "list_databases"})
	if err != nil || result.IsError {
		t.Errorf("after recovering: %+v, %v", result, err)
	}
}

func TestAnInsufficientScopeIsAToolErrorNamingTheScopeAndTheLoginCommand(t *testing.T) {
	cp := fakecp.New(t)
	source, _ := signIn(t, cp, "pds:read", "offline_access")
	r := start(t, cp, source, cp.URL+"/mcp", nil)
	cp.Configure(func(c *fakecp.Config) { c.ForbidScope = "pds:change" })
	forms := len(cp.Forms())

	result, err := r.session.CallTool(t.Context(), &sdk.CallToolParams{Name: "list_databases"})
	if err != nil {
		t.Fatal(err)
	}
	text := textOf(t, result)
	if !result.IsError || !strings.Contains(text, "pds:change") || !strings.Contains(text, "pds-mcp login") {
		t.Errorf("result = %q, want a tool error naming the scope and pds-mcp login", text)
	}
	if len(cp.Forms()) != forms {
		t.Error("a 403 was answered with a token refresh")
	}
}

func TestWithoutALoginConnectingFailsWithTheLoginCommand(t *testing.T) {
	cp := fakecp.New(t)
	store := tokenstore.New(filepath.Join(t.TempDir(), "credentials.json"))
	hc := oauth.NewHTTPClient(cp.URL, "test", time.Minute)
	server, _ := oauth.Discover(t.Context(), hc, cp.URL)

	downIn, clientOut := io.Pipe()
	clientIn, downOut := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_ = bridge.Run(ctx, bridge.Options{
			Endpoint: cp.URL + "/mcp", Tokens: oauth.NewSource(server, hc, store), Version: "1",
			HTTP: oauth.NewHTTPClient(cp.URL, "x", 0), Stdin: downIn, Stdout: downOut,
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()

	_, err := sdk.NewClient(&sdk.Implementation{Name: "a", Version: "1"}, nil).
		Connect(ctx, &sdk.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err == nil || !strings.Contains(err.Error(), "pds-mcp login") {
		t.Errorf("Connect error = %v, want one naming pds-mcp login", err)
	}
	if len(cp.Requests()) != 0 {
		t.Error("a request without a token was sent")
	}
}

func TestAControlPlaneWithNoMCPEndpointIsExplained(t *testing.T) {
	cp := fakecp.New(t)
	source, _ := signIn(t, cp, "pds:read", "offline_access")
	cp.Configure(func(c *fakecp.Config) { c.MCPOff = true })

	downIn, clientOut := io.Pipe()
	clientIn, downOut := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_ = bridge.Run(ctx, bridge.Options{
			Endpoint: cp.URL + "/mcp", Tokens: source, Version: "1",
			HTTP: oauth.NewHTTPClient(cp.URL, "x", 0), Stdin: downIn, Stdout: downOut,
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()

	_, err := sdk.NewClient(&sdk.Implementation{Name: "a", Version: "1"}, nil).
		Connect(ctx, &sdk.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err == nil || !strings.Contains(err.Error(), "does not serve AI agents") {
		t.Errorf("Connect error = %v", err)
	}
}

func TestNoTokenAppearsOnStdoutStderrOrInAResult(t *testing.T) {
	cp := fakecp.New(t)
	source, _ := signIn(t, cp, "pds:read", "pds:change", "offline_access")
	r := start(t, cp, source, cp.URL+"/mcp", nil)

	var results []string
	for range 2 {
		listed, err := r.session.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(listed)
		results = append(results, string(raw))
		cp.ExpireAccessTokens()
	}
	cp.ExpireAccessTokens()
	cp.RejectRefreshes(1)
	failed, _ := r.session.CallTool(t.Context(), &sdk.CallToolParams{Name: "list_databases"})
	raw, _ := json.Marshal(failed)
	results = append(results, string(raw))

	everything := strings.Join(results, "") + r.stdout.String() + r.stderr.String()
	tokens := cp.Tokens()
	if len(tokens) < 4 {
		t.Fatalf("only %d tokens were issued, the test did not exercise refreshes", len(tokens))
	}
	for _, token := range tokens {
		if strings.Contains(everything, token) {
			t.Errorf("a token appeared in the output: %s", token[:6]+"...")
		}
	}
}

func TestATokenIsNeverSentToAnotherOrigin(t *testing.T) {
	var reached atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	defer other.Close()
	home := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	defer home.Close()

	downIn, clientOut := io.Pipe()
	clientIn, downOut := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_ = bridge.Run(ctx, bridge.Options{
			Endpoint: home.URL + "/mcp", Tokens: staticToken("sentinel-token"), Version: "1",
			HTTP: oauth.NewHTTPClient(home.URL, "x", 0), Stdin: downIn, Stdout: downOut,
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()

	_, err := sdk.NewClient(&sdk.Implementation{Name: "a", Version: "1"}, nil).
		Connect(ctx, &sdk.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err == nil {
		t.Error("connecting through a redirect worked")
	}
	if reached.Load() != 0 {
		t.Errorf("%d requests reached another origin", reached.Load())
	}
}

type staticToken string

func (s staticToken) Token(context.Context) (string, error)           { return string(s), nil }
func (s staticToken) Refresh(context.Context, string) (string, error) { return string(s), nil }

func TestAServerNotificationInsideAResponseStreamReachesTheAgent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &msg)
		switch {
		case msg.Method == "initialize":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(msg.ID)+`,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"s","version":"1"}}}`)
		case msg.ID == nil:
			w.WriteHeader(http.StatusAccepted)
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
			_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":"+string(msg.ID)+",\"result\":{\"tools\":[]}}\n\n")
		}
	}))
	defer upstream.Close()

	changed := make(chan struct{}, 1)
	r := start(t, &fakecp.Server{Server: upstream}, staticToken("t"), upstream.URL+"/mcp", &sdk.ClientOptions{
		ToolListChangedHandler: func(context.Context, *sdk.ToolListChangedRequest) { changed <- struct{}{} },
	})
	if _, err := r.session.ListTools(t.Context(), nil); err != nil {
		t.Fatal(err)
	}

	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Error("the list changed notification did not reach the agent")
	}
}

func TestARequestOfTheNewerProtocolEraIsForwardedAsItIs(t *testing.T) {
	cp := fakecp.New(t)
	source, _ := signIn(t, cp, "pds:read", "offline_access")
	agent := startRaw(t, cp, source)

	// A request of the 2026-07-28 era has no initialize before it: the version and the agent's
	// name ride in _meta, and reach the control plane untouched.
	agent.send(`{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"new-agent","version":"1"}}}}`)

	var answer struct {
		ID     int `json:"id"`
		Result struct {
			Tools []struct{ Name string } `json:"tools"`
		} `json:"result"`
	}
	line := agent.answer()
	if err := json.Unmarshal([]byte(line), &answer); err != nil || answer.ID != 7 || len(answer.Result.Tools) == 0 {
		t.Errorf("answer = %s, %v", line, err)
	}
	seen := cp.Requests()
	if len(seen) != 1 || seen[0].Header.Get("MCP-Protocol-Version") != "2026-07-28" {
		t.Errorf("requests = %+v, want one carrying the 2026-07-28 version header", seen)
	}
}

func TestAQuestionTheControlPlaneAsksAndTheAnswerReachTheirEndsUnchanged(t *testing.T) {
	var mu sync.Mutex
	var seenState, seenAnswer string
	upstream := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server {
		server := sdk.NewServer(&sdk.Implementation{Name: "pds", Version: "test"}, nil)
		server.AddTool(&sdk.Tool{Name: "delete_database", InputSchema: map[string]any{"type": "object"}},
			func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				answer, answered := req.Params.InputResponses["confirm"].(*sdk.ElicitResult)
				if !answered {
					return &sdk.CallToolResult{RequestState: "state-1", InputRequests: sdk.InputRequestMap{"confirm": &sdk.ElicitParams{
						Mode: "form", Message: "Delete the database orders-dev?",
						RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}},
					}}}, nil
				}
				mu.Lock()
				seenState, seenAnswer = req.Params.RequestState, answer.Action
				mu.Unlock()

				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "deleted"}}}, nil
			})

		return server
	}, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer upstream.Close()

	asked := make(chan string, 1)
	r := start(t, &fakecp.Server{Server: upstream}, staticToken("t"), upstream.URL, &sdk.ClientOptions{
		ElicitationHandler: func(_ context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
			asked <- req.Params.Message

			return &sdk.ElicitResult{Action: "accept", Content: map[string]any{"name": "orders-dev"}}, nil
		},
	})

	result, err := r.session.CallTool(t.Context(), &sdk.CallToolParams{Name: "delete_database"})
	if err != nil {
		t.Fatal(err)
	}

	if got := textOf(t, result); got != "deleted" {
		t.Errorf("result = %q, want the call to finish after the answer", got)
	}
	select {
	case message := <-asked:
		if message != "Delete the database orders-dev?" {
			t.Errorf("the person was shown %q", message)
		}
	default:
		t.Error("the agent's client was never asked")
	}
	mu.Lock()
	defer mu.Unlock()
	if seenState != "state-1" || seenAnswer != "accept" {
		t.Errorf("the control plane got state %q and answer %q, want the ones the client sent", seenState, seenAnswer)
	}
}
