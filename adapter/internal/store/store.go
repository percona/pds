// Package store holds the adapter's own PostgreSQL database.
//
// This is the adapter's state, not a customer database: the engine plugins in
// internal/engine provision those through OpenEverest and never come here. The
// models in models.go are the schema, reconciled by GORM at start, so there is
// no separate migration step to run before a deploy.
//
// The connection URL carries a password, so it is read from a Secret, never
// logged, and never returned in an error.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/percona/pds/adapter/internal/log"
)

const (
	// schemaLockKey is the advisory lock every process takes before it
	// reconciles the schema. Replicas start together and PostgreSQL does not
	// serialize concurrent DDL on its own, so without it two pods can race on
	// the same CREATE TABLE and one of them fails. It is transaction scoped,
	// so the server releases it however the process goes away.
	//
	// The value is arbitrary but has to stay fixed: changing it lets an old
	// pod and a new one hold different locks and migrate at the same time.
	schemaLockKey int64 = 7264193058471002

	// defaultConnectTimeout bounds the first connection. Readiness reports the
	// wait rather than hanging on a database that is still starting.
	defaultConnectTimeout = 10 * time.Second

	// Pool sizes for a control plane that talks to PostgreSQL on a handful of
	// requests rather than on every read. A larger pool would sit idle and
	// still count against the server's connection limit.
	maxOpenConns    = 10
	maxIdleConns    = 2
	connMaxLifetime = 30 * time.Minute
	connMaxIdleTime = 5 * time.Minute
)

// errNotMigrated is what readiness reports before the schema is reconciled.
var errNotMigrated = errors.New("the schema has not been reconciled on this process yet")

// Options configures Open.
type Options struct {
	// URL is the PostgreSQL connection URL, from a Secret.
	URL string
	// Logger receives query errors and slow queries. Nil uses the default
	// logger, which is the one that redacts.
	Logger *slog.Logger
	// ConnectTimeout bounds the first connection. Zero uses ten seconds.
	ConnectTimeout time.Duration
	// RetryBackoff is the first wait between attempts in Start. It doubles up
	// to thirty seconds. Zero uses one second.
	RetryBackoff time.Duration
	// Organization is HUB_ORGANIZATION: the name of the one Organization this
	// deployment serves, which is made at start when it does not exist, or a
	// star for every one. Empty names none: nothing is made, and a database
	// from the release before Organizations is refused. That is what a test of
	// the store alone wants.
	Organization string
	// Cipher opens and seals the values the upgrade from the release before
	// Organizations re-keys. It may be nil on a database that holds none.
	Cipher *Cipher
}

// Store is the adapter's connection to its own database.
type Store struct {
	db *gorm.DB
	// migrated flips once Migrate has finished on this process. Readiness
	// reads it, and a pod that has not reconciled the schema must not take
	// traffic even though its connection is fine.
	migrated     atomic.Bool
	organization string
	cipher       *Cipher
}

