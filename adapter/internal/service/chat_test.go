package service_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/service"
)

type fakeChatDBs struct {
	instances []*engine.Instance
}

func newFakeChatDBs(instances ...*engine.Instance) *fakeChatDBs {
	return &fakeChatDBs{instances: instances}
}

func (f *fakeChatDBs) List(_ context.Context) ([]engine.Instance, error) {
	out := make([]engine.Instance, 0, len(f.instances))
	for _, inst := range f.instances {
		copy := *inst
		copy.Connection = nil
		out = append(out, copy)
	}

	return out, nil
}

func (f *fakeChatDBs) ListEnvironment(ctx context.Context, environment string) ([]engine.Instance, error) {
	all, err := f.List(ctx)
	if err != nil || environment == "" {
		return all, err
	}
	out := make([]engine.Instance, 0, len(all))
	for _, inst := range all {
		if inst.Environment == environment {
			out = append(out, inst)
		}
	}

	return out, nil
}

func (f *fakeChatDBs) Lookup(_ context.Context, id string) (*engine.Instance, error) {
	for _, inst := range f.instances {
		if inst.ID == id {
			copy := *inst
			copy.Connection = nil

			return &copy, nil
		}
	}

	return nil, engine.ErrNotFound
}

func (f *fakeChatDBs) Backups(_ context.Context, _ string) ([]engine.Backup, error) {
	return nil, nil
}

func toolNames(tools []claude.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}

	return names
}

func TestChatReplyReturnsTextAndActions(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{{
		StopReason: "end_turn",
		Content: []claude.ContentBlock{{
			Type: "text",
			Text: `orders-dev has a node down.<actions>[{"label":"View status","kind":"status","id":"pds.orders-dev"}]</actions>`,
		}},
	}}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), service.WithChatCompleter(completer))

	got, err := chat.Reply(context.Background(), service.ChatRequest{
		Provider: service.ProviderAnthropic,
		Model:    "claude-sonnet-4-20250514",
		Message:  "Why is orders-dev degraded?",
	})
	require.NoError(t, err)
	assert.Equal(t, "orders-dev has a node down.", got.Text)
	require.Len(t, got.Actions, 1)
	assert.Equal(t, "status", got.Actions[0].Kind)
	assert.Equal(t, "pds.orders-dev", got.Actions[0].ID)
}

func TestChatDropsUnknownKindsAndMissingIDs(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{{
		StopReason: "end_turn",
		Content: []claude.ContentBlock{{
			Type: "text",
			Text: `here are options
<actions>[
				{"label":"Reboot","kind":"reboot"},
				{"label":"Backup","kind":"backup"},
				{"label":"Contact support","kind":"support"},
				{"label":"View status","kind":"status","id":"pds.missing"}
			]</actions>`,
		}},
	}}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), service.WithChatCompleter(completer))

	got, err := chat.Reply(context.Background(), service.ChatRequest{
		Provider: service.ProviderAnthropic,
		Model:    "m",
		Message:  "help",
	})
	require.NoError(t, err)
	require.Len(t, got.Actions, 1)
	assert.Equal(t, "support", got.Actions[0].Kind)
}

func TestChatNoLongerOffersAButtonThatChangesADatabase(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{{
		StopReason: "end_turn",
		Content: []claude.ContentBlock{{
			Type: "text",
			Text: `try this<actions>[
				{"label":"Scale up","kind":"scale","id":"pds.orders-dev","field":"cpu"},
				{"label":"Retry backup","kind":"backup","id":"pds.orders-dev"},
				{"label":"Enable schedule","kind":"schedule","id":"pds.orders-dev"},
				{"label":"View metrics","kind":"metrics","id":"pds.orders-dev","metric":"cpu"}
			]</actions>`,
		}},
	}}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), service.WithChatCompleter(completer))

	got, err := chat.Reply(context.Background(), service.ChatRequest{Provider: service.ProviderAnthropic, Model: "m", Message: "help"})

	require.NoError(t, err)
	require.Len(t, got.Actions, 1, "only the buttons that open something are kept")
	assert.Equal(t, "metrics", got.Actions[0].Kind)
}

