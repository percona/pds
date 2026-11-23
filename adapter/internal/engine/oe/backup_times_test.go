package oe

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/percona/pds/adapter/internal/client/everest"
)

// The provider stamps the finish time with the moment it noticed, and the
// Backup object's creation can precede the real start by minutes. MongoDB
// shows the finish time it reports and nothing else.
func TestToBackupOfMongoDBReportsOnlyTheFinishTime(t *testing.T) {
	created := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	finished := created.Add(7 * time.Minute)
	in := everest.Backup{
		Metadata: everest.ObjectMeta{Name: "cron-orders-1", CreationTimestamp: &created},
		Spec:     everest.BackupSpec{Origin: everest.InstanceBackupOrigin("orders"), ScheduleName: "daily"},
		Status:   everest.BackupStatus{State: everest.BackupStateSucceeded, CompletedAt: &finished},
	}

	mongo := New(nil, MongoDB).toBackup(in)
	mysql := New(nil, MySQL).toBackup(in)

	assert.True(t, mongo.FinishOnly)
	assert.Equal(t, finished, mongo.FinishedAt)
	assert.True(t, mongo.Scheduled)
	assert.False(t, mysql.FinishOnly)
}
