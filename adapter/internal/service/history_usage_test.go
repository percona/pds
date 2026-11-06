package service_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// usageBookContract is what both books have to do with monitoring readings:
// keep them with the sample, and drop samples past the retention cut.
func usageBookContract(t *testing.T, ctx context.Context, book service.HistoryBook, id string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)

	old := now.Add(-service.UsageRetention - time.Hour)
	kept, err := book.RecordUsage(ctx, service.UsagePoint{DatabaseID: id, At: old,
		Monitoring: &service.MonitoringReading{CPU: num(5)}})
	require.NoError(t, err)
	require.True(t, kept)

	kept, err = book.RecordUsage(ctx, service.UsagePoint{DatabaseID: id, At: now,
		Monitoring: &service.MonitoringReading{CPU: num(31), Disk: num(42)}})
	require.NoError(t, err)
	require.True(t, kept)

	kept, err = book.RecordUsage(ctx, service.UsagePoint{DatabaseID: id + "-pending", At: now,
		Monitoring: &service.MonitoringReading{Pending: service.MonitoringNoTarget}})
	require.NoError(t, err)
	require.True(t, kept)

	kept, err = book.RecordUsage(ctx, service.UsagePoint{DatabaseID: id + "-bare", At: now})
	require.NoError(t, err)
	require.True(t, kept)

	samples, err := book.ListUsage(ctx, service.UsageQuery{DatabaseID: id})
	require.NoError(t, err)
	require.Len(t, samples, 2)
	require.NotNil(t, samples[0].Monitoring)
	assert.InDelta(t, 31, *samples[0].Monitoring.CPU, 0.001)
	assert.InDelta(t, 42, *samples[0].Monitoring.Disk, 0.001)
	assert.Nil(t, samples[0].Monitoring.Memory)

	pendingSamples, err := book.ListUsage(ctx, service.UsageQuery{DatabaseID: id + "-pending"})
	require.NoError(t, err)
	require.Len(t, pendingSamples, 1)
	require.NotNil(t, pendingSamples[0].Monitoring)
	assert.Equal(t, service.MonitoringNoTarget, pendingSamples[0].Monitoring.Pending)

	bare, err := book.ListUsage(ctx, service.UsageQuery{DatabaseID: id + "-bare"})
	require.NoError(t, err)
	require.Len(t, bare, 1)
	assert.Nil(t, bare[0].Monitoring, "a sample the agent sent without a reading stays without one")

	removed, err := book.PruneUsage(ctx, now.Add(-service.UsageRetention))
	require.NoError(t, err)
	assert.Equal(t, int64(1), removed)

	samples, err = book.ListUsage(ctx, service.UsageQuery{DatabaseID: id})
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Equal(t, now, samples[0].At.UTC())
}

func TestMemoryBookKeepsMonitoringAndPrunes(t *testing.T) {
	usageBookContract(t, context.Background(), service.NewMemoryBook(fixedTeam(1)), "spoke-a/pds.orders")
}

func TestMemoryBookPrunesOnlyItsTeam(t *testing.T) {
	book := service.NewMemoryBook(fixedTeam(1))
	other := auth.WithIdentity(context.Background(), identityIn(2))
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	_, err := book.RecordUsage(other, service.UsagePoint{DatabaseID: "ns.orders", At: old})
	require.NoError(t, err)

	removed, err := book.PruneUsage(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	assert.Zero(t, removed)

	samples, err := book.ListUsage(other, service.UsageQuery{DatabaseID: "ns.orders"})
	require.NoError(t, err)
	assert.Len(t, samples, 1)
}

func TestDBBookKeepsMonitoringAndPrunes(t *testing.T) {
	url := strings.TrimSpace(os.Getenv("ADAPTER_TEST_DATABASE_URL"))
	if url == "" {
		t.Skip("set ADAPTER_TEST_DATABASE_URL to run usage against PostgreSQL")
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

	usageBookContract(t, ctx, service.NewDBBook(lazy, fixedTeam(team)), "spoke-a/pds.orders")
}
