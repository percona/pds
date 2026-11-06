package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/percona/pds/adapter/internal/store"
)

const conversationsPath = "/api/chat/conversations"

func newConversationServer(t *testing.T, completer claude.Completer, stores service.ChatStores) *api.Server {
	t.Helper()

	return api.NewServer(api.Deps{
		Config: config.Config{Addr: ":8080", Env: config.EnvDev, EverestNamespace: "pds"},
		Logger: log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:   auth.NewStub(),
		Chat:   service.NewChat(nil, nil, service.WithChatCompleter(completer), service.WithChatStore(stores)),
	})
}

func answer(text string) claude.Response {
	return claude.Response{StopReason: "end_turn", Content: []claude.ContentBlock{{
		Type: "text", Text: text,
	}}}
}

type chatTurnBody struct {
	ConversationID string `json:"conversationId"`
	Text           string `json:"text"`
}

func sendTurn(t *testing.T, srv http.Handler, body string) chatTurnBody {
	t.Helper()

	rec := do(t, srv, http.MethodPost, chatPath, strings.NewReader(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out chatTurnBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))

	return out
}

type threadBody struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	DatabaseID    string `json:"databaseId"`
	ContextWindow int    `json:"contextWindow"`
	Messages      []struct {
		Role    string               `json:"role"`
		Content string               `json:"content"`
		Actions []service.ChatAction `json:"actions"`
	} `json:"messages"`
}

func readThread(t *testing.T, srv http.Handler, id string) threadBody {
	t.Helper()

	rec := do(t, srv, http.MethodGet, conversationsPath+"/"+id, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out threadBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))

	return out
}

