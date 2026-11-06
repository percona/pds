package oe_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/oe"
)

func TestBackupClassFor(t *testing.T) {
	assert.Equal(t, everest.BackupClassPXC, oe.BackupClassFor(everest.ProviderPXC))
	assert.Equal(t, everest.BackupClassPostgreSQL, oe.BackupClassFor(everest.ProviderPostgreSQL))
	assert.Equal(t, everest.BackupClassPXC, oe.BackupClassFor(""))
}

func TestApplyScheduleOnASpecWithNoBackupSection(t *testing.T) {
	weekly := engine.ScheduleInput{Enabled: true, Period: engine.PeriodWeekly, Hour: 1, Weekday: 3, RetentionCopies: 5}

	got := oe.ApplySchedule(nil, "s3", everest.BackupClassPostgreSQL, weekly)

	require.NotNil(t, got)
	assert.True(t, got.Enabled)
	assert.Equal(t, everest.BackupClassPostgreSQL, got.ClassRef.Name)
	require.Len(t, got.Storages, 1)
	assert.Equal(t, "s3", got.Storages[0].StorageRef.Name)
	require.Len(t, got.Storages[0].Schedules, 1)
	assert.Equal(t, everest.InstanceBackupSchedule{
		Name: engine.ScheduleName, Enabled: true, Cron: "0 1 * * 3", Retention: everest.CountRetention(5),
	}, got.Storages[0].Schedules[0])
}

// TestApplyScheduleKeepsOneOwnSchedule covers a spec that somehow has the
// control plane's schedule on a second storage: the write leaves exactly one.
func TestApplyScheduleKeepsOneOwnSchedule(t *testing.T) {
	current := &everest.InstanceBackupSpec{
		Enabled:  true,
		ClassRef: everest.ObjectRef{Name: "custom-class"},
		Storages: []everest.InstanceBackupStorage{
			{StorageRef: everest.ObjectRef{Name: "a"}, Schedules: []everest.InstanceBackupSchedule{{Name: "other", Enabled: true, Cron: "0 0 1 * *"}}},
			{StorageRef: everest.ObjectRef{Name: "b"}, Schedules: []everest.InstanceBackupSchedule{{Name: engine.ScheduleName, Enabled: true, Cron: "0 2 * * *"}}},
		},
	}

	got := oe.ApplySchedule(current, "ignored", everest.BackupClassPXC, engine.DefaultSchedule())

	assert.Equal(t, "custom-class", got.ClassRef.Name, "a class already on the spec is kept")
	own := 0
	for _, s := range got.Storages {
		for _, schedule := range s.Schedules {
			if schedule.Name == engine.ScheduleName {
				own++
			}
		}
	}
	assert.Equal(t, 1, own)
	assert.Equal(t, engine.ScheduleName, got.Storages[0].Schedules[0].Name)
	assert.Equal(t, "other", got.Storages[0].Schedules[1].Name)
	assert.False(t, got.Storages[0].Schedules[1].Enabled, "a second running schedule is turned off")
	assert.Equal(t, "0 0 1 * *", got.Storages[0].Schedules[1].Cron, "and keeps its settings")
	assert.Empty(t, got.Storages[1].Schedules)
	assert.Equal(t, "0 2 * * *", current.Storages[1].Schedules[0].Cron, "the input spec is not edited")
	assert.True(t, current.Storages[0].Schedules[0].Enabled, "the input spec is not edited")
}

func TestScheduleOfPrefersARunningSchedule(t *testing.T) {
	spec := &everest.InstanceBackupSpec{Storages: []everest.InstanceBackupStorage{{
		Schedules: []everest.InstanceBackupSchedule{
			{Name: engine.ScheduleName, Enabled: false, Cron: "0 2 * * *"},
			{Name: "other", Enabled: true, Cron: "0 3 * * *"},
		},
	}}}

	got := oe.ScheduleOf(spec)

	require.NotNil(t, got)
	assert.False(t, got.Disabled)
	assert.Equal(t, "0 3 * * *", got.Cron)
}

// TestScheduleOfReportsAForeignDisabledSchedule covers a database whose only
// schedule was written outside the control plane and then turned off: it
// reads as off, not as on-demand.
func TestScheduleOfReportsAForeignDisabledSchedule(t *testing.T) {
	spec := &everest.InstanceBackupSpec{Storages: []everest.InstanceBackupStorage{{
		Schedules: []everest.InstanceBackupSchedule{{Name: "other", Enabled: false, Cron: "0 3 * * *"}},
	}}}

	got := oe.ScheduleOf(spec)

	require.NotNil(t, got)
	assert.True(t, got.Disabled)
	assert.Equal(t, "other", got.Name)
	assert.Equal(t, "0 3 * * *", got.Cron)
}

func TestScheduleOfPrefersTheOwnDisabledSchedule(t *testing.T) {
	spec := &everest.InstanceBackupSpec{Storages: []everest.InstanceBackupStorage{{
		Schedules: []everest.InstanceBackupSchedule{
			{Name: "other", Enabled: false, Cron: "0 3 * * *"},
			{Name: engine.ScheduleName, Enabled: false, Cron: "0 2 * * *"},
		},
	}}}

	got := oe.ScheduleOf(spec)

	require.NotNil(t, got)
	assert.Equal(t, engine.ScheduleName, got.Name)
}

