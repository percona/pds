package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/mcp"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

const ordersID = "spoke-a/pds.orders-dev"

// toolRequest is one request a tool sent to the API.
type toolRequest struct {
	Method string
	URI    string
	Path   string
	Body   string
	Caller auth.Caller
}

type toolReply struct {
	status int
	body   any
}

func ok(body any) toolReply { return toolReply{http.StatusOK, body} }

// toolAPI stands in for the adapter's router, which is what the registry's
// tools call. It records every request with the caller that made it.
type toolAPI struct {
	mu     sync.Mutex
	routes map[string]toolReply
	seen   []toolRequest
	// hook runs before the answer, to block or to panic.
	hook func(r *http.Request)
}

func (a *toolAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.CallerFromContext(r.Context())
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	a.mu.Lock()
	a.seen = append(a.seen, toolRequest{Method: r.Method, URI: r.URL.RequestURI(), Path: r.URL.Path, Body: string(body), Caller: caller})
	a.mu.Unlock()
	if a.hook != nil {
		a.hook(r)
	}

	answer, found := a.routes[r.URL.Path]
	if !found {
		answer = toolReply{http.StatusNotFound, map[string]any{"code": "ErrNotFound", "message": "That database does not exist."}}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(answer.status)
	_ = json.NewEncoder(w).Encode(answer.body)
}

func (a *toolAPI) requests() []toolRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]toolRequest(nil), a.seen...)
}

func ordersDatabase(message string) map[string]any {
	return map[string]any{
		"id": ordersID, "name": "orders-dev", "engine": "mysql", "profile": "dev",
		"namespace": "pds", "environment": "spoke-a", "status": "ready", "message": message,
		"members": 1, "cpu": 1, "memoryGb": 2, "storageGb": 20,
		"access": map[string]any{"external": false, "visibility": "cluster"},
	}
}

// newToolAPI answers the routes the Chat's questions reach for orders-dev.
func newToolAPI() *toolAPI {
	return &toolAPI{routes: map[string]toolReply{
		"/api/databases":                                  ok(map[string]any{"databases": []any{ordersDatabase("")}}),
		"/api/databases/" + ordersID:                      ok(ordersDatabase("")),
		"/api/databases/" + ordersID + "/scale":           ok(ordersDatabase("")),
		"/api/databases/" + ordersID + "/backup":          ok(map[string]any{"id": "b1", "status": "running"}),
		"/api/databases/" + ordersID + "/backup-schedule": ok(ordersDatabase("")),
		"/api/databases/" + ordersID + "/metrics": ok(map[string]any{
			"databaseId": ordersID, "window": "1h", "start": "2026-09-08T11:00:00Z", "end": "2026-09-08T12:00:00Z",
			"stepSeconds": 60, "pending": false,
			"metrics": []any{map[string]any{
				"kind": "cpu", "unit": "cores", "pending": false, "current": 0.4, "min": 0.1, "max": 0.9, "average": 0.5, "trend": "steady",
			}},
		}),
	}}
}

func registryOver(api http.Handler) *mcp.Registry {
	return mcp.New(api, slog.New(slog.DiscardHandler), "test")
}

// withTools gives the Chat the registry over api.
func withTools(api http.Handler) service.ChatOption {
	return service.WithTools(registryOver(api))
}

// withToolFixtures gives the Chat the registry over an API that knows orders-dev.
func withToolFixtures() service.ChatOption { return withTools(newToolAPI()) }

func toolUse(id, name, input string) claude.Response {
	return claude.Response{StopReason: "tool_use", Content: []claude.ContentBlock{
		{Type: "tool_use", ID: id, Name: name, Input: json.RawMessage(input)},
	}}
}

// resultsOf is the tool results the model was sent in a request.
func resultsOf(req claude.Request) []claude.ContentBlock {
	return req.Messages[len(req.Messages)-1].Content
}

// signedIn is a User of acme acting in their own Team, as a session makes them.
func signedInAsAnn(ctx context.Context) context.Context {
	return auth.WithIdentity(ctx, auth.Identity{
		Subject: "ann",
		Scope: auth.Scope{
			UserID: 2, UserName: "ann", OrganizationID: 1, OrganizationName: "acme", Role: auth.RoleUser,
			Team: auth.Team{ID: 1, Name: "default"},
		},
	})
}

