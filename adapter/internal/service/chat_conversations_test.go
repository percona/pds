package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

func storeConversation(title, key string) store.ChatConversation {
	return store.ChatConversation{Title: title, Provider: service.ProviderAnthropic, Model: "m", RequestKey: key}
}

func storeMessage(role, content string) store.ChatMessage {
	return store.ChatMessage{Role: role, Content: content, Actions: "[]"}
}

func withTestChats() service.ChatOption {
	return service.WithChatStore(service.NewMemoryChats(fixedTeam(1)).Stores())
}

func textReply(text string) claude.Response {
	return claude.Response{StopReason: "end_turn", Content: []claude.ContentBlock{{
		Type: "text", Text: text,
	}}}
}

func TestChatFirstMessageStartsASavedConversation(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{{
		StopReason: "end_turn",
		Content: []claude.ContentBlock{{
			Type: "text",
			Text: `orders-dev is fine.<actions>[{"label":"View status","kind":"status","id":"pds.orders-dev"}]</actions>`,
		}},
	}}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), service.WithChatCompleter(completer))
	ctx := t.Context()

	reply, err := chat.Reply(ctx, service.ChatRequest{
		Provider: service.ProviderAnthropic, Model: "m", DatabaseID: "pds.orders-dev",
		Message: "How is orders-dev doing today?",
	})
	require.NoError(t, err)
	require.NotEmpty(t, reply.ConversationID)

	thread, err := chat.Conversation(ctx, reply.ConversationID)
	require.NoError(t, err)
	assert.Equal(t, "How is orders-dev doing today?", thread.Title)
	assert.Equal(t, service.ProviderAnthropic, thread.Provider)
	assert.Equal(t, "m", thread.Model)
	assert.Equal(t, "pds.orders-dev", thread.DatabaseID)
	assert.Equal(t, 32, thread.ContextWindow)
	require.Len(t, thread.Messages, 2)
	assert.Equal(t, "user", thread.Messages[0].Role)
	assert.Equal(t, "assistant", thread.Messages[1].Role)
	assert.Equal(t, "orders-dev is fine.", thread.Messages[1].Content)
	require.Len(t, thread.Messages[1].Actions, 1, "the reply's buttons are saved with it")
	assert.Equal(t, "status", thread.Messages[1].Actions[0].Kind)
}

func TestChatContinuesFromTheStoredThread(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("first answer"), textReply("second answer")}}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))
	ctx := t.Context()

	first, err := chat.Reply(ctx, service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "first question"})
	require.NoError(t, err)
	second, err := chat.Reply(ctx, service.ChatRequest{
		ConversationID: first.ConversationID, Provider: service.ProviderAnthropic, Model: "m", Message: "second question",
	})
	require.NoError(t, err)
	assert.Equal(t, first.ConversationID, second.ConversationID)

	sent := completer.calls[1].Messages
	require.Len(t, sent, 3, "the model reads the stored thread plus the new message")
	assert.Equal(t, "first question", sent[0].Content[0].Text)
	assert.Equal(t, "first answer", sent[1].Content[0].Text)
	assert.Equal(t, "second question", sent[2].Content[0].Text)
}

func TestChatRefusesAnotherModelOnAConversation(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("ok")}}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))
	ctx := t.Context()

	first, err := chat.Reply(ctx, service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "hi"})
	require.NoError(t, err)

	for _, req := range []service.ChatRequest{
		{ConversationID: first.ConversationID, Provider: service.ProviderAnthropic, Model: "other", Message: "hi"},
		{ConversationID: first.ConversationID, Provider: service.ProviderCursor, Model: "m", Message: "hi"},
	} {
		_, err := chat.Reply(ctx, req)
		require.ErrorIs(t, err, service.ErrChatMismatch)
	}
	assert.Len(t, completer.calls, 1, "a refused turn never reaches the model")
}

