package oe

import (
	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
)

// BackupClassFor is the backup class an instance of provider writes with. A
// provider this package does not serve gets the MySQL class, which is what
// every instance got before PostgreSQL was a choice.
func BackupClassFor(provider string) string {
	if provider == PostgreSQL.Provider {
		return PostgreSQL.BackupClass
	}

	return MySQL.BackupClass
}

// ScheduleSpec is the provider's shape of the one schedule the control plane
// writes.
func ScheduleSpec(in engine.ScheduleInput) everest.InstanceBackupSchedule {
	return everest.InstanceBackupSchedule{
		Name:      engine.ScheduleName,
		Enabled:   true,
		Cron:      in.Cron(),
		Retention: everest.CountRetention(int32(in.RetentionCopies)),
	}
}

// ScheduleOf reads the schedule a backup spec carries, as the UI states it.
// An enabled schedule wins over a disabled one, so a database that still runs
// a schedule never reads as off. Among the disabled, the control plane's own
// comes first, since that is the one turning back on restores; any other
// disabled schedule is still reported, so a database whose only schedule was
// turned off reads as off rather than as on-demand.
func ScheduleOf(backup *everest.InstanceBackupSpec) *engine.BackupSchedule {
	if backup == nil {
		return nil
	}

	var own, other *everest.InstanceBackupSchedule
	for i := range backup.Storages {
		for j := range backup.Storages[i].Schedules {
			schedule := &backup.Storages[i].Schedules[j]
			switch {
			case schedule.Enabled:
				return toBackupSchedule(*schedule)
			case schedule.Name == engine.ScheduleName && own == nil:
				own = schedule
			case other == nil:
				other = schedule
			}
		}
	}
	switch {
	case own != nil:
		return toBackupSchedule(*own)
	case other != nil:
		return toBackupSchedule(*other)
	default:
		return nil
	}
}

func toBackupSchedule(in everest.InstanceBackupSchedule) *engine.BackupSchedule {
	out := &engine.BackupSchedule{
		Name:            in.Name,
		Disabled:        !in.Enabled,
		Cron:            in.Cron,
		RetentionCopies: in.Copies(),
	}
	if in.Retention != nil && in.Retention.Type == everest.RetentionTypeTime {
		out.RetentionWindow = in.Retention.Duration
	}

	return out
}

// ApplySchedule writes in onto a copy of current as the control plane's one
// schedule. The entry keeps its name, so an edit replaces it in place, and it
// moves to the first storage if an older spec had it elsewhere. A schedule the
// control plane did not write is kept but turned off, so the database never
// runs two and the one it had can still be read back.
//
// storage and class fill in what a spec with no backup section lacks. A class
// already on the spec is kept, since changing it would strand the backups
// taken under it.
func ApplySchedule(current *everest.InstanceBackupSpec, storage, class string, in engine.ScheduleInput) *everest.InstanceBackupSpec {
	out := everest.InstanceBackupSpec{Enabled: true, ClassRef: everest.ObjectRef{Name: class}}
	if current != nil && current.ClassRef.Name != "" {
		out.ClassRef = current.ClassRef
	}

	if current != nil {
		out.Storages = make([]everest.InstanceBackupStorage, len(current.Storages))
		for i, s := range current.Storages {
			out.Storages[i] = everest.InstanceBackupStorage{
				StorageRef: s.StorageRef,
				Schedules:  withoutOwnSchedule(s.Schedules),
			}
		}
	}
	if len(out.Storages) == 0 {
		out.Storages = []everest.InstanceBackupStorage{{StorageRef: everest.ObjectRef{Name: storage}}}
	}

	out.Storages[0].Schedules = append([]everest.InstanceBackupSchedule{ScheduleSpec(in)}, out.Storages[0].Schedules...)

	return &out
}

// DisableSchedule turns off, on a copy of current, every running schedule,
// keeping each one's cron and retention so turning it back on restores them.
// A spec edited outside the control plane can carry more than one, and off
// has to mean no scheduled backup runs. Backups stay enabled on the instance,
// so the copies already taken are kept. False means nothing was running.
func DisableSchedule(current *everest.InstanceBackupSpec) (*everest.InstanceBackupSpec, bool) {
	if current == nil {
		return nil, false
	}

	out := *current
	out.Storages = make([]everest.InstanceBackupStorage, len(current.Storages))
	changed := false
	for i, s := range current.Storages {
		schedules := make([]everest.InstanceBackupSchedule, len(s.Schedules))
		copy(schedules, s.Schedules)
		for j := range schedules {
			if schedules[j].Enabled {
				schedules[j].Enabled = false
				changed = true
			}
		}
		out.Storages[i] = everest.InstanceBackupStorage{StorageRef: s.StorageRef, Schedules: schedules}
	}
	if !changed {
		return current, false
	}

	return &out, true
}

// withoutOwnSchedule drops the control plane's schedule and turns every other
// one off, ready for the one ApplySchedule writes.
func withoutOwnSchedule(schedules []everest.InstanceBackupSchedule) []everest.InstanceBackupSchedule {
	out := make([]everest.InstanceBackupSchedule, 0, len(schedules))
	for _, schedule := range schedules {
		if schedule.Name == engine.ScheduleName {
			continue
		}
		schedule.Enabled = false
		out = append(out, schedule)
	}

	return out
}
