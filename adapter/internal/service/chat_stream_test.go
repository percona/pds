package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/service"
)

// streamRound is one model call of a scripted turn: the pieces of text the
// model writes, and the response the call ends with.
type streamRound struct {
	deltas   []string
	response claude.Response
	// afterFirst runs once the first piece has been handed over, while the
	// call is still going.
	afterFirst func()
}

// streamingCompleter is a fake Anthropic client that streams each scripted
// round through the request's OnText, the way the real client does.
type streamingCompleter struct {
	rounds []streamRound
	calls  []claude.Request
}

func (s *streamingCompleter) Complete(_ context.Context, req claude.Request) (*claude.Response, error) {
	s.calls = append(s.calls, req)
	round := s.rounds[0]
	s.rounds = s.rounds[1:]
	for i, delta := range round.deltas {
		if req.OnText != nil {
			req.OnText(delta)
		}
		if i == 0 && round.afterFirst != nil {
			round.afterFirst()
		}
	}
	response := round.response

	return &response, nil
}

func endTurn(text string) claude.Response {
	return claude.Response{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: text}}}
}

// textLog collects the text a turn streams.
type textLog struct {
	mu     sync.Mutex
	deltas []string
}

func (l *textLog) record(delta string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.deltas = append(l.deltas, delta)
}

func (l *textLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.deltas...)
}

func (l *textLog) joined() string {
	return strings.Join(l.all(), "")
}

func streamingChat(completer claude.Completer) *service.Chat {
	return service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withToolFixtures(), service.WithChatCompleter(completer))
}

func askStreamed(t *testing.T, chat *service.Chat, text *textLog) *service.ChatReply {
	t.Helper()

	reply, err := chat.Reply(t.Context(), service.ChatRequest{
		Provider: service.ProviderAnthropic, Model: "m", Message: "how is orders-dev?",
		Text: text.record,
	})
	require.NoError(t, err)

	return reply
}

func TestChatTextDeltasReachTheStreamInOrderAndTheSavedReplyEqualsThem(t *testing.T) {
	full := "Orders-dev is healthy.\n\nBoth members are ready and the last backup finished."
	completer := &streamingCompleter{rounds: []streamRound{{
		deltas:   []string{"Orders-dev is", " healthy.\n\nBoth mem", "bers are ready and the", " last backup finished."},
		response: endTurn(full),
	}}}
	chat := streamingChat(completer)
	text := &textLog{}

	reply := askStreamed(t, chat, text)

	assert.Greater(t, len(text.all()), 1, "the answer is not sent as one piece")
	assert.Equal(t, full, text.joined())
	assert.Equal(t, full, reply.Text)
	thread, err := chat.Conversation(t.Context(), reply.ConversationID)
	require.NoError(t, err)
	assert.Equal(t, text.joined(), thread.Messages[1].Content, "the saved reply is the text the person saw")
	assert.Empty(t, thread.Messages[1].Actions)
}

func TestChatFirstWordsReachTheStreamWhileTheModelIsStillWriting(t *testing.T) {
	seen := &textLog{}
	completer := &streamingCompleter{rounds: []streamRound{{
		deltas:   []string{"Orders-dev is ", "healthy and", " idle."},
		response: endTurn("Orders-dev is healthy and idle."),
		afterFirst: func() {
			// The model has written one piece and is not done.
			assert.Equal(t, "Orders-dev is", seen.joined())
		},
	}}}
	chat := streamingChat(completer)

	reply := askStreamed(t, chat, seen)

	assert.Equal(t, "Orders-dev is healthy and idle.", reply.Text)
}

func TestChatStructuredPartBecomesTheRepliesActionsAndNeverReachesTheText(t *testing.T) {
	trailer := `<actions>[{"label":"View status","kind":"status","id":"pds.orders-dev"},{"label":"Reboot","kind":"reboot"},{"label":"Backup","kind":"backup"}]</actions>`
	full := "orders-dev has a node down.\n" + trailer
	completer := &streamingCompleter{rounds: []streamRound{{
		// The marker is cut across pieces, which is where a naive filter leaks.
		deltas:   []string{"orders-dev has a node down.\n<act", "ions>[{\"label\":\"View status\",\"kind\":\"status\",\"id\":\"pds.orders-dev\"},", `{"label":"Reboot","kind":"reboot"},{"label":"Backup","kind":"backup"}]</actions>`},
		response: endTurn(full),
	}}}
	chat := streamingChat(completer)
	text := &textLog{}

	reply := askStreamed(t, chat, text)

	assert.Equal(t, "orders-dev has a node down.", text.joined())
	assert.Equal(t, "orders-dev has a node down.", reply.Text)
	require.Len(t, reply.Actions, 1, "an unknown kind and an action with no database are dropped")
	assert.Equal(t, "status", reply.Actions[0].Kind)
	assert.Equal(t, "pds.orders-dev", reply.Actions[0].ID)
	for _, delta := range text.all() {
		assert.NotContains(t, delta, "<")
	}
}

