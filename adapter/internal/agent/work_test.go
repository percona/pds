package agent

import (
	"context"
	"errors"
	"fmt"
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

	results := applyWork(context.Background(), quietLogger(), db, nil, nil, nil, []hub.Work{
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

	results := applyWork(context.Background(), quietLogger(), db, nil, nil, nil, []hub.Work{
		{ID: "w1", Kind: hub.WorkBackup, Database: "pds.orders-dev"},
	})

	require.Len(t, results, 1)
	assert.Equal(t, "w1", results[0].ID)
	assert.Contains(t, results[0].Error, "instance is not ready")
}

// A seed names the database it seeds from as namespace.name, and carries the
// copy's name apart from it. The copy lands in the source's namespace.
func TestASeedReachesTheSeeder(t *testing.T) {
	var got struct{ namespace, source, backup, name string }
	seed := func(_ context.Context, namespace, source, backupID, name string) error {
		got.namespace, got.source, got.backup, got.name = namespace, source, backupID, name

		return nil
	}

	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, seed, nil, []hub.Work{
		{ID: "w1", Kind: hub.WorkSeed, Database: "pds.reporting", BackupID: "reporting-1", Name: "reporting-copy"},
	})

	require.Len(t, results, 1)
	assert.Empty(t, results[0].Error)
	assert.Equal(t, "pds", got.namespace)
	assert.Equal(t, "reporting", got.source)
	assert.Equal(t, "reporting-1", got.backup)
	assert.Equal(t, "reporting-copy", got.name)
}

func TestAFailedSeedIsReported(t *testing.T) {
	seed := func(context.Context, string, string, string, string) error { return errors.New("backup not found") }

	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, seed, nil, []hub.Work{
		{ID: "w1", Kind: hub.WorkSeed, Database: "pds.reporting", BackupID: "gone", Name: "copy"},
	})

	require.Len(t, results, 1)
	assert.Contains(t, results[0].Error, "backup not found")
}

// An agent that cannot seed says so, so the hub stops offering the item.
func TestASeedWithNoSeederIsReportedRatherThanLeftInFlight(t *testing.T) {
	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, nil, nil, []hub.Work{
		{ID: "w1", Kind: hub.WorkSeed, Database: "pds.reporting", BackupID: "b", Name: "copy"},
	})

	require.Len(t, results, 1)
	assert.Contains(t, results[0].Error, "cannot seed")
}

func TestASeedWithoutANamespacedSourceIsReported(t *testing.T) {
	seed := func(context.Context, string, string, string, string) error { return nil }

	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, seed, nil, []hub.Work{
		{ID: "w1", Kind: hub.WorkSeed, Database: "reporting", BackupID: "b", Name: "copy"},
	})

	require.Len(t, results, 1)
	assert.Contains(t, results[0].Error, "namespace")
}

// A spoke running an older agent than the hub gets kinds it does not know.
// Reporting one keeps the hub from handing it out forever.
func TestAnUnknownKindIsReportedAndSkipped(t *testing.T) {
	db := &fakeDatabases{}

	results := applyWork(context.Background(), quietLogger(), db, nil, nil, nil, []hub.Work{
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

	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, prepare, nil, nil, []hub.Work{
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

	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, prepare, nil, nil, []hub.Work{
		{ID: "w1", Kind: hub.WorkCredentials, Database: "pds.orders"},
	})

	require.Len(t, results, 1)
	assert.Contains(t, results[0].Error, "secrets is forbidden")
}

func TestCredentialsWorkWithoutKubernetesIsReported(t *testing.T) {
	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, nil, nil, []hub.Work{
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

	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, prepare, nil, nil, []hub.Work{
		{ID: "w1", Kind: hub.WorkCredentials, Database: "orders"},
	})

	require.Len(t, results, 1)
	assert.NotEmpty(t, results[0].Error)
	assert.False(t, called)
}

// The hub switches on the reason, not on the error text, so each way a seed
// can fail carries the reason that names its cause.
func TestAFailedSeedCarriesTheReasonForItsCause(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		reason string
	}{
		"backup missing":      {fmt.Errorf("%w: b", engine.ErrBackupNotFound), hub.SeedBackupUnusable},
		"backup not finished": {fmt.Errorf("%w: b is %q", engine.ErrBackupUnusable, "Running"), hub.SeedBackupUnusable},
		"source gone":         {fmt.Errorf("%w: reporting", engine.ErrNotFound), hub.SeedSourceGone},
		"no access":           {fmt.Errorf("%w: %w", engine.ErrSeedUnavailable, errors.New("forbidden")), hub.SeedUnavailable},
		"name taken":          {fmt.Errorf("%w: copy", engine.ErrExists), hub.SeedOther},
		"anything else":       {errors.New("api down"), hub.SeedOther},
	} {
		t.Run(name, func(t *testing.T) {
			seed := func(context.Context, string, string, string, string) error { return tc.err }

			results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, seed, nil, []hub.Work{
				{ID: "w1", Kind: hub.WorkSeed, Database: "pds.reporting", BackupID: "b", Name: "copy"},
			})

			require.Len(t, results, 1)
			assert.Equal(t, tc.reason, results[0].Reason)
			assert.Equal(t, tc.err.Error(), results[0].Error, "the text stays for the log")
		})
	}
}

func TestASeedWithNoSeederReportsItCannotSeed(t *testing.T) {
	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, nil, nil, []hub.Work{
		{ID: "w1", Kind: hub.WorkSeed, Database: "pds.reporting", BackupID: "b", Name: "copy"},
	})

	require.Len(t, results, 1)
	assert.Equal(t, hub.SeedUnavailable, results[0].Reason)
}

func TestASeedWithoutANamespacedSourceReportsOther(t *testing.T) {
	seed := func(context.Context, string, string, string, string) error { return nil }

	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, seed, nil, []hub.Work{
		{ID: "w1", Kind: hub.WorkSeed, Database: "reporting", BackupID: "b", Name: "copy"},
	})

	require.Len(t, results, 1)
	assert.Equal(t, hub.SeedOther, results[0].Reason)
}

func TestOnlyAFailedSeedCarriesAReason(t *testing.T) {
	seed := func(context.Context, string, string, string, string) error { return nil }
	databases := &fakeDatabases{}

	results := applyWork(context.Background(), quietLogger(), databases, nil, seed, nil, []hub.Work{
		{ID: "w1", Kind: hub.WorkSeed, Database: "pds.reporting", BackupID: "b", Name: "copy"},
		{ID: "w2", Kind: hub.WorkDelete, Database: "pds.gone"},
	})

	require.Len(t, results, 2)
	assert.Empty(t, results[0].Reason)
	assert.Empty(t, results[1].Reason)
}
