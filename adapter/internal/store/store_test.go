package store_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/store"
)

// liveURL returns the connection URL for the tests that need a real server.
// Without one the test is skipped rather than failed: a laptop run has no
// PostgreSQL, and the same suite runs in CI where the service container does.
func liveURL(t *testing.T) string {
	t.Helper()

	url := strings.TrimSpace(os.Getenv("ADAPTER_TEST_DATABASE_URL"))
	if url == "" {
		t.Skip("set ADAPTER_TEST_DATABASE_URL to run the store tests against a live PostgreSQL")
	}

	return url
}

func TestOpenRejectsAnUnusableURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"blank", "   "},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.Open(t.Context(), store.Options{URL: tc.url})

			require.Error(t, err)
		})
	}
}

func TestOpenFailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	// Port 1 is reserved and nothing listens on it, so the dial is refused
	// rather than left hanging on a firewall drop.
	_, err := store.Open(ctx, store.Options{
		URL:            "postgres://hub:hunter2@127.0.0.1:1/hub?sslmode=disable",
		ConnectTimeout: 2 * time.Second,
	})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hunter2", "a failed start prints this error to stderr")
}

func TestOpenConnectsAndReportsReady(t *testing.T) {
	db, err := store.Open(t.Context(), store.Options{URL: liveURL(t)})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	require.NoError(t, db.Ping(t.Context()))
	assert.Error(t, db.Ready(t.Context()), "a connection alone is not readiness, the schema has to be reconciled")

	require.NoError(t, db.Migrate(t.Context()))
	assert.NoError(t, db.Ready(t.Context()))
}

func TestMigrateCreatesTheSchema(t *testing.T) {
	db, err := store.Open(t.Context(), store.Options{URL: liveURL(t)})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	require.NoError(t, db.Migrate(t.Context()))

	assert.True(t, db.Gorm().Migrator().HasTable(&store.SchemaState{}))

	var state store.SchemaState
	require.NoError(t, db.Gorm().WithContext(t.Context()).First(&state).Error)
	assert.Equal(t, store.SchemaRevision, state.Revision)
	assert.False(t, state.AppliedAt.IsZero())
}

func TestMigrateIsIdempotent(t *testing.T) {
	db, err := store.Open(t.Context(), store.Options{URL: liveURL(t)})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	require.NoError(t, db.Migrate(t.Context()))
	require.NoError(t, db.Migrate(t.Context()))

	var rows int64
	require.NoError(t, db.Gorm().WithContext(t.Context()).Model(&store.SchemaState{}).Count(&rows).Error)
	assert.Equal(t, int64(1), rows, "the marker is one row however often the adapter restarts")
}

// TestConcurrentMigrateLeavesOneSchema is the replica case. Several adapter
// pods start together and each reconciles the schema; the advisory lock has to
// serialize them so none sees a half built table or a duplicate marker row.
func TestConcurrentMigrateLeavesOneSchema(t *testing.T) {
	url := liveURL(t)

	// Start from nothing so every replica races the same CREATE TABLE. Against
	// a schema that already exists AutoMigrate is mostly reads, and the race
	// this test is about would not happen.
	dropSchema(t, url)

	const replicas = 8

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)

	for range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()

			db, err := store.Open(t.Context(), store.Options{URL: url})
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()

				return
			}
			defer func() { _ = db.Close() }()

			if err := db.Migrate(t.Context()); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	require.Empty(t, errs)

	db, err := store.Open(t.Context(), store.Options{URL: url})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	var rows int64
	require.NoError(t, db.Gorm().WithContext(t.Context()).Model(&store.SchemaState{}).Count(&rows).Error)
	assert.Equal(t, int64(1), rows, "one marker row however many replicas reconciled")

	var state store.SchemaState
	require.NoError(t, db.Gorm().WithContext(t.Context()).First(&state).Error)
	assert.Equal(t, store.SchemaRevision, state.Revision)
}

// dropSchema removes the tables so a test can watch them be created.
func dropSchema(t *testing.T, url string) {
	t.Helper()

	db, err := store.Open(t.Context(), store.Options{URL: url})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	require.NoError(t, db.Gorm().WithContext(t.Context()).Migrator().DropTable(store.Models()...))
}

func TestPingFailsAfterClose(t *testing.T) {
	db, err := store.Open(t.Context(), store.Options{URL: liveURL(t)})
	require.NoError(t, err)

	require.NoError(t, db.Close())

	assert.Error(t, db.Ping(t.Context()), "readiness has to go red when the pool is gone")
}

