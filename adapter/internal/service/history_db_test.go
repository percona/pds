package service_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

func TestDBBookKeepsHistoryUsageAndJoins(t *testing.T) {
	url := strings.TrimSpace(os.Getenv("ADAPTER_TEST_DATABASE_URL"))
	if url == "" {
		t.Skip("set ADAPTER_TEST_DATABASE_URL to run history against PostgreSQL")
	}

	team := liveTeam(t, url)
	ctx := auth.WithIdentity(t.Context(), identityIn(team))
	starter := store.Start(ctx, store.Options{URL: url})
	t.Cleanup(func() { _ = starter.Close() })
	lazy := store.NewLazy(starter, team, nil)
	waitCtx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	_, err := lazy.Wait(waitCtx)
	require.NoError(t, err)

	book := service.NewDBBook(lazy, fixedTeam(team))
	require.NoError(t, book.RecordEntry(ctx, service.HistoryEntry{
		DatabaseID: "ns.orders", Kind: service.HistoryAction, Action: service.ActionDatabaseCreate,
		Target: "orders", Outcome: service.OutcomeSucceeded, Client: auth.ClientChat, Agent: "Anthropic",
	}))
	page, err := book.ListActions(ctx, service.ActivityQuery{DatabaseID: "ns.orders"})
	require.NoError(t, err)
	require.NotEmpty(t, page.Entries)
	got, err := book.GetAction(ctx, page.Entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, service.ActionDatabaseCreate, got.Action)
	assert.Equal(t, auth.ClientChat, got.Client)
	assert.Equal(t, "Anthropic", got.Agent)
	assert.Equal(t, auth.ClientChat, page.Entries[0].Client)
	assert.Equal(t, "Anthropic", page.Entries[0].Agent)

	started := time.Now().UTC().Add(-time.Minute)
	book.Observe(ctx, "spoke-a",
		[]engine.Instance{{ID: "ns.orders", Status: engine.Status{Phase: engine.PhaseProvisioning}}},
		[]engine.Instance{{ID: "ns.orders", Status: engine.Status{Phase: engine.PhaseReady}}},
		[]engine.Backup{{ID: "bak-db", Instance: "ns.orders", Status: engine.BackupSucceeded, SizeBytes: 8, CreatedAt: started, FinishedAt: time.Now().UTC()}},
		nil, nil, []service.UsagePoint{{DatabaseID: "ns.orders", StorageBytes: ptr(int64(3)), MetricsReason: "metrics-server did not answer"}},
	)
	backups, err := book.ListEntries(ctx, service.HistoryQuery{DatabaseID: "ns.orders", Kind: service.HistoryBackup})
	require.NoError(t, err)
	require.NotEmpty(t, backups.Entries)
	assert.Equal(t, int64(8), backups.Entries[0].SizeBytes)

	kept, err := book.RecordUsage(ctx, service.UsagePoint{DatabaseID: "ns.orders", At: time.Now().UTC()})
	require.NoError(t, err)
	assert.False(t, kept, "a second sample inside five minutes is dropped")

	require.NoError(t, book.RecordPhase(ctx, service.JoinStep{
		Environment: "spoke-a", RunID: "run-db", Phase: "installing", Outcome: "failed",
		DurationMS: 10, Output: "token=jnt_live",
	}))
	runs, err := book.ListJoins(ctx, "spoke-a")
	require.NoError(t, err)
	require.NotEmpty(t, runs)
	assert.NotContains(t, runs[0].Phases[0].Output, "jnt_live")
}

// liveTeam makes an Organization with one Team in the live database and returns
// the Team's id, so a test has rows of its own.
func liveTeam(t *testing.T, url string) uint {
	t.Helper()

	db, err := store.Open(t.Context(), store.Options{URL: url})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Migrate(t.Context()))

	now := time.Now().UTC()
	org := store.Organization{Name: fmt.Sprintf("service-it-%d", now.UnixNano()), CreatedAt: now}
	require.NoError(t, db.Gorm().Create(&org).Error)
	team := store.Team{OrganizationID: org.ID, Name: "default", CreatedAt: now}
	require.NoError(t, db.Gorm().Create(&team).Error)

	return team.ID
}
