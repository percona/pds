package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/postgresql"
)

// maxCleanupPasses bounds the passes spent on one deleted database, so an
// environment that refuses the deletes does not try on every beat forever.
const maxCleanupPasses = 20

// sweepBudget bounds one beat's whole sweep, so slow deletes cannot hold the
// heartbeat past the hub's stale window.
const sweepBudget = 10 * time.Second

type cleanupKey struct{ namespace, name string }

type cleanupState struct {
	// seen is the PostgreSQL databases the last list held.
	seen map[cleanupKey]struct{}
	// pending is the databases gone from the list, with the passes run.
	pending map[cleanupKey]int
}

func newCleanupState() *cleanupState {
	return &cleanupState{seen: map[cleanupKey]struct{}{}, pending: map[cleanupKey]int{}}
}

// sweepDeleted removes the Secrets a deleted PostgreSQL database leaves behind.
// The operator deletes them on termination and then recreates them a second
// later, while the cluster is still finishing. A pass that removed something
// is therefore followed by another, and the cleanup ends with the first pass
// that finds nothing. A database that is listed again is never swept: its
// Secrets belong to the new one. A failed pass is logged and tried again on
// the next beat, and the heartbeat never waits on it.
//
// Deleting a database is the only trigger, so a database this agent never saw
// listed is left alone, and so is a restart's worth of memory: a cleanup owed
// across a restart is not run.
func sweepDeleted(ctx context.Context, opts Options, listed []engine.Instance) {
	state := opts.cleanup
	if opts.Cleanup == nil || state == nil {
		return
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	current := map[cleanupKey]struct{}{}
	for _, item := range listed {
		if item.Engine == postgresql.EngineID {
			current[cleanupKey{item.Namespace, item.Name}] = struct{}{}
		}
	}
	for key := range state.seen {
		if _, ok := current[key]; !ok {
			state.pending[key] = 0
		}
	}
	state.seen = current

	ctx, cancelSweep := context.WithTimeout(ctx, sweepBudget)
	defer cancelSweep()

	for key, passes := range state.pending {
		if _, ok := current[key]; ok {
			delete(state.pending, key)

			continue
		}
		if ctx.Err() != nil {
			return
		}
		// The list above is older than the deletes this pass makes, and the
		// database may have been created again since. Look once more.
		gone, err := confirmedGone(ctx, opts, key)
		if err != nil {
			logger.WarnContext(ctx, "cleanup of a deleted database could not confirm it is gone",
				slog.String("database", key.name),
				slog.String("err", err.Error()),
			)

			continue
		}
		if !gone {
			state.seen[key] = struct{}{}
			delete(state.pending, key)

			continue
		}

		removed, err := opts.Cleanup(ctx, key.namespace, key.name)
		passes++
		switch {
		case err != nil:
			logger.WarnContext(ctx, "cleanup of a deleted database failed",
				slog.String("database", key.name),
				slog.String("namespace", key.namespace),
				slog.Int("pass", passes),
				slog.String("err", err.Error()),
			)
		case removed == 0:
			delete(state.pending, key)

			continue
		}
		if passes >= maxCleanupPasses {
			logger.ErrorContext(ctx, "cleanup of a deleted database gave up",
				slog.String("database", key.name),
				slog.String("namespace", key.namespace),
			)
			delete(state.pending, key)

			continue
		}
		state.pending[key] = passes
	}
}

// confirmedGone lists the databases again and reports whether key is absent.
// An agent with no database service has nothing to ask and takes the earlier
// list as the answer.
func confirmedGone(ctx context.Context, opts Options, key cleanupKey) (bool, error) {
	if opts.Databases == nil {
		return true, nil
	}
	items, err := opts.Databases.List(ctx)
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if item.Engine == postgresql.EngineID && item.Namespace == key.namespace && item.Name == key.name {
			return false, nil
		}
	}

	return true, nil
}
