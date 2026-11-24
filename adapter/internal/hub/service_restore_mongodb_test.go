package hub_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

var restoreBeatAt = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

// mongoSource reports a MongoDB database with one succeeded and one pending
// backup, and any copies the caller wants listed beside it.
func mongoSource(t *testing.T, inv *hub.Inventory, profile string, copies ...engine.Instance) {
	t.Helper()
	instances := []engine.Instance{{
		ID: "spoke-a/pds.orders", Name: "orders", Engine: "mongodb", Namespace: "pds",
		Environment: "spoke-a", Profile: profile, Status: engine.Status{Phase: engine.PhaseReady},
	}}
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: restoreBeatAt,
		Engines:   []string{"mongodb", "mysql", "postgresql"},
		Instances: append(instances, copies...),
		Backups: []engine.Backup{
			{ID: "orders-ok", Instance: "orders", Status: engine.BackupSucceeded},
			{ID: "orders-pending", Instance: "orders", Status: engine.BackupPending},
		},
	})
}

func mongoCopy(phase string) engine.Instance {
	return engine.Instance{
		ID: "spoke-a/pds.orders-copy", Name: "orders-copy", Engine: "mongodb", Namespace: "pds",
		Environment: "spoke-a", Profile: "dev", Status: engine.Status{Phase: phase},
	}
}

func restoreMongoDB(svc *hub.Service, backupID, name string) (*hub.RestoreIssued, error) {
	return svc.RestoreToNewDatabase(context.Background(), "spoke-a/pds.orders", backupID, name)
}

func TestRestoreWritesAMongoDBCopyThatProvisionsFromTheBackup(t *testing.T) {
	for _, profile := range []string{"dev", "staging", "prod"} {
		t.Run(profile, func(t *testing.T) {
			svc, store, inv := newHub(t)
			mongoSource(t, inv, profile)

			issued, err := restoreMongoDB(svc, "orders-ok", "orders-copy")
			require.NoError(t, err)
			assert.Equal(t, "spoke-a/pds.orders-copy", issued.ID)

			body, err := store.Get(context.Background(), "clusters/spoke-a/instances/orders-copy.yaml")
			require.NoError(t, err)
			assert.Equal(t, "orders-ok", dataSourceBackup(t, body))

			var doc struct {
				Metadata struct {
					Labels map[string]string `json:"labels"`
				} `json:"metadata"`
				Spec struct {
					Backup struct {
						Enabled  bool `json:"enabled"`
						Storages []struct {
							StorageRef struct {
								Name string `json:"name"`
							} `json:"storageRef"`
						} `json:"storages"`
					} `json:"backup"`
					Components map[string]struct {
						Service any `json:"service"`
					} `json:"components"`
				} `json:"spec"`
			}
			require.NoError(t, yaml.Unmarshal(body, &doc))
			assert.Equal(t, profile, doc.Metadata.Labels["pds.percona.com/profile"], "the copy keeps the source's profile")
			assert.Equal(t, "mongodb", doc.Metadata.Labels["pds.percona.com/engine"])
			assert.True(t, doc.Spec.Backup.Enabled, "the API refuses a data source on an Instance with backups off")
			require.Len(t, doc.Spec.Backup.Storages, 1)
			assert.Equal(t, "s3", doc.Spec.Backup.Storages[0].StorageRef.Name)
			for name, component := range doc.Spec.Components {
				assert.Nil(t, component.Service, "the copy gets no load balancer: %s", name)
			}
		})
	}
}

func TestRestoreOfAMongoDBBackupOwesTheCopyNewCredentials(t *testing.T) {
	svc, _, inv := newHub(t)
	mongoSource(t, inv, "dev")

	_, err := restoreMongoDB(svc, "orders-ok", "orders-copy")
	require.NoError(t, err)

	work := svc.TakeWork("spoke-a")
	require.Len(t, work, 1)
	assert.Equal(t, hub.WorkRotate, work[0].Kind)
	assert.Equal(t, "pds.orders-copy", work[0].Database)
}

func TestMySQLRestoreOwesNoRotation(t *testing.T) {
	svc, _, inv := newHub(t)
	created, err := svc.Create(context.Background(), engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	svc.TakeWork("spoke-a")
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: restoreBeatAt,
		Instances: []engine.Instance{*created},
		Backups:   []engine.Backup{{ID: "orders-dev-ok", Instance: "orders-dev", Status: engine.BackupSucceeded}},
	})

	_, err = svc.RestoreToNewDatabase(context.Background(), created.ID, "orders-dev-ok", "orders-restore-1")
	require.NoError(t, err)

	assert.Empty(t, svc.TakeWork("spoke-a"))
}

func TestRestoreRefusesAMongoDBCopyNameOver22Characters(t *testing.T) {
	svc, store, inv := newHub(t)
	mongoSource(t, inv, "dev")

	long := strings.Repeat("a", 23)
	_, err := restoreMongoDB(svc, "orders-ok", long)

	var bounds *engine.BoundsError
	require.ErrorAs(t, err, &bounds)
	assert.Equal(t, "A MongoDB database name must be between 1 and 22 characters.", bounds.UserMessage())
	requireNothingWritten(t, store, long)
	assert.Empty(t, svc.TakeWork("spoke-a"), "a refused restore owes nothing")

	issued, err := restoreMongoDB(svc, "orders-ok", strings.Repeat("a", 22))
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", 22), issued.Name)
}

