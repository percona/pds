package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

// A team whose environments stopped reporting sends no heartbeat to prune on, so
// the store prunes every team's old samples at once.
func TestPruneUsageCoversEveryTeam(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(42))
	require.NoError(t, err)
	ctx := t.Context()
	// The delete is not scoped to a team, and other packages' tests share
	// this database while they run. Fixed dates far in the past keep the cut
	// below anything those tests write, so this test deletes only its own rows.
	old := time.Date(2001, 1, 1, 12, 0, 0, 0, time.UTC)
	now := time.Date(2001, 1, 10, 12, 0, 0, 0, time.UTC)
	cut := time.Date(2001, 1, 5, 0, 0, 0, 0, time.UTC)

	quiet := db.Hub(nextTeam(t, db), cipher)
	busy := db.Hub(nextTeam(t, db), cipher)
	for _, h := range []*store.HubState{quiet, busy} {
		_, kept, err := h.InsertUsageIfDue(ctx, store.UsageSample{DatabaseID: "spoke-a/pds.orders", At: old}, time.Minute)
		require.NoError(t, err)
		require.True(t, kept)
		_, kept, err = h.InsertUsageIfDue(ctx, store.UsageSample{DatabaseID: "spoke-a/pds.orders", At: now}, time.Minute)
		require.NoError(t, err)
		require.True(t, kept)
	}

	removed, err := db.PruneUsage(ctx, cut)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, removed, int64(2))

	for _, h := range []*store.HubState{quiet, busy} {
		rows, err := h.ListUsage(ctx, "spoke-a/pds.orders", time.Time{}, time.Time{}, 10)
		require.NoError(t, err)
		require.Len(t, rows, 1, "the old sample is gone and the new one stayed")
		assert.WithinDuration(t, now, rows[0].At, time.Second)
	}
}

func TestStarterPruneUsageBeforeTheDatabaseIsUp(t *testing.T) {
	var starter *store.Starter
	_, err := starter.PruneUsage(t.Context(), time.Now())

	require.Error(t, err)
}
