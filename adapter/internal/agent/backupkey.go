package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/percona/pds/adapter/internal/client/backupkey"
)

// The keys of the Secret the release's BackupStorage reads its credential from.
const (
	backupKeyID     = "AWS_ACCESS_KEY_ID"
	backupKeySecret = "AWS_SECRET_ACCESS_KEY"
)

// backupKeyTimeout bounds one reconcile.
const backupKeyTimeout = 30 * time.Second

// BackupKeyUsers is the environment's IAM user for backups. *backupkey.Users is
// the implementation, and tests fake it.
type BackupKeyUsers interface {
	Ensure(ctx context.Context, spec backupkey.Spec) error
	NewKey(ctx context.Context, spec backupkey.Spec) (backupkey.Key, error)
	Remove(ctx context.Context, spec backupkey.Spec) error
}

// BackupKeyCluster is what the agent reads and writes in its own environment.
type BackupKeyCluster interface {
	// Secret returns the backup Secret's data, and nothing when there is none.
	Secret(ctx context.Context) (map[string]string, error)
	// WriteSecret creates the Secret or sets these keys on it.
	WriteSecret(ctx context.Context, values map[string]string) error
	// Removal says whether the control plane asked for the user to be removed,
	// and whether the agent already did.
	Removal(ctx context.Context) (requested, done bool, err error)
	MarkRemoved(ctx context.Context) error
}

// BackupKey keeps the environment's backup key: it makes the IAM user and its
// access key and writes the key straight to the Secret, so the key never leaves
// the environment. When the control plane asks, as it does while deleting the
// environment, it removes the user, which the role the control plane assumes in
// the customer's account has no permission to do.
type BackupKey struct {
	Users   BackupKeyUsers
	Cluster BackupKeyCluster
	Spec    backupkey.Spec
}

// Reconcile is safe to run on every beat. A key that is in the Secret is left
// alone, because IAM cannot show a secret again. A write that failed after the
// key was made leaves the Secret without it, and the next run makes a new one.
func (b BackupKey) Reconcile(ctx context.Context) error {
	requested, done, err := b.Cluster.Removal(ctx)
	if err != nil {
		return fmt.Errorf("read the backup user removal request: %w", err)
	}
	if requested {
		if done {
			return nil
		}
		if err := b.Users.Remove(ctx, b.Spec); err != nil {
			return err
		}

		return b.Cluster.MarkRemoved(ctx)
	}

	values, err := b.Cluster.Secret(ctx)
	if err != nil {
		return fmt.Errorf("read the backup Secret: %w", err)
	}
	if values[backupKeyID] != "" && values[backupKeySecret] != "" {
		return nil
	}
	if err := b.Users.Ensure(ctx, b.Spec); err != nil {
		return err
	}
	key, err := b.Users.NewKey(ctx, b.Spec)
	if err != nil {
		return err
	}

	return b.Cluster.WriteSecret(ctx, map[string]string{backupKeyID: key.ID, backupKeySecret: key.Secret})
}

// reconcileBackupKey runs the environment's backup key reconcile for the beat.
// A failure is logged once until it changes and never holds the beat back.
func reconcileBackupKey(ctx context.Context, opts Options) {
	if opts.BackupKey == nil {
		return
	}
	// IAM that hangs must not hold the heartbeat back: the environment is not
	// Ready until its first one arrives.
	ctx, cancel := context.WithTimeout(ctx, backupKeyTimeout)
	defer cancel()
	if err := opts.BackupKey(ctx); err != nil {
		if err.Error() != *opts.backupKeyErr {
			*opts.backupKeyErr = err.Error()
			opts.Logger.WarnContext(ctx, "backup key reconcile failed", "err", err.Error())
		}

		return
	}
	*opts.backupKeyErr = ""
}