func TestChatSendsTheModelTheNewest32Messages(t *testing.T) {
	var replies []claude.Response
	for i := range 20 {
		replies = append(replies, textReply(fmt.Sprintf("answer %d", i)))
	}
	completer := &scriptedCompleter{replies: replies}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))
	ctx := t.Context()

	id := ""
	for i := range 20 {
		reply, err := chat.Reply(ctx, service.ChatRequest{
			ConversationID: id, Provider: service.ProviderAnthropic, Model: "m", Message: fmt.Sprintf("question %d", i),
		})
		require.NoError(t, err, "turn %d", i)
		id = reply.ConversationID
	}

	last := completer.calls[len(completer.calls)-1].Messages
	// 39 messages reach the last turn. The newest 32 open on "answer 3", and
	// a thread may not start with the assistant, so that one is dropped too.
	require.Len(t, last, 31, "the model sees at most 32 messages")
	assert.Equal(t, "user", last[0].Role, "the window starts on a user turn")
	assert.Equal(t, "question 4", last[0].Content[0].Text)
	assert.Equal(t, "question 19", last[30].Content[0].Text)

	thread, err := chat.Conversation(ctx, id)
	require.NoError(t, err)
	assert.Len(t, thread.Messages, 40, "every message stays stored")
}

func TestChatSavesTheReplyAfterTheCallerGoesAway(t *testing.T) {
	completer := &blockingCompleter{release: make(chan struct{}), reply: textReply("saved anyway")}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))

	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		_, err := chat.Reply(ctx, service.ChatRequest{
			Provider: service.ProviderAnthropic, Model: "m", Message: "slow question",
			Started: func(id string) { started <- id },
		})
		done <- err
	}()

	id := <-started
	cancel()
	close(completer.release)
	require.NoError(t, <-done, "the turn is detached from the caller")

	thread, err := chat.Conversation(t.Context(), id)
	require.NoError(t, err)
	require.Len(t, thread.Messages, 2)
	assert.Equal(t, "saved anyway", thread.Messages[1].Content)
}

func TestChatKeyCheckComesBeforeAnythingIsSaved(t *testing.T) {
	chats := service.NewMemoryChats(fixedTeam(1))
	chat := service.NewChat(nil, nil, service.WithChatStore(chats.Stores()))

	_, err := chat.Reply(t.Context(), service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "hi"})
	require.ErrorIs(t, err, service.ErrAssistantUnavailable)

	page, err := chat.Conversations(t.Context(), "", 0)
	require.NoError(t, err)
	assert.Empty(t, page.Conversations, "a provider with no key leaves no empty conversation behind")
}

func TestChatIsUnavailableWithoutAStore(t *testing.T) {
	chat := service.NewChat(nil, nil, service.WithChatCompleter(&scriptedCompleter{}))

	_, err := chat.Reply(t.Context(), service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "hi"})
	require.ErrorIs(t, err, service.ErrChatUnavailable)
	_, err = chat.Conversations(t.Context(), "", 0)
	require.ErrorIs(t, err, service.ErrChatUnavailable)
}

func TestChatIsUnavailableBeforeTheDatabaseConnects(t *testing.T) {
	lazy := store.NewLazy(nil, 1, nil)
	chat := service.NewChat(nil, nil, service.WithChatStore(service.TeamChatStores(lazy, fixedTeam(1))), service.WithChatCompleter(&scriptedCompleter{}))

	_, err := chat.Reply(t.Context(), service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "hi"})
	require.ErrorIs(t, err, service.ErrChatUnavailable)
	_, err = chat.Conversation(t.Context(), "abc")
	require.ErrorIs(t, err, service.ErrChatUnavailable)
	require.ErrorIs(t, chat.DeleteConversation(t.Context(), "abc"), service.ErrChatUnavailable)
}

func TestChatConversationsAreTeamScoped(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("ok")}}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))
	acme := auth.WithIdentity(t.Context(), auth.Identity{Subject: "acme", Scope: auth.Scope{Team: auth.Team{ID: 1}}})
	globex := auth.WithIdentity(t.Context(), auth.Identity{Subject: "globex", Scope: auth.Scope{Team: auth.Team{ID: 2}}})

	reply, err := chat.Reply(acme, service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "hi"})
	require.NoError(t, err)

	page, err := chat.Conversations(globex, "", 0)
	require.NoError(t, err)
	assert.Empty(t, page.Conversations)
	_, err = chat.Conversation(globex, reply.ConversationID)
	require.ErrorIs(t, err, service.ErrChatNotFound)
	_, err = chat.Reply(globex, service.ChatRequest{
		ConversationID: reply.ConversationID, Provider: service.ProviderAnthropic, Model: "m", Message: "hi",
	})
	require.ErrorIs(t, err, service.ErrChatNotFound)
}

