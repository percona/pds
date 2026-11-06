package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/service"
)

// blockingCompleter holds every Complete call until release is closed, so a
// test can observe what the turn emits while the model is still thinking.
type blockingCompleter struct {
	release chan struct{}
	reply   claude.Response
}

func (b *blockingCompleter) Complete(ctx context.Context, _ claude.Request) (*claude.Response, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	reply := b.reply

	return &reply, nil
}

// progressLog collects progress labels from the goroutine the turn runs on.
type progressLog struct {
	mu     sync.Mutex
	labels []string
	first  chan struct{}
	once   sync.Once
}

func newProgressLog() *progressLog {
	return &progressLog{first: make(chan struct{})}
}

func (p *progressLog) record(progress service.ChatProgress) {
	p.mu.Lock()
	p.labels = append(p.labels, progress.Label)
	p.mu.Unlock()
	p.once.Do(func() { close(p.first) })
}

func (p *progressLog) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.labels...)
}

func TestChatReportsProgressBeforeASlowModelAnswers(t *testing.T) {
	completer := &blockingCompleter{
		release: make(chan struct{}),
		reply: claude.Response{StopReason: "end_turn", Content: []claude.ContentBlock{{
			Type: "text", Text: "done",
		}}},
	}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), service.WithChatCompleter(completer))
	progress := newProgressLog()

	done := make(chan *service.ChatReply, 1)
	go func() {
		reply, err := chat.Reply(context.Background(), service.ChatRequest{
			Provider: service.ProviderAnthropic, Model: "m",
			Message:  "how is orders-dev?",
			Progress: progress.record,
		})
		assert.NoError(t, err)
		done <- reply
	}()

	select {
	case <-progress.first:
	case <-time.After(3 * time.Second):
		t.Fatal("no progress within 3s while the model was still working")
	}
	select {
	case <-done:
		t.Fatal("the turn finished before the model was released")
	default:
	}

	close(completer.release)
	reply := <-done
	assert.Equal(t, "done", reply.Text)
	assert.Equal(t, []string{"Thinking"}, progress.all())
}

func TestChatReportsEachToolItRuns(t *testing.T) {
	completer := &scriptedCompleter{replies: []claude.Response{
		{StopReason: "tool_use", Content: []claude.ContentBlock{
			{Type: "tool_use", ID: "1", Name: "get_database", Input: json.RawMessage(`{"id":"` + ordersID + `"}`)},
			{Type: "tool_use", ID: "2", Name: "get_metrics", Input: json.RawMessage(`{"id":"` + ordersID + `"}`)},
		}},
		{StopReason: "end_turn", Content: []claude.ContentBlock{{
			Type: "text", Text: "healthy",
		}}},
	}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withToolFixtures(), service.WithChatCompleter(completer))
	progress := newProgressLog()

	_, err := chat.Reply(context.Background(), service.ChatRequest{
		Provider: service.ProviderAnthropic, Model: "m",
		Message:  "how is orders-dev?",
		Progress: progress.record,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"Thinking", "Reading status", "Reading metrics", "Thinking"}, progress.all())
}

func TestChatMultiRoundTurnEndsInAnAnswer(t *testing.T) {
	toolRound := claude.Response{StopReason: "tool_use", Content: []claude.ContentBlock{{
		Type: "tool_use", ID: "1", Name: "list_databases", Input: json.RawMessage(`{}`),
	}}}
	completer := &scriptedCompleter{replies: []claude.Response{
		toolRound, toolRound, toolRound,
		{StopReason: "end_turn", Content: []claude.ContentBlock{{
			Type: "text", Text: "orders-dev is healthy",
		}}},
	}}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withToolFixtures(), service.WithChatCompleter(completer))

	got, err := chat.Reply(context.Background(), service.ChatRequest{
		Provider: service.ProviderAnthropic, Model: "m",
		Message: "is orders-dev healthy?",
	})
	require.NoError(t, err)
	assert.Equal(t, "orders-dev is healthy", got.Text)

	require.Len(t, completer.calls, 4)
	for _, call := range completer.calls[:3] {
		assert.NotEmpty(t, call.Tools)
	}
	for _, call := range completer.calls[:3] {
		assert.False(t, call.NoToolCalls)
	}
	last := completer.calls[3]
	assert.NotEmpty(t, last.Tools, "earlier tool blocks are only valid alongside the definitions")
	assert.True(t, last.NoToolCalls, "the last round rules out tools, so the model has to answer")
	assert.NotContains(t, last.System, "Answer now", "the system prompt is the cached prefix, so it does not change")
	lastMessage := last.Messages[len(last.Messages)-1]
	assert.Equal(t, "Answer now with what the tools have returned. No more tools are available in this turn.",
		lastMessage.Content[len(lastMessage.Content)-1].Text)
}

func TestChatCursorGroundingReadsTheListOnce(t *testing.T) {
	dbs := &countingDBs{fakeChatDBs: newFakeChatDBs(testInstance())}
	completer := &scriptedCompleter{replies: []claude.Response{{
		StopReason: "end_turn",
		Content:    []claude.ContentBlock{{Type: "text", Text: "ok"}},
	}}}
	chat := service.NewChat(dbs, nil, withTestChats(), service.WithChatCompleter(completer))

	_, err := chat.Reply(context.Background(), service.ChatRequest{
		Provider: service.ProviderCursor, Model: "composer-2",
		Message: "how are my databases?",
	})
	require.NoError(t, err)
	require.Len(t, completer.calls, 1)
	question := completer.calls[0].Messages[0].Content[0].Text
	assert.Contains(t, question, "orders-dev")
	assert.Contains(t, question, engine.PhaseReady)
	assert.Zero(t, dbs.lookups.Load(), "grounding reads the list, not each database")
}

type countingDBs struct {
	*fakeChatDBs
	lookups atomic.Int32
}

func (c *countingDBs) Lookup(ctx context.Context, id string) (*engine.Instance, error) {
	c.lookups.Add(1)

	return c.fakeChatDBs.Lookup(ctx, id)
}

func TestChatCursorGroundingCapsTheDatabaseList(t *testing.T) {
	instances := make([]*engine.Instance, 0, 25)
	for i := range 25 {
		inst := testInstance()
		inst.ID = fmt.Sprintf("pds.db-%02d", i)
		inst.Name = fmt.Sprintf("db-%02d", i)
		instances = append(instances, inst)
	}
	completer := &scriptedCompleter{replies: []claude.Response{{
		StopReason: "end_turn",
		Content:    []claude.ContentBlock{{Type: "text", Text: "ok"}},
	}}}
	chat := service.NewChat(newFakeChatDBs(instances...), nil, withTestChats(), service.WithChatCompleter(completer))

	_, err := chat.Reply(context.Background(), service.ChatRequest{
		Provider: service.ProviderCursor, Model: "composer-2",
		Message: "how are my databases?",
	})
	require.NoError(t, err)
	question := completer.calls[0].Messages[0].Content[0].Text
	assert.Contains(t, question, "db-19")
	assert.NotContains(t, question, "db-20")
	assert.Contains(t, question, "5 more")
}
