package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/mcp"
	"github.com/percona/pds/adapter/internal/service"
)

// modelScript is a model that asks for list_databases once and then answers
// with what the tool returned. It keeps what it was sent.
type modelScript struct {
	mu      sync.Mutex
	results []string
}

func (m *modelScript) Complete(_ context.Context, req claude.Request) (*claude.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	last := req.Messages[len(req.Messages)-1]
	if len(last.Content) > 0 && last.Content[0].Type == "tool_result" {
		m.results = append(m.results, last.Content[0].Content)

		return &claude.Response{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: "done"}}}, nil
	}

	return &claude.Response{StopReason: "tool_use", Content: []claude.ContentBlock{
		{Type: "tool_use", ID: "1", Name: "list_databases", Input: json.RawMessage(`{}`)},
	}}, nil
}

// chatWorld is the team world with the Chat wired the way main wires it: its
// tools are the registry's, which call the router that serves the Chat.
func chatWorld(t *testing.T, model claude.Completer) *teamWorld {
	t.Helper()

	var world *teamWorld
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { world.srv.ServeHTTP(w, r) })
	world = newTeamWorld(t, func(deps *api.Deps) {
		chats := service.NewMemoryChats(func(context.Context) uint { return 0 })
		deps.Chat = service.NewChat(deps.Databases, nil,
			service.WithChatStore(chats.Stores()),
			service.WithTools(mcp.New(router, discardLogger(), "test")),
			service.WithChatCompleter(model))
	})

	return world
}

func (w *teamWorld) chat(t *testing.T, as auth.Caller, message string) *httptest.ResponseRecorder {
	t.Helper()

	body := `{"provider":"anthropic","model":"m","message":"` + message + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req.WithContext(auth.WithCaller(req.Context(), as)))

	return rec
}

func TestTheChatReadsThroughTheRegistryAsThePersonAndSeesOnlyTheirTeam(t *testing.T) {
	model := &modelScript{}
	world := chatWorld(t, model)
	world.createDatabase(t, ann, "acme-env", "orders-dev")
	world.createDatabase(t, bob, "pay-env", "ledger")

	annReply := world.chat(t, ann, "what is running?")
	bobReply := world.chat(t, bob, "what is running?")

	require.Equal(t, http.StatusOK, annReply.Code, annReply.Body.String())
	require.Equal(t, http.StatusOK, bobReply.Code, bobReply.Body.String())
	require.Len(t, model.results, 2)
	assert.Contains(t, model.results[0], "acme-env/pds.orders-dev")
	assert.NotContains(t, model.results[0], "ledger", "the Chat reads as ann, in ann's Team")
	assert.Contains(t, model.results[1], "pay-env/pds.ledger")
	assert.NotContains(t, model.results[1], "orders-dev")
}
