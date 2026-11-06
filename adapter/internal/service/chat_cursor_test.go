package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// fakeCursorAgents stands in for Cursor Cloud Agents: a client opened with no
// agent creates one on its first call, a client opened with one runs on it,
// and an agent listed in gone refuses the run, so a new one takes over.
type fakeCursorAgents struct {
	mu        sync.Mutex
	opened    []string
	created   int
	runs      []string
	requests  []claude.Request
	gone      map[string]bool
	deleted   []string
	deleteErr error
	failRun   error
	openErr   error
	// onOpen runs once, as the first client opens, which is after the turn read
	// its conversation and before it asked its question.
	onOpen func()
}

func newFakeCursorAgents() *fakeCursorAgents {
	return &fakeCursorAgents{gone: map[string]bool{}}
}

func (f *fakeCursorAgents) Open(_ context.Context, model, agentID string) (service.CursorAgent, error) {
	if hook := f.onOpen; hook != nil {
		f.onOpen = nil
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.openErr != nil {
		return nil, f.openErr
	}
	f.opened = append(f.opened, model+"/"+agentID)

	return &fakeAgent{parent: f, id: agentID}, nil
}

func (f *fakeCursorAgents) Delete(_ context.Context, agentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deleted = append(f.deleted, agentID)

	return f.deleteErr
}

type fakeAgent struct {
	parent *fakeCursorAgents
	id     string
}

func (a *fakeAgent) AgentID() string { return a.id }

func (a *fakeAgent) Complete(_ context.Context, req claude.Request) (*claude.Response, error) {
	f := a.parent
	f.mu.Lock()
	defer f.mu.Unlock()

	f.requests = append(f.requests, req)
	if a.id == "" || f.gone[a.id] {
		f.created++
		a.id = fmt.Sprintf("agent-%d", f.created)
	}
	f.runs = append(f.runs, a.id)
	if f.failRun != nil {
		return nil, f.failRun
	}

	return &claude.Response{StopReason: "end_turn", Content: []claude.ContentBlock{{
		Type: "text", Text: `{"text":"answer","actions":[]}`,
	}}}, nil
}

func cursorChat(agents *fakeCursorAgents) *service.Chat {
	return service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), service.WithCursorAgents(agents))
}

func cursorTurn(conversationID, message string) service.ChatRequest {
	return service.ChatRequest{
		ConversationID: conversationID, Provider: service.ProviderCursor, Model: "composer-2", Message: message,
	}
}

func TestChatSecondCursorTurnRunsOnTheStoredAgentAndCreatesNone(t *testing.T) {
	agents := newFakeCursorAgents()
	chat := cursorChat(agents)

	first, err := chat.Reply(t.Context(), cursorTurn("", "first question"))
	require.NoError(t, err)
	_, err = chat.Reply(t.Context(), cursorTurn(first.ConversationID, "second question"))
	require.NoError(t, err)
	_, err = chat.Reply(t.Context(), cursorTurn(first.ConversationID, "third question"))
	require.NoError(t, err)

	assert.Equal(t, 1, agents.created, "only the first turn creates an agent")
	assert.Equal(t, []string{"agent-1", "agent-1", "agent-1"}, agents.runs)
	assert.Equal(t, []string{"composer-2/", "composer-2/agent-1", "composer-2/agent-1"}, agents.opened,
		"a later turn opens its client on the agent the conversation kept")
	assert.Empty(t, agents.deleted)
}

