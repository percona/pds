package claude_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/claude"
)

func TestCompleteSendsTheKeyAndParsesToolUse(t *testing.T) {
	var captured struct {
		APIKey  string
		Version string
		Body    map[string]any
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.APIKey = r.Header.Get("x-api-key")
		captured.Version = r.Header.Get("anthropic-version")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured.Body)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"stop_reason": "tool_use",
			"content": [
				{"type": "text", "text": "I will read the logs."},
				{"type": "tool_use", "id": "toolu_1", "name": "get_logs", "input": {"severity": "error"}}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	got, err := client.Complete(context.Background(), claude.Request{
		System: "You investigate databases.",
		Messages: []claude.Message{{
			Role:    "user",
			Content: []claude.ContentBlock{{Type: "text", Text: "Why is backup failing?"}},
		}},
		Tools: []claude.Tool{{
			Name:        "get_logs",
			Description: "Read recent logs",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"severity":{"type":"string"}}}`),
		}},
	})
	require.NoError(t, err)

	assert.Equal(t, "sk-ant-test", captured.APIKey)
	assert.Equal(t, "2023-06-01", captured.Version)
	assert.Equal(t, "tool_use", got.StopReason)
	require.Len(t, got.Content, 2)
	assert.Equal(t, "text", got.Content[0].Type)
	assert.Equal(t, "tool_use", got.Content[1].Type)
	assert.Equal(t, "get_logs", got.Content[1].Name)
	assert.Equal(t, "toolu_1", got.Content[1].ID)
	assert.JSONEq(t, `{"severity":"error"}`, string(got.Content[1].Input))
	assert.NotContains(t, fmt.Sprint(captured.Body["system"]), "sk-ant")
}

func TestCompleteParsesEndTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/messages", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"stop_reason": "end_turn",
			"content": [{"type": "text", "text": "{\"sentence\":\"Last backup failed.\",\"findings\":[],\"actions\":[]}"}]
		}`))
	}))
	t.Cleanup(srv.Close)

	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	got, err := client.Complete(context.Background(), claude.Request{
		Messages: []claude.Message{{
			Role:    "user",
			Content: []claude.ContentBlock{{Type: "text", Text: "status"}},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "end_turn", got.StopReason)
	require.Len(t, got.Content, 1)
	assert.Contains(t, got.Content[0].Text, "Last backup failed.")
}

func TestCompleteMaps401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	t.Cleanup(srv.Close)

	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "bad"})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), claude.Request{
		Messages: []claude.Message{{Role: "user", Content: []claude.ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	require.ErrorIs(t, err, claude.ErrUnauthorized)
}

func TestNewRequiresAKey(t *testing.T) {
	_, err := claude.New(claude.Options{BaseURL: "https://api.anthropic.com"})
	require.Error(t, err)
}

func TestCompleteUsesRequestModelWhenSet(t *testing.T) {
	var capturedModel string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		capturedModel, _ = body["model"].(string)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}`))
	}))
	t.Cleanup(srv.Close)

	client, err := claude.New(claude.Options{
		BaseURL: srv.URL,
		APIKey:  "sk-ant-test",
		Model:   "client-default",
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), claude.Request{
		Model: "request-override",
		Messages: []claude.Message{{
			Role:    "user",
			Content: []claude.ContentBlock{{Type: "text", Text: "hi"}},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "request-override", capturedModel)

	_, err = client.Complete(context.Background(), claude.Request{
		Messages: []claude.Message{{
			Role:    "user",
			Content: []claude.ContentBlock{{Type: "text", Text: "hi"}},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "client-default", capturedModel)
}

func TestCompleteForbidsToolCallsButKeepsTheDefinitions(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}`))
	}))
	t.Cleanup(srv.Close)

	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), claude.Request{
		Messages: []claude.Message{
			{Role: "assistant", Content: []claude.ContentBlock{{Type: "tool_use", ID: "t1", Name: "get_logs", Input: json.RawMessage(`{}`)}}},
			{Role: "user", Content: []claude.ContentBlock{{Type: "tool_result", ToolUseID: "t1", Content: "no logs"}}},
		},
		Tools:       []claude.Tool{{Name: "get_logs", Description: "Read recent logs"}},
		NoToolCalls: true,
	})
	require.NoError(t, err)

	// The API refuses tool_use blocks in a request that defines no tools, so
	// the definitions stay and tool_choice rules out another call.
	assert.NotEmpty(t, body["tools"])
	assert.Equal(t, map[string]any{"type": "none"}, body["tool_choice"])
}

type sentRequest struct {
	Header http.Header
	Raw    []byte
	Body   map[string]any
}

// answeringServer replies to every request with response and records what it
// was sent.
func answeringServer(t *testing.T, response string) (*httptest.Server, *[]sentRequest) {
	t.Helper()

	var sent []sentRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		sent = append(sent, sentRequest{Header: r.Header.Clone(), Raw: raw, Body: body})

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)

	return srv, &sent
}

