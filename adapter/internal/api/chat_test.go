package api_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
)

const chatPath = "/api/chat"

func newChatServer(t *testing.T, connections *service.Connections, completer claude.Completer) *api.Server {
	t.Helper()

	opts := []service.ChatOption{}
	if completer != nil {
		opts = append(opts, service.WithChatCompleter(completer))
	}

	return api.NewServer(api.Deps{
		Config: config.Config{Addr: ":8080", Env: config.EnvDev, EverestNamespace: "pds"},
		Logger: log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:   auth.NewStub(),
		Chat:   service.NewChat(nil, connections, append(opts, service.WithChatStore(service.NewMemoryChats(fixedTeam(1)).Stores()))...),
	})
}

func TestChatRejectsAMissingMessage(t *testing.T) {
	rec := do(t, newChatServer(t, nil, &scriptedCompleter{}), http.MethodPost, chatPath,
		strings.NewReader(`{"provider":"anthropic","model":"claude-sonnet-4-20250514","message":""}`))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "ErrValidation", decodeBody(t, rec)["code"])
}

func TestChatRejectsBlankModel(t *testing.T) {
	rec := do(t, newChatServer(t, nil, &scriptedCompleter{}), http.MethodPost, chatPath,
		strings.NewReader(`{"provider":"anthropic","model":"","message":"hi"}`))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "ErrValidation", decodeBody(t, rec)["code"])
}

func TestChatRejectsUnknownProvider(t *testing.T) {
	rec := do(t, newChatServer(t, nil, &scriptedCompleter{}), http.MethodPost, chatPath,
		strings.NewReader(`{"provider":"bedrock","model":"m","message":"hi"}`))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	decoded := decodeBody(t, rec)
	assert.Equal(t, "ErrValidation", decoded["code"])
	assert.Contains(t, decoded["message"], "provider")
}

func TestChatAnthropicUnavailable(t *testing.T) {
	conns := service.NewConnections(&memStore{}, service.ConnectionsOptions{
		Namespace:           "pds",
		AssistantSecretName: "pds-assistant",
		CursorSecretName:    "pds-cursor",
	})

	rec := do(t, newChatServer(t, conns, nil), http.MethodPost, chatPath,
		strings.NewReader(`{"provider":"anthropic","model":"claude-sonnet-4-20250514","message":"hi"}`))

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	decoded := decodeBody(t, rec)
	assert.Equal(t, "ErrNotImplemented", decoded["code"])
	assert.Equal(t, "Chat needs an Anthropic key, which is not configured.", decoded["message"])
}

func TestChatCursorUnavailable(t *testing.T) {
	conns := service.NewConnections(&memStore{}, service.ConnectionsOptions{
		Namespace:           "pds",
		AssistantSecretName: "pds-assistant",
		CursorSecretName:    "pds-cursor",
	})

	rec := do(t, newChatServer(t, conns, nil), http.MethodPost, chatPath,
		strings.NewReader(`{"provider":"cursor","model":"gpt-4","message":"hi"}`))

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	decoded := decodeBody(t, rec)
	assert.Equal(t, "ErrNotImplemented", decoded["code"])
	assert.Equal(t, "Chat needs a Cursor key, which is not configured.", decoded["message"])
}

func TestChatRefusesARemovedProvider(t *testing.T) {
	for _, provider := range []string{"openai", "gemini", "openrouter"} {
		t.Run(provider, func(t *testing.T) {
			conns := service.NewConnections(&memStore{}, service.ConnectionsOptions{Namespace: "pds"})

			rec := do(t, newChatServer(t, conns, nil), http.MethodPost, chatPath,
				strings.NewReader(`{"provider":"`+provider+`","model":"m","message":"hi"}`))

			require.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Equal(t, "ErrValidation", decodeBody(t, rec)["code"])
		})
	}
}

func TestChatReturnsTextAndActions(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{{
		StopReason: "end_turn",
		Content: []claude.ContentBlock{{
			Type: "text",
			Text: `orders-dev has a node down.<actions>[{"label":"Contact support","kind":"support"}]</actions>`,
		}},
	}}}

	rec := do(t, newChatServer(t, nil, completer), http.MethodPost, chatPath,
		strings.NewReader(`{"provider":"anthropic","model":"claude-sonnet-4-20250514","message":"Why is orders-dev degraded?"}`))

	require.Equal(t, http.StatusOK, rec.Code)
	decoded := decodeBody(t, rec)
	assert.Equal(t, "orders-dev has a node down.", decoded["text"])
	actions, ok := decoded["actions"].([]any)
	require.True(t, ok)
	require.Len(t, actions, 1)
	first := actions[0].(map[string]any)
	assert.Equal(t, "support", first["kind"])
	assert.Contains(t, rec.Body.String(), `"actions":[`)
	assert.NotContains(t, rec.Body.String(), `"actions":null`)
}

func TestChatMapsUnauthorizedTo502(t *testing.T) {
	completer := &scriptedCompleter{err: claude.ErrUnauthorized}

	rec := do(t, newChatServer(t, nil, completer), http.MethodPost, chatPath,
		strings.NewReader(`{"provider":"anthropic","model":"claude-sonnet-4-20250514","message":"hi"}`))

	require.Equal(t, http.StatusBadGateway, rec.Code)
	decoded := decodeBody(t, rec)
	assert.Equal(t, "ErrDownstream", decoded["code"])
	assert.Equal(t, "The assistant could not authenticate. Check the key in Connections.", decoded["message"])
}

func TestChatMapsCompleterFailureTo502(t *testing.T) {
	completer := &scriptedCompleter{err: errors.New("upstream timeout")}

	rec := do(t, newChatServer(t, nil, completer), http.MethodPost, chatPath,
		strings.NewReader(`{"provider":"anthropic","model":"claude-sonnet-4-20250514","message":"hi"}`))

	require.Equal(t, http.StatusBadGateway, rec.Code)
	decoded := decodeBody(t, rec)
	assert.Equal(t, "ErrDownstream", decoded["code"])
	assert.Equal(t, "The assistant did not answer. Try again in a moment.", decoded["message"])
}