func TestChatTitleIsTheFirstMessageCut(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("ok")}}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))
	long := "Why did the nightly backup of orders-dev fail\n\nand what should I change in the schedule so it stops?"

	reply, err := chat.Reply(t.Context(), service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: long})
	require.NoError(t, err)

	thread, err := chat.Conversation(t.Context(), reply.ConversationID)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(thread.Title, "..."))
	assert.NotContains(t, thread.Title, "\n")
	assert.LessOrEqual(t, len([]rune(thread.Title)), 63)
}

func TestChatKeepsSecretsOutOfTheStore(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("ok")}}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))

	reply, err := chat.Reply(t.Context(), service.ChatRequest{
		Provider: service.ProviderAnthropic, Model: "m", Message: "connect with mysql://app:s3cret@host/db",
	})
	require.NoError(t, err)

	thread, err := chat.Conversation(t.Context(), reply.ConversationID)
	require.NoError(t, err)
	assert.NotContains(t, thread.Title, "s3cret")
	assert.NotContains(t, thread.Messages[0].Content, "s3cret")
}

func TestChatDeleteRemovesTheConversation(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("ok")}}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))

	reply, err := chat.Reply(t.Context(), service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "hi"})
	require.NoError(t, err)
	require.NoError(t, chat.DeleteConversation(t.Context(), reply.ConversationID))

	_, err = chat.Conversation(t.Context(), reply.ConversationID)
	require.ErrorIs(t, err, service.ErrChatNotFound)
	require.ErrorIs(t, chat.DeleteConversation(t.Context(), reply.ConversationID), service.ErrChatNotFound)
}

func TestChatListPagesNewestFirst(t *testing.T) {
	var replies []claude.Response
	for range 3 {
		replies = append(replies, textReply("ok"))
	}
	completer := &scriptedCompleter{replies: replies}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))

	for i := range 3 {
		_, err := chat.Reply(t.Context(), service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: fmt.Sprintf("chat %d", i)})
		require.NoError(t, err)
		time.Sleep(time.Millisecond)
	}

	first, err := chat.Conversations(t.Context(), "", 2)
	require.NoError(t, err)
	require.Len(t, first.Conversations, 2)
	assert.Equal(t, "chat 2", first.Conversations[0].Title)
	require.NotEmpty(t, first.Next)

	second, err := chat.Conversations(t.Context(), first.Next, 2)
	require.NoError(t, err)
	require.Len(t, second.Conversations, 1)
	assert.Equal(t, "chat 0", second.Conversations[0].Title)
	assert.Empty(t, second.Next)
}

// answerOnceCompleter answers its first call and fails every call after.
type answerOnceCompleter struct {
	calls int
}

func (f *answerOnceCompleter) Complete(_ context.Context, _ claude.Request) (*claude.Response, error) {
	f.calls++
	if f.calls == 1 {
		reply := textReply("first answer")

		return &reply, nil
	}

	return nil, errors.New("upstream reset")
}

func TestChatTakesBackAMessageTheModelNeverAnswered(t *testing.T) {
	completer := &answerOnceCompleter{}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))
	ctx := t.Context()

	first, err := chat.Reply(ctx, service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "first"})
	require.NoError(t, err)

	_, err = chat.Reply(ctx, service.ChatRequest{
		ConversationID: first.ConversationID, Provider: service.ProviderAnthropic, Model: "m", Message: "never answered",
	})
	require.Error(t, err)

	thread, err := chat.Conversation(ctx, first.ConversationID)
	require.NoError(t, err)
	require.Len(t, thread.Messages, 2, "the unanswered message is taken back out")
	assert.Equal(t, "first answer", thread.Messages[1].Content)
}