// readTools is what a registry serves a caller at the read tier, as an MCP
// client would list it.
func readTools(t *testing.T, api http.Handler) ([]*sdk.Tool, string) {
	t.Helper()

	caller := auth.Caller{
		Identity: auth.Identity{Client: auth.Client{Kind: auth.ClientMCP}, Scope: auth.Scope{OrganizationID: 1}},
		Access:   auth.AccessRead,
	}
	session, err := registryOver(api).Connect(t.Context(), caller)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	var tools []*sdk.Tool
	for tool, err := range session.Tools(t.Context(), nil) {
		require.NoError(t, err)
		tools = append(tools, tool)
	}

	return tools, session.InitializeResult().Instructions
}

func askAnthropic(t *testing.T, chat *service.Chat, message string) *service.ChatReply {
	t.Helper()

	reply, err := chat.Reply(signedInAsAnn(t.Context()), service.ChatRequest{
		Provider: service.ProviderAnthropic, Model: "m", Message: message,
	})
	require.NoError(t, err)

	return reply
}

func TestChatTurnThatNeedsMetricsCallsGetMetricsThroughTheRegistry(t *testing.T) {
	api := newToolAPI()
	completer := &scriptedCompleter{replies: []claude.Response{
		toolUse("1", "get_metrics", `{"id":"`+ordersID+`"}`),
		textReply("CPU is steady at 0.4 cores."),
	}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withTools(api), service.WithChatCompleter(completer))

	reply := askAnthropic(t, chat, "How busy is orders-dev?")

	assert.Equal(t, "CPU is steady at 0.4 cores.", reply.Text)
	requests := api.requests()
	require.Len(t, requests, 1)
	assert.Equal(t, http.MethodGet, requests[0].Method)
	assert.Equal(t, "/api/databases/"+ordersID+"/metrics", requests[0].Path)
	require.Len(t, completer.calls, 2)
	results := resultsOf(completer.calls[1])
	require.Len(t, results, 1)
	assert.Equal(t, "1", results[0].ToolUseID)
	assert.False(t, results[0].IsError)
	assert.Contains(t, results[0].Content, `"kind":"cpu"`)
}

func TestChatCallsToolsAsThePersonWithClientChatAndTheProviderAsAgent(t *testing.T) {
	for provider, agent := range map[string]string{service.ProviderAnthropic: "Anthropic", service.ProviderCursor: "Cursor"} {
		api := newToolAPI()
		reply := toolUse("1", "list_databases", `{}`)
		final := textReply("one database")
		if provider == service.ProviderCursor {
			final = textReply(`{"text":"one database","actions":[]}`)
		}
		completer := &scriptedCompleter{replies: []claude.Response{reply, final}}
		chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withTools(api), service.WithChatCompleter(completer))

		_, err := chat.Reply(signedInAsAnn(t.Context()), service.ChatRequest{Provider: provider, Model: "m", Message: "what is running?"})
		require.NoError(t, err)

		requests := api.requests()
		require.Len(t, requests, 1, provider)
		caller := requests[0].Caller
		assert.Equal(t, auth.ClientChat, caller.Identity.Client.Kind, provider)
		assert.Equal(t, agent, caller.Agent, provider)
		assert.True(t, auth.ValidAgent(caller.Agent), provider)
		assert.Equal(t, auth.AccessRead, caller.Access, "the Chat reads and waits until a person lets it change something")
		assert.Equal(t, "ann", caller.Identity.Subject, provider)
		assert.Equal(t, uint(1), caller.Identity.Scope.OrganizationID, provider)
		assert.Equal(t, uint(1), caller.Identity.Scope.Team.ID, provider)
	}
}

func TestChatSendsAnthropicExactlyTheToolsTheRegistryListsForARead(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("ok")}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withToolFixtures(), service.WithChatCompleter(completer))

	askAnthropic(t, chat, "help")

	listed, _ := readTools(t, newToolAPI())
	require.NotEmpty(t, listed)
	sent := completer.calls[0].Tools
	require.Len(t, sent, len(listed))
	for i, want := range listed {
		assert.Equal(t, want.Name, sent[i].Name)
		assert.Equal(t, want.Description, sent[i].Description, want.Name)
		schema, err := json.Marshal(want.InputSchema)
		require.NoError(t, err)
		assert.JSONEq(t, string(schema), string(sent[i].InputSchema), want.Name)
	}
	names := toolNames(sent)
	assert.Contains(t, names, "get_metrics")
	assert.Contains(t, names, "wait_for_database")
	assert.NotContains(t, names, "create_database", "the Chat does not change anything yet")
	assert.NotContains(t, names, "delete_database")
}

