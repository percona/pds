package store

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const (
	// defaultRetryBackoff is the first wait between connection attempts. It
	// doubles up to maxRetryBackoff so a database that is still bootstrapping
	// is retried quickly and one that is gone for good is not hammered.
	defaultRetryBackoff = time.Second
	maxRetryBackoff     = 30 * time.Second
)

// errStarting is what readiness reports before the first attempt has finished.
var errStarting = errors.New("the database connection has not been established yet")

// Starter opens the database in the background and keeps trying until it
// succeeds. The process serves HTTP throughout, reporting not ready, rather
// than exiting: a database that is still bootstrapping is the ordinary case on
// a first install, and a pod that exits for it restarts with a backoff that
// grows into minutes.
type Starter struct {
	mu    sync.Mutex
	store *Store
	err   error

	// cancel stops the attempt in flight, not just the wait between attempts.
	// Close cannot rely on the caller's context: the process also shuts down on
	// a listen failure, where that context is never cancelled, and an attempt
	// can be parked in a connect to a host that drops packets or in a wait for
	// the schema lock, which is deliberately unbounded.
	cancel context.CancelFunc
	once   sync.Once
	done   sync.WaitGroup
}

// Start begins connecting. It returns immediately; readiness reports progress.
func Start(ctx context.Context, opts Options) *Starter {
	ctx, cancel := context.WithCancel(ctx)
	s := &Starter{err: errStarting, cancel: cancel}

	backoff := opts.RetryBackoff
	if backoff <= 0 {
		backoff = defaultRetryBackoff
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	s.done.Add(1)
	go s.connect(ctx, opts, logger, backoff)

	return s
}

// connect retries open and migrate until one attempt gets through both.
func (s *Starter) connect(ctx context.Context, opts Options, logger *slog.Logger, backoff time.Duration) {
	defer s.done.Done()

	for {
		db, err := s.attempt(ctx, opts)
		if err == nil {
			s.mu.Lock()
			s.store = db
			s.err = nil
			s.mu.Unlock()

			logger.InfoContext(ctx, "database schema reconciled", slog.Int("revision", SchemaRevision))

			return
		}

		s.mu.Lock()
		s.err = err
		s.mu.Unlock()

		logger.WarnContext(ctx, "database is not usable yet, readiness stays red",
			slog.String("cause", err.Error()),
			slog.Duration("retry_in", backoff))

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()

			return
		case <-timer.C:
		}

		if backoff *= 2; backoff > maxRetryBackoff {
			backoff = maxRetryBackoff
		}
	}
}

// attempt is one open plus one migrate. A connection that opens but cannot
// reconcile the schema is closed again, so a retry does not leak a pool per
// attempt.
func (s *Starter) attempt(ctx context.Context, opts Options) (*Store, error) {
	db, err := Open(ctx, opts)
	if err != nil {
		return nil, err
	}

	if err := db.Migrate(ctx); err != nil {
		_ = db.Close()

		return nil, err
	}

	return db, nil
}

// Ready reports whether the database is connected, migrated, and answering.
func (s *Starter) Ready(ctx context.Context) error {
	s.mu.Lock()
	db, err := s.store, s.err
	s.mu.Unlock()

	if db == nil {
		if err == nil {
			return errStarting
		}

		return err
	}

	return db.Ready(ctx)
}

// Store returns the connection once there is one, and nil before that. It is
// how the state that moves here later reaches the database.
func (s *Starter) Store() *Store {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.store
}

// PruneUsage is Store.PruneUsage once the connection has landed, and the
// not-connected error before that.
func (s *Starter) PruneUsage(ctx context.Context, before time.Time) (int64, error) {
	if s == nil {
		return 0, ErrNotConnected
	}

	return s.Store().PruneUsage(ctx, before)
}

// TokenTeam looks up the Team that minted this hash once the connection
// has landed. Before that it is the same not-connected error every other lazy
// call returns.
func (s *Starter) TokenTeam(ctx context.Context, hash string) (uint, error) {
	if s == nil {
		return 0, ErrNotConnected
	}
	s.mu.Lock()
	db := s.store
	s.mu.Unlock()
	if db == nil {
		return 0, ErrNotConnected
	}

	return db.TokenTeam(ctx, hash)
}

// AllSupportAccess is Store.AllSupportAccess once the connection has landed,
// and the not-connected error before that.
func (s *Starter) AllSupportAccess(ctx context.Context) ([]string, error) {
	if s == nil {
		return nil, ErrNotConnected
	}
	db := s.Store()
	if db == nil {
		return nil, ErrNotConnected
	}

	return db.AllSupportAccess(ctx)
}

// Close stops retrying and releases the pool. Closing before the first
// connection succeeds is not an error: there is nothing to release.
func (s *Starter) Close() error {
	s.once.Do(s.cancel)
	s.done.Wait()

	s.mu.Lock()
	db := s.store
	s.store = nil
	s.err = errors.New("the database connection was closed")
	s.mu.Unlock()

	if db == nil {
		return nil
	}

	return db.Close()
}
