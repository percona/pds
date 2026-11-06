package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// manualClock is the time a turn sees and the timers that follow it. Nothing
// fires until a test moves it.
type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
}

type manualTimer struct {
	at      time.Time
	fire    func()
	stopped bool
	fired   bool
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *manualClock) afterFunc(d time.Duration, fire func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &manualTimer{at: c.now.Add(d), fire: fire}
	c.timers = append(c.timers, timer)

	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		active := !timer.stopped && !timer.fired
		timer.stopped = true

		return active
	}
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*manualTimer
	for _, timer := range c.timers {
		if !timer.stopped && !timer.fired && !timer.at.After(c.now) {
			timer.fired = true
			due = append(due, timer)
		}
	}
	c.mu.Unlock()
	for _, timer := range due {
		timer.fire()
	}
}

type turnResult struct {
	reply *service.ChatReply
	err   error
}

// approvalTurn is one Chat turn running in the background with its approval
// cards and their outcomes collected.
type approvalTurn struct {
	t         *testing.T
	chat      *service.Chat
	approvals chan service.ChatApproval
	settled   chan service.ChatApprovalEnd
	done      chan turnResult
	ctx       context.Context
}

type approvalSetup struct {
	api       *toolAPI
	completer *scriptedCompleter
	clock     *manualClock
	limits    *fakeLimits
	chat      *service.Chat
}

func newApprovalSetup(replies ...claude.Response) *approvalSetup {
	setup := &approvalSetup{
		api:       newToolAPI(),
		completer: &scriptedCompleter{replies: replies},
		clock:     newManualClock(),
		limits:    &fakeLimits{settings: store.AISettings{Access: "change_delete"}},
	}
	setup.chat = service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withTools(setup.api),
		service.WithAccessLimits(setup.limits), service.WithChatCompleter(setup.completer),
		service.WithChatClock(setup.clock.Now), service.WithChatTimer(setup.clock.afterFunc))

	return setup
}

func (s *approvalSetup) start(t *testing.T, ctx context.Context, provider string) *approvalTurn {
	t.Helper()

	turn := &approvalTurn{
		t: t, chat: s.chat, ctx: ctx,
		approvals: make(chan service.ChatApproval, 4),
		settled:   make(chan service.ChatApprovalEnd, 4),
		done:      make(chan turnResult, 1),
	}
	go func() {
		reply, err := s.chat.Reply(ctx, service.ChatRequest{
			Provider: provider, Model: "m", Message: "change it",
			Approval: func(approval service.ChatApproval) { turn.approvals <- approval },
			Settled:  func(end service.ChatApprovalEnd) { turn.settled <- end },
		})
		turn.done <- turnResult{reply, err}
	}()

	return turn
}

func (a *approvalTurn) nextApproval() service.ChatApproval {
	a.t.Helper()
	select {
	case approval := <-a.approvals:
		return approval
	case <-time.After(5 * time.Second):
		a.t.Fatal("no approval was asked for")

		return service.ChatApproval{}
	}
}

func (a *approvalTurn) nextSettled() service.ChatApprovalEnd {
	a.t.Helper()
	select {
	case end := <-a.settled:
		return end
	case <-time.After(5 * time.Second):
		a.t.Fatal("no approval was settled")

		return service.ChatApprovalEnd{}
	}
}

func (a *approvalTurn) finish() turnResult {
	a.t.Helper()
	select {
	case result := <-a.done:
		return result
	case <-time.After(5 * time.Second):
		a.t.Fatal("the turn did not end")

		return turnResult{}
	}
}

const scaleToTwo = `{"id":"` + ordersID + `","cpu":2}`

func changeRequests(api *toolAPI) []toolRequest {
	var out []toolRequest
	for _, request := range api.requests() {
		if request.Method != "GET" {
			out = append(out, request)
		}
	}

	return out
}