func TestChatSystemPromptIsTheRegistrysInstructionsAndWhatOnlyTheChatNeeds(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("first"), textReply("second")}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withToolFixtures(), service.WithChatCompleter(completer))

	first := askAnthropic(t, chat, "help")
	_, err := chat.Reply(signedInAsAnn(t.Context()), service.ChatRequest{
		ConversationID: first.ConversationID, Provider: service.ProviderAnthropic, Model: "m", Message: "and again",
	})
	require.NoError(t, err)

	_, instructions := readTools(t, newToolAPI())
	require.NotEmpty(t, instructions)
	require.Len(t, completer.calls, 2)
	system := completer.calls[0].System
	assert.Contains(t, system, instructions)
	assert.Contains(t, system, "<actions>", "how to send a button is the Chat's own")
	assert.Equal(t, system, completer.calls[1].System, "the prompt is the cached prefix, so no turn changes it")
	assert.Contains(t, system, "reads only", "a read session is told what it cannot do")
}

func TestChatCursorIsTaughtTheRegistrysToolsAndTheEmulatedProtocol(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply(`{"text":"ok","actions":[]}`)}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withToolFixtures(), service.WithChatCompleter(completer))

	_, err := chat.Reply(signedInAsAnn(t.Context()), service.ChatRequest{Provider: service.ProviderCursor, Model: "m", Message: "help"})
	require.NoError(t, err)

	listed, instructions := readTools(t, newToolAPI())
	call := completer.calls[0]
	assert.Equal(t, names(listed), toolNames(call.Tools))
	assert.Contains(t, call.System, instructions)
	assert.Contains(t, call.System, "tool_calls")
	assert.NotContains(t, call.System, "<actions>", "Cursor answers in JSON")
}

func names(tools []*sdk.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}

	return out
}

func TestChatCursorToolCallsAreEachDispatchedToTheRegistry(t *testing.T) {
	api := newToolAPI()
	completer := &scriptedCompleter{replies: []claude.Response{
		{StopReason: "tool_use", Content: []claude.ContentBlock{
			{Type: "tool_use", ID: "1", Name: "list_databases", Input: json.RawMessage(`{}`)},
			{Type: "tool_use", ID: "2", Name: "get_database", Input: json.RawMessage(`{"id":"` + ordersID + `"}`)},
		}},
		textReply(`{"text":"orders-dev is ready","actions":[]}`),
	}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withTools(api), service.WithChatCompleter(completer))

	reply, err := chat.Reply(signedInAsAnn(t.Context()), service.ChatRequest{
		Provider: service.ProviderCursor, Model: "composer-2", Message: "how is orders-dev?",
	})
	require.NoError(t, err)

	assert.Equal(t, "orders-dev is ready", reply.Text)
	paths := map[string]bool{}
	for _, request := range api.requests() {
		paths[request.Path] = true
	}
	assert.Equal(t, map[string]bool{"/api/databases": true, "/api/databases/" + ordersID: true}, paths)
	results := resultsOf(completer.calls[1])
	require.Len(t, results, 2)
	assert.Equal(t, "1", results[0].ToolUseID)
	assert.Equal(t, "2", results[1].ToolUseID)
	assert.Contains(t, results[1].Content, `"status":"ready"`)
}

func TestChatToolFailureReachesTheModelAsAToolErrorAndTheTurnStillAnswers(t *testing.T) {
	api := newToolAPI()
	completer := &scriptedCompleter{replies: []claude.Response{
		toolUse("1", "get_database", `{"id":"spoke-a/pds.missing"}`),
		textReply("That database does not exist."),
	}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withTools(api), service.WithChatCompleter(completer))

	reply := askAnthropic(t, chat, "how is missing?")

	assert.Equal(t, "That database does not exist.", reply.Text)
	result := resultsOf(completer.calls[1])[0]
	assert.True(t, result.IsError, "the model has to see a failed call, not data")
	assert.Contains(t, result.Content, `"kind":"not_found"`)
	assert.Contains(t, result.Content, "That database does not exist.")
}

func TestChatToolAboveTheReadTierIsAToolErrorAndNoRequestIsMade(t *testing.T) {
	api := newToolAPI()
	completer := &scriptedCompleter{replies: []claude.Response{
		toolUse("1", "delete_database", `{"id":"`+ordersID+`","confirm":"orders-dev"}`),
		textReply("I cannot delete it."),
	}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withTools(api), service.WithChatCompleter(completer))

	reply := askAnthropic(t, chat, "delete orders-dev")

	assert.Equal(t, "I cannot delete it.", reply.Text)
	result := resultsOf(completer.calls[1])[0]
	assert.True(t, result.IsError)
	assert.NotEmpty(t, result.Content)
	assert.Empty(t, api.requests())
}