func TestChatSanitizesOutboundText(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{{
		StopReason: "end_turn",
		Content:    []claude.ContentBlock{{Type: "text", Text: "ok"}},
	}}}
	inst := testInstance()
	chat := service.NewChat(newFakeChatDBs(inst), nil, withTestChats(), service.WithChatCompleter(completer))
	_, err := chat.Reply(context.Background(), service.ChatRequest{
		Provider: "anthropic",
		Model:    "m",
		Message:  "look at mysql://app:s3cret@host/db",
	})
	require.NoError(t, err)
	require.NotEmpty(t, completer.calls)
	raw, _ := json.Marshal(completer.calls[0])
	assert.NotContains(t, string(raw), "s3cret")
}

func TestChatBlankReplyIncludesSupportFallback(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{{
		StopReason: "end_turn",
		Content: []claude.ContentBlock{{
			Type: "text",
			Text: "",
		}},
	}}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), service.WithChatCompleter(completer))

	got, err := chat.Reply(context.Background(), service.ChatRequest{
		Provider: service.ProviderAnthropic,
		Model:    "m",
		Message:  "help",
	})
	require.NoError(t, err)
	assert.Equal(t, "The assistant could not produce a usable reply.", got.Text)
	require.Len(t, got.Actions, 1)
	assert.Equal(t, "Contact support", got.Actions[0].Label)
	assert.Equal(t, "support", got.Actions[0].Kind)
}

func TestChatValidatesTheThread(t *testing.T) {
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(&scriptedCompleter{}))
	_, err := chat.Reply(context.Background(), service.ChatRequest{Provider: "anthropic", Model: "m", Message: ""})
	require.ErrorIs(t, err, service.ErrInvalidChat)
	_, err = chat.Reply(context.Background(), service.ChatRequest{Provider: "anthropic", Model: "", Message: "hi"})
	require.ErrorIs(t, err, service.ErrInvalidChat)
	_, err = chat.Reply(context.Background(), service.ChatRequest{Provider: "nope", Model: "m", Message: "hi"})
	require.ErrorIs(t, err, service.ErrUnknownProvider)
}

func TestChatCursorSystemExplainsJSONToolsAndTheQuestionCarriesTheCatalog(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{{
		StopReason: "end_turn",
		Content:    []claude.ContentBlock{{Type: "text", Text: "ok"}},
	}}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), service.WithChatCompleter(completer))

	_, err := chat.Reply(context.Background(), service.ChatRequest{
		Provider: service.ProviderCursor,
		Model:    "composer-2",
		Message:  "can I store a million rows?",
	})
	require.NoError(t, err)
	require.Len(t, completer.calls, 1)
	assert.Contains(t, completer.calls[0].System, "tool_calls")
	assert.Contains(t, completer.calls[0].System, "You cannot invoke tools yourself")
	question := completer.calls[0].Messages[0].Content[0].Text
	assert.Contains(t, question, "orders-dev")
	assert.Contains(t, question, "databases: ")
	assert.Contains(t, question, "can I store a million rows?")
	assert.NotContains(t, question, "s3cret")
}

func TestChatRefusesARemovedProviderAsInvalidInput(t *testing.T) {
	completer := &scriptedCompleter{}
	chat := service.NewChat(nil, nil, withTestChats(), service.WithChatCompleter(completer))

	for _, provider := range []string{"openai", "gemini", "openrouter"} {
		_, err := chat.Reply(context.Background(), service.ChatRequest{Provider: provider, Model: "m", Message: "hi"})

		require.ErrorIs(t, err, service.ErrUnknownProvider, provider)
	}
	assert.Empty(t, completer.calls, "a refused provider never reaches the model")
}

func TestChatAnthropicSystemOmitsCursorToolProtocol(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{{
		StopReason: "end_turn",
		Content:    []claude.ContentBlock{{Type: "text", Text: "ok"}},
	}}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), service.WithChatCompleter(completer))

	_, err := chat.Reply(context.Background(), service.ChatRequest{
		Provider: service.ProviderAnthropic,
		Model:    "m",
		Message:  "help",
	})
	require.NoError(t, err)
	require.Len(t, completer.calls, 1)
	assert.NotContains(t, completer.calls[0].System, "tool_calls")
	assert.NotContains(t, completer.calls[0].System, "You cannot invoke tools yourself")
}
