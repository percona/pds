package service_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/service"
)

// A backup whose start is not the provider's to report gets no start and no
// duration in the history, only the finish time.
func TestBackupEntriesOfAFinishOnlyBackupHaveNoDuration(t *testing.T) {
	created := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	finished := created.Add(5 * time.Minute)

	entries := service.BackupEntries("spoke-a", "spoke-a/pds.orders", []engine.Backup{
		{ID: "mongo", Status: engine.BackupSucceeded, CreatedAt: created, FinishedAt: finished, FinishOnly: true},
		{ID: "other", Status: engine.BackupSucceeded, CreatedAt: created, FinishedAt: finished},
	}, finished)

	assert.True(t, entries[0].StartedAt.IsZero())
	assert.Equal(t, finished, entries[0].FinishedAt)
	assert.Zero(t, entries[0].DurationMS)
	assert.Equal(t, created, entries[1].StartedAt)
	assert.Equal(t, (5 * time.Minute).Milliseconds(), entries[1].DurationMS)
}