func TestChatRedactsACredentialInAToolResultBeforeTheModelReadsIt(t *testing.T) {
	api := newToolAPI()
	api.routes["/api/databases/"+ordersID] = ok(ordersDatabase("restore it with mysql://app:s3cret@orders-dev:3306/orders"))
	completer := &scriptedCompleter{replies: []claude.Response{
		toolUse("1", "get_database", `{"id":"`+ordersID+`"}`),
		textReply("ok"),
	}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withTools(api), service.WithChatCompleter(completer))

	askAnthropic(t, chat, "how is orders-dev?")

	raw, err := json.Marshal(completer.calls[1])
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "s3cret")
	assert.Contains(t, string(raw), "restore it with")
}

func TestChatToolPanicBecomesAToolError(t *testing.T) {
	api := newToolAPI()
	api.hook = func(*http.Request) { panic("tool bug") }
	completer := &scriptedCompleter{replies: []claude.Response{
		toolUse("1", "get_database", `{"id":"`+ordersID+`"}`),
		textReply("status is unavailable"),
	}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withTools(api), service.WithChatCompleter(completer))

	reply := askAnthropic(t, chat, "status?")

	assert.Equal(t, "status is unavailable", reply.Text)
	result := resultsOf(completer.calls[1])[0]
	assert.Equal(t, "1", result.ToolUseID)
	assert.True(t, result.IsError, "the model has to see a failed call, not data")
	assert.NotContains(t, result.Content, "tool bug")
}

func TestChatRunsToolsOfOneRoundInParallel(t *testing.T) {
	api := newToolAPI()
	var inFlight, peak atomic.Int32
	both := make(chan struct{})
	var once sync.Once
	api.hook = func(r *http.Request) {
		if r.URL.Path != "/api/databases/"+ordersID {
			return
		}
		now := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			seen := peak.Load()
			if now <= seen || peak.CompareAndSwap(seen, now) {
				break
			}
		}
		if now >= 2 {
			once.Do(func() { close(both) })
		}
		select {
		case <-both:
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}
	completer := &scriptedCompleter{replies: []claude.Response{
		{StopReason: "tool_use", Content: []claude.ContentBlock{
			{Type: "tool_use", ID: "1", Name: "get_database", Input: json.RawMessage(`{"id":"` + ordersID + `"}`)},
			{Type: "tool_use", ID: "2", Name: "get_database", Input: json.RawMessage(`{"id":"` + ordersID + `"}`)},
		}},
		textReply("ok"),
	}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withTools(api), service.WithChatCompleter(completer))

	askAnthropic(t, chat, "status twice")

	assert.Equal(t, int32(2), peak.Load())
	results := resultsOf(completer.calls[1])
	require.Len(t, results, 2)
	assert.Equal(t, "1", results[0].ToolUseID)
	assert.Equal(t, "2", results[1].ToolUseID)
}

func TestChatWithoutTheRegistryOffersNoTools(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{
		toolUse("1", "list_databases", `{}`),
		textReply("I could not look."),
	}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), service.WithChatCompleter(completer))

	reply := askAnthropic(t, chat, "what is running?")

	assert.Equal(t, "I could not look.", reply.Text)
	assert.Empty(t, completer.calls[0].Tools)
	assert.True(t, resultsOf(completer.calls[1])[0].IsError)
}

func TestChatAnswersEveryKindOfQuestionThroughTheRegistry(t *testing.T) {
	db := "/api/databases/" + ordersID
	cases := []struct {
		tool, input, path string
		body              any
	}{
		{"list_backups", `{"id":"` + ordersID + `"}`, db + "/backups", map[string]any{"backups": []any{}}},
		{"get_database_history", `{"id":"` + ordersID + `"}`, db + "/history", map[string]any{"entries": []any{}}},
		{"get_database_usage", `{"id":"` + ordersID + `"}`, db + "/usage", map[string]any{"points": []any{}}},
		{"get_environment_history", `{"name":"spoke-a"}`, "/api/environments/spoke-a/history", map[string]any{"runs": []any{}}},
		{"list_support_tickets", `{}`, "/api/support/tickets", map[string]any{"tickets": []any{}}},
		{"list_activity", `{}`, "/api/activity", map[string]any{"entries": []any{}, "cursor": "", "truncated": false}},
	}
	for _, tc := range cases {
		api := newToolAPI()
		api.routes[tc.path] = ok(tc.body)
		completer := &scriptedCompleter{replies: []claude.Response{toolUse("1", tc.tool, tc.input), textReply("done")}}
		chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withTools(api), service.WithChatCompleter(completer))

		askAnthropic(t, chat, "question")

		var paths []string
		for _, request := range api.requests() {
			paths = append(paths, request.Path)
		}
		assert.Equal(t, []string{tc.path}, paths, tc.tool)
		result := resultsOf(completer.calls[1])[0]
		assert.False(t, result.IsError, "%s: %s", tc.tool, result.Content)
	}
}