func TestChatDropsAConversationWhoseFirstMessageFailed(t *testing.T) {
	completer := &answerOnceCompleter{calls: 1}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))
	started := ""

	_, err := chat.Reply(t.Context(), service.ChatRequest{
		Provider: service.ProviderAnthropic, Model: "m", Message: "never answered",
		Started: func(id string) { started = id },
	})
	require.Error(t, err)
	require.NotEmpty(t, started, "the message was saved before the model was asked")

	page, err := chat.Conversations(t.Context(), "", 0)
	require.NoError(t, err)
	assert.Empty(t, page.Conversations, "a conversation with no answered turn is not left in the list")
}

// flakyChats wraps a store and fails the writes a test names.
type flakyChats struct {
	service.ChatStore
	failQuestion bool
	failReply    bool
	fullReads    int
	// beforeQuestion runs just before a question is appended, which is where
	// another turn's reply can land.
	beforeQuestion func(ctx context.Context, id string)
}

func (f *flakyChats) AppendChatQuestion(ctx context.Context, id string, msg store.ChatMessage, since time.Time) (store.ChatMessage, error) {
	if f.failQuestion {
		return store.ChatMessage{}, errors.New("insert refused")
	}
	if f.beforeQuestion != nil {
		f.beforeQuestion(ctx, id)
	}

	return f.ChatStore.AppendChatQuestion(ctx, id, msg, since)
}

func (f *flakyChats) StartChatConversation(ctx context.Context, conv store.ChatConversation, question store.ChatMessage) (store.ChatConversation, store.ChatMessage, error) {
	if f.failQuestion {
		return store.ChatConversation{}, store.ChatMessage{}, errors.New("insert refused")
	}

	return f.ChatStore.StartChatConversation(ctx, conv, question)
}

func (f *flakyChats) AppendChatMessage(ctx context.Context, id string, msg store.ChatMessage) (store.ChatMessage, error) {
	if f.failReply && msg.Role == "assistant" {
		return store.ChatMessage{}, errors.New("insert refused")
	}

	return f.ChatStore.AppendChatMessage(ctx, id, msg)
}

func (f *flakyChats) ChatMessages(ctx context.Context, id string) ([]store.ChatMessage, error) {
	f.fullReads++

	return f.ChatStore.ChatMessages(ctx, id)
}

func flakyChat(t *testing.T, completer claude.Completer) (*service.Chat, *flakyChats) {
	t.Helper()

	flaky := &flakyChats{}
	base := service.NewMemoryChats(fixedTeam(1)).Stores()
	stores := func(ctx context.Context) service.ChatStore {
		flaky.ChatStore = base(ctx)

		return flaky
	}

	return service.NewChat(nil, nil, service.WithChatStore(stores), service.WithChatCompleter(completer)), flaky
}

func TestChatTakesBackAQuestionWhoseReplyCouldNotBeSaved(t *testing.T) {
	chat, flaky := flakyChat(t, &scriptedCompleter{replies: []claude.Response{textReply("one"), textReply("two"), textReply("three")}})
	ctx := t.Context()

	first, err := chat.Reply(ctx, service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "first"})
	require.NoError(t, err)

	flaky.failReply = true
	_, err = chat.Reply(ctx, service.ChatRequest{
		ConversationID: first.ConversationID, Provider: service.ProviderAnthropic, Model: "m", Message: "lost reply",
	})
	require.ErrorIs(t, err, service.ErrChatUnsaved)

	flaky.failReply = false
	_, err = chat.Reply(ctx, service.ChatRequest{
		ConversationID: first.ConversationID, Provider: service.ProviderAnthropic, Model: "m", Message: "try again",
	})
	require.NoError(t, err, "the unsaved turn does not leave the conversation busy")

	thread, err := chat.Conversation(ctx, first.ConversationID)
	require.NoError(t, err)
	require.Len(t, thread.Messages, 4)
	assert.Equal(t, "try again", thread.Messages[2].Content)
}