func TestChatCursorRefusedRunStartsANewAgentFromTheStoredThreadAndKeepsItsID(t *testing.T) {
	agents := newFakeCursorAgents()
	chat := cursorChat(agents)
	first, err := chat.Reply(t.Context(), cursorTurn("", "first question"))
	require.NoError(t, err)
	agents.gone["agent-1"] = true

	_, err = chat.Reply(t.Context(), cursorTurn(first.ConversationID, "second question"))
	require.NoError(t, err)
	_, err = chat.Reply(t.Context(), cursorTurn(first.ConversationID, "third question"))
	require.NoError(t, err)

	assert.Equal(t, 2, agents.created, "the refused run is answered by one new agent")
	second := agents.requests[1]
	require.Len(t, second.Messages, 3, "the new agent starts from the stored thread, the question, its answer and the new question")
	assert.Equal(t, "first question", strings.TrimSpace(second.Messages[0].Content[0].Text))
	assert.Equal(t, []string{"composer-2/", "composer-2/agent-1", "composer-2/agent-2"}, agents.opened,
		"the conversation keeps the id of the agent that took over")
	assert.Equal(t, []string{"agent-1", "agent-2", "agent-2"}, agents.runs)
}

func TestChatCursorQuestionCarriesTheCurrentDatabases(t *testing.T) {
	agents := newFakeCursorAgents()
	chat := cursorChat(agents)

	_, err := chat.Reply(t.Context(), cursorTurn("", "can I store a million rows?"))
	require.NoError(t, err)

	require.Len(t, agents.requests, 1)
	call := agents.requests[0]
	assert.Contains(t, call.System, "tool_calls")
	assert.NotContains(t, call.System, "orders-dev", "the system prompt is not rebuilt around a list that changes between turns")
	last := call.Messages[len(call.Messages)-1].Content[0].Text
	assert.Contains(t, last, "orders-dev", "a kept agent is only sent the question, so the question brings the list")
	assert.Contains(t, last, "can I store a million rows?")
}

func TestChatCursorFailedFirstTurnDeletesTheAgentItStarted(t *testing.T) {
	agents := newFakeCursorAgents()
	agents.failRun = errors.New("cursor: run failed")
	chat := cursorChat(agents)

	_, err := chat.Reply(t.Context(), cursorTurn("", "first question"))

	require.Error(t, err)
	page, listErr := chat.Conversations(t.Context(), "", 10)
	require.NoError(t, listErr)
	assert.Empty(t, page.Conversations, "the conversation goes with its unanswered question")
	assert.Equal(t, []string{"agent-1"}, agents.deleted, "so the agent that was started for it does not stay at Cursor")
}

func TestChatCursorFailedLaterTurnKeepsTheAgent(t *testing.T) {
	agents := newFakeCursorAgents()
	chat := cursorChat(agents)
	first, err := chat.Reply(t.Context(), cursorTurn("", "first question"))
	require.NoError(t, err)
	agents.failRun = errors.New("cursor: run failed")

	_, err = chat.Reply(t.Context(), cursorTurn(first.ConversationID, "second question"))
	require.Error(t, err)
	agents.failRun = nil
	_, err = chat.Reply(t.Context(), cursorTurn(first.ConversationID, "second question again"))
	require.NoError(t, err)

	assert.Empty(t, agents.deleted)
	assert.Equal(t, "composer-2/agent-1", agents.opened[len(agents.opened)-1])
	assert.Equal(t, 1, agents.created)
}

func TestChatDeleteConversationDeletesItsCursorAgent(t *testing.T) {
	agents := newFakeCursorAgents()
	chat := cursorChat(agents)
	first, err := chat.Reply(t.Context(), cursorTurn("", "first question"))
	require.NoError(t, err)

	require.NoError(t, chat.DeleteConversation(t.Context(), first.ConversationID))

	assert.Equal(t, []string{"agent-1"}, agents.deleted)
	_, err = chat.Conversation(t.Context(), first.ConversationID)
	require.ErrorIs(t, err, service.ErrChatNotFound)
}