func TestChatChangeCallAsksForAnApprovalAndRunsOnlyAfterApprove(t *testing.T) {
	setup := newApprovalSetup(toolUse("1", "scale_database", scaleToTwo), textReply("Scaled orders-dev to 2 CPUs."))
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)

	approval := turn.nextApproval()

	assert.NotEmpty(t, approval.ID)
	assert.Equal(t, "scale_database", approval.Tool)
	assert.Equal(t, "Scale a database", approval.Title)
	assert.Equal(t, "orders-dev", approval.Database)
	assert.False(t, approval.Destructive)
	assert.Equal(t, setup.clock.Now().Add(15*time.Minute), approval.ExpiresAt)
	assert.Contains(t, approval.Lines, service.ApprovalLine{Label: "Database", Value: ordersID})
	assert.Contains(t, approval.Lines, service.ApprovalLine{Label: "CPU cores", Value: "2"})
	assert.Empty(t, changeRequests(setup.api), "nothing is sent before the person answers")
	assert.Len(t, setup.completer.calls, 1, "the model is not asked again while the person decides")

	require.NoError(t, setup.chat.Decide(signedInAsAnn(t.Context()), approval.ID, service.ChatDecision{Approve: true}))
	result := turn.finish()

	require.NoError(t, result.err)
	assert.Equal(t, "Scaled orders-dev to 2 CPUs.", result.reply.Text)
	assert.Equal(t, service.ChatApprovalEnd{ID: approval.ID, Outcome: service.OutcomeApproved}, turn.nextSettled())
	sent := changeRequests(setup.api)
	require.Len(t, sent, 1)
	assert.Equal(t, "POST", sent[0].Method)
	assert.Equal(t, "/api/databases/"+ordersID+"/scale", sent[0].Path)
	assert.JSONEq(t, `{"cpu":2}`, sent[0].Body, "what runs is what the card showed")
	assert.Equal(t, auth.ClientChat, sent[0].Caller.Identity.Client.Kind)
	assert.Equal(t, "Anthropic", sent[0].Caller.Agent)
	assert.Equal(t, "ann", sent[0].Caller.Identity.Subject)
}

func TestChatBackupAndScheduleAreAskedForToo(t *testing.T) {
	setup := newApprovalSetup(
		claude.Response{StopReason: "tool_use", Content: []claude.ContentBlock{
			{Type: "tool_use", ID: "1", Name: "backup_database", Input: []byte(`{"id":"` + ordersID + `"}`)},
		}},
		toolUse("2", "set_backup_schedule", `{"id":"`+ordersID+`","enabled":true,"period":"daily","hour":3}`),
		textReply("Done."),
	)
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)

	first := turn.nextApproval()
	assert.Equal(t, "backup_database", first.Tool)
	require.NoError(t, setup.chat.Decide(signedInAsAnn(t.Context()), first.ID, service.ChatDecision{Approve: true}))
	second := turn.nextApproval()
	assert.Equal(t, "set_backup_schedule", second.Tool)
	assert.Contains(t, second.Lines, service.ApprovalLine{Label: "Runs", Value: "daily"})
	assert.Contains(t, second.Lines, service.ApprovalLine{Label: "Hour (UTC)", Value: "3"})
	require.NoError(t, setup.chat.Decide(signedInAsAnn(t.Context()), second.ID, service.ChatDecision{Approve: true}))

	require.NoError(t, turn.finish().err)
	paths := []string{}
	for _, request := range changeRequests(setup.api) {
		paths = append(paths, request.Path)
	}
	assert.Equal(t, []string{"/api/databases/" + ordersID + "/backup", "/api/databases/" + ordersID + "/backup-schedule"}, paths)
}

func TestChatRejectedChangeGoesBackToTheModelAsARefusalAndSendsNothing(t *testing.T) {
	setup := newApprovalSetup(toolUse("1", "scale_database", scaleToTwo), textReply("Understood, I left it as it was."))
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)

	approval := turn.nextApproval()
	require.NoError(t, setup.chat.Decide(signedInAsAnn(t.Context()), approval.ID, service.ChatDecision{Approve: false}))
	result := turn.finish()

	require.NoError(t, result.err)
	assert.Equal(t, "Understood, I left it as it was.", result.reply.Text)
	assert.Equal(t, service.OutcomeRejected, turn.nextSettled().Outcome)
	assert.Empty(t, changeRequests(setup.api))
	refusal := resultsOf(setup.completer.calls[1])[0]
	assert.True(t, refusal.IsError)
	assert.Equal(t, "1", refusal.ToolUseID)
	assert.Contains(t, refusal.Content, "rejected")
	assert.Contains(t, refusal.Content, "Nothing was changed")
}

