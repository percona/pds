package oe_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/oe"
)

// The MongoDB provider's own retention deletes a backup with its data and then
// recreates it, so its schedule carries none and the control plane prunes.
func TestAMongoDBScheduleCarriesNoProviderRetention(t *testing.T) {
	in := engine.ScheduleInput{Enabled: true, Period: engine.PeriodDaily, Hour: 2, RetentionCopies: 2}

	got := oe.ApplySchedule(nil, "s3", everest.ProviderMongoDB, in)

	assert.Equal(t, everest.BackupClassMongoDB, got.ClassRef.Name)
	require.Len(t, got.Storages[0].Schedules, 1)
	assert.Nil(t, got.Storages[0].Schedules[0].Retention)
	assert.Equal(t, everest.CountRetention(2), oe.ScheduleSpec(everest.ProviderPostgreSQL, in).Retention,
		"other providers keep their own retention")
}

func TestTheCopiesAMongoDBScheduleKeepsLiveOnTheInstance(t *testing.T) {
	in := engine.ScheduleInput{Enabled: true, Period: engine.PeriodDaily, RetentionCopies: 4}
	meta := everest.ObjectMeta{}
	oe.StampCopies(&meta, everest.ProviderMongoDB, in)
	spec := everest.InstanceSpec{
		ProviderRef: everest.ObjectRef{Name: everest.ProviderMongoDB},
		Backup:      oe.ApplySchedule(nil, "s3", everest.ProviderMongoDB, in),
	}

	assert.Equal(t, int32(4), oe.ScheduleOfInstance(spec, meta).RetentionCopies)

	other := everest.ObjectMeta{}
	oe.StampCopies(&other, everest.ProviderPXC, in)
	assert.Empty(t, other.Annotations, "a provider that prunes needs no annotation")
}

type retentionEverest struct {
	oe.EverestClient
	backups []everest.Backup
}

func (f *retentionEverest) ListInstanceBackups(_ context.Context, _, name string) ([]everest.Backup, error) {
	var out []everest.Backup
	for _, b := range f.backups {
		if b.Spec.InstanceName() == name {
			out = append(out, b)
		}
	}

	return out, nil
}

func mongoBackup(name, instance, schedule, state string, finished time.Time) everest.Backup {
	return everest.Backup{
		Metadata: everest.ObjectMeta{Name: name},
		Spec: everest.BackupSpec{
			Origin:       everest.InstanceBackupOrigin(instance),
			ScheduleName: schedule,
		},
		Status: everest.BackupStatus{State: state, CompletedAt: &finished},
	}
}

func prunedNames(t *testing.T, backups []everest.Backup, copies int32, instance string) []string {
	t.Helper()
	var deleted []string
	plugin := oe.New(&retentionEverest{backups: backups}, oe.MongoDB,
		oe.WithBackupDeleter(func(_ context.Context, _, name string) error {
			deleted = append(deleted, name)

			return nil
		}))

	_, err := plugin.PruneBackups(context.Background(), []engine.Instance{{
		Name: instance, Namespace: "db", Engine: oe.MongoDB.ID,
		BackupSchedule: &engine.BackupSchedule{Name: engine.ScheduleName, RetentionCopies: copies},
	}})
	require.NoError(t, err)

	return deleted
}

func TestPruneDeletesTheOldestScheduledBackupsBeyondTheCount(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	backups := []everest.Backup{
		mongoBackup("c3", "db1", engine.ScheduleName, everest.BackupStateSucceeded, at.Add(3*time.Hour)),
		mongoBackup("c1", "db1", engine.ScheduleName, everest.BackupStateSucceeded, at.Add(1*time.Hour)),
		mongoBackup("c2", "db1", engine.ScheduleName, everest.BackupStateSucceeded, at.Add(2*time.Hour)),
		mongoBackup("manual", "db1", "", everest.BackupStateSucceeded, at),
		mongoBackup("other", "db2", engine.ScheduleName, everest.BackupStateSucceeded, at),
		mongoBackup("failed", "db1", engine.ScheduleName, everest.BackupStateFailed, at),
		mongoBackup("running", "db1", engine.ScheduleName, everest.BackupStateRunning, at),
	}

	assert.Equal(t, []string{"c1"}, prunedNames(t, backups, 2, "db1"),
		"only the oldest succeeded backup of this schedule goes: on demand, failed, running and other databases stay")
	assert.Empty(t, prunedNames(t, backups, 3, "db1"), "at the count nothing is deleted")
}

func TestPruneLeavesBackupsAloneWhenNoCountIsKept(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	backups := []everest.Backup{
		mongoBackup("a", "db1", engine.ScheduleName, everest.BackupStateSucceeded, at),
		mongoBackup("b", "db1", engine.ScheduleName, everest.BackupStateSucceeded, at.Add(time.Hour)),
	}

	assert.Empty(t, prunedNames(t, backups, 0, "db1"))
}

func TestPruneSkipsASwitchedOffScheduleAndOtherEngines(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	backups := []everest.Backup{
		mongoBackup("a", "db1", engine.ScheduleName, everest.BackupStateSucceeded, at),
		mongoBackup("b", "db1", engine.ScheduleName, everest.BackupStateSucceeded, at.Add(time.Hour)),
	}
	var deleted []string
	plugin := oe.New(&retentionEverest{backups: backups}, oe.MongoDB,
		oe.WithBackupDeleter(func(_ context.Context, _, name string) error {
			deleted = append(deleted, name)

			return nil
		}))

	_, err := plugin.PruneBackups(context.Background(), []engine.Instance{
		{Name: "db1", Namespace: "db", Engine: oe.MongoDB.ID,
			BackupSchedule: &engine.BackupSchedule{Name: engine.ScheduleName, Disabled: true, RetentionCopies: 1}},
		{Name: "db1", Namespace: "db", Engine: oe.MySQL.ID,
			BackupSchedule: &engine.BackupSchedule{Name: engine.ScheduleName, RetentionCopies: 1}},
	})

	require.NoError(t, err)
	assert.Empty(t, deleted, "turning the schedule off keeps the old backups, and other engines prune themselves")
}

func TestPruneWithoutADeleterDoesNothing(t *testing.T) {
	plugin := oe.New(&retentionEverest{}, oe.MongoDB)

	n, err := plugin.PruneBackups(context.Background(), []engine.Instance{{Engine: oe.MongoDB.ID}})

	require.NoError(t, err)
	assert.Zero(t, n)
}