func TestChatDropsAConversationWhoseFirstQuestionCouldNotBeSaved(t *testing.T) {
	chat, flaky := flakyChat(t, &scriptedCompleter{})
	flaky.failQuestion = true

	_, err := chat.Reply(t.Context(), service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "hi"})
	require.ErrorIs(t, err, service.ErrChatStorage)

	flaky.failQuestion = false
	page, err := chat.Conversations(t.Context(), "", 0)
	require.NoError(t, err)
	assert.Empty(t, page.Conversations, "no empty chat is left in the list")
}

func TestChatTurnReadsOnlyTheModelWindow(t *testing.T) {
	var replies []claude.Response
	for range 3 {
		replies = append(replies, textReply("ok"))
	}
	chat, flaky := flakyChat(t, &scriptedCompleter{replies: replies})
	ctx := t.Context()

	first, err := chat.Reply(ctx, service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "one"})
	require.NoError(t, err)
	_, err = chat.Reply(ctx, service.ChatRequest{
		ConversationID: first.ConversationID, Provider: service.ProviderAnthropic, Model: "m", Message: "two",
	})
	require.NoError(t, err)

	assert.Zero(t, flaky.fullReads, "a turn reads the bounded window, never the whole thread")
}

func TestChatTurnSeesAReplyThatLandedBeforeItsQuestion(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("one"), textReply("two")}}
	chat, flaky := flakyChat(t, completer)
	ctx := t.Context()

	first, err := chat.Reply(ctx, service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "first"})
	require.NoError(t, err)

	// Another turn's reply is saved after this turn loaded the conversation
	// and before its question goes in.
	flaky.beforeQuestion = func(ctx context.Context, id string) {
		flaky.beforeQuestion = nil
		_, err := flaky.ChatStore.AppendChatMessage(ctx, id, store.ChatMessage{Role: "user", Content: "from another tab"})
		require.NoError(t, err)
		_, err = flaky.ChatStore.AppendChatMessage(ctx, id, store.ChatMessage{Role: "assistant", Content: "late reply", Actions: "[]"})
		require.NoError(t, err)
	}
	_, err = chat.Reply(ctx, service.ChatRequest{
		ConversationID: first.ConversationID, Provider: service.ProviderAnthropic, Model: "m", Message: "second",
	})
	require.NoError(t, err)

	sent := completer.calls[len(completer.calls)-1].Messages
	var texts []string
	for _, msg := range sent {
		texts = append(texts, msg.Content[0].Text)
	}
	assert.Equal(t, []string{"first", "one", "from another tab", "late reply", "second"}, texts,
		"the model reads the thread as it stood once this question went in")
}

const retryKey = "0123456789abcdef0123456789abcdef"

func TestChatReplaysAnAnsweredRequestInsteadOfOpeningAnother(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("the answer")}}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))
	ctx := t.Context()
	req := service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "question", RequestKey: retryKey}

	first, err := chat.Reply(ctx, req)
	require.NoError(t, err)
	again, err := chat.Reply(ctx, req)
	require.NoError(t, err)

	assert.Equal(t, first.ConversationID, again.ConversationID)
	assert.Equal(t, "the answer", again.Text)
	assert.Len(t, completer.calls, 1, "a replay never asks the model again")
	page, err := chat.Conversations(ctx, "", 0)
	require.NoError(t, err)
	assert.Len(t, page.Conversations, 1)
}

func TestChatReplayOfARunningRequestNamesItsConversation(t *testing.T) {
	completer := &blockingCompleter{release: make(chan struct{}), reply: textReply("slow answer")}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))
	ctx := t.Context()
	req := service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "question", RequestKey: retryKey}

	started := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		first := req
		first.Started = func(id string) { started <- id }
		_, err := chat.Reply(ctx, first)
		done <- err
	}()
	id := <-started

	replayed := ""
	retry := req
	retry.Started = func(got string) { replayed = got }
	_, err := chat.Reply(ctx, retry)
	require.ErrorIs(t, err, service.ErrChatReplayPending)
	require.ErrorIs(t, err, service.ErrChatBusy)
	assert.Equal(t, id, replayed, "the retry is told which conversation holds its message")

	close(completer.release)
	require.NoError(t, <-done)
}