func TestChatStructuredPartThatDoesNotParseLeavesTheTextAlone(t *testing.T) {
	completer := &streamingCompleter{rounds: []streamRound{{
		deltas:   []string{"All good.\n<actions>[{\"label\":"},
		response: endTurn("All good.\n<actions>[{\"label\":"),
	}}}
	chat := streamingChat(completer)
	text := &textLog{}

	reply := askStreamed(t, chat, text)

	assert.Equal(t, "All good.", reply.Text)
	assert.Empty(t, reply.Actions)
}

func TestChatToolRoundSendsNoPartialStructuredPartToThePerson(t *testing.T) {
	completer := &streamingCompleter{rounds: []streamRound{
		{
			deltas: []string{"Let me check the status. <act", `ions>[{"label":"Retry backup","kind":"backup","id":"pds.or`},
			response: claude.Response{StopReason: "tool_use", Content: []claude.ContentBlock{
				{Type: "text", Text: `Let me check the status. <actions>[{"label":"Retry backup","kind":"backup","id":"pds.or`},
				{Type: "tool_use", ID: "1", Name: "get_database", Input: json.RawMessage(`{"id":"` + ordersID + `"}`)},
			}},
		},
		{
			deltas:   []string{"It is ", "healthy."},
			response: endTurn("It is healthy."),
		},
	}}
	chat := streamingChat(completer)
	text := &textLog{}

	reply := askStreamed(t, chat, text)

	for _, delta := range text.all() {
		assert.NotContains(t, delta, "<")
		assert.NotContains(t, delta, "Retry backup")
	}
	assert.Equal(t, "Let me check the status.\n\nIt is healthy.", text.joined())
	assert.Equal(t, text.joined(), reply.Text)
	assert.Empty(t, reply.Actions, "only the last round's structured part counts")
}

func TestChatRedactsACredentialBeforeItIsStreamed(t *testing.T) {
	full := "Connect with mysql://app:s3cret@orders-dev:3306/orders to see it.\nThe password: hunter2 is wrong.\nDone."
	completer := &streamingCompleter{rounds: []streamRound{{
		deltas: []string{
			"Connect with mysql", "://app:s3cret@orders-dev", ":3306/orders to see it.\nThe pass", "word: hunt", "er2 is wrong.\nDone.",
		},
		response: endTurn(full),
	}}}
	chat := streamingChat(completer)
	text := &textLog{}

	reply := askStreamed(t, chat, text)

	for _, delta := range text.all() {
		assert.NotContains(t, delta, "s3cret")
		assert.NotContains(t, delta, "hunter2")
		assert.NotContains(t, delta, "hunt")
	}
	assert.NotContains(t, text.joined(), "s3cret")
	assert.NotContains(t, text.joined(), "hunter2")
	assert.Contains(t, text.joined(), "Done.")
	assert.Equal(t, text.joined(), reply.Text)
}

func TestChatRedactsAKeyPastedAcrossLines(t *testing.T) {
	completer := &streamingCompleter{rounds: []streamRound{{
		deltas: []string{
			"Here it is:\n-----BEGIN PRIVATE KEY-----\nMIIEvQIBAD", "ANBgkq\n-----END PRIVATE KEY-----\nGone.",
		},
		response: endTurn("Here it is:\n-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkq\n-----END PRIVATE KEY-----\nGone."),
	}}}
	chat := streamingChat(completer)
	text := &textLog{}

	reply := askStreamed(t, chat, text)

	assert.NotContains(t, text.joined(), "MIIEvQ")
	assert.NotContains(t, text.joined(), "BEGIN")
	assert.Equal(t, text.joined(), reply.Text)
}

func TestChatRefusalReplacesWhatWasStreamed(t *testing.T) {
	completer := &streamingCompleter{rounds: []streamRound{{
		deltas:   []string{"Here is how to"},
		response: claude.Response{StopReason: claude.StopRefusal, Content: []claude.ContentBlock{{Type: "text", Text: claude.RefusalSentence}}},
	}}}
	chat := streamingChat(completer)
	text := &textLog{}

	reply := askStreamed(t, chat, text)

	assert.Equal(t, claude.RefusalSentence, reply.Text)
	assert.Empty(t, reply.Actions)
	thread, err := chat.Conversation(t.Context(), reply.ConversationID)
	require.NoError(t, err)
	assert.Equal(t, claude.RefusalSentence, thread.Messages[1].Content, "the partial text is not saved")
}

