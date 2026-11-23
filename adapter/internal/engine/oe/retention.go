package oe

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
)

// BackupDeleter deletes one Backup object, and with it the data it wrote to the
// backup storage.
type BackupDeleter func(ctx context.Context, namespace, name string) error

// WithBackupDeleter lets the plugin prune the scheduled backups of a provider
// whose retention the control plane keeps. Without one it prunes nothing.
func WithBackupDeleter(deleter BackupDeleter) Option {
	return func(p *Plugin) {
		p.deleteBackup = deleter
	}
}

// PruneBackups deletes, for each of this engine's instances whose schedule is
// running, the succeeded backups of that schedule beyond the count it keeps,
// oldest first. A backup that is on demand, still running, failed, or taken by
// another schedule or database is never counted or deleted, and turning the
// schedule off keeps every backup already taken. It returns how many it
// deleted. One backup failing to delete does not stop the others: the first
// error is returned after the pass.
func (p *Plugin) PruneBackups(ctx context.Context, instances []engine.Instance) (int, error) {
	if p.deleteBackup == nil || !PrunedByControlPlane(p.flavor.Provider) {
		return 0, nil
	}

	var (
		deleted  int
		firstErr error
	)
	for _, item := range instances {
		schedule := item.BackupSchedule
		if item.Engine != p.flavor.ID || schedule == nil || schedule.Disabled || schedule.RetentionCopies <= 0 {
			continue
		}

		backups, err := p.everest.ListInstanceBackups(ctx, item.Namespace, item.Name)
		if err != nil {
			if !errors.Is(err, everest.ErrNotFound) && firstErr == nil {
				firstErr = fmt.Errorf("list backups of %q: %w", item.ID, err)
			}

			continue
		}

		for _, backup := range beyondCount(backups, item.Name, schedule.Name, int(schedule.RetentionCopies)) {
			if err := p.deleteBackup(ctx, item.Namespace, backup.Metadata.Name); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("delete backup %q: %w", backup.Metadata.Name, err)
				}

				continue
			}
			deleted++
		}
	}

	return deleted, firstErr
}

// beyondCount picks, oldest first, the succeeded backups of one schedule of one
// instance that are past the newest keep.
func beyondCount(backups []everest.Backup, instance, schedule string, keep int) []everest.Backup {
	var counted []everest.Backup
	for _, backup := range backups {
		if backup.Spec.InstanceName() != instance || backup.Spec.ScheduleName != schedule {
			continue
		}
		if toBackupState(backup.Status.State) != engine.BackupSucceeded {
			continue
		}
		counted = append(counted, backup)
	}
	if len(counted) <= keep {
		return nil
	}

	sort.SliceStable(counted, func(i, j int) bool {
		return backupFinishedAt(counted[i]).After(backupFinishedAt(counted[j]))
	})

	oldest := counted[keep:]
	slices.Reverse(oldest)

	return oldest
}
