package cursor_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/client/cursor"
)

func TestCompleteCreatesANoRepoAgentAndReturnsText(t *testing.T) {
	var createBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents":
			raw, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(raw, &createBody))
			assert.NotContains(t, createBody, "repos")
			assert.NotContains(t, createBody, "env")

			model, ok := createBody["model"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "composer-2", model["id"])

			prompt, ok := createBody["prompt"].(map[string]any)
			require.True(t, ok)
			text, _ := prompt["text"].(string)
			assert.Contains(t, text, "get_status")
			assert.Contains(t, text, "JSON")
			assert.Contains(t, text, "tool_calls")
			assert.Contains(t, text, "You cannot invoke tools yourself")

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"agent":{"id":"bc-1"},"run":{"id":"run-1","status":"FINISHED"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/bc-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"bc-1","latestRunId":"run-1","status":"IDLE"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/bc-1/runs/run-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"run-1","status":"FINISHED","result":"{\"text\":\"hello\",\"actions\":[]}"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := cursor.New(cursor.Options{
		BaseURL: srv.URL,
		APIKey:  "cursor-key-test",
		Model:   "composer-2",
		Sleep:   func(time.Duration) {},
	})
	require.NoError(t, err)

	got, err := client.Complete(context.Background(), claude.Request{
		System: "Reply with JSON only.",
		Model:  "composer-2",
		Messages: []claude.Message{{
			Role:    "user",
			Content: []claude.ContentBlock{{Type: "text", Text: "What is the status of orders-dev?"}},
		}},
		Tools: []claude.Tool{{
			Name:        "get_status",
			Description: "Read database status",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		}},
	})
	require.NoError(t, err)

	assert.Equal(t, "end_turn", got.StopReason)
	require.Len(t, got.Content, 1)
	assert.Equal(t, "text", got.Content[0].Type)
	assert.Contains(t, got.Content[0].Text, "hello")
}

