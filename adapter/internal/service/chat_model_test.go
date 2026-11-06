package service_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

const retiredModel = "claude-sonnet-4-20250514"

// savedOn seeds an answered conversation that was saved on model, the way the
// release before the default moved saved them.
func savedOn(t *testing.T, chats *service.MemoryChats, model string) string {
	t.Helper()

	ctx := t.Context()
	team := chats.Stores()(ctx)
	conv, _, err := team.StartChatConversation(ctx,
		store.ChatConversation{Title: "old chat", Provider: service.ProviderAnthropic, Model: model},
		storeMessage("user", "How is orders-dev?"))
	require.NoError(t, err)
	_, err = team.AppendChatMessage(ctx, conv.ID, storeMessage("assistant", "Fine."))
	require.NoError(t, err)

	return conv.ID
}

func TestChatConversationSavedOnADeprecatedModelContinuesOnTheDefault(t *testing.T) {
	chats := service.NewMemoryChats(fixedTeam(1))
	id := savedOn(t, chats, retiredModel)
	completer := &scriptedCompleter{replies: []claude.Response{textReply("still fine")}}
	chat := service.NewChat(nil, nil, service.WithChatStore(chats.Stores()), service.WithChatCompleter(completer))

	// The console sends the model the conversation was saved on.
	reply, err := chat.Reply(t.Context(), service.ChatRequest{
		ConversationID: id, Provider: service.ProviderAnthropic, Model: retiredModel, Message: "And now?",
	})
	require.NoError(t, err)
	assert.Equal(t, "still fine", reply.Text)
	require.Len(t, completer.calls, 1)
	assert.Equal(t, "claude-sonnet-5-5", completer.calls[0].Model)

	thread, err := chat.Conversation(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5-5", thread.Model, "the console reads the model the next turn will use")
	require.Len(t, thread.Messages, 4)

	// And the model the thread now reports is accepted as well.
	_, err = chat.Reply(t.Context(), service.ChatRequest{
		ConversationID: id, Provider: service.ProviderAnthropic, Model: thread.Model, Message: "Once more?",
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5-5", completer.calls[1].Model)
}

func TestChatListShowsTheDefaultForADeprecatedModel(t *testing.T) {
	chats := service.NewMemoryChats(fixedTeam(1))
	savedOn(t, chats, retiredModel)
	chat := service.NewChat(nil, nil, service.WithChatStore(chats.Stores()))

	page, err := chat.Conversations(t.Context(), "", 10)
	require.NoError(t, err)

	require.Len(t, page.Conversations, 1)
	assert.Equal(t, "claude-sonnet-5-5", page.Conversations[0].Model)
}

func TestChatNewConversationOnADeprecatedModelStartsOnTheDefault(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("ok")}}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))

	reply, err := chat.Reply(t.Context(), service.ChatRequest{
		Provider: service.ProviderAnthropic, Model: retiredModel, Message: "hi",
	})
	require.NoError(t, err)

	assert.Equal(t, "claude-sonnet-5-5", completer.calls[0].Model)
	thread, err := chat.Conversation(t.Context(), reply.ConversationID)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5-5", thread.Model)
}

func TestChatCursorModelsAreNeverRewritten(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{textReply("ok")}}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))

	_, err := chat.Reply(t.Context(), service.ChatRequest{
		Provider: service.ProviderCursor, Model: retiredModel, Message: "hi",
	})
	require.NoError(t, err)

	assert.Equal(t, retiredModel, completer.calls[0].Model)
}

func toolRound(id string) claude.Response {
	return claude.Response{StopReason: "tool_use", Content: []claude.ContentBlock{
		{Type: "tool_use", ID: id, Name: "list_databases", Input: json.RawMessage(`{}`)},
	}}
}

func TestChatEveryRoundSendsTheSamePrefixAndLeavesRoomToThink(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{
		toolRound("t1"), toolRound("t2"), toolRound("t3"), textReply("done"),
	}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withToolFixtures(), service.WithChatCompleter(completer))

	_, err := chat.Reply(context.Background(), service.ChatRequest{
		Provider: service.ProviderAnthropic, Model: "claude-sonnet-5-5", Message: "What is running?",
	})
	require.NoError(t, err)
	require.Len(t, completer.calls, 4)

	for _, call := range completer.calls {
		assert.Equal(t, completer.calls[0].System, call.System, "the final round must not change the cached system prompt")
		assert.Equal(t, completer.calls[0].Tools, call.Tools, "descriptions and schemas are part of the prefix too")
		assert.GreaterOrEqual(t, call.MaxTokens, 4096, "thinking counts against max_tokens")
	}

	last := completer.calls[3]
	assert.True(t, last.NoToolCalls)
	final := last.Messages[len(last.Messages)-1]
	require.Equal(t, "user", final.Role)
	require.Len(t, final.Content, 2, "the tool result, then the instruction to answer")
	assert.Equal(t, "tool_result", final.Content[0].Type)
	assert.Equal(t, "text", final.Content[1].Type)
	assert.Contains(t, final.Content[1].Text, "Answer now")

	for _, call := range completer.calls[:3] {
		assert.False(t, call.NoToolCalls)
	}
}

func TestChatSendsTheModelsResponseBackAsItCame(t *testing.T) {
	thinking := json.RawMessage(`{"type":"thinking","thinking":"","signature":"EqQBCkYIARgC"}`)
	completer := &scriptedCompleter{replies: []claude.Response{
		{StopReason: "tool_use", Content: []claude.ContentBlock{
			{Type: "thinking", Raw: thinking},
			{Type: "tool_use", ID: "t1", Name: "list_databases", Input: json.RawMessage(`{}`)},
		}},
		textReply("done"),
	}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withToolFixtures(), service.WithChatCompleter(completer))

	_, err := chat.Reply(t.Context(), service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "What is running?"})
	require.NoError(t, err)
	require.Len(t, completer.calls, 2)

	sent := completer.calls[1].Messages
	require.Len(t, sent, 3, "the question, the response, the tool results")
	assert.Equal(t, "assistant", sent[1].Role)
	assert.Equal(t, thinking, sent[1].Content[0].Raw)
	assert.Equal(t, "t1", sent[2].Content[0].ToolUseID)
}

func TestChatAnswersARefusalWithItsSentence(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{{
		StopReason: claude.StopRefusal,
		Content:    []claude.ContentBlock{{Type: "text", Text: "text the model wrote before it declined"}},
	}}}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))

	reply, err := chat.Reply(t.Context(), service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "hi"})

	require.NoError(t, err)
	assert.Equal(t, claude.RefusalSentence, reply.Text, "whatever the model wrote is not shown")
	assert.Empty(t, reply.Actions)
}