func TestChatCutsAnOversizedToolResultOnACharacterBoundary(t *testing.T) {
	api := newToolAPI()
	api.routes["/api/databases/"+ordersID] = ok(ordersDatabase(strings.Repeat("é", 15000)))
	completer := &scriptedCompleter{replies: []claude.Response{toolUse("1", "get_database", `{"id":"`+ordersID+`"}`), textReply("ok")}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withTools(api), service.WithChatCompleter(completer))

	askAnthropic(t, chat, "status")

	content := resultsOf(completer.calls[1])[0].Content
	assert.True(t, utf8.ValidString(content))
	assert.Contains(t, content, "cut off")
	assert.NotContains(t, content, "ticket")
	assert.Less(t, len(content), 20200)
}

func TestChatRolePromptSaysAChangeIsAskedForAndOnlyNavigationHasButtons(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("ok")}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withToolFixtures(), service.WithChatCompleter(completer))

	askAnthropic(t, chat, "scale it")

	system := completer.calls[0].System
	assert.Contains(t, system, "the person is asked to approve it")
	assert.NotContains(t, system, "offer the button instead of saying it needs change access")
	assert.NotContains(t, system, "kind that is one of scale")
	assert.Contains(t, system, "metrics, status, support")
}

// fakeLimits is the Organization's AI settings, which a test changes between turns.
type fakeLimits struct {
	mu       sync.Mutex
	settings store.AISettings
	err      error
	asked    []uint
}

func (f *fakeLimits) AI(_ context.Context, organizationID uint) (store.AISettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, organizationID)

	return f.settings, f.err
}

func (f *fakeLimits) set(settings store.AISettings) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settings = settings
}

func accessOfNextTurn(t *testing.T, limits *fakeLimits, ctx context.Context) auth.Access {
	t.Helper()

	api := newToolAPI()
	completer := &scriptedCompleter{replies: []claude.Response{toolUse("1", "list_databases", `{}`), textReply("done")}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withTools(api),
		service.WithAccessLimits(limits), service.WithChatCompleter(completer))

	_, err := chat.Reply(ctx, service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "what is running?"})
	require.NoError(t, err)
	requests := api.requests()
	require.NotEmpty(t, requests)

	return requests[0].Caller.Access
}

func TestChatToolsAreServedAtTheLowestOfTheOrganizationLimitAndTheRole(t *testing.T) {
	limits := &fakeLimits{settings: store.AISettings{Access: "change"}}
	ctx := signedInAsAnn(t.Context())

	assert.Equal(t, auth.AccessChange, accessOfNextTurn(t, limits, ctx))
	assert.Equal(t, []uint{1}, limits.asked, "the limit is the signed in person's Organization's")

	limits.set(store.AISettings{Access: "change_delete"})
	assert.Equal(t, auth.AccessChangeDelete, accessOfNextTurn(t, limits, ctx))

	limits.set(store.AISettings{Access: "read"})
	assert.Equal(t, auth.AccessRead, accessOfNextTurn(t, limits, ctx), "lowering the limit applies at the next turn")
}

func TestChatToolsStayAtReadWhenTheLimitCannotBeRead(t *testing.T) {
	limits := &fakeLimits{err: errors.New("database is down")}

	assert.Equal(t, auth.AccessRead, accessOfNextTurn(t, limits, signedInAsAnn(t.Context())))
}

func TestCallerAccessIsTheLowestOfLimitAndRole(t *testing.T) {
	cases := []struct {
		limit auth.Access
		role  string
		want  auth.Access
	}{
		{auth.AccessChangeDelete, auth.RoleAdmin, auth.AccessChangeDelete},
		{auth.AccessChange, auth.RoleAdmin, auth.AccessChange},
		{auth.AccessChangeDelete, auth.RoleUser, auth.AccessChangeDelete},
		{auth.AccessRead, auth.RoleUser, auth.AccessRead},
		{auth.AccessChangeDelete, "stranger", auth.AccessRead},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, service.CallerAccess(tc.limit, tc.role), "%s at %s", tc.role, tc.limit)
	}
}