func TestChatDeleteConversationGoesOnWhenCursorCannotDeleteTheAgent(t *testing.T) {
	agents := newFakeCursorAgents()
	chat := cursorChat(agents)
	first, err := chat.Reply(t.Context(), cursorTurn("", "first question"))
	require.NoError(t, err)
	agents.deleteErr = errors.New("cursor: rejected: status 500")

	require.NoError(t, chat.DeleteConversation(t.Context(), first.ConversationID))

	assert.Equal(t, []string{"agent-1"}, agents.deleted, "Cursor was asked")
	_, err = chat.Conversation(t.Context(), first.ConversationID)
	require.ErrorIs(t, err, service.ErrChatNotFound, "and the conversation is gone anyway")
}

func TestChatDeleteConversationOfAnotherProviderAsksCursorForNothing(t *testing.T) {
	agents := newFakeCursorAgents()
	completer := &scriptedCompleter{replies: []claude.Response{textReply("answer")}}
	chat := service.NewChat(nil, nil, withTestChats(),
		service.WithChatCompleter(completer), service.WithCursorAgents(agents))
	first, err := chat.Reply(t.Context(), service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "hi"})
	require.NoError(t, err)

	require.NoError(t, chat.DeleteConversation(t.Context(), first.ConversationID))

	assert.Empty(t, agents.opened)
	assert.Empty(t, agents.deleted)
}

func TestChatDeleteMissingConversationAsksCursorForNothing(t *testing.T) {
	agents := newFakeCursorAgents()
	chat := cursorChat(agents)

	err := chat.DeleteConversation(t.Context(), "no-such-conversation")

	require.ErrorIs(t, err, service.ErrChatNotFound)
	assert.Empty(t, agents.deleted)
}

func TestChatCursorWithoutAKeyIsUnavailable(t *testing.T) {
	chat := service.NewChat(nil, nil, withTestChats())

	_, err := chat.Reply(t.Context(), cursorTurn("", "hi"))

	require.ErrorIs(t, err, service.ErrCursorUnavailable)
}

func TestChatCursorKeyFailureLeavesNoConversation(t *testing.T) {
	agents := newFakeCursorAgents()
	agents.openErr = service.ErrCursorUnavailable
	chat := cursorChat(agents)

	_, err := chat.Reply(t.Context(), cursorTurn("", "hi"))

	require.ErrorIs(t, err, service.ErrCursorUnavailable)
	page, listErr := chat.Conversations(t.Context(), "", 10)
	require.NoError(t, listErr)
	assert.Empty(t, page.Conversations)
}

// failingChats is a conversation store whose chosen writes fail.
type failingChats struct {
	service.ChatStore
	appendErr error
	agentErr  error
}

func (f failingChats) AppendChatMessage(ctx context.Context, id string, msg store.ChatMessage) (store.ChatMessage, error) {
	if f.appendErr != nil {
		return store.ChatMessage{}, f.appendErr
	}

	return f.ChatStore.AppendChatMessage(ctx, id, msg)
}

func (f failingChats) SetChatAgent(ctx context.Context, id, agentID string) error {
	if f.agentErr != nil {
		return f.agentErr
	}

	return f.ChatStore.SetChatAgent(ctx, id, agentID)
}

func chatOn(agents *fakeCursorAgents, wrap func(service.ChatStore) service.ChatStore) *service.Chat {
	stores := service.NewMemoryChats(fixedTeam(1)).Stores()

	return service.NewChat(newFakeChatDBs(testInstance()), nil,
		service.WithChatStore(func(ctx context.Context) service.ChatStore { return wrap(stores(ctx)) }),
		service.WithCursorAgents(agents))
}

func TestChatCursorReplyThatCannotBeSavedDeletesTheAgentOfTheConversationItTakesBack(t *testing.T) {
	agents := newFakeCursorAgents()
	chat := chatOn(agents, func(chats service.ChatStore) service.ChatStore {
		return failingChats{ChatStore: chats, appendErr: errors.New("disk full")}
	})

	_, err := chat.Reply(t.Context(), cursorTurn("", "first question"))

	require.ErrorIs(t, err, service.ErrChatUnsaved)
	page, listErr := chat.Conversations(t.Context(), "", 10)
	require.NoError(t, listErr)
	assert.Empty(t, page.Conversations)
	assert.Equal(t, []string{"agent-1"}, agents.deleted, "the agent holds an answer nobody can reach")
}