func TestChatClosingTheStreamCancelsThePendingCallAndSendsNothing(t *testing.T) {
	setup := newApprovalSetup(toolUse("1", "scale_database", scaleToTwo), textReply("must not be asked"))
	ctx, leave := context.WithCancel(signedInAsAnn(t.Context()))
	turn := setup.start(t, ctx, service.ProviderAnthropic)

	approval := turn.nextApproval()
	leave()
	result := turn.finish()

	require.NoError(t, result.err)
	assert.Contains(t, result.reply.Text, "nothing changed")
	assert.Equal(t, service.ChatApprovalEnd{ID: approval.ID, Outcome: service.OutcomeCancelled}, turn.nextSettled())
	assert.Empty(t, changeRequests(setup.api))
	assert.Len(t, setup.completer.calls, 1, "the turn ends without asking the model again")
	err := setup.chat.Decide(signedInAsAnn(t.Context()), approval.ID, service.ChatDecision{Approve: true})
	assert.ErrorIs(t, err, service.ErrApprovalNotFound, "an approval that arrives after the stream closed changes nothing")
	assert.Empty(t, changeRequests(setup.api))
}

func TestChatApprovalNotAnsweredWithinFifteenMinutesIsCancelled(t *testing.T) {
	setup := newApprovalSetup(toolUse("1", "scale_database", scaleToTwo), textReply("must not be asked"))
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)

	approval := turn.nextApproval()
	setup.clock.advance(15*time.Minute - time.Second)
	select {
	case end := <-turn.settled:
		t.Fatalf("settled early: %+v", end)
	default:
	}
	setup.clock.advance(time.Second)
	result := turn.finish()

	require.NoError(t, result.err)
	assert.Contains(t, result.reply.Text, "expired")
	assert.Contains(t, result.reply.Text, "nothing changed")
	assert.Equal(t, service.ChatApprovalEnd{ID: approval.ID, Outcome: service.OutcomeExpired}, turn.nextSettled())
	assert.Empty(t, changeRequests(setup.api))
	assert.Len(t, setup.completer.calls, 1)
	err := setup.chat.Decide(signedInAsAnn(t.Context()), approval.ID, service.ChatDecision{Approve: true})
	assert.ErrorIs(t, err, service.ErrApprovalNotFound, "a late approval is refused")
}

func TestChatModelBudgetDoesNotCountTheWaitForAnApproval(t *testing.T) {
	setup := newApprovalSetup(toolUse("1", "scale_database", scaleToTwo), textReply("Scaled."))
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)

	approval := turn.nextApproval()
	// Well past the 165 seconds a turn may run, and short of the 15 minutes.
	setup.clock.advance(10 * time.Minute)
	require.NoError(t, setup.chat.Decide(signedInAsAnn(t.Context()), approval.ID, service.ChatDecision{Approve: true}))
	result := turn.finish()

	require.NoError(t, result.err, "the minutes spent waiting are not the model's")
	assert.Equal(t, "Scaled.", result.reply.Text)
	assert.Len(t, changeRequests(setup.api), 1)
}

// stallingCompleter answers its first call with a change and then holds the
// second until the turn's context ends.
type stallingCompleter struct {
	first   claude.Response
	calls   int
	stalled chan struct{}
}

func (s *stallingCompleter) Complete(ctx context.Context, _ claude.Request) (*claude.Response, error) {
	s.calls++
	if s.calls == 1 {
		return &s.first, nil
	}
	close(s.stalled)
	<-ctx.Done()

	return nil, ctx.Err()
}

