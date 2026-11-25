package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

// fakeDatabases records what the work loop called and answers with whatever the
// test set up. Every method is here because workDatabases requires it, not
// because every test uses it.
type fakeDatabases struct {
	backupErr error

	backedUp string
	deleted  string
	scaled   engine.ScaleInput
}

func (f *fakeDatabases) Backup(_ context.Context, id string) (*engine.Backup, error) {
	f.backedUp = id

	return &engine.Backup{}, f.backupErr
}

func (f *fakeDatabases) BackupNamed(_ context.Context, id, backupID string) (*engine.Backup, error) {
	f.backedUp = id

	return &engine.Backup{ID: backupID}, f.backupErr
}

func (f *fakeDatabases) Delete(_ context.Context, id string) error {
	f.deleted = id

	return nil
}

func (f *fakeDatabases) Scale(_ context.Context, _ string, in engine.ScaleInput) error {
	f.scaled = in

	return nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEachWorkKindReachesTheDatabaseService(t *testing.T) {
	db := &fakeDatabases{}
	replicas := 3

	results := applyWork(context.Background(), quietLogger(), db, nil, []hub.Work{
		{ID: "w1", Kind: hub.WorkBackup, Database: "pds.orders-dev", BackupID: "orders-dev-1"},
		{ID: "w2", Kind: hub.WorkDelete, Database: "pds.orders-old"},
		{ID: "w3", Kind: hub.WorkScale, Database: "pds.orders-dev", Replicas: &replicas},
	})

	require.Len(t, results, 3)
	for _, result := range results {
		assert.Empty(t, result.Error)
	}
	assert.Equal(t, "pds.orders-dev", db.backedUp)
	assert.Equal(t, "pds.orders-old", db.deleted)
	require.NotNil(t, db.scaled.Replicas)
	assert.Equal(t, 3, *db.scaled.Replicas)
}

// The hub holds an item until it hears back, so a failure has to come back as
// a reported result rather than as silence.
func TestAFailedItemIsReportedRatherThanDropped(t *testing.T) {
	db := &fakeDatabases{backupErr: errors.New("instance is not ready")}

	results := applyWork(context.Background(), quietLogger(), db, nil, []hub.Work{
		{ID: "w1", Kind: hub.WorkBackup, Database: "pds.orders-dev"},
	})

	require.Len(t, results, 1)
	assert.Equal(t, "w1", results[0].ID)
	assert.Contains(t, results[0].Error, "instance is not ready")
}

// A spoke running an older agent than the hub gets kinds it does not know.
// Reporting one keeps the hub from handing it out forever.
func TestAnUnknownKindIsReportedAndSkipped(t *testing.T) {
	db := &fakeDatabases{}

	results := applyWork(context.Background(), quietLogger(), db, nil, []hub.Work{
		{ID: "w1", Kind: "teleport", Database: "pds.orders-dev"},
	})

	require.Len(t, results, 1)
	assert.Contains(t, results[0].Error, "unknown kind teleport")
	assert.Empty(t, db.backedUp)
}

func TestCredentialsWorkPreparesTheUsersSecret(t *testing.T) {
	var namespace, name string
	prepare := func(_ context.Context, ns, db string) error {
		namespace, name = ns, db

		return nil
	}

	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, prepare, []hub.Work{
		{ID: "w1", Kind: hub.WorkCredentials, Database: "pds.orders"},
	})

	require.Len(t, results, 1)
	assert.Empty(t, results[0].Error)
	assert.Equal(t, "pds", namespace)
	assert.Equal(t, "orders", name)
}

// The hub releases the database either way, so a failure has to reach it as a
// result. The operator then generates the passwords as it always did.
func TestCredentialsWorkReportsAFailure(t *testing.T) {
	prepare := func(context.Context, string, string) error { return errors.New("secrets is forbidden") }

	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, prepare, []hub.Work{
		{ID: "w1", Kind: hub.WorkCredentials, Database: "pds.orders"},
	})

	require.Len(t, results, 1)
	assert.Contains(t, results[0].Error, "secrets is forbidden")
}

func TestCredentialsWorkWithoutKubernetesIsReported(t *testing.T) {
	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, []hub.Work{
		{ID: "w1", Kind: hub.WorkCredentials, Database: "pds.orders"},
	})

	require.Len(t, results, 1)
	assert.NotEmpty(t, results[0].Error)
}

func TestCredentialsWorkRefusesAnIDWithoutANamespace(t *testing.T) {
	called := false
	prepare := func(context.Context, string, string) error {
		called = true

		return nil
	}

	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, prepare, []hub.Work{
		{ID: "w1", Kind: hub.WorkCredentials, Database: "orders"},
	})

	require.Len(t, results, 1)
	assert.NotEmpty(t, results[0].Error)
	assert.False(t, called)
}

// A seed item queued before the agent stopped seeding is answered as an unknown
// kind, so the hub acknowledges it and drops it instead of handing it out again.
func TestALeftoverSeedItemIsReportedAsUnknown(t *testing.T) {
	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, []hub.Work{
		{ID: "w1", Kind: "seed", Database: "pds.reporting"},
	})

	require.Len(t, results, 1)
	assert.Equal(t, "unknown kind seed", results[0].Error)
}