func TestChatWholeAnswerFromAClientThatDoesNotStreamIsStillSent(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{endTurn("Everything is fine.\n<actions>[{\"label\":\"View status\",\"kind\":\"status\",\"id\":\"pds.orders-dev\"}]</actions>")}}
	chat := streamingChat(completer)
	text := &textLog{}

	reply := askStreamed(t, chat, text)

	assert.Equal(t, "Everything is fine.", text.joined())
	assert.Equal(t, "Everything is fine.", reply.Text)
	require.Len(t, reply.Actions, 1)
}

func TestChatAnswerWithNoTextIsReplacedByASentence(t *testing.T) {
	completer := &streamingCompleter{rounds: []streamRound{{
		deltas:   []string{"  \n "},
		response: endTurn("  \n "),
	}}}
	chat := streamingChat(completer)

	reply := askStreamed(t, chat, &textLog{})

	assert.Equal(t, "The assistant could not produce a usable reply.", reply.Text)
	require.Len(t, reply.Actions, 1)
	assert.Equal(t, "support", reply.Actions[0].Kind)
}

func TestChatCursorAnswersArriveWholeThroughTheSameEvents(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{endTurn(
		`{"text":"orders-dev is fine.","actions":[{"label":"View status","kind":"status","id":"pds.orders-dev"}]}`,
	)}}
	chat := streamingChat(completer)
	text := &textLog{}

	reply, err := chat.Reply(t.Context(), service.ChatRequest{
		Provider: service.ProviderCursor, Model: "m", Message: "how is orders-dev?", Text: text.record,
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"orders-dev is fine."}, text.all())
	assert.Equal(t, "orders-dev is fine.", reply.Text)
	require.Len(t, reply.Actions, 1)
	assert.Nil(t, completer.calls[0].OnText, "Cursor is never asked to stream")
}

func TestChatAsksAnthropicToStreamAndKeepsTheSystemPromptTheSameEveryRound(t *testing.T) {
	completer := &streamingCompleter{rounds: []streamRound{
		{response: claude.Response{StopReason: "tool_use", Content: []claude.ContentBlock{
			{Type: "tool_use", ID: "1", Name: "get_database", Input: json.RawMessage(`{"id":"` + ordersID + `"}`)},
		}}},
		{deltas: []string{"ok"}, response: endTurn("ok")},
	}}
	chat := streamingChat(completer)

	askStreamed(t, chat, &textLog{})

	require.Len(t, completer.calls, 2)
	for _, call := range completer.calls {
		assert.NotNil(t, call.OnText)
		assert.Contains(t, call.System, "<actions>")
		assert.NotContains(t, call.System, `{"text"`)
	}
	assert.Equal(t, completer.calls[0].System, completer.calls[1].System)
}

func TestChatKeepsAnAngleBracketThatIsNotTheMarkerAndRedactsAMysqlFlag(t *testing.T) {
	full := "Lag stays x < y while you run -pSecret123 against mysql here."
	completer := &streamingCompleter{rounds: []streamRound{{deltas: strings.SplitAfter(full, " "), response: endTurn(full)}}}
	chat := streamingChat(completer)
	text := &textLog{}

	reply := askStreamed(t, chat, text)

	assert.Contains(t, text.joined(), "x < y")
	assert.NotContains(t, text.joined(), "Secret123")
	assert.Equal(t, text.joined(), reply.Text)
}

func TestChatSaysWhenTheModelRanOutOfRoomMidAnswer(t *testing.T) {
	completer := &streamingCompleter{rounds: []streamRound{{
		deltas: []string{"Orders-dev has two problems. First, the disk is nearly full.\n<actions>[{\"label\":"},
		response: claude.Response{StopReason: "max_tokens", Content: []claude.ContentBlock{{
			Type: "text", Text: "Orders-dev has two problems. First, the disk is nearly full.\n<actions>[{\"label\":",
		}}},
	}}}
	chat := streamingChat(completer)
	text := &textLog{}

	reply := askStreamed(t, chat, text)

	want := "Orders-dev has two problems. First, the disk is nearly full.\n\nThe answer was cut off. Ask again to get the rest."
	assert.Equal(t, want, reply.Text)
	assert.Equal(t, want, text.joined(), "the sentence is streamed too, so what is saved is what was shown")
	assert.Empty(t, reply.Actions)
}