func TestStartStaysUnreadyUntilTheDatabaseAnswers(t *testing.T) {
	url := liveURL(t)
	dropSchema(t, url)

	starter := store.Start(t.Context(), store.Options{
		URL:          url,
		RetryBackoff: 10 * time.Millisecond,
	})
	t.Cleanup(func() { assert.NoError(t, starter.Close()) })

	require.Eventually(t, func() bool {
		return starter.Ready(t.Context()) == nil
	}, 30*time.Second, 50*time.Millisecond, "a reachable database has to bring readiness green")

	db, err := store.Open(t.Context(), store.Options{URL: url})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	assert.True(t, db.Gorm().Migrator().HasTable(&store.SchemaState{}),
		"readiness turning green means the schema was reconciled")
}

func TestStartKeepsRetryingAnUnreachableDatabase(t *testing.T) {
	starter := store.Start(t.Context(), store.Options{
		URL:            "postgres://hub:hunter2@127.0.0.1:1/hub?sslmode=disable",
		ConnectTimeout: 200 * time.Millisecond,
		RetryBackoff:   10 * time.Millisecond,
	})
	t.Cleanup(func() { assert.NoError(t, starter.Close()) })

	// Long enough for several attempts. The point is that it is still trying
	// rather than having given up, and that it never went ready.
	assert.Never(t, func() bool {
		return starter.Ready(t.Context()) == nil
	}, 2*time.Second, 100*time.Millisecond)

	err := starter.Ready(t.Context())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hunter2")
}

func TestStartIsClosableBeforeItConnects(t *testing.T) {
	starter := store.Start(t.Context(), store.Options{
		URL:            "postgres://hub:pw@127.0.0.1:1/hub?sslmode=disable",
		ConnectTimeout: 200 * time.Millisecond,
		RetryBackoff:   10 * time.Millisecond,
	})

	assert.NoError(t, starter.Close(), "a shutdown while the first connection is still failing is not an error")
	assert.Error(t, starter.Ready(t.Context()))
}

// TestReadyGoesRedWhenTheSchemaGoesAway is the restore case. A database that
// answers is not the same as a database that still carries the schema, and a
// pod whose readiness only pinged would take traffic against an empty one.
func TestReadyGoesRedWhenTheSchemaGoesAway(t *testing.T) {
	url := liveURL(t)

	db, err := store.Open(t.Context(), store.Options{URL: url})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	require.NoError(t, db.Migrate(t.Context()))
	require.NoError(t, db.Ready(t.Context()))

	require.NoError(t, db.Gorm().WithContext(t.Context()).Migrator().DropTable(store.Models()...))

	assert.Error(t, db.Ready(t.Context()), "the connection is fine, the schema is gone")

	require.NoError(t, db.Migrate(t.Context()))
	assert.NoError(t, db.Ready(t.Context()), "readiness recovers once the schema is back")
}

func TestMigrateKeepsTheHighestRevision(t *testing.T) {
	url := liveURL(t)
	dropSchema(t, url)

	db, err := store.Open(t.Context(), store.Options{URL: url})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	require.NoError(t, db.Migrate(t.Context()))

	// A newer pod migrated to a later revision, then an older one restarted and
	// reconciled again. The row has to keep the revision that is really there.
	ahead := store.SchemaRevision + 1
	require.NoError(t, db.Gorm().WithContext(t.Context()).
		Model(&store.SchemaState{}).
		Where("id = ?", 1).
		Update("revision", ahead).Error)

	require.NoError(t, db.Migrate(t.Context()))

	var state store.SchemaState
	require.NoError(t, db.Gorm().WithContext(t.Context()).First(&state).Error)
	assert.Equal(t, ahead, state.Revision, "an older pod must not report the schema as older than it is")
}

// TestMigrateLeavesNoLockBehind covers the release path. The lock is scoped to
// a transaction, so a connection going back to the pool cannot still hold it,
// and the next migrate must not block on the last one.
func TestMigrateLeavesNoLockBehind(t *testing.T) {
	url := liveURL(t)

	db, err := store.Open(t.Context(), store.Options{URL: url})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	require.NoError(t, db.Migrate(t.Context()))

	var held int64
	require.NoError(t, db.Gorm().WithContext(t.Context()).
		Raw("SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'").
		Scan(&held).Error)

	assert.Zero(t, held, "no advisory lock outlives the migration that took it")
}

