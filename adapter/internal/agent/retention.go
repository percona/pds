package agent

import (
	"context"
	"log/slog"

	"github.com/percona/pds/adapter/internal/engine"
)

// retainBackups prunes the scheduled backups a database's schedule no longer
// keeps, for the engines whose provider leaves that to the control plane. It
// runs every beat, so a backup that finishes is counted on the next one. A
// failed pass is logged and tried again on the next beat, and the heartbeat
// never waits longer than the sweep budget on it.
func retainBackups(ctx context.Context, opts Options, listed []engine.Instance) {
	if opts.Retain == nil {
		return
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	ctx, cancel := context.WithTimeout(ctx, sweepBudget)
	defer cancel()

	deleted, err := opts.Retain(ctx, listed)
	if err != nil {
		logger.WarnContext(ctx, "backup retention could not finish", slog.String("err", err.Error()))
	}
	if deleted > 0 {
		logger.InfoContext(ctx, "backups beyond the schedule's count deleted", slog.Int("deleted", deleted))
	}
}