// TestTurningOffAForeignOnlyScheduleReadsAsOff is the path through the
// plugin's off branch: the result and the next read both report it as off.
func TestTurningOffAForeignOnlyScheduleReadsAsOff(t *testing.T) {
	current := &everest.InstanceBackupSpec{Enabled: true, Storages: []everest.InstanceBackupStorage{{
		Schedules: []everest.InstanceBackupSchedule{{Name: "nightly", Enabled: true, Cron: "0 3 */2 * *", Retention: everest.CountRetention(5)}},
	}}}

	got, changed := oe.DisableSchedule(current)

	require.True(t, changed)
	read := oe.ScheduleOf(got)
	require.NotNil(t, read)
	assert.True(t, read.Disabled)
	assert.Equal(t, "0 3 */2 * *", read.Cron)
	assert.Equal(t, int32(5), read.RetentionCopies)
}

func TestDisableScheduleKeepsSettings(t *testing.T) {
	current := oe.ApplySchedule(nil, "s3", everest.BackupClassPXC,
		engine.ScheduleInput{Enabled: true, Period: engine.PeriodHourly, Minute: 7, RetentionCopies: 2})

	got, changed := oe.DisableSchedule(current)

	require.True(t, changed)
	assert.True(t, got.Enabled, "backups stay on so existing copies are kept")
	require.Len(t, got.Storages[0].Schedules, 1)
	assert.False(t, got.Storages[0].Schedules[0].Enabled)
	assert.Equal(t, "7 * * * *", got.Storages[0].Schedules[0].Cron)
	assert.Equal(t, int32(2), got.Storages[0].Schedules[0].Copies())
	assert.True(t, current.Storages[0].Schedules[0].Enabled, "the input spec is not edited")

	_, changed = oe.DisableSchedule(got)
	assert.False(t, changed, "turning off a schedule that is off changes nothing")

	_, changed = oe.DisableSchedule(nil)
	assert.False(t, changed)
}

// TestDisableScheduleStopsEveryRunningSchedule covers a spec edited outside
// the control plane with two running schedules: off has to stop both.
func TestDisableScheduleStopsEveryRunningSchedule(t *testing.T) {
	current := &everest.InstanceBackupSpec{
		Enabled: true,
		Storages: []everest.InstanceBackupStorage{
			{Schedules: []everest.InstanceBackupSchedule{{Name: engine.ScheduleName, Enabled: true, Cron: "0 2 * * *"}}},
			{Schedules: []everest.InstanceBackupSchedule{{Name: "audit", Enabled: true, Cron: "0 0 1 * *", Retention: everest.CountRetention(3)}}},
		},
	}

	got, changed := oe.DisableSchedule(current)

	require.True(t, changed)
	assert.False(t, got.Storages[0].Schedules[0].Enabled)
	assert.False(t, got.Storages[1].Schedules[0].Enabled)
	assert.Equal(t, "0 0 1 * *", got.Storages[1].Schedules[0].Cron)
	assert.Equal(t, int32(3), got.Storages[1].Schedules[0].Copies())
	assert.False(t, oe.ScheduleOf(got).Runs(), "nothing runs")
}

func TestScheduleOfReadsCountRetentionAndNotATimeWindow(t *testing.T) {
	spec := func(r *everest.ScheduleRetention) *everest.InstanceBackupSpec {
		return &everest.InstanceBackupSpec{Enabled: true, Storages: []everest.InstanceBackupStorage{{
			Schedules: []everest.InstanceBackupSchedule{{Name: engine.ScheduleName, Enabled: true, Cron: "0 2 * * *", Retention: r}},
		}}}
	}

	assert.Equal(t, int32(7), oe.ScheduleOf(spec(everest.CountRetention(7))).RetentionCopies)
	assert.Zero(t, oe.ScheduleOf(spec(&everest.ScheduleRetention{Type: everest.RetentionTypeTime, Duration: "4w"})).RetentionCopies)
	assert.Zero(t, oe.ScheduleOf(spec(nil)).RetentionCopies)
}

func TestScheduleOfKeepsATimeRetentionWindow(t *testing.T) {
	spec := &everest.InstanceBackupSpec{Enabled: true, Storages: []everest.InstanceBackupStorage{{
		Schedules: []everest.InstanceBackupSchedule{{
			Name: engine.ScheduleName, Enabled: true, Cron: "0 2 * * *",
			Retention: &everest.ScheduleRetention{Type: everest.RetentionTypeTime, Duration: "4w"},
		}},
	}}}

	assert.Equal(t, "4w", oe.ScheduleOf(spec).RetentionWindow)
	assert.Empty(t, oe.ScheduleOf(&everest.InstanceBackupSpec{Storages: []everest.InstanceBackupStorage{{
		Schedules: []everest.InstanceBackupSchedule{{Enabled: true, Retention: everest.CountRetention(3)}},
	}}}).RetentionWindow)
}