func TestRestoreRefusesAMongoDBBackupThatHasNotSucceeded(t *testing.T) {
	svc, store, inv := newHub(t)
	mongoSource(t, inv, "dev")

	_, err := restoreMongoDB(svc, "orders-pending", "orders-copy")

	var source *engine.BackupSourceError
	require.ErrorAs(t, err, &source)
	requireNothingWritten(t, store, "orders-copy")
	assert.Empty(t, svc.TakeWork("spoke-a"))
}

func TestRestoreRefusesAMongoDBCopyWhenTheEnvironmentHasNoBackupStorage(t *testing.T) {
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	svc := hub.NewService(store, inv, hub.Options{Namespace: "pds", Now: func() time.Time { return restoreBeatAt }})
	mongoSource(t, inv, "dev")

	_, err := restoreMongoDB(svc, "orders-ok", "orders-copy")

	require.ErrorIs(t, err, engine.ErrBackupsUnavailable)
	requireNothingWritten(t, store, "orders-copy")
	assert.Empty(t, svc.TakeWork("spoke-a"))
}

// The copy accepts the source's password until the agent writes a new one, so
// a copy the provider reports as Ready is shown as still restoring.
func TestMongoDBCopyReadsAsRestoringUntilItsCredentialsAreRotated(t *testing.T) {
	svc, _, inv := newHub(t)
	mongoSource(t, inv, "dev")
	_, err := restoreMongoDB(svc, "orders-ok", "orders-copy")
	require.NoError(t, err)
	work := svc.TakeWork("spoke-a")
	require.Len(t, work, 1)
	mongoSource(t, inv, "dev", mongoCopy(engine.PhaseReady))
	ctx := context.Background()

	got, err := svc.Get(ctx, "spoke-a/pds.orders-copy")
	require.NoError(t, err)
	assert.Equal(t, engine.PhaseRestoring, got.Status.Phase)
	assert.NotEmpty(t, got.Status.Message)
	listed, err := svc.List(ctx, "spoke-a")
	require.NoError(t, err)
	found := false
	for _, inst := range listed {
		if inst.Name == "orders-copy" {
			found = true
			assert.Equal(t, engine.PhaseRestoring, inst.Status.Phase)
		}
	}
	require.True(t, found)

	_, accepted, err := svc.AcceptHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds",
		Instances: []engine.Instance{mongoCopy(engine.PhaseReady)},
		Results:   []hub.WorkResult{{ID: work[0].ID}},
	}, nil)
	require.NoError(t, err)
	require.True(t, accepted)

	got, err = svc.Get(ctx, "spoke-a/pds.orders-copy")
	require.NoError(t, err)
	assert.Equal(t, engine.PhaseReady, got.Status.Phase)
}

// Only a Ready copy is held back: one whose restore failed keeps its phase and
// the provider's reason.
func TestMongoDBCopyThatFailedKeepsItsReasonWhileRotationIsOwed(t *testing.T) {
	svc, _, inv := newHub(t)
	mongoSource(t, inv, "dev")
	_, err := restoreMongoDB(svc, "orders-ok", "orders-copy")
	require.NoError(t, err)
	failed := mongoCopy(engine.PhaseFailed)
	failed.Status.Message = "The restore could not read the backup."
	mongoSource(t, inv, "dev", failed)

	got, err := svc.Get(context.Background(), "spoke-a/pds.orders-copy")
	require.NoError(t, err)

	assert.Equal(t, engine.PhaseFailed, got.Status.Phase)
	assert.Equal(t, "The restore could not read the backup.", got.Status.Message)
}

// An error from the agent leaves the copy held: dropping the item would show
// a copy that still shares the source's passwords.
func TestAFailedRotationIsNotAcknowledged(t *testing.T) {
	svc, _, inv := newHub(t)
	mongoSource(t, inv, "dev")
	_, err := restoreMongoDB(svc, "orders-ok", "orders-copy")
	require.NoError(t, err)
	work := svc.TakeWork("spoke-a")
	require.Len(t, work, 1)
	mongoSource(t, inv, "dev", mongoCopy(engine.PhaseReady))

	_, _, err = svc.AcceptHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds",
		Instances: []engine.Instance{mongoCopy(engine.PhaseReady)},
		Results:   []hub.WorkResult{{ID: work[0].ID, Error: "forbidden"}},
	}, nil)
	require.NoError(t, err)

	got, err := svc.Get(context.Background(), "spoke-a/pds.orders-copy")
	require.NoError(t, err)
	assert.Equal(t, engine.PhaseRestoring, got.Status.Phase)
}

func TestDeletingAMongoDBCopyDropsItsRotation(t *testing.T) {
	svc, store, inv := newHub(t)
	mongoSource(t, inv, "dev")
	_, err := restoreMongoDB(svc, "orders-ok", "orders-copy")
	require.NoError(t, err)
	mongoSource(t, inv, "dev", mongoCopy(engine.PhaseFailed))

	require.NoError(t, svc.Delete(context.Background(), "spoke-a/pds.orders-copy"))

	requireNothingWritten(t, store, "orders-copy")
	assert.Empty(t, svc.TakeWork("spoke-a"))
}