func TestStarterRecoversWhenTheSchemaComesBack(t *testing.T) {
	url := liveURL(t)
	dropSchema(t, url)

	starter := store.Start(t.Context(), store.Options{URL: url, RetryBackoff: 10 * time.Millisecond})
	t.Cleanup(func() { assert.NoError(t, starter.Close()) })

	require.Eventually(t, func() bool {
		return starter.Ready(t.Context()) == nil
	}, 30*time.Second, 50*time.Millisecond)

	require.NoError(t, starter.Store().Gorm().WithContext(t.Context()).
		Migrator().DropTable(store.Models()...))

	require.Error(t, starter.Ready(t.Context()))

	require.NoError(t, starter.Store().Migrate(t.Context()))
	assert.NoError(t, starter.Ready(t.Context()))
}

// TestLoggedSQLKeepsBoundValuesOut is about the models that come later. GORM
// hands its logger the statement with bind values already interpolated, so a
// future model holding a token or a password would write it into the log on
// any failed or slow statement. Redaction cannot save that: the value is a
// bare SQL literal with nothing around it to match on.
func TestLoggedSQLKeepsBoundValuesOut(t *testing.T) {
	url := liveURL(t)

	var buf bytes.Buffer
	logger := log.New(&buf, log.FormatJSON, slog.LevelDebug)

	db, err := store.Open(t.Context(), store.Options{URL: url, Logger: logger})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	const secret = "sup3rs3cr3t-bound-value"

	// A statement that fails, so the logger records it along with the SQL.
	err = db.Gorm().WithContext(t.Context()).
		Exec("INSERT INTO a_table_that_does_not_exist (v) VALUES (?)", secret).Error
	require.Error(t, err)

	assert.NotContains(t, buf.String(), secret, "a bound value must not reach the log")
	assert.Contains(t, buf.String(), "a_table_that_does_not_exist", "the statement itself is still logged")
}

// TestMigrateKeepsRowsWrittenUnderTheClusterNames is the upgrade of a hub that // allow-legacy-name
// stored an environment as a cluster: the rows must survive under the new
// names, not be left in a column nothing reads.
func TestMigrateKeepsRowsWrittenUnderTheClusterNames(t *testing.T) { // allow-legacy-name
	url := liveURL(t)
	dropSchema(t, url)

	db, err := store.Open(t.Context(), store.Options{URL: url})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	ctx := t.Context()
	gorm := db.Gorm().WithContext(ctx)

	// Build the schema, then put the old names back: that is exactly what a
	// database from before the rename holds.
	require.NoError(t, db.Migrate(ctx))
	team := nextTeam(t, db)
	for _, statement := range []string{
		`ALTER TABLE hub_environments RENAME TO hub_clusters`, // allow-legacy-name
		`ALTER TABLE hub_work RENAME COLUMN environment TO cluster`,
		`ALTER TABLE hub_clone_keys RENAME COLUMN environment TO cluster`,
		`ALTER TABLE hub_history RENAME COLUMN environment TO cluster`,
		`ALTER TABLE hub_usage_samples RENAME COLUMN environment TO cluster`,
		`ALTER TABLE hub_join_phases RENAME COLUMN environment TO cluster`,
		fmt.Sprintf(`INSERT INTO hub_clusters (team_id, name, document, updated_at) VALUES (%d, 'spoke-a', '{"seen":true}', now())`, team), // allow-legacy-name
		fmt.Sprintf(`INSERT INTO hub_work (team_id, cluster, document, updated_at) VALUES (%d, 'spoke-a', '[]', now())`, team),
	} {
		require.NoError(t, gorm.Exec(statement).Error, statement)
	}

	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Migrate(ctx), "a second start finds nothing left to rename")

	hubState := db.Hub(team, nil)
	environments, err := hubState.Environments(ctx)
	require.NoError(t, err)
	require.Len(t, environments, 1)
	assert.JSONEq(t, `{"seen":true}`, environments["spoke-a"])
	work, err := hubState.Work(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"spoke-a": "[]"}, work)
	assert.False(t, gorm.Migrator().HasTable("hub_clusters")) // allow-legacy-name
	for _, model := range []any{&store.HubWork{}, &store.HubCloneKey{}, &store.HistoryRow{}, &store.UsageSample{}, &store.JoinPhase{}} {
		assert.True(t, gorm.Migrator().HasColumn(model, "environment"), "%T", model)
		assert.False(t, gorm.Migrator().HasColumn(model, "cluster"), "%T", model)
	}
}