func TestChatReplayRefusesAnotherModel(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("ok")}}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))
	ctx := t.Context()

	_, err := chat.Reply(ctx, service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "q", RequestKey: retryKey})
	require.NoError(t, err)
	_, err = chat.Reply(ctx, service.ChatRequest{Provider: service.ProviderAnthropic, Model: "other", Message: "q", RequestKey: retryKey})
	require.ErrorIs(t, err, service.ErrChatMismatch)
}

func TestChatRequestKeyOfATakenBackTurnStartsAgain(t *testing.T) {
	completer := &answerOnceCompleter{calls: 1}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))
	ctx := t.Context()
	req := service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "q", RequestKey: retryKey}

	_, err := chat.Reply(ctx, req)
	require.Error(t, err, "the model fails and the conversation is taken back")

	completer.calls = 0
	reply, err := chat.Reply(ctx, req)
	require.NoError(t, err, "with nothing saved under the key, the retry is a fresh first message")
	assert.NotEmpty(t, reply.ConversationID)
}

func TestChatReplayReturnsTheOpeningAnswerAfterTheChatMovedOn(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("opening answer"), textReply("later answer")}}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))
	ctx := t.Context()
	opening := service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "opening", RequestKey: retryKey}

	first, err := chat.Reply(ctx, opening)
	require.NoError(t, err)
	_, err = chat.Reply(ctx, service.ChatRequest{
		ConversationID: first.ConversationID, Provider: service.ProviderAnthropic, Model: "m", Message: "later",
	})
	require.NoError(t, err)

	replay, err := chat.Reply(ctx, opening)
	require.NoError(t, err)
	assert.Equal(t, "opening answer", replay.Text, "the retry gets the answer to its own message")
}

func TestChatRetryRecoversAFirstMessageARestartLeftUnanswered(t *testing.T) {
	chats := service.NewMemoryChats(fixedTeam(1))
	now := time.Now()
	completer := &scriptedCompleter{replies: []claude.Response{textReply("recovered answer")}}
	chat := service.NewChat(nil, nil, service.WithChatStore(chats.Stores()), service.WithChatCompleter(completer), service.WithChatClock(func() time.Time { return now }))
	ctx := t.Context()

	// A first message saved by a process that went away before answering.
	saved := chats.Stores()(ctx)
	conv, _, err := saved.StartChatConversation(ctx, storeConversation("question", retryKey), storeMessage("user", "question"))
	require.NoError(t, err)

	req := service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "question", RequestKey: retryKey}
	_, err = chat.Reply(ctx, req)
	require.ErrorIs(t, err, service.ErrChatReplayPending, "while a turn could still be running, the retry waits")

	now = now.Add(10 * time.Minute)
	reply, err := chat.Reply(ctx, req)
	require.NoError(t, err, "once no turn can still be running, the retry answers it")
	assert.Equal(t, conv.ID, reply.ConversationID)
	assert.Equal(t, "recovered answer", reply.Text)

	thread, err := chat.Conversation(ctx, conv.ID)
	require.NoError(t, err)
	require.Len(t, thread.Messages, 2, "the orphan was replaced, not stacked behind")
	assert.Equal(t, "question", thread.Messages[0].Content)
}

func TestChatResumedFirstMessageThatFailsLeavesNoEmptyChat(t *testing.T) {
	chats := service.NewMemoryChats(fixedTeam(1))
	now := time.Now()
	chat := service.NewChat(nil, nil, service.WithChatStore(chats.Stores()), service.WithChatCompleter(&answerOnceCompleter{calls: 1}), service.WithChatClock(func() time.Time { return now.Add(10 * time.Minute) }))
	ctx := t.Context()

	saved := chats.Stores()(ctx)
	_, _, err := saved.StartChatConversation(ctx, storeConversation("question", retryKey), storeMessage("user", "question"))
	require.NoError(t, err)

	_, err = chat.Reply(ctx, service.ChatRequest{
		Provider: service.ProviderAnthropic, Model: "m", Message: "question", RequestKey: retryKey,
	})
	require.Error(t, err)

	page, err := chat.Conversations(ctx, "", 0)
	require.NoError(t, err)
	assert.Empty(t, page.Conversations, "a resumed first message that fails takes its conversation back")
}