// Open connects to PostgreSQL and verifies the connection before returning.
// It does not reconcile the schema: call Migrate for that, so a caller can
// report the two failures apart.
func Open(ctx context.Context, opts Options) (*Store, error) {
	url := strings.TrimSpace(opts.URL)
	if url == "" {
		return nil, errors.New("no database URL was configured")
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	timeout := opts.ConnectTimeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}

	db, err := gorm.Open(postgres.Open(url), &gorm.Config{
		Logger:                 newGormLogger(logger),
		SkipDefaultTransaction: true,
		TranslateError:         true,
		// gorm pings on its own inside Open, with a background context and no
		// timeout, and pgx applies no connect timeout of its own. A host that
		// drops packets rather than refusing them would block there for the
		// whole OS TCP timeout, past any cancellation. The explicit ping below
		// is the one that bounds the connection.
		DisableAutomaticPing: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", scrub(err))
	}

	pool, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("database pool: %w", scrub(err))
	}

	pool.SetMaxOpenConns(maxOpenConns)
	pool.SetMaxIdleConns(maxIdleConns)
	pool.SetConnMaxLifetime(connMaxLifetime)
	pool.SetConnMaxIdleTime(connMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := pool.PingContext(pingCtx); err != nil {
		// The pool is already open, so it has to be closed here or a failed
		// start leaks the connections it just made.
		_ = pool.Close()

		return nil, fmt.Errorf("connect to database: %w", scrub(err))
	}

	return &Store{db: db, organization: strings.TrimSpace(opts.Organization), cipher: opts.Cipher}, nil
}

// Gorm exposes the underlying handle for the models that will live here.
func (s *Store) Gorm() *gorm.DB { return s.db }

// Migrate reconciles the schema with the models and records that it ran.
//
// Every replica calls this at start. An advisory lock makes them take turns, so
// the pod that gets there first does the work and the rest find the schema
// already correct rather than racing it.
func (s *Store) Migrate(ctx context.Context) error {
	pool, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("database pool: %w", scrub(err))
	}

	// The lock is held for a transaction on one pinned connection, not for the
	// session. A session lock outlives conn.Close, which only returns the
	// connection to the pool rather than ending the session, so a failure to
	// unlock would strand the lock on a pooled connection and leave every
	// other replica waiting on it. PostgreSQL releases this one on commit,
	// rollback, or disconnect, whatever happens to the process.
	conn, err := pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("database connection: %w", scrub(err))
	}
	defer func() { _ = conn.Close() }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("database transaction: %w", scrub(err))
	}
	// Rollback after a successful commit is a no-op, so this is the release
	// path for every early return below.
	defer func() { _ = tx.Rollback() }()

	if err := lock(ctx, tx); err != nil {
		return err
	}

	if err := renameLegacyNames(s.db.WithContext(ctx)); err != nil {
		return fmt.Errorf("rename legacy names: %w", scrub(err))
	}
	if err := s.contractLegacySchema(ctx); err != nil {
		return fmt.Errorf("upgrade from the release before organizations: %w", scrub(err))
	}
	if err := s.db.WithContext(ctx).AutoMigrate(Models()...); err != nil {
		return fmt.Errorf("reconcile schema: %w", scrub(err))
	}

	if err := removeProviderKeys(s.db.WithContext(ctx)); err != nil {
		return fmt.Errorf("remove the keys of the removed providers: %w", scrub(err))
	}

	if err := s.ensureServedOrganization(ctx); err != nil {
		return fmt.Errorf("make the served organization: %w", scrub(err))
	}

	// The recorded revision only ever moves forward. During a rolling upgrade an
	// older pod can restart after a newer one has migrated, and writing its own
	// revision over the newer one would make the row report a schema that is
	// not there.
	state := SchemaState{ID: schemaStateID, Revision: SchemaRevision, AppliedAt: time.Now().UTC()}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"revision":   gorm.Expr("GREATEST(hub_schema_state.revision, EXCLUDED.revision)"),
			"applied_at": gorm.Expr("EXCLUDED.applied_at"),
		}),
	}).Create(&state).Error; err != nil {
		return fmt.Errorf("record schema revision: %w", scrub(err))
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("release schema lock: %w", scrub(err))
	}

	s.migrated.Store(true)

	return nil
}

// Ping reports whether the database answers.
func (s *Store) Ping(ctx context.Context) error {
	pool, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("database pool: %w", scrub(err))
	}

	if err := pool.PingContext(ctx); err != nil {
		return fmt.Errorf("database unreachable: %w", scrub(err))
	}

	return nil
}

// Ready reports whether this process can serve. Both halves have to hold: this
// pod finished reconciling the schema, and the schema is still there now.
//
// The second half is a read of the marker row rather than a ping. A database
// restored from a backup, or dropped and recreated, under a running adapter
// answers a ping perfectly well while carrying none of the schema, and this pod
// would otherwise report ready and take traffic against nothing.
func (s *Store) Ready(ctx context.Context) error {
	if !s.migrated.Load() {
		return errNotMigrated
	}

	var revision int
	if err := s.db.WithContext(ctx).
		Model(&SchemaState{}).
		Where("id = ?", schemaStateID).
		Pluck("revision", &revision).Error; err != nil {
		return fmt.Errorf("read schema revision: %w", scrub(err))
	}

	if revision < SchemaRevision {
		return errNotMigrated
	}

	return nil
}

// Close releases the pool.
func (s *Store) Close() error {
	pool, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("database pool: %w", scrub(err))
	}

	if err := pool.Close(); err != nil {
		return fmt.Errorf("close database: %w", scrub(err))
	}

	return nil
}

// lock waits for the schema lock. The wait is unbounded except by ctx: a
// replica that arrives while another is migrating should queue, not fail.
func lock(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", schemaLockKey); err != nil {
		return fmt.Errorf("take schema lock: %w", scrub(err))
	}

	return nil
}

// scrub hides anything credential shaped in a driver error. pgx reports a
// failed connection as the whole keyword string, password included, and that
// string reaches both the log and the message a failed start prints to stderr.
//
// The cause stays reachable through errors.Is and errors.As. Only the rendered
// text is rewritten, which is the part that gets printed.
func scrub(err error) error {
	if err == nil {
		return nil
	}

	return &redactedError{cause: err}
}

type redactedError struct{ cause error }

func (e *redactedError) Error() string { return log.RedactText(e.cause.Error()) }

func (e *redactedError) Unwrap() error { return e.cause }