func TestChatCursorAgentThatCannotBeSavedIsDeleted(t *testing.T) {
	agents := newFakeCursorAgents()
	chat := chatOn(agents, func(chats service.ChatStore) service.ChatStore {
		return failingChats{ChatStore: chats, agentErr: errors.New("disk full")}
	})

	got, err := chat.Reply(t.Context(), cursorTurn("", "first question"))

	require.NoError(t, err, "the answer is still the answer")
	assert.NotEmpty(t, got.ConversationID)
	assert.Equal(t, []string{"agent-1"}, agents.deleted, "an agent the conversation does not know would stay at Cursor")
}

func TestChatCursorConversationSavedBeforeItKeptAnAgentStartsOneOnItsNextTurn(t *testing.T) {
	agents := newFakeCursorAgents()
	chats := service.NewMemoryChats(fixedTeam(1))
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil,
		service.WithChatStore(chats.Stores()), service.WithCursorAgents(agents))
	saved, _, err := chats.Stores()(t.Context()).StartChatConversation(t.Context(),
		store.ChatConversation{Title: "old", Provider: service.ProviderCursor, Model: "composer-2"},
		storeMessage("user", "an old question"))
	require.NoError(t, err)
	_, err = chats.Stores()(t.Context()).AppendChatMessage(t.Context(), saved.ID, storeMessage("assistant", "an old answer"))
	require.NoError(t, err)

	_, err = chat.Reply(t.Context(), cursorTurn(saved.ID, "a new question"))
	require.NoError(t, err)
	_, err = chat.Reply(t.Context(), cursorTurn(saved.ID, "another"))
	require.NoError(t, err)

	assert.Equal(t, 1, agents.created)
	assert.Equal(t, []string{"composer-2/", "composer-2/agent-1"}, agents.opened)
	require.Len(t, agents.requests[0].Messages, 3, "the agent it starts is given the thread saved before")
}

func TestChatCursorFailedTurnThatReplacedTheAgentKeepsTheNewOne(t *testing.T) {
	agents := newFakeCursorAgents()
	chat := cursorChat(agents)
	first, err := chat.Reply(t.Context(), cursorTurn("", "first question"))
	require.NoError(t, err)
	agents.gone["agent-1"] = true
	agents.failRun = errors.New("cursor: run failed")

	_, err = chat.Reply(t.Context(), cursorTurn(first.ConversationID, "second question"))
	require.Error(t, err)
	agents.failRun = nil
	_, err = chat.Reply(t.Context(), cursorTurn(first.ConversationID, "second question again"))
	require.NoError(t, err)

	assert.Equal(t, 2, agents.created, "the agent that replaced the lost one is not started over")
	assert.Equal(t, "composer-2/agent-2", agents.opened[len(agents.opened)-1])
}

func TestChatCursorTurnRunsOnTheAgentAnotherTurnJustKept(t *testing.T) {
	agents := newFakeCursorAgents()
	chats := service.NewMemoryChats(fixedTeam(1))
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil,
		service.WithChatStore(chats.Stores()), service.WithCursorAgents(agents))
	first, err := chat.Reply(t.Context(), cursorTurn("", "first question"))
	require.NoError(t, err)
	// Another turn replaces the agent after this one read the conversation and
	// before its question is let in.
	agents.onOpen = func() {
		require.NoError(t, chats.Stores()(t.Context()).SetChatAgent(t.Context(), first.ConversationID, "agent-9"))
	}

	_, err = chat.Reply(t.Context(), cursorTurn(first.ConversationID, "second question"))
	require.NoError(t, err)

	assert.Equal(t, 1, agents.created, "the agent that was just kept is the one that is run on")
	assert.Equal(t, []string{"agent-1", "agent-9"}, agents.runs)
}