func TestChatFirstMessageCreatesAConversation(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{{
		StopReason: "end_turn",
		Content: []claude.ContentBlock{{
			Type: "text", Text: `Backups look fine.<actions>[{"label":"Contact support","kind":"support"}]</actions>`,
		}},
	}}}
	srv := newConversationServer(t, completer, service.NewMemoryChats(fixedTeam(1)).Stores())

	turn := sendTurn(t, srv, `{"provider":"anthropic","model":"m","databaseId":"pds.orders-dev","message":"Are my backups healthy?"}`)
	require.NotEmpty(t, turn.ConversationID)
	assert.Equal(t, "Backups look fine.", turn.Text)

	rec := do(t, srv, http.MethodGet, conversationsPath, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var list struct {
		Conversations []struct {
			ID         string `json:"id"`
			Title      string `json:"title"`
			Provider   string `json:"provider"`
			Model      string `json:"model"`
			DatabaseID string `json:"databaseId"`
			UpdatedAt  string `json:"updatedAt"`
		} `json:"conversations"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Conversations, 1)
	assert.Equal(t, turn.ConversationID, list.Conversations[0].ID)
	assert.Equal(t, "Are my backups healthy?", list.Conversations[0].Title)
	assert.Equal(t, "anthropic", list.Conversations[0].Provider)
	assert.Equal(t, "m", list.Conversations[0].Model)
	assert.Equal(t, "pds.orders-dev", list.Conversations[0].DatabaseID)
	assert.NotEmpty(t, list.Conversations[0].UpdatedAt)

	thread := readThread(t, srv, turn.ConversationID)
	assert.Equal(t, 32, thread.ContextWindow)
	require.Len(t, thread.Messages, 2)
	assert.Equal(t, "Are my backups healthy?", thread.Messages[0].Content)
	assert.Empty(t, thread.Messages[0].Actions)
	require.Len(t, thread.Messages[1].Actions, 1, "the saved reply keeps its buttons")
	assert.Equal(t, "support", thread.Messages[1].Actions[0].Kind)
}

func TestChatContinuesAConversation(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{answer("one"), answer("two")}}
	srv := newConversationServer(t, completer, service.NewMemoryChats(fixedTeam(1)).Stores())

	first := sendTurn(t, srv, `{"provider":"anthropic","model":"m","message":"first"}`)
	second := sendTurn(t, srv, `{"conversationId":"`+first.ConversationID+`","provider":"anthropic","model":"m","message":"second"}`)
	assert.Equal(t, first.ConversationID, second.ConversationID)

	sent := completer.calls[1].Messages
	require.Len(t, sent, 3, "the thread is read from the store, not the request")
	assert.Equal(t, "one", sent[1].Content[0].Text)

	thread := readThread(t, srv, first.ConversationID)
	require.Len(t, thread.Messages, 4)
	assert.Equal(t, "two", thread.Messages[3].Content)
}

func TestChatRefusesAProviderMismatch(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{answer("one")}}
	srv := newConversationServer(t, completer, service.NewMemoryChats(fixedTeam(1)).Stores())
	first := sendTurn(t, srv, `{"provider":"anthropic","model":"m","message":"first"}`)

	rec := do(t, srv, http.MethodPost, chatPath, strings.NewReader(
		`{"conversationId":"`+first.ConversationID+`","provider":"cursor","model":"m","message":"second"}`))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, "ErrValidation", body["code"])
	assert.Equal(t, "This conversation uses another provider or model. Start a new chat to switch.", body["message"])
}

func TestChatUnknownConversationIsNotFound(t *testing.T) {
	srv := newConversationServer(t, &scriptedCompleter{}, service.NewMemoryChats(fixedTeam(1)).Stores())

	for _, rec := range []*httptest.ResponseRecorder{
		do(t, srv, http.MethodPost, chatPath, strings.NewReader(`{"conversationId":"nope","provider":"anthropic","model":"m","message":"hi"}`)),
		do(t, srv, http.MethodGet, conversationsPath+"/nope", nil),
		do(t, srv, http.MethodDelete, conversationsPath+"/nope", nil),
	} {
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, "ErrNotFound", decodeBody(t, rec)["code"])
	}
}

func TestChatKeepsWorkingPast32Messages(t *testing.T) {
	var replies []claude.Response
	for i := range 18 {
		replies = append(replies, answer(fmt.Sprintf("answer %d", i)))
	}
	completer := &scriptedCompleter{replies: replies}
	srv := newConversationServer(t, completer, service.NewMemoryChats(fixedTeam(1)).Stores())

	id := sendTurn(t, srv, `{"provider":"anthropic","model":"m","message":"question 0"}`).ConversationID
	for i := 1; i < 18; i++ {
		sendTurn(t, srv, fmt.Sprintf(`{"conversationId":%q,"provider":"anthropic","model":"m","message":"question %d"}`, id, i))
	}

	thread := readThread(t, srv, id)
	assert.Len(t, thread.Messages, 36, "every message is stored and shown")
	assert.Equal(t, 32, thread.ContextWindow)
	assert.LessOrEqual(t, len(completer.calls[len(completer.calls)-1].Messages), 32,
		"the model is sent the newest 32 at most")
}

func TestChatDeleteRemovesTheConversation(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{answer("one")}}
	srv := newConversationServer(t, completer, service.NewMemoryChats(fixedTeam(1)).Stores())
	id := sendTurn(t, srv, `{"provider":"anthropic","model":"m","message":"first"}`).ConversationID

	rec := do(t, srv, http.MethodDelete, conversationsPath+"/"+id, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)

	assert.Equal(t, http.StatusNotFound, do(t, srv, http.MethodGet, conversationsPath+"/"+id, nil).Code)
}

func TestChatListRefusesAForgedCursorAndLimit(t *testing.T) {
	srv := newConversationServer(t, &scriptedCompleter{}, service.NewMemoryChats(fixedTeam(1)).Stores())

	for _, target := range []string{conversationsPath + "?cursor=forged", conversationsPath + "?limit=zero"} {
		rec := do(t, srv, http.MethodGet, target, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code, target)
	}
}

func TestChatAnswers503BeforeTheDatabaseConnects(t *testing.T) {
	srv := newConversationServer(t, &scriptedCompleter{},
		service.TeamChatStores(store.NewLazy(nil, 1, nil), fixedTeam(1)))

	for _, rec := range []*httptest.ResponseRecorder{
		do(t, srv, http.MethodPost, chatPath, strings.NewReader(`{"provider":"anthropic","model":"m","message":"hi"}`)),
		do(t, srv, http.MethodGet, conversationsPath, nil),
		do(t, srv, http.MethodGet, conversationsPath+"/abc", nil),
		do(t, srv, http.MethodDelete, conversationsPath+"/abc", nil),
	} {
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
		body := decodeBody(t, rec)
		assert.Equal(t, "Saved chats are not available yet. Try again in a moment.", body["message"])
		assert.NotContains(t, rec.Body.String(), "adapter", "the cause is logged, not sent")
	}
}

// brokenChats is a connected store whose every read and write fails.
type brokenChats struct{}

var errBrokenStore = errors.New("pq: relation hub_chat_messages is locked by pid 4242")

func (brokenChats) ChatConversation(context.Context, string) (store.ChatConversation, error) {
	return store.ChatConversation{}, errBrokenStore
}

func (brokenChats) StartChatConversation(context.Context, store.ChatConversation, store.ChatMessage) (store.ChatConversation, store.ChatMessage, error) {
	return store.ChatConversation{}, store.ChatMessage{}, errBrokenStore
}

func (brokenChats) ChatConversationByRequestKey(context.Context, string) (store.ChatConversation, error) {
	return store.ChatConversation{}, errBrokenStore
}

func (brokenChats) ListChatConversations(context.Context, string, int) (store.ChatConversationPage, error) {
	return store.ChatConversationPage{}, errBrokenStore
}

func (brokenChats) ChatMessages(context.Context, string) ([]store.ChatMessage, error) {
	return nil, errBrokenStore
}

func (brokenChats) AppendChatMessage(context.Context, string, store.ChatMessage) (store.ChatMessage, error) {
	return store.ChatMessage{}, errBrokenStore
}

func (brokenChats) FirstChatMessages(context.Context, string, int) ([]store.ChatMessage, error) {
	return nil, errBrokenStore
}

func (brokenChats) RecentChatMessages(context.Context, string, int) ([]store.ChatMessage, error) {
	return nil, errBrokenStore
}

func (brokenChats) AppendChatQuestion(context.Context, string, store.ChatMessage, time.Time) (store.ChatMessage, error) {
	return store.ChatMessage{}, errBrokenStore
}

func (brokenChats) DeleteChatMessage(context.Context, string, store.ChatMessage) error {
	return errBrokenStore
}

func (brokenChats) DeleteChatConversation(context.Context, string) error { return errBrokenStore }

func (brokenChats) SetChatAgent(context.Context, string, string) error { return errBrokenStore }

func TestChatReportsAFailingStoreAsSuch(t *testing.T) {
	srv := newConversationServer(t, &scriptedCompleter{},
		func(context.Context) service.ChatStore { return brokenChats{} })

	for _, rec := range []*httptest.ResponseRecorder{
		do(t, srv, http.MethodPost, chatPath, strings.NewReader(`{"provider":"anthropic","model":"m","message":"hi"}`)),
		do(t, srv, http.MethodGet, conversationsPath, nil),
		do(t, srv, http.MethodDelete, conversationsPath+"/abc", nil),
	} {
		require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
		assert.Equal(t, "Saved chats are not working right now. Try again in a moment.", decodeBody(t, rec)["message"],
			"a database failure is not reported as the assistant not answering")
		assert.NotContains(t, rec.Body.String(), "pq:")
	}
}

func TestChatSavesTheReplyWhenTheBrowserLeavesMidStream(t *testing.T) {
	completer := &gatedCompleter{gate: make(chan struct{}), replies: []claude.Response{answer("saved after you left")}}
	srv := newConversationServer(t, completer, service.NewMemoryChats(fixedTeam(1)).Stores())
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+chatPath,
		strings.NewReader(`{"provider":"anthropic","model":"m","message":"slow question"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	stream := &sseReader{scanner: bufio.NewScanner(resp.Body)}
	started := stream.next(t, 3*time.Second)
	require.Equal(t, "conversation", started.name)
	var event struct {
		ConversationID string `json:"conversationId"`
	}
	require.NoError(t, json.Unmarshal([]byte(started.data), &event))
	require.Equal(t, "progress", stream.next(t, 3*time.Second).name)

	// The tab closes while the model is still working.
	cancel()
	_ = resp.Body.Close()
	completer.gate <- struct{}{}

	require.Eventually(t, func() bool {
		rec := do(t, srv, http.MethodGet, conversationsPath+"/"+event.ConversationID, nil)
		if rec.Code != http.StatusOK {
			return false
		}
		var thread threadBody
		_ = json.Unmarshal(rec.Body.Bytes(), &thread)

		return len(thread.Messages) == 2 && thread.Messages[1].Content == "saved after you left"
	}, 5*time.Second, 20*time.Millisecond, "the reply is saved though nobody was listening")
}

func TestChatRefusesASecondTurnWhileOneIsAnswering(t *testing.T) {
	completer := &gatedCompleter{gate: make(chan struct{}), replies: []claude.Response{answer("one"), answer("two")}}
	srv := newConversationServer(t, completer, service.NewMemoryChats(fixedTeam(1)).Stores())
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	first := make(chan string, 1)
	go func() {
		resp, err := http.Post(ts.URL+chatPath, "application/json",
			strings.NewReader(`{"provider":"anthropic","model":"m","message":"slow"}`))
		if err != nil {
			first <- ""

			return
		}
		defer func() { _ = resp.Body.Close() }()
		var body chatTurnBody
		_ = json.NewDecoder(resp.Body).Decode(&body)
		first <- body.ConversationID
	}()

	// Wait for the first turn's question to be saved, then ask again on it.
	var id string
	require.Eventually(t, func() bool {
		rec := do(t, srv, http.MethodGet, conversationsPath, nil)
		var list struct {
			Conversations []struct {
				ID string `json:"id"`
			} `json:"conversations"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &list)
		if len(list.Conversations) == 1 {
			id = list.Conversations[0].ID
		}

		return id != ""
	}, 3*time.Second, 10*time.Millisecond)

	rec := do(t, srv, http.MethodPost, chatPath, strings.NewReader(
		`{"conversationId":"`+id+`","provider":"anthropic","model":"m","message":"from another tab"}`))
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, "ErrConflict", body["code"])
	assert.Equal(t, "This chat is still answering. Try again when the reply lands.", body["message"])

	completer.gate <- struct{}{}
	assert.Equal(t, id, <-first)

	thread := readThread(t, srv, id)
	require.Len(t, thread.Messages, 2, "the refused question was never saved")
}

func TestChatConversationRoutesWithoutAChatServiceAnswer503(t *testing.T) {
	srv := api.NewServer(api.Deps{
		Config: config.Config{Addr: ":8080", Env: config.EnvDev, EverestNamespace: "pds"},
		Logger: log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:   auth.NewStub(),
	})

	for _, rec := range []*httptest.ResponseRecorder{
		do(t, srv, http.MethodGet, conversationsPath, nil),
		do(t, srv, http.MethodGet, conversationsPath+"/abc", nil),
		do(t, srv, http.MethodDelete, conversationsPath+"/abc", nil),
	} {
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "a nil Chat answers, it does not panic")
	}
}

func TestChatStreamTellsARetryWhereItsMessageIsBeingAnswered(t *testing.T) {
	completer := &gatedCompleter{gate: make(chan struct{}), replies: []claude.Response{answer("slow answer")}}
	srv := newConversationServer(t, completer, service.NewMemoryChats(fixedTeam(1)).Stores())
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	body := `{"provider":"anthropic","model":"m","message":"slow","requestKey":"0123456789abcdef0123456789abcdef"}`

	open := func() *sseReader {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+chatPath, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		return &sseReader{scanner: bufio.NewScanner(resp.Body)}
	}

	first := open()
	started := first.next(t, 3*time.Second)
	require.Equal(t, "conversation", started.name)

	retry := open()
	again := retry.next(t, 3*time.Second)
	require.Equal(t, "conversation", again.name)
	assert.Equal(t, started.data, again.data, "the retry names the conversation the first send opened")
	assert.Equal(t, "pending", retry.next(t, 3*time.Second).name)

	completer.gate <- struct{}{}
	for {
		event := first.next(t, 3*time.Second)
		if event.name == "reply" {
			break
		}
	}

	rec := do(t, srv, http.MethodPost, chatPath, strings.NewReader(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var replay chatTurnBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &replay))
	assert.Equal(t, "slow answer", replay.Text, "once answered, a retry gets the saved reply")

	list := do(t, srv, http.MethodGet, conversationsPath, nil)
	var page struct {
		Conversations []json.RawMessage `json:"conversations"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &page))
	assert.Len(t, page.Conversations, 1, "no retry opened a second chat")
}

func TestChatRejectsAMalformedRequestKey(t *testing.T) {
	srv := newConversationServer(t, &scriptedCompleter{}, service.NewMemoryChats(fixedTeam(1)).Stores())

	rec := do(t, srv, http.MethodPost, chatPath, strings.NewReader(
		`{"provider":"anthropic","model":"m","message":"hi","requestKey":"not a key; drop table"}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
