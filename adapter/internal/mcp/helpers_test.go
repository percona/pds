package mcp_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/mcp"
)

// readCaller is a signed in User of acme acting in their own Team.
func readCaller() auth.Caller {
	return auth.Caller{
		Identity: auth.Identity{
			Subject: "ann",
			Scope: auth.Scope{
				UserID: 2, UserName: "ann", OrganizationID: 1, OrganizationName: "acme", Role: auth.RoleUser,
				Team: auth.Team{ID: 1, Name: "default"},
			},
			Client: auth.Client{Kind: auth.ClientMCP},
		},
		Access: auth.AccessRead,
		Agent:  "Claude Code",
	}
}

func discardLogger() *slog.Logger {
	return log.New(io.Discard, log.FormatJSON, slog.LevelDebug)
}

func connect(t *testing.T, api http.Handler, opts ...mcp.Option) *mcp.Session {
	t.Helper()

	return connectAs(t, api, readCaller(), discardLogger(), opts...)
}

func connectAs(t *testing.T, api http.Handler, caller auth.Caller, logger *slog.Logger, opts ...mcp.Option) *mcp.Session {
	t.Helper()

	session, err := mcp.New(api, logger, "test", opts...).Connect(t.Context(), caller)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	return session
}

func callTool(t *testing.T, session *mcp.Session, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()

	res, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)

	return res
}

// structured is the result's structured content as a generic map.
func structured(t *testing.T, res *sdk.CallToolResult) map[string]any {
	t.Helper()

	body, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))

	return out
}

// text is the result's one text block.
func text(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()

	require.Len(t, res.Content, 1)
	block, ok := res.Content[0].(*sdk.TextContent)
	require.True(t, ok, "the one content block is text")

	return block.Text
}

// listedTools is what tools/list answers, by name and in order.
func listedTools(t *testing.T, session *mcp.Session) []*sdk.Tool {
	t.Helper()

	list, err := session.ListTools(t.Context(), nil)
	require.NoError(t, err)

	return list.Tools
}

func names(tools []*sdk.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}

	return out
}

// validates reports whether the value matches the schema a client received.
func validates(t *testing.T, schema any, value map[string]any) error {
	t.Helper()

	body, err := json.Marshal(schema)
	require.NoError(t, err)
	var parsed jsonschema.Schema
	require.NoError(t, json.Unmarshal(body, &parsed))
	resolved, err := parsed.Resolve(nil)
	require.NoError(t, err)

	return resolved.Validate(value)
}

// seen is one request the fake API received.
type seen struct {
	Method        string
	URI           string
	RequestID     string
	Cookie        string
	Authorization string
	ContentType   string
	Body          string
	Caller        auth.Caller
	HasCaller     bool
}

// reply is what the fake API answers a path with.
type reply struct {
	status int
	body   any
}

// fakeAPI stands in for the adapter's router: it records every request and
// answers by path.
type fakeAPI struct {
	mu       sync.Mutex
	requests []seen
	routes   map[string]reply
	// steps answers a path with each reply in turn and then repeats the last.
	steps map[string][]reply
	// hook runs before the answer, to block or to panic.
	hook func(r *http.Request)
}

func newFakeAPI(routes map[string]reply) *fakeAPI {
	return &fakeAPI{routes: routes}
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	caller, hasCaller := auth.CallerFromContext(r.Context())
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	f.mu.Lock()
	f.requests = append(f.requests, seen{
		Method:        r.Method,
		URI:           r.URL.RequestURI(),
		RequestID:     r.Header.Get("X-Request-Id"),
		Cookie:        r.Header.Get("Cookie"),
		Authorization: r.Header.Get("Authorization"),
		ContentType:   r.Header.Get("Content-Type"),
		Body:          string(body),
		Caller:        caller,
		HasCaller:     hasCaller,
	})
	f.mu.Unlock()

	if f.hook != nil {
		f.hook(r)
	}

	answer, ok := f.routes[r.Method+" "+r.URL.Path]
	if !ok {
		answer, ok = f.routes[r.URL.Path]
	}
	f.mu.Lock()
	if steps := f.steps[r.URL.Path]; len(steps) > 0 {
		answer, ok = steps[0], true
		if len(steps) > 1 {
			f.steps[r.URL.Path] = steps[1:]
		}
	}
	f.mu.Unlock()
	if !ok {
		answer = reply{http.StatusNotFound, map[string]any{"code": "ErrNotFound", "message": "That endpoint does not exist."}}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(answer.status)
	_ = json.NewEncoder(w).Encode(answer.body)
}

// then makes the path answer each reply in turn, and the last one from then on.
func (f *fakeAPI) then(path string, replies ...reply) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.steps == nil {
		f.steps = map[string][]reply{}
	}
	f.steps[path] = replies
}

func (f *fakeAPI) sent() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]seen(nil), f.requests...)
}

func ok(body any) reply { return reply{http.StatusOK, body} }

func failed(status int, code, message string) reply {
	return reply{status, map[string]any{"code": code, "message": message}}
}

func assertNoRequest(t *testing.T, api *fakeAPI) {
	t.Helper()

	assert.Empty(t, api.sent(), "no request was built")
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()

	body, err := json.Marshal(value)
	require.NoError(t, err)

	return string(body)
}

func withShortTimeout() mcp.Option { return mcp.WithTimeout(50 * time.Millisecond) }