const endTurn = `{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}`

func userText(text string) claude.Message {
	return claude.Message{Role: "user", Content: []claude.ContentBlock{{Type: "text", Text: text}}}
}

func TestCompleteDefaultsToSonnet55AtLowEffort(t *testing.T) {
	srv, sent := answeringServer(t, endTurn)
	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), claude.Request{Messages: []claude.Message{userText("hi")}})
	require.NoError(t, err)

	body := (*sent)[0].Body
	assert.Equal(t, "claude-sonnet-5-5", body["model"])
	assert.Equal(t, "claude-sonnet-5-5", claude.DefaultModel)
	assert.Equal(t, map[string]any{"effort": "low"}, body["output_config"])
	assert.NotContains(t, body, "thinking", "thinking is adaptive when the request does not say")
}

func TestCompleteAsksForTheRefusalFallback(t *testing.T) {
	srv, sent := answeringServer(t, endTurn)
	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), claude.Request{Messages: []claude.Message{userText("hi")}})
	require.NoError(t, err)

	assert.Equal(t, "default", (*sent)[0].Body["fallbacks"])
	assert.Equal(t, "server-side-fallback-2026-07-01", (*sent)[0].Header.Get("anthropic-beta"))
}

func TestCompleteSendsOnlyWhatAModelAccepts(t *testing.T) {
	srv, sent := answeringServer(t, endTurn)
	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	for _, model := range []string{"claude-haiku-4-5", "claude-sonnet-4-5-20250929", "claude-haiku-5-5"} {
		_, err = client.Complete(context.Background(), claude.Request{
			Model:    model,
			Messages: []claude.Message{userText("hi")},
		})
		require.NoError(t, err)
	}

	for _, request := range *sent {
		assert.NotContains(t, request.Body, "fallbacks", request.Body["model"])
		assert.Empty(t, request.Header.Get("anthropic-beta"), request.Body["model"])
	}
	assert.NotContains(t, (*sent)[0].Body, "output_config", "claude-haiku-4-5 has no effort setting")
	assert.NotContains(t, (*sent)[1].Body, "output_config", "claude-sonnet-4-5 has no effort setting")
	assert.Equal(t, map[string]any{"effort": "low"}, (*sent)[2].Body["output_config"])
}

func TestCompleteCachesToolsAndSystemWithOneBreakpoint(t *testing.T) {
	srv, sent := answeringServer(t, endTurn)
	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	tools := []claude.Tool{
		{Name: "get_logs", Description: "Read recent logs", InputSchema: json.RawMessage(`{"type":"object","properties":{"b":{},"a":{}}}`)},
		{Name: "get_status", Description: "Read status"},
	}
	for _, question := range []string{"first question", "a different second question"} {
		_, err = client.Complete(context.Background(), claude.Request{
			System:   "You investigate databases.",
			Messages: []claude.Message{userText(question)},
			Tools:    tools,
		})
		require.NoError(t, err)
	}

	for _, request := range *sent {
		assert.Equal(t, 1, strings.Count(string(request.Raw), "cache_control"), "one breakpoint per request")
		system, ok := request.Body["system"].([]any)
		require.True(t, ok, "the system prompt is a block list so it can carry the breakpoint")
		require.Len(t, system, 1)
		assert.Equal(t, map[string]any{
			"type":          "text",
			"text":          "You investigate databases.",
			"cache_control": map[string]any{"type": "ephemeral"},
		}, system[0])
	}

	// The prefix is what precedes the messages: tools, then system. It has to
	// be byte for byte the same on every request or nothing is read from the
	// cache.
	prefix := func(raw []byte) string {
		var parts struct {
			Tools  json.RawMessage `json:"tools"`
			System json.RawMessage `json:"system"`
		}
		require.NoError(t, json.Unmarshal(raw, &parts))

		return string(parts.Tools) + string(parts.System)
	}
	assert.Equal(t, prefix((*sent)[0].Raw), prefix((*sent)[1].Raw))
	assert.NotContains(t, prefix((*sent)[0].Raw), "question")
}

func TestCompleteBreakpointFallsOnTheLastToolWhenThereIsNoSystemPrompt(t *testing.T) {
	srv, sent := answeringServer(t, endTurn)
	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), claude.Request{
		Messages: []claude.Message{userText("hi")},
		Tools:    []claude.Tool{{Name: "a", Description: "A"}, {Name: "b", Description: "B"}},
	})
	require.NoError(t, err)

	request := (*sent)[0]
	assert.Equal(t, 1, strings.Count(string(request.Raw), "cache_control"))
	tools := request.Body["tools"].([]any)
	assert.NotContains(t, tools[0], "cache_control")
	assert.Contains(t, tools[1], "cache_control")
}

