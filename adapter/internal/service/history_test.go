package service_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/service"
)

// asTeam is a request in the Team.
func asTeam(team uint) context.Context {
	return auth.WithIdentity(context.Background(), identityIn(team))
}

// identityIn is a caller acting in the Team.
func identityIn(team uint) auth.Identity {
	return auth.Identity{Scope: auth.Scope{Team: auth.Team{ID: team}}}
}

// fixedTeam is the Team a call with no request Team is recorded under.
func fixedTeam(team uint) service.TeamFallback {
	return func(context.Context) uint { return team }
}

func TestHistoryKeepsTheClientOfAnAction(t *testing.T) {
	book := service.NewMemoryBook(nil)
	book.RecordAction(asTeam(1), service.ActivityEvent{
		Action: service.ActionDatabaseCreate, DatabaseID: "ns.orders", Target: "orders", Client: auth.ClientMCP,
	})

	page, err := book.ListActions(asTeam(1), service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, auth.ClientMCP, page.Entries[0].Client)

	got, err := book.GetAction(asTeam(1), page.Entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, auth.ClientMCP, got.Client)
}

func TestHistoryRecordsActionsPerTeam(t *testing.T) {
	book := service.NewMemoryBook(nil)
	book.RecordAction(asTeam(1), service.ActivityEvent{
		Action: service.ActionDatabaseCreate, DatabaseID: "ns.orders", Target: "orders", Outcome: service.OutcomeSucceeded,
	})
	book.RecordAction(asTeam(2), service.ActivityEvent{
		Action: service.ActionDatabaseDelete, DatabaseID: "ns.orders", Target: "orders", Outcome: service.OutcomeSucceeded,
	})

	acme, err := book.ListActions(asTeam(1), service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, acme.Entries, 1)
	assert.Equal(t, service.ActionDatabaseCreate, acme.Entries[0].Action)
	got, err := book.GetAction(asTeam(1), acme.Entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, service.ActionDatabaseCreate, got.Action)
	_, err = book.GetAction(asTeam(1), "00000000000000000099")
	assert.ErrorIs(t, err, service.ErrActivityEventNotFound)
	globex, err := book.ListEntries(asTeam(2), service.HistoryQuery{Kind: service.HistoryAction})
	require.NoError(t, err)
	require.Len(t, globex.Entries, 1)
	assert.Equal(t, service.ActionDatabaseDelete, globex.Entries[0].Action)

	_, err = book.ListActions(context.Background(), service.ActivityQuery{})
	assert.ErrorIs(t, err, service.ErrActivityUnavailable)
}

func TestHistoryKeepsBackupAfterItWouldLeaveTheEnvironment(t *testing.T) {
	book := service.NewMemoryBook(fixedTeam(1))
	ctx := context.Background()
	started := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	finished := started.Add(90 * time.Second)
	book.Observe(ctx, "spoke-a", nil, nil, []engine.Backup{{
		ID: "bak-1", Instance: "orders", Status: engine.BackupRunning, CreatedAt: started,
	}}, nil, nil, nil)
	book.Observe(ctx, "spoke-a", nil, nil, []engine.Backup{{
		ID: "bak-1", Instance: "orders", Status: engine.BackupSucceeded, SizeBytes: 4096,
		CreatedAt: started, FinishedAt: finished,
	}}, nil, nil, nil)
	// The object is gone. Nothing new is observed, and the row stays.
	book.Observe(ctx, "spoke-a", nil, nil, nil, nil, nil, nil)

	page, err := book.ListEntries(ctx, service.HistoryQuery{DatabaseID: "orders", Kind: service.HistoryBackup})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, engine.BackupSucceeded, page.Entries[0].Outcome)
	assert.Equal(t, int64(4096), page.Entries[0].SizeBytes)
	assert.Equal(t, int64(90000), page.Entries[0].DurationMS)
}

