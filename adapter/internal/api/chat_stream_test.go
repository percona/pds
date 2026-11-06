package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
)

const chatBody = `{"provider":"anthropic","model":"m","message":"how is orders-dev?"}`

// gatedCompleter answers each Complete call only once a value arrives on
// gate, so a test controls exactly when the model finishes.
type gatedCompleter struct {
	gate    chan struct{}
	replies []claude.Response
	err     error
}

func (g *gatedCompleter) Complete(ctx context.Context, _ claude.Request) (*claude.Response, error) {
	select {
	case <-g.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if g.err != nil {
		return nil, g.err
	}
	reply := g.replies[0]
	g.replies = g.replies[1:]

	return &reply, nil
}

type sseEvent struct {
	name string
	data string
}

// sseReader reads one event, or one comment line, at a time from a live stream.
type sseReader struct {
	scanner *bufio.Scanner
}

func (s *sseReader) next(t *testing.T, within time.Duration) sseEvent {
	t.Helper()

	got := make(chan sseEvent, 1)
	go func() {
		var event sseEvent
		for s.scanner.Scan() {
			line := s.scanner.Text()
			switch {
			case line == "":
				if event.name != "" || event.data != "" {
					got <- event

					return
				}
			case strings.HasPrefix(line, ":"):
				got <- sseEvent{name: "comment", data: strings.TrimSpace(line[1:])}

				return
			case strings.HasPrefix(line, "event: "):
				event.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				event.data = strings.TrimPrefix(line, "data: ")
			}
		}
		got <- sseEvent{name: "eof"}
	}()

	select {
	case event := <-got:
		return event
	case <-time.After(within):
		t.Fatalf("no event within %s", within)

		return sseEvent{}
	}
}

func openChatStream(t *testing.T, completer claude.Completer, heartbeat time.Duration) (*http.Response, *sseReader) {
	t.Helper()

	srv := api.NewServer(api.Deps{
		Config:        config.Config{Addr: ":8080", Env: config.EnvDev, EverestNamespace: "pds"},
		Logger:        log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:          auth.NewStub(),
		Chat:          service.NewChat(nil, nil, service.WithChatCompleter(completer), service.WithChatStore(service.NewMemoryChats(fixedTeam(1)).Stores())),
		ChatHeartbeat: heartbeat,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+chatPath, strings.NewReader(chatBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	stream := &sseReader{scanner: bufio.NewScanner(resp.Body)}
	// The message is saved before the model is asked, and the stream names
	// the conversation that holds it before anything else.
	started := stream.next(t, 3*time.Second)
	require.Equal(t, "conversation", started.name)
	assert.Contains(t, started.data, `"conversationId":"`)

	return resp, stream
}

func TestChatStreamSendsProgressBeforeASlowModelAnswers(t *testing.T) {
	completer := &gatedCompleter{gate: make(chan struct{}), replies: []claude.Response{{
		StopReason: "end_turn",
		Content: []claude.ContentBlock{{
			Type: "text", Text: `orders-dev is healthy.<actions>[{"label":"Contact support","kind":"support"}]</actions>`,
		}},
	}}}

	resp, stream := openChatStream(t, completer, time.Hour)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no", resp.Header.Get("X-Accel-Buffering"))

	first := stream.next(t, 3*time.Second)
	assert.Equal(t, "progress", first.name)
	assert.JSONEq(t, `{"label":"Thinking"}`, first.data)

	completer.gate <- struct{}{}
	// A client that does not stream hands over the whole answer at once.
	text := stream.next(t, 3*time.Second)
	require.Equal(t, "text", text.name)
	assert.JSONEq(t, `{"delta":"orders-dev is healthy."}`, text.data)
	reply := stream.next(t, 3*time.Second)
	require.Equal(t, "reply", reply.name)
	var decoded struct {
		Text    string               `json:"text"`
		Actions []service.ChatAction `json:"actions"`
	}
	require.NoError(t, json.Unmarshal([]byte(reply.data), &decoded))
	assert.Equal(t, "orders-dev is healthy.", decoded.Text)
	require.Len(t, decoded.Actions, 1)
	assert.Equal(t, "eof", stream.next(t, 3*time.Second).name)
}

func TestChatStreamKeepsASlowTurnAlive(t *testing.T) {
	completer := &gatedCompleter{gate: make(chan struct{}), replies: []claude.Response{{
		StopReason: "end_turn",
		Content:    []claude.ContentBlock{{Type: "text", Text: "done"}},
	}}}

	_, stream := openChatStream(t, completer, 20*time.Millisecond)
	require.Equal(t, "progress", stream.next(t, 3*time.Second).name)

	// A proxy gives up on a connection that carries no bytes. While the model
	// is still working the stream keeps sending comments.
	for range 3 {
		event := stream.next(t, time.Second)
		require.Equal(t, "comment", event.name)
		assert.Equal(t, "ping", event.data)
	}

	completer.gate <- struct{}{}
	for {
		event := stream.next(t, 3*time.Second)
		if event.name == "comment" || event.name == "text" {
			continue
		}
		assert.Equal(t, "reply", event.name)

		break
	}
}

func TestChatStreamSendsProgressForEachToolRound(t *testing.T) {
	completer := &gatedCompleter{gate: make(chan struct{}), replies: []claude.Response{
		{StopReason: "tool_use", Content: []claude.ContentBlock{{
			Type: "tool_use", ID: "1", Name: "list_databases", Input: json.RawMessage(`{}`),
		}}},
		{StopReason: "tool_use", Content: []claude.ContentBlock{{
			Type: "tool_use", ID: "2", Name: "get_metrics", Input: json.RawMessage(`{"id":"pds.db"}`),
		}}},
		{StopReason: "end_turn", Content: []claude.ContentBlock{{
			Type: "text", Text: "all good",
		}}},
	}}

	_, stream := openChatStream(t, completer, time.Hour)

	var labels []string
	expect := func() {
		event := stream.next(t, 3*time.Second)
		require.Equal(t, "progress", event.name)
		var progress service.ChatProgress
		require.NoError(t, json.Unmarshal([]byte(event.data), &progress))
		labels = append(labels, progress.Label)
	}

	expect()
	completer.gate <- struct{}{}
	expect()
	expect()
	completer.gate <- struct{}{}
	expect()
	expect()
	completer.gate <- struct{}{}

	assert.Equal(t, []string{"Thinking", "Listing databases", "Thinking", "Reading metrics", "Thinking"}, labels)
	assert.Equal(t, "text", stream.next(t, 3*time.Second).name)
	assert.Equal(t, "reply", stream.next(t, 3*time.Second).name)
}

func TestChatStreamSendsAnErrorEventWhenTheModelFails(t *testing.T) {
	completer := &gatedCompleter{gate: make(chan struct{}), err: errors.New("upstream reset")}

	_, stream := openChatStream(t, completer, time.Hour)
	require.Equal(t, "progress", stream.next(t, 3*time.Second).name)

	completer.gate <- struct{}{}
	event := stream.next(t, 3*time.Second)
	require.Equal(t, "error", event.name)
	assert.JSONEq(t, `{"code":"ErrDownstream","message":"The assistant did not answer. Try again in a moment."}`, event.data)
	assert.NotContains(t, event.data, "upstream reset")
}

func TestChatStreamAnswersAPlainErrorBeforeTheStreamStarts(t *testing.T) {
	conns := service.NewConnections(&memStore{}, service.ConnectionsOptions{
		Namespace:           "pds",
		AssistantSecretName: "pds-assistant",
	})
	srv := newChatServer(t, conns, nil)

	req := httptest.NewRequest(http.MethodPost, chatPath, strings.NewReader(chatBody))
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	assert.Equal(t, "ErrNotImplemented", decodeBody(t, rec)["code"])
}

// writingCompleter streams a scripted answer through the request's OnText,
// holding back after the first piece until gate delivers a value.
type writingCompleter struct {
	gate   chan struct{}
	pieces []string
	reply  string
}

func (w *writingCompleter) Complete(ctx context.Context, req claude.Request) (*claude.Response, error) {
	for i, piece := range w.pieces {
		if i == 1 {
			select {
			case <-w.gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if req.OnText != nil {
			req.OnText(piece)
		}
	}

	return &claude.Response{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: w.reply}}}, nil
}

func TestChatStreamSendsTheTextWhileTheModelIsStillWriting(t *testing.T) {
	completer := &writingCompleter{
		gate:   make(chan struct{}),
		pieces: []string{"orders-dev is ", "healthy.\n<actions>[{\"label\":\"Contact support\",\"kind\":\"support\"}]</actions>"},
		reply:  "orders-dev is healthy.\n<actions>[{\"label\":\"Contact support\",\"kind\":\"support\"}]</actions>",
	}

	_, stream := openChatStream(t, completer, time.Hour)
	require.Equal(t, "progress", stream.next(t, 3*time.Second).name)

	// The model has written one piece and is waiting; the person already has it.
	first := stream.next(t, 3*time.Second)
	require.Equal(t, "text", first.name)
	assert.JSONEq(t, `{"delta":"orders-dev is"}`, first.data)

	completer.gate <- struct{}{}
	var text strings.Builder
	text.WriteString("orders-dev is")
	for {
		event := stream.next(t, 3*time.Second)
		if event.name == "text" {
			var piece struct {
				Delta string `json:"delta"`
			}
			require.NoError(t, json.Unmarshal([]byte(event.data), &piece))
			text.WriteString(piece.Delta)

			continue
		}
		require.Equal(t, "reply", event.name)
		var decoded struct {
			Text    string               `json:"text"`
			Actions []service.ChatAction `json:"actions"`
		}
		require.NoError(t, json.Unmarshal([]byte(event.data), &decoded))
		assert.Equal(t, decoded.Text, text.String(), "the pieces add up to the reply")
		require.Len(t, decoded.Actions, 1)

		break
	}
}

func TestChatAnswersWholeAsJSONToAClientThatDoesNotAskForAnEventStream(t *testing.T) {
	completer := &writingCompleter{
		gate:   make(chan struct{}),
		pieces: []string{"orders-dev is healthy."},
		reply:  "orders-dev is healthy.",
	}

	rec := do(t, newChatServer(t, nil, completer), http.MethodPost, chatPath, strings.NewReader(chatBody))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	assert.NotContains(t, rec.Body.String(), "event:")
	decoded := decodeBody(t, rec)
	assert.Equal(t, "orders-dev is healthy.", decoded["text"])
	assert.Equal(t, []any{}, decoded["actions"])
}

func TestChatStreamCarriesTheModelsFirstWordsBeforeTheModelHasFinished(t *testing.T) {
	released := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sent map[string]any
		_ = json.NewDecoder(r.Body).Decode(&sent)
		assert.Equal(t, true, sent["stream"])
		w.Header().Set("Content-Type", "text/event-stream")
		write := func(event, data string) {
			_, _ = io.WriteString(w, "event: "+event+"\ndata: "+data+"\n\n")
			w.(http.Flusher).Flush()
		}
		write("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":10}}}`)
		write("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"orders-dev is "}}`)
		write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ready"}}`)
		<-released
		write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" now."}}`)
		write("content_block_stop", `{"type":"content_block_stop","index":0}`)
		write("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":6}}`)
		write("message_stop", `{"type":"message_stop"}`)
	}))
	t.Cleanup(model.Close)
	client, err := claude.New(claude.Options{BaseURL: model.URL, APIKey: "sk-ant-test"})
	require.NoError(t, err)

	_, stream := openChatStream(t, client, time.Hour)
	require.Equal(t, "progress", stream.next(t, 3*time.Second).name)

	first := stream.next(t, 3*time.Second)
	require.Equal(t, "text", first.name)
	assert.JSONEq(t, `{"delta":"orders-dev is"}`, first.data)

	// The model has not finished, and the person already has its first words.
	close(released)
	var text strings.Builder
	text.WriteString("orders-dev is")
	for {
		event := stream.next(t, 3*time.Second)
		if event.name == "text" {
			var piece struct {
				Delta string `json:"delta"`
			}
			require.NoError(t, json.Unmarshal([]byte(event.data), &piece))
			text.WriteString(piece.Delta)

			continue
		}
		require.Equal(t, "reply", event.name)
		assert.JSONEq(t, `{"conversationId":"`+conversationID(t, event.data)+`","text":"orders-dev is ready now.","actions":[]}`, event.data)

		break
	}
	assert.Equal(t, "orders-dev is ready now.", text.String())
}

func conversationID(t *testing.T, replyData string) string {
	t.Helper()

	var reply struct {
		ConversationID string `json:"conversationId"`
	}
	require.NoError(t, json.Unmarshal([]byte(replyData), &reply))

	return reply.ConversationID
}