func TestCompleteSendsEachEarlierResponseBackUnchanged(t *testing.T) {
	const content = `[
		{"type":"thinking","thinking":"","signature":"EqQBCkYIARgC"},
		{"type":"text","text":"I will read the logs."},
		{"type":"tool_use","id":"toolu_1","name":"get_logs","input":{"severity":"error"},"caller":{"type":"direct"}}
	]`
	srv, sent := answeringServer(t, `{"stop_reason":"tool_use","content":`+content+`}`)
	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	messages := []claude.Message{userText("Why is backup failing?")}
	first, err := client.Complete(context.Background(), claude.Request{Messages: messages})
	require.NoError(t, err)

	messages = append(messages,
		claude.Message{Role: "assistant", Content: first.Content},
		claude.Message{Role: "user", Content: []claude.ContentBlock{{Type: "tool_result", ToolUseID: "toolu_1", Content: "no logs"}}},
	)
	_, err = client.Complete(context.Background(), claude.Request{Messages: messages})
	require.NoError(t, err)

	var second struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal((*sent)[1].Raw, &second))
	require.Len(t, second.Messages, 3)
	assert.JSONEq(t, content, string(second.Messages[1].Content),
		"the thinking block, its signature and every other field go back as they came")
}

func TestCompleteSendsNoPrefillForcedToolChoiceOrSamplingParameter(t *testing.T) {
	srv, sent := answeringServer(t, endTurn)
	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	tools := []claude.Tool{{Name: "get_logs", Description: "Read recent logs"}}
	for _, req := range []claude.Request{
		{Messages: []claude.Message{userText("hi")}},
		{Messages: []claude.Message{userText("hi")}, Tools: tools},
		{Messages: []claude.Message{userText("hi")}, Tools: tools, NoToolCalls: true},
	} {
		_, err = client.Complete(context.Background(), req)
		require.NoError(t, err)
	}

	for _, request := range *sent {
		for _, parameter := range []string{"temperature", "top_p", "top_k", "thinking"} {
			assert.NotContains(t, request.Body, parameter)
		}
		if choice, ok := request.Body["tool_choice"]; ok {
			assert.Equal(t, map[string]any{"type": "none"}, choice, "only ruling tools out is allowed")
		}
		messages := request.Body["messages"].([]any)
		assert.Equal(t, "user", messages[len(messages)-1].(map[string]any)["role"], "the request ends on the user")
	}
}

func TestCompleteRefusesToPrefillTheAnswer(t *testing.T) {
	srv, sent := answeringServer(t, endTurn)
	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), claude.Request{Messages: []claude.Message{
		userText("hi"),
		{Role: "assistant", Content: []claude.ContentBlock{{Type: "text", Text: "{"}}},
	}})

	require.ErrorIs(t, err, claude.ErrInvalidRequest)
	assert.Empty(t, *sent, "the request never leaves the adapter")
}

func TestCompleteTurnsARefusalIntoACustomerSentence(t *testing.T) {
	srv, _ := answeringServer(t, `{
		"stop_reason": "refusal",
		"stop_details": {"type": "refusal", "category": "cyber", "explanation": "internal detail"},
		"content": [{"type": "text", "text": "partial output that must not be shown"}]
	}`)
	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	got, err := client.Complete(context.Background(), claude.Request{Messages: []claude.Message{userText("hi")}})

	require.NoError(t, err, "a refusal is an answer, not a failure")
	assert.Equal(t, claude.StopRefusal, got.StopReason)
	require.Len(t, got.Content, 1)
	assert.Equal(t, "text", got.Content[0].Type)
	assert.Equal(t, claude.RefusalSentence, got.Content[0].Text)
	assert.NotContains(t, claude.RefusalSentence, "\u2014")
	assert.NotContains(t, claude.RefusalSentence, "internal detail")
}

func TestCompleteReportsTokenUsage(t *testing.T) {
	srv, _ := answeringServer(t, `{
		"stop_reason": "end_turn",
		"content": [{"type": "text", "text": "ok"}],
		"usage": {"input_tokens": 12, "output_tokens": 30, "cache_creation_input_tokens": 900, "cache_read_input_tokens": 1800}
	}`)
	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	got, err := client.Complete(context.Background(), claude.Request{Messages: []claude.Message{userText("hi")}})
	require.NoError(t, err)

	assert.Equal(t, claude.Usage{InputTokens: 12, OutputTokens: 30, CacheCreationInputTokens: 900, CacheReadInputTokens: 1800}, got.Usage)
}

func TestDeprecatedNamesTheModelsAnthropicHasWithdrawn(t *testing.T) {
	for _, model := range []string{
		"claude-sonnet-4-20250514", "claude-sonnet-4-0", "claude-opus-4-20250514", "claude-opus-4-0",
		"claude-opus-4-1-20250805", "claude-3-haiku-20240307", "claude-3-7-sonnet-20250219",
	} {
		assert.True(t, claude.Deprecated(model), model)
	}
	for _, model := range []string{"claude-sonnet-5-5", "claude-sonnet-4-6", "claude-haiku-4-5", "claude-opus-5-5", "gpt"} {
		assert.False(t, claude.Deprecated(model), model)
	}
}