func TestHistoryTransitionsAndUsageInterval(t *testing.T) {
	book := service.NewMemoryBook(fixedTeam(1))
	ctx := context.Background()
	prev := []engine.Instance{{ID: "ns.orders", Status: engine.Status{Phase: engine.PhaseProvisioning}}}
	next := []engine.Instance{{ID: "ns.orders", Status: engine.Status{Phase: engine.PhaseReady}}}
	book.Observe(ctx, "", prev, next, nil, nil, nil, nil)
	book.Observe(ctx, "", prev, next, nil, nil, nil, nil)
	book.Observe(ctx, "", next, next, nil, nil, nil, nil)

	page, err := book.ListEntries(ctx, service.HistoryQuery{Kind: service.HistoryTransition, DatabaseID: "ns.orders"})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, engine.PhaseProvisioning, page.Entries[0].FromPhase)
	assert.Equal(t, engine.PhaseReady, page.Entries[0].ToPhase)
	assert.Equal(t, engine.PhaseReady, book.LastPhases(ctx)["ns.orders"])

	at := time.Now().UTC()
	kept, err := book.RecordUsage(ctx, service.UsagePoint{DatabaseID: "ns.orders", At: at, StorageBytes: ptr(int64(100))})
	require.NoError(t, err)
	assert.True(t, kept)
	kept, err = book.RecordUsage(ctx, service.UsagePoint{DatabaseID: "ns.orders", At: at.Add(time.Minute), StorageBytes: ptr(int64(110))})
	require.NoError(t, err)
	assert.False(t, kept)
	kept, err = book.RecordUsage(ctx, service.UsagePoint{
		DatabaseID: "ns.orders", At: at.Add(service.UsageInterval), StorageBytes: ptr(int64(120)),
		MetricsReason: "metrics-server did not answer",
	})
	require.NoError(t, err)
	assert.True(t, kept)

	samples, err := book.ListUsage(ctx, service.UsageQuery{DatabaseID: "ns.orders"})
	require.NoError(t, err)
	require.Len(t, samples, 2)
	assert.Nil(t, samples[0].CPUAvg)
	assert.Equal(t, "metrics-server did not answer", samples[0].MetricsReason)
}

func TestJoinStepJSONUsesLowerCamel(t *testing.T) {
	raw, err := json.Marshal(service.JoinStep{Phase: "installing", Outcome: "failed", DurationMS: 12, Output: "tail"})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"phase":"installing"`)
	assert.Contains(t, string(raw), `"durationMs":12`)
	assert.NotContains(t, string(raw), `"Phase"`)
}

func TestScrubTailRemovesCredentials(t *testing.T) {
	raw := stringsJoin(
		"AKIAIOSFODNN7EXAMPLE",
		"ASIAIOSFODNN7EXAMPLE",
		"aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"password=s3cret",
		`password="secret with spaces"`,
		"token: jnt_live",
		"Authorization: Bearer opaque-join-token",
		"CLIENT_TOKEN=client-secret-value",
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0.signaturevalue",
		"client-key-data: LS0tLS1CRUdJTi",
		"-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----",
	)
	got := service.ScrubTail(raw)
	for _, secret := range []string{
		"AKIAIOSFODNN7EXAMPLE",
		"ASIAIOSFODNN7EXAMPLE",
		"wJalrXUtnFEMI",
		"s3cret",
		"secret with spaces",
		"jnt_live",
		"opaque-join-token",
		"client-secret-value",
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		"LS0tLS1CRUdJTi",
		"BEGIN RSA PRIVATE KEY",
	} {
		assert.NotContains(t, got, secret)
	}
}

func TestJoinPhaseKeepsScrubbedTail(t *testing.T) {
	book := service.NewMemoryBook(fixedTeam(1))
	ctx := context.Background()
	start := time.Now().UTC().Add(-2 * time.Minute)
	require.NoError(t, book.RecordPhase(ctx, service.JoinStep{
		Environment: "spoke-a", RunID: "run-1", Phase: "installing", Outcome: "failed",
		StartedAt: start, FinishedAt: start.Add(time.Minute), DurationMS: 60000,
		Output: "boom password=hunter2 AKIAIOSFODNN7EXAMPLE",
	}))
	runs, err := book.ListJoins(ctx, "spoke-a")
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Len(t, runs[0].Phases, 1)
	assert.Equal(t, "failed", runs[0].Phases[0].Outcome)
	assert.Equal(t, int64(60000), runs[0].Phases[0].DurationMS)
	assert.NotContains(t, runs[0].Phases[0].Output, "hunter2")
	assert.NotContains(t, runs[0].Phases[0].Output, "AKIAIOSFODNN7EXAMPLE")

	other, err := book.ListJoins(asTeam(2), "spoke-a")
	require.NoError(t, err)
	assert.Empty(t, other)
}

func TestHistoryDoesNotPrune(t *testing.T) {
	book := service.NewMemoryBook(fixedTeam(1))
	ctx := context.Background()
	for range service.MaxActivityEvents + 5 {
		book.RecordAction(ctx, service.ActivityEvent{Action: service.ActionDatabaseScale, Target: "orders"})
	}
	page, err := book.ListActions(ctx, service.ActivityQuery{Limit: service.MaxActivityEvents + 10})
	require.NoError(t, err)
	assert.Len(t, page.Entries, service.MaxActivityEvents)
	assert.True(t, page.Truncated)
	rest, err := book.ListEntries(ctx, service.HistoryQuery{
		Kind: service.HistoryAction, Cursor: page.Entries[len(page.Entries)-1].ID, Limit: 20,
	})
	require.NoError(t, err)
	assert.Len(t, rest.Entries, 5)
}

func ptr[T any](v T) *T { return &v }

func stringsJoin(lines ...string) string {
	out := ""
	for i, line := range lines {
		if i > 0 {
			out += "\n"
		}
		out += line
	}

	return out
}