func TestChatModelBudgetRunsAgainOnceTheApprovalIsAnswered(t *testing.T) {
	setup := newApprovalSetup()
	stalling := &stallingCompleter{first: toolUse("1", "scale_database", scaleToTwo), stalled: make(chan struct{})}
	chat := service.NewChat(newFakeChatDBs(testInstance()), nil, withTestChats(), withTools(setup.api),
		service.WithAccessLimits(setup.limits), service.WithChatCompleter(stalling),
		service.WithChatClock(setup.clock.Now), service.WithChatTimer(setup.clock.afterFunc))
	setup.chat = chat
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)

	approval := turn.nextApproval()
	setup.clock.advance(10 * time.Minute)
	require.NoError(t, chat.Decide(signedInAsAnn(t.Context()), approval.ID, service.ChatDecision{Approve: true}))
	<-stalling.stalled
	setup.clock.advance(165 * time.Second)
	result := turn.finish()

	require.Error(t, result.err)
	assert.ErrorIs(t, result.err, context.DeadlineExceeded, "the model's own time still runs out")
}

func deleteUse(confirm string) claude.Response {
	return toolUse("1", "delete_database", `{"id":"`+ordersID+`","confirm":"`+confirm+`"}`)
}

func TestChatDeleteRunsOnlyWhenTheTypedNameMatches(t *testing.T) {
	setup := newApprovalSetup(deleteUse("anything the model says"), textReply("Deleted."))
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)

	approval := turn.nextApproval()
	assert.True(t, approval.Destructive)
	assert.Equal(t, "orders-dev", approval.Database, "the name the person has to type")
	for _, line := range approval.Lines {
		assert.NotEqual(t, "Confirm", line.Label, "the model's own confirmation is not shown as if it counted")
	}

	for _, wrong := range []string{"", "orders", "ORDERS-DEV", "orders-dev "} {
		err := setup.chat.Decide(signedInAsAnn(t.Context()), approval.ID, service.ChatDecision{Approve: true, Name: wrong})
		assert.ErrorIs(t, err, service.ErrApprovalName, "typed %q", wrong)
	}
	assert.Empty(t, changeRequests(setup.api), "a wrong name leaves the approval open and sends nothing")

	require.NoError(t, setup.chat.Decide(signedInAsAnn(t.Context()), approval.ID, service.ChatDecision{Approve: true, Name: "orders-dev"}))
	require.NoError(t, turn.finish().err)

	sent := changeRequests(setup.api)
	require.Len(t, sent, 1)
	assert.Equal(t, "DELETE", sent[0].Method)
	assert.Equal(t, "/api/databases/"+ordersID, sent[0].Path)
}

func TestChatRejectingADeleteNeedsNoName(t *testing.T) {
	setup := newApprovalSetup(deleteUse("orders-dev"), textReply("Kept it."))
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)

	approval := turn.nextApproval()
	require.NoError(t, setup.chat.Decide(signedInAsAnn(t.Context()), approval.ID, service.ChatDecision{Approve: false}))

	require.NoError(t, turn.finish().err)
	assert.Empty(t, changeRequests(setup.api))
}

func TestChatWithTheLimitAtReadOffersNoChangeTool(t *testing.T) {
	setup := newApprovalSetup(textReply("ok"))
	setup.limits.set(store.AISettings{Access: "read"})

	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)
	require.NoError(t, turn.finish().err)

	for _, tool := range toolNames(setup.completer.calls[0].Tools) {
		assert.NotContains(t, []string{"scale_database", "backup_database", "set_backup_schedule", "delete_database", "create_database"}, tool)
	}
}

func TestChatWithTheLimitAtChangeOffersChangesButNotDelete(t *testing.T) {
	setup := newApprovalSetup(textReply("ok"))
	setup.limits.set(store.AISettings{Access: "change"})

	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)
	require.NoError(t, turn.finish().err)

	offered := toolNames(setup.completer.calls[0].Tools)
	assert.Contains(t, offered, "scale_database")
	assert.NotContains(t, offered, "delete_database")
}

