package claude_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/claude"
)

// sseServer replies to one request with the given events, and records the
// request body so a test can read what was asked for.
func sseServer(t *testing.T, events []string, body *map[string]any) *claude.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body != nil {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			name := event[:strings.Index(event, "|")]
			data := event[strings.Index(event, "|")+1:]
			_, _ = w.Write([]byte("event: " + name + "\ndata: " + data + "\n\n"))
		}
	}))
	t.Cleanup(srv.Close)

	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	return client
}

func streamRequest(onText func(string)) claude.Request {
	return claude.Request{
		System: "You help.",
		Messages: []claude.Message{{
			Role:    "user",
			Content: []claude.ContentBlock{{Type: "text", Text: "hi"}},
		}},
		OnText: onText,
	}
}

const messageStart = `message_start|{"type":"message_start","message":{"id":"msg_1","role":"assistant","content":[],"usage":{"input_tokens":120,"cache_creation_input_tokens":0,"cache_read_input_tokens":900,"output_tokens":1}}}`

func TestCompleteStreamsWhenAskedForTextAndHearsEachDelta(t *testing.T) {
	var body map[string]any
	client := sseServer(t, []string{
		messageStart,
		`ping|{"type":"ping"}`,
		`content_block_start|{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`content_block_delta|{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Orders-dev "}}`,
		`content_block_delta|{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"is healthy."}}`,
		`content_block_stop|{"type":"content_block_stop","index":0}`,
		`message_delta|{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":14}}`,
		`message_stop|{"type":"message_stop"}`,
	}, &body)

	var heard []string
	got, err := client.Complete(context.Background(), streamRequest(func(delta string) { heard = append(heard, delta) }))
	require.NoError(t, err)

	assert.Equal(t, true, body["stream"])
	assert.Equal(t, []string{"Orders-dev ", "is healthy."}, heard)
	assert.Equal(t, "end_turn", got.StopReason)
	require.Len(t, got.Content, 1)
	assert.Equal(t, "Orders-dev is healthy.", got.Content[0].Text)
	assert.Equal(t, claude.Usage{InputTokens: 120, OutputTokens: 14, CacheReadInputTokens: 900}, got.Usage)
}

func TestCompleteDoesNotStreamWithoutAnOnTextCallback(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}`))
	}))
	t.Cleanup(srv.Close)
	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "k"})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), streamRequest(nil))
	require.NoError(t, err)
	assert.NotContains(t, body, "stream")
}

func TestStreamRebuildsEachBlockSoItGoesBackUnchanged(t *testing.T) {
	client := sseServer(t, []string{
		messageStart,
		`content_block_start|{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`content_block_delta|{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Two tools."}}`,
		`content_block_delta|{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-abc=="}}`,
		`content_block_stop|{"type":"content_block_stop","index":0}`,
		`content_block_start|{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`content_block_delta|{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Checking."}}`,
		`content_block_stop|{"type":"content_block_stop","index":1}`,
		`content_block_start|{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_status","input":{}}}`,
		`content_block_delta|{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":""}}`,
		`content_block_delta|{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"id\":\"pds."}}`,
		`content_block_delta|{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"orders-dev\"}"}}`,
		`content_block_stop|{"type":"content_block_stop","index":2}`,
		`content_block_start|{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_2","name":"list_tickets","input":{}}}`,
		`content_block_stop|{"type":"content_block_stop","index":3}`,
		`message_delta|{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":40}}`,
		`message_stop|{"type":"message_stop"}`,
	}, nil)

	got, err := client.Complete(context.Background(), streamRequest(func(string) {}))
	require.NoError(t, err)

	assert.Equal(t, "tool_use", got.StopReason)
	require.Len(t, got.Content, 4)

	thinking := got.Content[0]
	assert.Equal(t, "thinking", thinking.Type)
	assert.JSONEq(t, `{"type":"thinking","thinking":"Two tools.","signature":"sig-abc=="}`, string(thinking.Raw))

	assert.JSONEq(t, `{"type":"text","text":"Checking."}`, string(got.Content[1].Raw))

	call := got.Content[2]
	assert.Equal(t, "get_status", call.Name)
	assert.Equal(t, "toolu_1", call.ID)
	assert.JSONEq(t, `{"id":"pds.orders-dev"}`, string(call.Input))
	assert.JSONEq(t, `{"type":"tool_use","id":"toolu_1","name":"get_status","input":{"id":"pds.orders-dev"}}`, string(call.Raw))

	assert.JSONEq(t, `{}`, string(got.Content[3].Input), "a call with no input arguments keeps an empty object")
}