func TestCompleteFollowUpSendsToolResults(t *testing.T) {
	var createCount atomic.Int32
	var followUpBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents":
			createCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"agent":{"id":"bc-1"},"run":{"id":"run-1","status":"FINISHED"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/bc-1/runs":
			raw, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(raw, &followUpBody))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"run":{"id":"run-2","status":"FINISHED"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/bc-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"bc-1","latestRunId":"run-2","status":"IDLE"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/agents/bc-1/runs/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"run-2","status":"FINISHED","result":"{\"tool_calls\":[{\"id\":\"2\",\"name\":\"get_metrics\",\"input\":{\"id\":\"pds.orders-dev\"}}]}"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := cursor.New(cursor.Options{
		BaseURL: srv.URL,
		APIKey:  "cursor-key-test",
		Model:   "composer-2",
		Sleep:   func(time.Duration) {},
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), claude.Request{
		System: "Reply with JSON only.",
		Messages: []claude.Message{{
			Role:    "user",
			Content: []claude.ContentBlock{{Type: "text", Text: "Show metrics"}},
		}},
	})
	require.NoError(t, err)

	got, err := client.Complete(context.Background(), claude.Request{
		Messages: []claude.Message{{
			Role: "user",
			Content: []claude.ContentBlock{{
				Type:      "tool_result",
				ToolUseID: "1",
				Content:   "status=ready",
			}},
		}},
	})
	require.NoError(t, err)

	assert.Equal(t, int32(1), createCount.Load())
	require.NotNil(t, followUpBody)
	prompt, ok := followUpBody["prompt"].(map[string]any)
	require.True(t, ok)
	text, _ := prompt["text"].(string)
	assert.Contains(t, text, "tool_results")
	assert.Contains(t, text, "tool_calls")
	assert.Contains(t, text, "You cannot invoke tools yourself")
	assert.Equal(t, "tool_use", got.StopReason)
	require.Len(t, got.Content, 1)
	assert.Equal(t, "tool_use", got.Content[0].Type)
	assert.Equal(t, "get_metrics", got.Content[0].Name)
}

func TestCompleteFollowUpSendsOnlyLatestToolResults(t *testing.T) {
	var followUpBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"agent":{"id":"bc-1"},"run":{"id":"run-1","status":"FINISHED"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/bc-1/runs":
			raw, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(raw, &followUpBody))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"run":{"id":"run-2","status":"FINISHED"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/bc-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"bc-1","latestRunId":"run-2","status":"IDLE"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/agents/bc-1/runs/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"run-2","status":"FINISHED","result":"{\"text\":\"done\",\"actions\":[]}"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := cursor.New(cursor.Options{
		BaseURL: srv.URL,
		APIKey:  "cursor-key-test",
		Model:   "composer-2",
		Sleep:   func(time.Duration) {},
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), claude.Request{
		Messages: []claude.Message{{
			Role:    "user",
			Content: []claude.ContentBlock{{Type: "text", Text: "start"}},
		}},
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), claude.Request{
		Messages: []claude.Message{
			{
				Role: "user",
				Content: []claude.ContentBlock{{
					Type:      "tool_result",
					ToolUseID: "old-round",
					Content:   "stale status",
				}},
			},
			{
				Role: "assistant",
				Content: []claude.ContentBlock{{
					Type: "tool_use",
					ID:   "old-round",
					Name: "get_status",
				}},
			},
			{
				Role: "user",
				Content: []claude.ContentBlock{{
					Type:      "tool_result",
					ToolUseID: "new-round",
					Content:   "fresh status",
				}},
			},
		},
	})
	require.NoError(t, err)

	require.NotNil(t, followUpBody)
	prompt, ok := followUpBody["prompt"].(map[string]any)
	require.True(t, ok)
	text, _ := prompt["text"].(string)
	assert.Contains(t, text, "new-round")
	assert.Contains(t, text, "fresh status")
	assert.NotContains(t, text, "old-round")
	assert.NotContains(t, text, "stale status")

	lines := strings.SplitN(text, "\n", 2)
	require.NotEmpty(t, lines)
	var payload struct {
		ToolResults []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		} `json:"tool_results"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &payload))
	require.Len(t, payload.ToolResults, 1)
	assert.Equal(t, "new-round", payload.ToolResults[0].ID)
	assert.Equal(t, "fresh status", payload.ToolResults[0].Content)
}

func TestCompleteTimesOutDuringPolling(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"agent":{"id":"bc-1"},"run":{"id":"run-1","status":"RUNNING"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/bc-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"bc-1","latestRunId":"run-1","status":"RUNNING"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/bc-1/runs/run-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"run-1","status":"RUNNING"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := cursor.New(cursor.Options{
		BaseURL: srv.URL,
		APIKey:  "cursor-key-test",
		Model:   "composer-2",
		Timeout: 50 * time.Millisecond,
		Sleep:   func(time.Duration) {},
	})
	require.NoError(t, err)

	start := time.Now()
	_, err = client.Complete(context.Background(), claude.Request{
		Messages: []claude.Message{{
			Role:    "user",
			Content: []claude.ContentBlock{{Type: "text", Text: "hi"}},
		}},
	})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, 500*time.Millisecond)
}

func TestCompleteUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	client, err := cursor.New(cursor.Options{
		BaseURL: srv.URL,
		APIKey:  "bad-key",
		Model:   "composer-2",
		Sleep:   func(time.Duration) {},
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), claude.Request{
		Messages: []claude.Message{{
			Role:    "user",
			Content: []claude.ContentBlock{{Type: "text", Text: "hi"}},
		}},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, cursor.ErrUnauthorized)
}

func TestNewRequiresAKey(t *testing.T) {
	_, err := cursor.New(cursor.Options{BaseURL: cursor.DefaultBaseURL})
	require.Error(t, err)
}

func TestCompleteFollowUpAsksForAnAnswerWhenToolCallsAreOff(t *testing.T) {
	var followUpText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents":
			_, _ = w.Write([]byte(`{"agent":{"id":"bc-1"},"run":{"id":"run-1","status":"FINISHED"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/bc-1/runs":
			var body struct {
				Prompt struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			raw, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(raw, &body))
			followUpText = body.Prompt.Text
			_, _ = w.Write([]byte(`{"run":{"id":"run-2","status":"FINISHED"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/bc-1":
			_, _ = w.Write([]byte(`{"id":"bc-1","latestRunId":"run-2","status":"IDLE"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/agents/bc-1/runs/"):
			_, _ = w.Write([]byte(`{"id":"run-2","status":"FINISHED","result":"{\"text\":\"done\",\"actions\":[]}"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := cursor.New(cursor.Options{BaseURL: srv.URL, APIKey: "cursor-key-test", Model: "composer-2", Sleep: func(time.Duration) {}})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), claude.Request{
		Messages: []claude.Message{{Role: "user", Content: []claude.ContentBlock{{Type: "text", Text: "Show metrics"}}}},
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), claude.Request{
		Messages: []claude.Message{{
			Role:    "user",
			Content: []claude.ContentBlock{{Type: "tool_result", ToolUseID: "1", Content: "status=ready"}},
		}},
		NoToolCalls: true,
	})
	require.NoError(t, err)

	assert.Contains(t, followUpText, "tool_results")
	assert.Contains(t, followUpText, "No more tools")
	assert.NotContains(t, followUpText, "Reply with more")
}