func TestChatToolAboveTheTierIsRefusedWithoutAnApprovalCard(t *testing.T) {
	setup := newApprovalSetup(deleteUse("orders-dev"), textReply("I cannot delete it."))
	setup.limits.set(store.AISettings{Access: "change"})
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)

	result := turn.finish()

	require.NoError(t, result.err)
	select {
	case approval := <-turn.approvals:
		t.Fatalf("a call the tier does not include was put to the person: %+v", approval)
	default:
	}
	refusal := resultsOf(setup.completer.calls[1])[0]
	assert.True(t, refusal.IsError)
	assert.Contains(t, refusal.Content, "not_allowed")
	assert.Empty(t, changeRequests(setup.api))
}

func TestChatChangeWithNoOneToAskIsRefusedAndSendsNothing(t *testing.T) {
	setup := newApprovalSetup(toolUse("1", "scale_database", scaleToTwo), textReply("I could not."))

	reply, err := setup.chat.Reply(signedInAsAnn(t.Context()), service.ChatRequest{
		Provider: service.ProviderAnthropic, Model: "m", Message: "scale it",
	})

	require.NoError(t, err)
	assert.Equal(t, "I could not.", reply.Text)
	refusal := resultsOf(setup.completer.calls[1])[0]
	assert.True(t, refusal.IsError)
	assert.Empty(t, changeRequests(setup.api), "a request that cannot show a card cannot change anything")
}

func TestChatReadsRunWithoutAnApproval(t *testing.T) {
	setup := newApprovalSetup(toolUse("1", "list_databases", `{}`), textReply("One database."))
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)

	require.NoError(t, turn.finish().err)

	select {
	case approval := <-turn.approvals:
		t.Fatalf("a read was put to the person: %+v", approval)
	default:
	}
	assert.NotEmpty(t, setup.api.requests())
}

func TestChatApprovalIsOnlyTheAskingPersonsToAnswer(t *testing.T) {
	setup := newApprovalSetup(toolUse("1", "scale_database", scaleToTwo), textReply("Scaled."))
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)
	approval := turn.nextApproval()

	bob := auth.WithIdentity(t.Context(), auth.Identity{Subject: "bob", Scope: auth.Scope{
		UserID: 3, UserName: "bob", OrganizationID: 1, OrganizationName: "acme", Role: auth.RoleAdmin, Team: auth.Team{ID: 1, Name: "default"},
	}})
	err := setup.chat.Decide(bob, approval.ID, service.ChatDecision{Approve: true})
	assert.ErrorIs(t, err, service.ErrApprovalNotFound, "another person cannot approve what Ann was asked")
	assert.Empty(t, changeRequests(setup.api))

	assert.ErrorIs(t, setup.chat.Decide(t.Context(), approval.ID, service.ChatDecision{Approve: true}), service.ErrApprovalNotFound, "nor can a caller with no session")
	assert.ErrorIs(t, setup.chat.Decide(signedInAsAnn(t.Context()), "no-such-id", service.ChatDecision{Approve: true}), service.ErrApprovalNotFound)

	require.NoError(t, setup.chat.Decide(signedInAsAnn(t.Context()), approval.ID, service.ChatDecision{Approve: true}))
	require.NoError(t, turn.finish().err)
}

func TestChatApprovalIsAnsweredOnce(t *testing.T) {
	setup := newApprovalSetup(toolUse("1", "scale_database", scaleToTwo), textReply("Scaled."))
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)
	approval := turn.nextApproval()

	require.NoError(t, setup.chat.Decide(signedInAsAnn(t.Context()), approval.ID, service.ChatDecision{Approve: true}))
	require.NoError(t, turn.finish().err)

	err := setup.chat.Decide(signedInAsAnn(t.Context()), approval.ID, service.ChatDecision{Approve: true})
	assert.ErrorIs(t, err, service.ErrApprovalNotFound, "a replayed approval does nothing")
	assert.Len(t, changeRequests(setup.api), 1)
}