func TestStreamedRefusalDropsWhatWasWrittenBeforeIt(t *testing.T) {
	client := sseServer(t, []string{
		messageStart,
		`content_block_start|{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`content_block_delta|{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Here is how to"}}`,
		`message_delta|{"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":5}}`,
		`message_stop|{"type":"message_stop"}`,
	}, nil)

	got, err := client.Complete(context.Background(), streamRequest(func(string) {}))
	require.NoError(t, err)

	assert.Equal(t, claude.StopRefusal, got.StopReason)
	require.Len(t, got.Content, 1)
	assert.Equal(t, claude.RefusalSentence, got.Content[0].Text)
}

func TestStreamAFallbackKeepsTheTextAndDropsTheBlocksBeforeIt(t *testing.T) {
	client := sseServer(t, []string{
		messageStart,
		`content_block_start|{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`content_block_delta|{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"old"}}`,
		`content_block_stop|{"type":"content_block_stop","index":0}`,
		`content_block_start|{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`content_block_delta|{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Partial "}}`,
		`content_block_stop|{"type":"content_block_stop","index":1}`,
		`content_block_start|{"type":"content_block_start","index":2,"content_block":{"type":"fallback","from":{"model":"claude-sonnet-5-5"},"to":{"model":"claude-opus-5-5"}}}`,
		`content_block_stop|{"type":"content_block_stop","index":2}`,
		`content_block_start|{"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`,
		`content_block_delta|{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"answer."}}`,
		`content_block_stop|{"type":"content_block_stop","index":3}`,
		`message_delta|{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
		`message_stop|{"type":"message_stop"}`,
	}, nil)

	got, err := client.Complete(context.Background(), streamRequest(func(string) {}))
	require.NoError(t, err)

	var types []string
	for _, block := range got.Content {
		types = append(types, block.Type)
	}
	assert.Equal(t, []string{"text", "text"}, types)
	assert.Equal(t, "Partial ", got.Content[0].Text)
	assert.Equal(t, "answer.", got.Content[1].Text)
}

func TestStreamAnErrorEventIsRejected(t *testing.T) {
	client := sseServer(t, []string{
		messageStart,
		`error|{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
	}, nil)

	_, err := client.Complete(context.Background(), streamRequest(func(string) {}))
	require.ErrorIs(t, err, claude.ErrRejected)
	assert.Contains(t, err.Error(), "overloaded_error")
}

func TestStreamThatEndsBeforeMessageStopIsAnError(t *testing.T) {
	client := sseServer(t, []string{
		messageStart,
		`content_block_start|{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`content_block_delta|{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Half an ans"}}`,
	}, nil)

	_, err := client.Complete(context.Background(), streamRequest(func(string) {}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ended before")
}

func TestStreamMaps401BeforeAnyEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid x-api-key"}}`))
	}))
	t.Cleanup(srv.Close)
	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "bad"})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), streamRequest(func(string) {}))
	require.ErrorIs(t, err, claude.ErrUnauthorized)
}

func TestStreamDeliversTextBeforeTheModelHasFinished(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			w.(http.Flusher).Flush()
		}
		write("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		write("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"First words.\"}}\n\n")
		<-release
		write("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		write("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n")
		write("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	client, err := claude.New(claude.Options{BaseURL: srv.URL, APIKey: "k"})
	require.NoError(t, err)

	first := make(chan string, 1)
	var once sync.Once
	done := make(chan error, 1)
	go func() {
		_, err := client.Complete(context.Background(), streamRequest(func(delta string) {
			once.Do(func() { first <- delta })
		}))
		done <- err
	}()

	assert.Equal(t, "First words.", <-first)
	select {
	case <-done:
		t.Fatal("Complete returned before the model finished")
	default:
	}
	close(release)
	require.NoError(t, <-done)
}

func TestStreamedToolCallCutOffByTheTokenLimitKeepsAnEmptyInput(t *testing.T) {
	client := sseServer(t, []string{
		messageStart,
		`content_block_start|{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_status","input":{}}}`,
		`content_block_delta|{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"id\":\"pds.or"}}`,
		`content_block_stop|{"type":"content_block_stop","index":0}`,
		`message_delta|{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":4096}}`,
		`message_stop|{"type":"message_stop"}`,
	}, nil)

	got, err := client.Complete(context.Background(), streamRequest(func(string) {}))
	require.NoError(t, err)

	assert.Equal(t, "max_tokens", got.StopReason)
	require.Len(t, got.Content, 1)
	assert.JSONEq(t, `{}`, string(got.Content[0].Input))
	assert.JSONEq(t, `{"type":"tool_use","id":"toolu_1","name":"get_status","input":{}}`, string(got.Content[0].Raw))
}