func TestChatTwoChangesInOneRoundAreAskedOneAtATime(t *testing.T) {
	setup := newApprovalSetup(
		claude.Response{StopReason: "tool_use", Content: []claude.ContentBlock{
			{Type: "tool_use", ID: "1", Name: "scale_database", Input: []byte(scaleToTwo)},
			{Type: "tool_use", ID: "2", Name: "backup_database", Input: []byte(`{"id":"` + ordersID + `"}`)},
		}},
		textReply("Both done."),
	)
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)

	first := turn.nextApproval()
	select {
	case other := <-turn.approvals:
		t.Fatalf("a second card was shown while the first was open: %+v", other)
	case <-time.After(50 * time.Millisecond):
	}
	require.NoError(t, setup.chat.Decide(signedInAsAnn(t.Context()), first.ID, service.ChatDecision{Approve: true}))
	second := turn.nextApproval()
	assert.NotEqual(t, first.ID, second.ID)
	require.NoError(t, setup.chat.Decide(signedInAsAnn(t.Context()), second.ID, service.ChatDecision{Approve: false}))

	require.NoError(t, turn.finish().err)
	results := resultsOf(setup.completer.calls[1])
	require.Len(t, results, 2)
	approvedID := map[string]string{"scale_database": "1", "backup_database": "2"}[first.Tool]
	for _, result := range results {
		assert.Equal(t, result.ToolUseID != approvedID, result.IsError, "only the call that was approved ran: %s", result.ToolUseID)
	}
	assert.Len(t, changeRequests(setup.api), 1)
}

func TestChatCursorChangesAreAskedForLikeAnthropicsAndRecordedAsCursor(t *testing.T) {
	setup := newApprovalSetup(toolUse("1", "scale_database", scaleToTwo), textReply(`{"text":"Scaled.","actions":[]}`))
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderCursor)

	approval := turn.nextApproval()
	assert.Equal(t, "scale_database", approval.Tool)
	assert.Empty(t, changeRequests(setup.api))
	require.NoError(t, setup.chat.Decide(signedInAsAnn(t.Context()), approval.ID, service.ChatDecision{Approve: true}))

	result := turn.finish()
	require.NoError(t, result.err)
	assert.Equal(t, "Scaled.", result.reply.Text)
	sent := changeRequests(setup.api)
	require.Len(t, sent, 1)
	assert.Equal(t, "Cursor", sent[0].Caller.Agent)
	assert.Equal(t, auth.ClientChat, sent[0].Caller.Identity.Client.Kind)
}

func TestChatApprovalCardNeverShowsACredential(t *testing.T) {
	setup := newApprovalSetup(
		toolUse("1", "reply_to_support_ticket", `{"number":"CS0061234","body":"the root password: hunter2 is set"}`),
		textReply("Sent."),
	)
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)

	approval := turn.nextApproval()
	assert.Contains(t, approval.Lines, service.ApprovalLine{Label: "Ticket", Value: "CS0061234"})
	for _, line := range approval.Lines {
		assert.NotContains(t, line.Value, "hunter2")
	}
	require.NoError(t, setup.chat.Decide(signedInAsAnn(t.Context()), approval.ID, service.ChatDecision{Approve: false}))
	require.NoError(t, turn.finish().err)
}

func TestChatDecideRefusesAnUnknownApprovalForANilChat(t *testing.T) {
	var chat *service.Chat

	err := chat.Decide(t.Context(), "x", service.ChatDecision{})

	assert.True(t, errors.Is(err, service.ErrApprovalNotFound))
}

func TestChatChangeWithTwoSpellingsOfAnArgumentIsRefusedWithoutACard(t *testing.T) {
	setup := newApprovalSetup(
		toolUse("1", "scale_database", `{"id":"`+ordersID+`","memoryGb":2,"memorygb":512}`),
		textReply("I could not."),
	)
	turn := setup.start(t, signedInAsAnn(t.Context()), service.ProviderAnthropic)

	require.NoError(t, turn.finish().err)

	select {
	case approval := <-turn.approvals:
		t.Fatalf("a call with two values for one argument was put to the person: %+v", approval)
	default:
	}
	assert.True(t, resultsOf(setup.completer.calls[1])[0].IsError)
	assert.Empty(t, changeRequests(setup.api))
}
