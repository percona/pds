package hub_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/oe"
	"github.com/percona/pds/adapter/internal/hub"
)

func newHub(t *testing.T) (*hub.Service, *hub.MemoryStore, *hub.Inventory) {
	t.Helper()
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
	})

	return svc, store, inv
}

func TestCreateWritesInstanceYAML(t *testing.T) {
	svc, store, _ := newHub(t)
	ctx := context.Background()

	got, err := svc.Create(ctx, engine.CreateInput{
		Name:        "orders-dev",
		Engine:      "mysql",
		Profile:     "dev",
		Environment: "spoke-a",
	})
	require.NoError(t, err)
	assert.Equal(t, "spoke-a/pds.orders-dev", got.ID)
	assert.Equal(t, engine.PhasePending, got.Status.Phase)
	assert.Equal(t, "spoke-a", got.Environment)
	assert.Equal(t, 1, got.Sizing.Members)
	assert.Equal(t, 2.0, got.Sizing.MemoryGB)

	body, err := store.Get(ctx, "clusters/spoke-a/instances/orders-dev.yaml")
	require.NoError(t, err)
	text := string(body)
	assert.Contains(t, text, "kind: Instance")
	assert.Contains(t, text, "percona-xtradb-cluster")
	assert.Contains(t, text, "openeverest.io/instance-preset: pds-mysql-dev")
	assert.Contains(t, text, "name: s3")
	assert.NotContains(t, text, "status:")

	var decoded map[string]any
	require.NoError(t, yaml.Unmarshal(body, &decoded))
	assert.Equal(t, "Instance", decoded["kind"])
}

func createPostgreSQL(t *testing.T, svc *hub.Service, in engine.CreateInput) (*engine.Instance, error) {
	t.Helper()
	in.Name, in.Engine, in.Profile, in.Environment = "reports", "postgresql", "dev", "spoke-a"

	return svc.Create(context.Background(), in)
}

func TestCreateWritesAPostgreSQLInstance(t *testing.T) {
	svc, store, _ := newHub(t)

	got, err := createPostgreSQL(t, svc, engine.CreateInput{})
	require.NoError(t, err)
	assert.Equal(t, "postgresql", got.Engine)
	assert.True(t, got.Exposure.External)
	assert.True(t, got.Exposure.Private)

	body, err := store.Get(context.Background(), "clusters/spoke-a/instances/reports.yaml")
	require.NoError(t, err)
	var doc struct {
		Metadata everest.ObjectMeta   `json:"metadata"`
		Spec     everest.InstanceSpec `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(body, &doc))
	assert.Equal(t, everest.ProviderPostgreSQL, doc.Spec.ProviderRef.Name)
	assert.Equal(t, "postgresql", doc.Metadata.Labels["pds.percona.com/engine"])
	assert.Equal(t, "pds-postgresql-dev", doc.Metadata.Annotations["openeverest.io/instance-preset"])
	assert.Equal(t, everest.ComponentTypePostgreSQL, doc.Spec.Components[everest.ComponentEngine].Type)
	proxy := doc.Spec.Components[everest.ComponentProxy]
	assert.Equal(t, everest.ComponentTypePgBouncer, proxy.Type)
	require.NotNil(t, proxy.Service, "PgBouncer sits behind a load balancer")
	assert.Equal(t, everest.ServiceTypeLoadBalancer, proxy.Service.ServiceType)
	assert.Equal(t, engine.Exposure{External: true, Private: true}, oe.ExposureOf(&everest.Instance{Spec: doc.Spec}))
	assert.Equal(t, everest.BackupClassPostgreSQL, doc.Spec.Backup.ClassRef.Name)
	assert.NotContains(t, string(body), "haproxy")
}

func TestCreateWritesTheEngineLabelOnMySQLToo(t *testing.T) {
	svc, store, _ := newHub(t)
	_, err := svc.Create(context.Background(), engine.CreateInput{
		Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.NoError(t, err)

	body, err := store.Get(context.Background(), "clusters/spoke-a/instances/orders.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), "pds.percona.com/engine: mysql")
}

func TestCreateWritesAPublicPostgreSQLLoadBalancer(t *testing.T) {
	svc, store, _ := newHub(t)

	// The allow list is outside the private ranges, so a manifest that wrote
	// a private load balancer instead cannot pass for this one.
	want := engine.Exposure{External: true, SourceRanges: []string{"203.0.113.0/24"}}
	got, err := createPostgreSQL(t, svc, engine.CreateInput{Access: engine.AccessPublic, SourceRanges: want.SourceRanges})
	require.NoError(t, err)
	assert.Equal(t, want, got.Exposure)

	body, err := store.Get(context.Background(), "clusters/spoke-a/instances/reports.yaml")
	require.NoError(t, err)
	var doc struct {
		Spec everest.InstanceSpec `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(body, &doc))
	assert.Equal(t, want, oe.ExposureOf(&everest.Instance{Spec: doc.Spec}))
}

func TestCreateRefusesAPublicPostgreSQLWithoutRanges(t *testing.T) {
	svc, store, _ := newHub(t)

	_, err := createPostgreSQL(t, svc, engine.CreateInput{Access: engine.AccessPublic})

	var rangeErr *engine.SourceRangeError
	require.ErrorAs(t, err, &rangeErr)
	_, getErr := store.Get(context.Background(), "clusters/spoke-a/instances/reports.yaml")
	require.ErrorIs(t, getErr, hub.ErrNotFound)
}

// The operator labels every PostgreSQL member <name>-instance1-<4>-<10>, and
// Kubernetes refuses a label over 63, so a longer name never starts.
func TestCreateRefusesAPostgreSQLNameItsMembersCannotCarry(t *testing.T) {
	svc, store, _ := newHub(t)
	long := strings.Repeat("a", 38)

	_, err := svc.Create(context.Background(), engine.CreateInput{
		Name: long, Engine: "postgresql", Profile: "dev", Environment: "spoke-a",
	})

	var bounds *engine.BoundsError
	require.ErrorAs(t, err, &bounds)
	assert.Equal(t, 37, bounds.Max)
	_, getErr := store.Get(context.Background(), "clusters/spoke-a/instances/"+long+".yaml")
	require.ErrorIs(t, getErr, hub.ErrNotFound)
}

func TestCreateTakesTheLongestPostgreSQLNameItsMembersCarry(t *testing.T) {
	svc, _, _ := newHub(t)

	_, err := svc.Create(context.Background(), engine.CreateInput{
		Name: strings.Repeat("a", 37), Engine: "postgresql", Profile: "dev", Environment: "spoke-a",
	})

	require.NoError(t, err)
}

func TestCreateRefusesAnUnknownEngine(t *testing.T) {
	svc, _, _ := newHub(t)
	_, err := svc.Create(context.Background(), engine.CreateInput{
		Name: "orders", Engine: "mongodb", Profile: "dev", Environment: "spoke-a",
	})

	require.ErrorIs(t, err, hub.ErrEngineNotSupported)
}

func TestAPendingPostgreSQLDatabaseReportsItsEngine(t *testing.T) {
	svc, _, _ := newHub(t)
	ctx := context.Background()
	_, err := createPostgreSQL(t, svc, engine.CreateInput{})
	require.NoError(t, err)

	got, err := svc.Get(ctx, "spoke-a/pds.reports")
	require.NoError(t, err)
	assert.Equal(t, "postgresql", got.Engine)

	list, err := svc.List(ctx, "spoke-a")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "postgresql", list[0].Engine)
}

func TestGetReadsProfileFromGitYAML(t *testing.T) {
	svc, _, _ := newHub(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, engine.CreateInput{
		Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.NoError(t, err)

	got, err := svc.Get(ctx, "spoke-a/pds.orders-dev")
	require.NoError(t, err)
	assert.Equal(t, "dev", got.Profile)
}

func TestCreateDuplicateConflicts(t *testing.T) {
	svc, _, _ := newHub(t)
	ctx := context.Background()
	in := engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"}
	_, err := svc.Create(ctx, in)
	require.NoError(t, err)
	_, err = svc.Create(ctx, in)
	require.ErrorIs(t, err, engine.ErrExists)
}

func TestCreateUnreachableEnvironment(t *testing.T) {
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	svc := hub.NewService(store, inv, hub.Options{
		Namespace: "pds",
		Now:       func() time.Time { return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC) },
	})

	_, err := svc.Create(context.Background(), engine.CreateInput{
		Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.ErrorIs(t, err, hub.ErrUnreachable)
}

func TestDeleteRemovesInstanceFile(t *testing.T) {
	svc, store, _ := newHub(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)

	require.NoError(t, svc.Delete(ctx, "spoke-a/pds.orders-dev"))
	_, err = store.Get(ctx, "clusters/spoke-a/instances/orders-dev.yaml")
	require.ErrorIs(t, err, hub.ErrNotFound)
}

func TestBackupEnqueuesWorkAndDoesNotWriteGit(t *testing.T) {
	svc, store, inv := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	releaseCredentials(t, svc)

	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		Instances:   []engine.Instance{*created},
	})

	backup, err := svc.Backup(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, engine.BackupPending, backup.Status)
	assert.True(t, strings.HasPrefix(backup.ID, "orders-dev-"))

	_, err = store.Get(ctx, "clusters/spoke-a/backups/"+backup.ID+".yaml")
	require.ErrorIs(t, err, hub.ErrNotFound)

	work := svc.TakeWork("spoke-a")
	require.Len(t, work, 1)
	assert.Equal(t, hub.WorkBackup, work[0].Kind)
	assert.Equal(t, backup.ID, work[0].BackupID)
	assert.Equal(t, "pds.orders-dev", work[0].Database)
}

func TestBackupIDFitsTheLabelLimit(t *testing.T) {
	svc, _, inv := newHub(t)
	ctx := context.Background()
	long := "a-very-long-database-name-that-uses-most-of-the-dns-label-limit"
	require.Len(t, long, 63)
	created, err := svc.Create(ctx, engine.CreateInput{Name: long, Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		Instances:   []engine.Instance{*created},
	})

	backup, err := svc.Backup(ctx, created.ID)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(backup.ID), 63)
	assert.Regexp(t, `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`, backup.ID)
}

func TestBackupsKeepsPendingUntilHeartbeatShowsIt(t *testing.T) {
	svc, _, inv := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
	})

	backup, err := svc.Backup(ctx, created.ID)
	require.NoError(t, err)

	listed, err := svc.Backups(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, backup.ID, listed[0].ID)
	assert.Equal(t, engine.BackupPending, listed[0].Status)

	svc.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
	})
	listed, err = svc.Backups(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, backup.ID, listed[0].ID)

	svc.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
		Backups: []engine.Backup{{
			ID: backup.ID, Instance: created.Name, Status: engine.BackupSucceeded, SizeBytes: 1024,
		}},
	})
	listed, err = svc.Backups(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, engine.BackupSucceeded, listed[0].Status)
	assert.Equal(t, int64(1024), listed[0].SizeBytes)
}

func TestBackupsMissingDatabaseOnKnownEnvironment(t *testing.T) {
	svc, _, inv := newHub(t)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{{
			ID:          "spoke-a/pds.orders-dev",
			Name:        "orders-dev",
			Namespace:   "pds",
			Environment: "spoke-a",
			Status:      engine.Status{Phase: engine.PhaseReady},
		}},
	})

	_, err := svc.Backups(context.Background(), "spoke-a/pds.missing")
	require.ErrorIs(t, err, engine.ErrNotFound)
}

func TestApplyHeartbeatRequeuesMissingPendingBackup(t *testing.T) {
	svc, _, inv := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	releaseCredentials(t, svc)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
	})

	backup, err := svc.Backup(ctx, created.ID)
	require.NoError(t, err)
	sent := svc.TakeWork("spoke-a")
	require.Len(t, sent, 1)

	// The spoke reports the item failed, so the hub stops holding it. The
	// backup is still pending in the inventory, which is what makes the next
	// beat ask for it again.
	svc.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
		Results:   []hub.WorkResult{{ID: sent[0].ID, Error: "everest was not ready"}},
	})
	work := svc.TakeWork("spoke-a")
	require.Len(t, work, 1)
	assert.Equal(t, hub.WorkBackup, work[0].Kind)
	assert.Equal(t, backup.ID, work[0].BackupID)

	// Once the spoke reports the backup running, it is no longer pending and
	// nothing is asked for again.
	svc.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
		Backups:   []engine.Backup{{ID: backup.ID, Instance: created.Name, Status: engine.BackupRunning}},
		Results:   []hub.WorkResult{{ID: work[0].ID}},
	})
	assert.Empty(t, svc.TakeWork("spoke-a"))
}

// TestUnacknowledgedWorkComesBack covers the spoke that reads a reply and
// dies before applying it. The hub used to delete the item when it built the
// reply, so a delete the UI reported as accepted simply never happened.
func TestUnacknowledgedWorkComesBack(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	clock := base

	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Hour)
	inv.Register("spoke-a", "pds")
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return clock },
	})
	svc.SetWorkClock(func() time.Time { return clock })

	id := "spoke-a/pds.orders-dev"
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: clock,
		Instances: []engine.Instance{{
			ID: id, Name: "orders-dev", Namespace: "pds", Environment: "spoke-a",
			Status: engine.Status{Phase: engine.PhaseReady},
		}},
	})

	require.NoError(t, svc.Delete(ctx, id))
	sent := svc.TakeWork("spoke-a")
	require.Len(t, sent, 1)
	assert.Equal(t, hub.WorkDelete, sent[0].Kind)
	assert.NotEmpty(t, sent[0].ID, "work the spoke has to acknowledge needs an id")

	// The spoke never answers. Nothing comes back inside the window, because
	// it may simply still be working.
	clock = clock.Add(30 * time.Second)
	assert.Empty(t, svc.TakeWork("spoke-a"))

	// Past the window the hub asks again rather than losing the delete.
	clock = clock.Add(5 * time.Minute)
	again := svc.TakeWork("spoke-a")
	require.Len(t, again, 1)
	assert.Equal(t, sent[0].ID, again[0].ID, "a retry keeps the id so the spoke can be idempotent")

	// Once acknowledged it is gone for good.
	svc.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: clock,
		Results: []hub.WorkResult{{ID: again[0].ID}},
	})
	clock = clock.Add(10 * time.Minute)
	assert.Empty(t, svc.TakeWork("spoke-a"))
}

func TestRestoreRefusesUnfinishedBackup(t *testing.T) {
	svc, _, inv := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
	})
	backup, err := svc.Backup(ctx, created.ID)
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
		Backups:   []engine.Backup{{ID: backup.ID, Instance: created.Name, Status: engine.BackupPending}},
	})

	_, err = svc.RestoreToNewDatabase(ctx, created.ID, backup.ID, "orders-restore-1")
	require.ErrorIs(t, err, engine.ErrBackupUnusable)
}

// TestRestoreWritesANewInstanceOnTheSourceEnvironment covers what a restore is:
// a second database on the environment the source already runs on, provisioned
// from the backup, with the source left as it is. Nothing is built for it.
func TestRestoreWritesANewInstanceOnTheSourceEnvironment(t *testing.T) {
	svc, store, inv := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
		Backups: []engine.Backup{{
			ID: "orders-dev-ok", Instance: "orders-dev", Status: engine.BackupSucceeded,
		}},
	})

	issued, err := svc.RestoreToNewDatabase(ctx, created.ID, "orders-dev-ok", "orders-restore-1")
	require.NoError(t, err)
	assert.Equal(t, "spoke-a", issued.Environment)
	assert.Equal(t, "orders-restore-1", issued.Name)
	assert.Equal(t, "spoke-a/pds.orders-restore-1", issued.ID)

	body, err := store.Get(ctx, "clusters/spoke-a/instances/orders-restore-1.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), "kind: Instance")
	assert.Equal(t, "orders-dev-ok", dataSourceBackup(t, body),
		"the new instance provisions from the backup rather than coming up empty")

	// The source keeps the manifest create wrote. A dataSource on it would
	// restore over the database the developer still depends on.
	source, err := store.Get(ctx, "clusters/spoke-a/instances/orders-dev.yaml")
	require.NoError(t, err)
	assert.Empty(t, dataSourceBackup(t, source))

	// Flux applies what the index lists, so a manifest missing from it is a
	// database that never appears.
	index, err := store.Get(ctx, "clusters/spoke-a/instances/kustomization.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(index), "orders-restore-1.yaml")

	// No second environment: no join minted, no tree for one to pull.
	environments := svc.Environments()
	require.Len(t, environments, 1)
	assert.Equal(t, "spoke-a", environments[0].Name)
	paths, err := store.List(ctx, "clusters/")
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		assert.True(t, strings.HasPrefix(path, "clusters/spoke-a/"), path)
	}
}

// The provider builds its restore job name out of the copy's name twice over,
// and Kubernetes refuses that label past 63 characters. A longer name used to
// commit a manifest that Flux applied and that came up as an empty database.
func TestRestoreRefusesACopyNameTooLongForTheRestoreJob(t *testing.T) {
	svc, store, inv := newHub(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{{
			ID:          "spoke-a/pds.orders-dev",
			Name:        "orders-dev",
			Engine:      "mysql",
			Namespace:   "pds",
			Environment: "spoke-a",
			Profile:     "dev",
			Status:      engine.Status{Phase: engine.PhaseReady},
		}},
		Backups: []engine.Backup{{
			ID: "orders-dev-ok", Instance: "orders-dev", Status: engine.BackupSucceeded,
		}},
	})

	long := strings.Repeat("a", 20)
	_, err := svc.RestoreToNewDatabase(ctx, "spoke-a/pds.orders-dev", "orders-dev-ok", long)
	require.ErrorIs(t, err, engine.ErrInvalidName)
	assert.Contains(t, err.Error(), "19")
	_, getErr := store.Get(ctx, "clusters/spoke-a/instances/"+long+".yaml")
	require.ErrorIs(t, getErr, hub.ErrNotFound, "nothing is committed for a name the provider cannot restore")

	// One character shorter is the longest name the job label still fits.
	issued, err := svc.RestoreToNewDatabase(ctx, "spoke-a/pds.orders-dev", "orders-dev-ok", strings.Repeat("a", 19))
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", 19), issued.Name)
}

func postgresSource(t *testing.T, inv *hub.Inventory) {
	t.Helper()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{{
			ID:          "spoke-a/pds.reporting",
			Name:        "reporting",
			Engine:      "postgresql",
			Namespace:   "pds",
			Environment: "spoke-a",
			Profile:     "dev",
			Status:      engine.Status{Phase: engine.PhaseReady},
		}},
		Backups: []engine.Backup{{
			ID: "reporting-ok", Instance: "reporting", Status: engine.BackupSucceeded,
		}},
	})
}

// A PostgreSQL copy is committed like a MySQL one: the Instance carries the
// dataSource and the provider bootstraps the new cluster from the source's
// backup, so the hub queues nothing for the agent.
func TestRestoreOfAPostgreSQLBackupCommitsTheInstanceWithItsDataSource(t *testing.T) {
	svc, store, inv := newHub(t)
	ctx := context.Background()
	postgresSource(t, inv)

	issued, err := svc.RestoreToNewDatabase(ctx, "spoke-a/pds.reporting", "reporting-ok", "reporting-restore-1")
	require.NoError(t, err)
	assert.Equal(t, "spoke-a/pds.reporting-restore-1", issued.ID)

	body, err := store.Get(ctx, "clusters/spoke-a/instances/reporting-restore-1.yaml")
	require.NoError(t, err)
	assert.Equal(t, "reporting-ok", dataSourceBackup(t, body))
	index, err := store.Get(ctx, "clusters/spoke-a/instances/kustomization.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(index), "reporting-restore-1.yaml")

	assert.Empty(t, svc.TakeWork("spoke-a"), "nothing is left for the agent to seed")

	// The name is taken as soon as the manifest is committed.
	_, err = svc.RestoreToNewDatabase(ctx, "spoke-a/pds.reporting", "reporting-ok", "reporting-restore-1")
	require.ErrorIs(t, err, engine.ErrExists)
}

// The longest label a copy's name lands in is the controller revision hash of
// its instance set, which adds 26 characters to it.
func TestRestoreRefusesAPostgreSQLCopyNameTooLongForItsMembers(t *testing.T) {
	svc, store, inv := newHub(t)
	ctx := context.Background()
	postgresSource(t, inv)

	long := strings.Repeat("a", 38)
	_, err := svc.RestoreToNewDatabase(ctx, "spoke-a/pds.reporting", "reporting-ok", long)
	require.ErrorIs(t, err, engine.ErrInvalidName)
	assert.Contains(t, err.Error(), "37")
	_, getErr := store.Get(ctx, "clusters/spoke-a/instances/"+long+".yaml")
	require.ErrorIs(t, getErr, hub.ErrNotFound)
	assert.Empty(t, svc.TakeWork("spoke-a"))

	issued, err := svc.RestoreToNewDatabase(ctx, "spoke-a/pds.reporting", "reporting-ok", strings.Repeat("a", 37))
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", 37), issued.Name)
}

// A restore that can never work says so, rather than asking the developer to
// wait for a backup or to pick another one.
func TestRestoreRefusesAnUnknownEngineBeforeLookingAtTheBackup(t *testing.T) {
	svc, _, inv := newHub(t)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		Instances: []engine.Instance{{
			ID: "spoke-a/pds.reporting", Name: "reporting", Engine: "mongodb",
			Namespace: "pds", Environment: "spoke-a", Profile: "dev",
			Status: engine.Status{Phase: engine.PhaseReady},
		}},
	})

	_, err := svc.RestoreToNewDatabase(context.Background(), "spoke-a/pds.reporting", "reporting-unseen", "reporting-restore-1")

	require.ErrorIs(t, err, hub.ErrRestoreNotSupported)
	require.ErrorIs(t, err, hub.ErrEngineNotSupported, "the cause stays on the error")
}

// dataSourceBackup reads the backup an Instance manifest provisions from, and
// is empty for one that provisions empty.
func dataSourceBackup(t *testing.T, body []byte) string {
	t.Helper()

	var doc struct {
		Spec struct {
			DataSource struct {
				Type   string `json:"type"`
				Backup struct {
					BackupRef struct {
						Name string `json:"name"`
					} `json:"backupRef"`
				} `json:"backup"`
			} `json:"dataSource"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(body, &doc))
	if doc.Spec.DataSource.Backup.BackupRef.Name == "" {
		return ""
	}
	require.Equal(t, "Backup", doc.Spec.DataSource.Type)

	return doc.Spec.DataSource.Backup.BackupRef.Name
}

// indexFailingStore fails the listing the kustomization rewrite reads. That is
// the second of the two writes a restore makes, and the one that decides
// whether Flux ever sees the manifest.
type indexFailingStore struct {
	hub.Store
	fail bool
}

func (s *indexFailingStore) List(ctx context.Context, prefix string) ([]string, error) {
	if s.fail {
		return nil, errors.New("git listing failed")
	}

	return s.Store.List(ctx, prefix)
}

// TestARestoreThatCannotIndexRollsBackItsManifest covers the half written
// restore. A manifest no index lists is a database that never appears, and
// leaving it behind also takes the name: the absent check matches it, so the
// same request answers 409 from then on.
func TestARestoreThatCannotIndexRollsBackItsManifest(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	backing := hub.NewMemoryStore()
	store := &indexFailingStore{Store: backing}
	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
	})

	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
		Backups:   []engine.Backup{{ID: "orders-dev-ok", Instance: "orders-dev", Status: engine.BackupSucceeded}},
	})

	store.fail = true
	_, err = svc.RestoreToNewDatabase(ctx, created.ID, "orders-dev-ok", "orders-restore-1")
	require.Error(t, err)

	_, err = backing.Get(ctx, "clusters/spoke-a/instances/orders-restore-1.yaml")
	require.ErrorIs(t, err, hub.ErrNotFound, "the manifest outlived the failure")

	// The name has to be usable again, which is the reason the rollback
	// matters more here than the orphan file does.
	store.fail = false
	issued, err := svc.RestoreToNewDatabase(ctx, created.ID, "orders-dev-ok", "orders-restore-1")
	require.NoError(t, err)
	assert.Equal(t, "orders-restore-1", issued.Name)
}

// TestRestoreRefusesANameThatIsNotALabel checks the name before it becomes a
// path segment in Git and a metadata.name Flux applies.
func TestRestoreRefusesANameThatIsNotALabel(t *testing.T) {
	svc, store, inv := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
		Backups:   []engine.Backup{{ID: "orders-dev-ok", Instance: "orders-dev", Status: engine.BackupSucceeded}},
	})

	names := map[string]string{
		"traversal": "../../spoke-b/instances/evil",
		"slash":     "orders/restore",
		"space":     "orders restore",
		"capital":   "OrdersRestore",
		"empty":     "",
	}

	for label, name := range names {
		t.Run(label, func(t *testing.T) {
			_, err := svc.RestoreToNewDatabase(ctx, created.ID, "orders-dev-ok", name)
			require.ErrorIs(t, err, engine.ErrInvalidName)
		})
	}

	paths, err := store.List(ctx, "clusters/spoke-b/")
	require.NoError(t, err)
	assert.Empty(t, paths)
}

func TestGetOverlaysLastBackupAtFromHeartbeat(t *testing.T) {
	svc, _, inv := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	finished := time.Date(2026, 9, 8, 11, 30, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		Instances: []engine.Instance{*created},
		Backups: []engine.Backup{{
			ID: "orders-dev-ok", Instance: "orders-dev", Status: engine.BackupSucceeded, FinishedAt: finished,
		}},
	})

	got, err := svc.Get(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LastBackupAt)
	assert.True(t, got.LastBackupAt.Equal(finished))
}

func TestListReadsHeartbeatCache(t *testing.T) {
	svc, _, inv := newHub(t)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{{
			ID: "spoke-a/pds.orders-dev", Name: "orders-dev",
			Namespace: "pds", Environment: "spoke-a",
			Status: engine.Status{Phase: engine.PhaseReady},
		}},
	})

	items, err := svc.List(context.Background(), "spoke-a")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, engine.PhaseReady, items[0].Status.Phase)
}

// TestCreateRefusesANameThatIsNotALabel covers the name check. The HTTP role
// never reaches a plugin, and the name becomes both a path segment in Git and
// a metadata.name Flux applies.
func TestCreateRefusesANameThatIsNotALabel(t *testing.T) {
	svc, store, _ := newHub(t)
	ctx := context.Background()

	names := map[string]string{
		"traversal":   "../../spoke-b/instances/evil",
		"slash":       "orders/dev",
		"space":       "orders dev",
		"capital":     "OrdersDev",
		"leading dot": ".orders",
		"empty":       "",
	}

	for label, name := range names {
		t.Run(label, func(t *testing.T) {
			_, err := svc.Create(ctx, engine.CreateInput{
				Name:        name,
				Engine:      "mysql",
				Profile:     "dev",
				Environment: "spoke-a",
			})
			require.ErrorIs(t, err, engine.ErrInvalidName)
		})
	}

	// The traversal must not have reached another environment's directory.
	paths, err := store.List(ctx, "clusters/spoke-b/")
	require.NoError(t, err)
	assert.Empty(t, paths)
}

// TestRestoreRefusesANameAlreadyOnTheEnvironment guards the one path that could
// overwrite a database nobody named. The dialog promises the source is
// unchanged and says nothing about anything else on the environment.
func TestRestoreRefusesANameAlreadyOnTheEnvironment(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	seed := func(t *testing.T) (*hub.Service, *hub.Inventory, string) {
		t.Helper()
		svc, _, inv := newHub(t)
		created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
		require.NoError(t, err)
		inv.ApplyHeartbeat(hub.Heartbeat{
			Environment: "spoke-a", Namespace: "pds", At: now,
			Instances: []engine.Instance{*created},
			Backups:   []engine.Backup{{ID: "orders-dev-ok", Instance: "orders-dev", Status: engine.BackupSucceeded}},
		})

		return svc, inv, created.ID
	}

	t.Run("already in git", func(t *testing.T) {
		svc, _, id := seed(t)
		_, err := svc.Create(ctx, engine.CreateInput{Name: "orders-restore-1", Engine: "mysql", Profile: "prod", Environment: "spoke-a"})
		require.NoError(t, err)

		_, err = svc.RestoreToNewDatabase(ctx, id, "orders-dev-ok", "orders-restore-1")
		require.ErrorIs(t, err, engine.ErrExists)
	})

	t.Run("only in the heartbeat", func(t *testing.T) {
		svc, inv, id := seed(t)
		inv.ApplyHeartbeat(hub.Heartbeat{
			Environment: "spoke-a", Namespace: "pds", At: now,
			Instances: []engine.Instance{{
				ID:          "spoke-a/pds.orders-restore-1",
				Name:        "orders-restore-1",
				Namespace:   "pds",
				Environment: "spoke-a",
				Status:      engine.Status{Phase: engine.PhaseReady},
			}},
			Backups: []engine.Backup{{ID: "orders-dev-ok", Instance: "orders-dev", Status: engine.BackupSucceeded}},
		})

		_, err := svc.RestoreToNewDatabase(ctx, id, "orders-dev-ok", "orders-restore-1")
		require.ErrorIs(t, err, engine.ErrExists)
	})

	// The source database is the name most likely to be typed by mistake.
	t.Run("the source itself", func(t *testing.T) {
		svc, _, id := seed(t)

		_, err := svc.RestoreToNewDatabase(ctx, id, "orders-dev-ok", "orders-dev")
		require.ErrorIs(t, err, engine.ErrExists)
	})
}

// TestCreateUsesTheStorageTheSpokeReported covers a hub serving spokes with
// different backup storage. One global name stamped a kind spoke's manifests
// with s3, which that environment has never registered, so Flux applied an
// Instance referencing an object that is not there.
func TestCreateUsesTheStorageTheSpokeReported(t *testing.T) {
	svc, store, inv := newHub(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	// The hub default is s3, but this spoke runs MinIO and says so.
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment:   "spoke-a",
		Namespace:     "pds",
		BackupStorage: "minio",
		At:            now,
	})

	_, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)

	body, err := store.Get(ctx, "clusters/spoke-a/instances/orders-dev.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), "name: minio")
	assert.NotContains(t, string(body), "name: s3")
}

// TestCreateFallsBackToTheConfiguredStorage keeps a spoke that has not
// reported one yet on the hub default.
func TestCreateFallsBackToTheConfiguredStorage(t *testing.T) {
	svc, store, _ := newHub(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)

	body, err := store.Get(ctx, "clusters/spoke-a/instances/orders-dev.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), "name: s3")
}

func TestBackupsBelongToOneDatabase(t *testing.T) {
	svc, _, inv := newHub(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	instance := func(name string) engine.Instance {
		return engine.Instance{
			ID:          "spoke-a/pds." + name,
			Name:        name,
			Namespace:   "pds",
			Environment: "spoke-a",
			Status:      engine.Status{Phase: engine.PhaseReady},
			Sizing:      engine.Sizing{Members: 1, CPUCores: 1, MemoryGB: 2, StorageGB: 20},
		}
	}

	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{instance("orders"), instance("orders-eu")},
		Backups: []engine.Backup{
			{ID: "orders-20260908120000", Instance: "orders", Status: engine.BackupSucceeded, CreatedAt: now},
			{ID: "orders-eu-20260908120000", Instance: "orders-eu", Status: engine.BackupSucceeded, CreatedAt: now},
		},
	})

	orders, err := svc.Backups(ctx, "spoke-a/pds.orders")
	require.NoError(t, err)
	require.Len(t, orders, 1, "orders picked up its sibling's backup")
	assert.Equal(t, "orders-20260908120000", orders[0].ID)

	eu, err := svc.Backups(ctx, "spoke-a/pds.orders-eu")
	require.NoError(t, err)
	require.Len(t, eu, 1)
	assert.Equal(t, "orders-eu-20260908120000", eu[0].ID)

	// And a restore cannot reach across either.
	_, err = svc.RestoreToNewDatabase(ctx, "spoke-a/pds.orders", "orders-eu-20260908120000", "orders-restore-1")
	require.Error(t, err, "orders restored from its sibling's backup")
}

func TestOwnerlessBackupsAreIgnored(t *testing.T) {
	svc, _, inv := newHub(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{{
			ID: "spoke-a/pds.orders", Name: "orders", Namespace: "pds", Environment: "spoke-a",
			Status: engine.Status{Phase: engine.PhaseReady},
			Sizing: engine.Sizing{Members: 1, CPUCores: 1, MemoryGB: 2, StorageGB: 20},
		}, {
			ID: "spoke-a/pds.orders-eu", Name: "orders-eu", Namespace: "pds", Environment: "spoke-a",
			Status: engine.Status{Phase: engine.PhaseReady},
			Sizing: engine.Sizing{Members: 1, CPUCores: 1, MemoryGB: 2, StorageGB: 20},
		}},
		Backups: []engine.Backup{
			{ID: "orders-eu-20260908120000", Status: engine.BackupSucceeded, CreatedAt: now},
		},
	})

	got, err := svc.Backups(ctx, "spoke-a/pds.orders")
	require.NoError(t, err)
	assert.Empty(t, got, "an ownerless backup must not attach to a prefix sibling")

	_, err = svc.RestoreToNewDatabase(ctx, "spoke-a/pds.orders", "orders-eu-20260908120000", "orders-restore-1")
	require.Error(t, err)
}

// TestScaleRewritesTheManifest covers the instance kind the hub itself
// creates. Scaling one used to answer 501, so the only databases hub mode
// produces were the ones it could not resize.
func TestScaleRewritesTheManifest(t *testing.T) {
	svc, store, _ := newHub(t)
	ctx := context.Background()

	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)

	cpu, memory, storageGB, replicas := 4, 16, 250, 3
	require.NoError(t, svc.Scale(ctx, created.ID, engine.ScaleInput{
		CPUCores:  &cpu,
		MemoryGB:  &memory,
		StorageGB: &storageGB,
		Replicas:  &replicas,
	}))

	body, err := store.Get(ctx, "clusters/spoke-a/instances/orders-dev.yaml")
	require.NoError(t, err)
	text := string(body)
	assert.Contains(t, text, `cpu: "4"`)
	assert.Contains(t, text, "memory: 16Gi")
	assert.Contains(t, text, "size: 250Gi")
	assert.Contains(t, text, "replicas: 3")
	// The proxy tier follows the engine tier, the way the chart's presets do.
	assert.Contains(t, text, "type: haproxy")
	// Rewriting must keep what the manifest already carried.
	assert.Contains(t, text, "openeverest.io/instance-preset: pds-mysql-dev")
	assert.Contains(t, text, "pds.percona.com/environment: spoke-a")
}

func TestHubScaleRejectsTheSameValuesOpenEverestDoes(t *testing.T) {
	svc, _, inv := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)

	zero := 0
	err = svc.Scale(ctx, created.ID, engine.ScaleInput{CPUCores: &zero})
	var bounds *engine.BoundsError
	require.ErrorAs(t, err, &bounds)

	shrink := 10
	err = svc.Scale(ctx, created.ID, engine.ScaleInput{StorageGB: &shrink})
	require.ErrorIs(t, err, engine.ErrStorageShrink)

	even := 2
	err = svc.Scale(ctx, created.ID, engine.ScaleInput{Replicas: &even})
	var choice *engine.ChoiceError
	require.ErrorAs(t, err, &choice)

	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
		Backups: []engine.Backup{{
			ID: "orders-dev-1", Instance: "orders-dev", Status: engine.BackupRunning, CreatedAt: now,
		}},
	})
	cpu := 2
	err = svc.Scale(ctx, created.ID, engine.ScaleInput{CPUCores: &cpu})
	var busy *engine.BusyError
	require.ErrorAs(t, err, &busy)
}

// TestSetBackupScheduleWithoutInputRewritesTheManifest covers a chat
// schedule action with no body on a Dev database.
func TestSetBackupScheduleWithoutInputRewritesTheManifest(t *testing.T) {
	svc, store, _ := newHub(t)
	ctx := context.Background()

	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)

	before, err := store.Get(ctx, "clusters/spoke-a/instances/orders-dev.yaml")
	require.NoError(t, err)
	require.NotContains(t, string(before), "daily", "dev starts with on-demand backups")

	got, err := svc.SetBackupSchedule(ctx, created.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "0 2 * * *", got.Cron)

	after, err := store.Get(ctx, "clusters/spoke-a/instances/orders-dev.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(after), "name: daily")
	assert.Contains(t, string(after), "0 2 * * *")
	assert.Contains(t, string(after), "retention:")
	assert.Contains(t, string(after), "count: 7")
	assert.NotContains(t, string(after), "retentionCopies")

	// Repeating it changes nothing, because the same action can be sent twice.
	_, err = svc.SetBackupSchedule(ctx, created.ID, nil)
	require.NoError(t, err)
	again, err := store.Get(ctx, "clusters/spoke-a/instances/orders-dev.yaml")
	require.NoError(t, err)
	assert.Equal(t, string(after), string(again))
}

func TestSetBackupScheduleWithoutInputReplacesADisabledDaily(t *testing.T) {
	svc, store, _ := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders", Engine: "mysql", Profile: "staging", Environment: "spoke-a"})
	require.NoError(t, err)

	require.NoError(t, rewriteInstanceSchedules(ctx, store, "clusters/spoke-a/instances/orders.yaml", []everest.InstanceBackupSchedule{{
		Name:      "daily",
		Enabled:   false,
		Cron:      "0 2 * * *",
		Retention: everest.CountRetention(7),
	}}))

	_, err = svc.SetBackupSchedule(ctx, created.ID, nil)
	require.NoError(t, err)

	schedules := instanceSchedules(t, ctx, store, "clusters/spoke-a/instances/orders.yaml")
	require.Len(t, schedules, 1)
	assert.Equal(t, "daily", schedules[0].Name)
	assert.True(t, schedules[0].Enabled)
	assert.Equal(t, "0 2 * * *", schedules[0].Cron)
}

// TestSetBackupScheduleEditsInPlace writes a period and time over the
// schedule a Staging database was created with, on every period, and turns
// off a schedule the hub did not write so only one runs.
func TestSetBackupScheduleEditsInPlace(t *testing.T) {
	tests := []struct {
		name     string
		in       engine.ScheduleInput
		wantCron string
	}{
		{"hourly", engine.ScheduleInput{Enabled: true, Period: engine.PeriodHourly, Minute: 10, RetentionCopies: 24}, "10 * * * *"},
		{"daily", engine.ScheduleInput{Enabled: true, Period: engine.PeriodDaily, Minute: 30, Hour: 4, RetentionCopies: 14}, "30 4 * * *"},
		{"weekly", engine.ScheduleInput{Enabled: true, Period: engine.PeriodWeekly, Hour: 6, Weekday: 0, RetentionCopies: 4}, "0 6 * * 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, store, _ := newHub(t)
			ctx := context.Background()
			created, err := svc.Create(ctx, engine.CreateInput{Name: "orders", Engine: "mysql", Profile: "staging", Environment: "spoke-a"})
			require.NoError(t, err)
			path := "clusters/spoke-a/instances/orders.yaml"
			schedules := instanceSchedules(t, ctx, store, path)
			require.NoError(t, rewriteInstanceSchedules(ctx, store, path, append(schedules, everest.InstanceBackupSchedule{
				Name: "audit", Enabled: true, Cron: "0 0 1 * *",
			})))

			in := tt.in
			got, err := svc.SetBackupSchedule(ctx, created.ID, &in)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, tt.wantCron, got.Cron)

			schedules = instanceSchedules(t, ctx, store, path)
			require.Len(t, schedules, 2)
			assert.Equal(t, engine.ScheduleName, schedules[0].Name)
			assert.True(t, schedules[0].Enabled)
			assert.Equal(t, tt.wantCron, schedules[0].Cron)
			assert.Equal(t, int32(tt.in.RetentionCopies), schedules[0].Copies())
			assert.Equal(t, "audit", schedules[1].Name)
			assert.False(t, schedules[1].Enabled)
		})
	}
}

// TestSetBackupScheduleOffKeepsSettings turns a schedule off in the manifest
// and keeps its cron and retention for when it is turned back on.
func TestSetBackupScheduleOffKeepsSettings(t *testing.T) {
	svc, store, _ := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)

	weekly := engine.ScheduleInput{Enabled: true, Period: engine.PeriodWeekly, Minute: 15, Hour: 3, Weekday: 2, RetentionCopies: 6}
	_, err = svc.SetBackupSchedule(ctx, created.ID, &weekly)
	require.NoError(t, err)

	got, err := svc.SetBackupSchedule(ctx, created.ID, &engine.ScheduleInput{Enabled: false})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.Disabled)

	schedules := instanceSchedules(t, ctx, store, "clusters/spoke-a/instances/orders.yaml")
	require.Len(t, schedules, 1)
	assert.False(t, schedules[0].Enabled)
	assert.Equal(t, "15 3 * * 2", schedules[0].Cron)
	assert.Equal(t, int32(6), schedules[0].Copies())
}

func TestSetBackupScheduleRefusesAnInvalidSchedule(t *testing.T) {
	svc, store, _ := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders", Engine: "mysql", Profile: "staging", Environment: "spoke-a"})
	require.NoError(t, err)
	before, err := store.Get(ctx, "clusters/spoke-a/instances/orders.yaml")
	require.NoError(t, err)

	bad := engine.ScheduleInput{Enabled: true, Period: engine.PeriodWeekly, Hour: 3, Weekday: 7, RetentionCopies: 7}
	_, err = svc.SetBackupSchedule(ctx, created.ID, &bad)

	require.ErrorIs(t, err, engine.ErrInvalidSchedule)
	after, err := store.Get(ctx, "clusters/spoke-a/instances/orders.yaml")
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
}

func TestBackupsListsAGaveUpPending(t *testing.T) {
	svc, _, inv := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
	})
	backup, err := svc.Backup(ctx, created.ID)
	require.NoError(t, err)

	gaveUp := false
	for range 20 {
		svc.ApplyHeartbeat(hub.Heartbeat{
			Environment: "spoke-a", Namespace: "pds", At: now,
			Instances: []engine.Instance{*created},
		})
		listed, listErr := svc.Backups(ctx, created.ID)
		require.NoError(t, listErr)
		require.Len(t, listed, 1)
		assert.Equal(t, backup.ID, listed[0].ID)
		_ = svc.TakeWork("spoke-a")
		if listed[0].Status == engine.BackupFailed {
			gaveUp = true
			break
		}
	}
	require.True(t, gaveUp)
	svc.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
	})
	assert.Empty(t, svc.TakeWork("spoke-a"))
}

func TestOmitBackupsDoesNotReconcilePending(t *testing.T) {
	svc, _, inv := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
	})
	backup, err := svc.Backup(ctx, created.ID)
	require.NoError(t, err)
	_ = svc.TakeWork("spoke-a")

	svc.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances:   []engine.Instance{*created},
		OmitBackups: true,
	})
	assert.Empty(t, svc.TakeWork("spoke-a"))
	listed, err := svc.Backups(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, backup.ID, listed[0].ID)
	assert.Equal(t, engine.BackupPending, listed[0].Status)
}

func TestHeartbeatDoesNotWaitOnAnotherEnvironmentWrite(t *testing.T) {
	inner := hub.NewMemoryStore()
	entered := make(chan struct{})
	release := make(chan struct{})
	store := &holdWriteStore{inner: inner, entered: entered, release: release}
	inv := hub.NewInventory(time.Minute)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"spoke-a", "spoke-b"} {
		inv.Register(name, "pds")
		inv.ApplyHeartbeat(hub.Heartbeat{Environment: name, Namespace: "pds", At: now})
	}
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
	})

	errc := make(chan error, 1)
	go func() {
		_, err := svc.Create(context.Background(), engine.CreateInput{
			Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
		})
		errc <- err
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("create did not reach the store write")
	}

	done := make(chan struct{})
	go func() {
		_, _, _ = svc.AcceptHeartbeat(hub.Heartbeat{
			Environment: "spoke-b", Namespace: "pds", At: now,
		}, func() bool { return true })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("heartbeat waited on the other environment write")
	}
	close(release)
	require.NoError(t, <-errc)
}

type instanceDoc struct {
	APIVersion string               `json:"apiVersion"`
	Kind       string               `json:"kind"`
	Metadata   everest.ObjectMeta   `json:"metadata"`
	Spec       everest.InstanceSpec `json:"spec"`
}

func rewriteInstanceSchedules(ctx context.Context, store *hub.MemoryStore, path string, schedules []everest.InstanceBackupSchedule) error {
	body, err := store.Get(ctx, path)
	if err != nil {
		return err
	}
	var doc instanceDoc
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return err
	}
	if doc.Spec.Backup == nil {
		doc.Spec.Backup = &everest.InstanceBackupSpec{
			Enabled:  true,
			ClassRef: everest.ObjectRef{Name: everest.BackupClassPXC},
			Storages: []everest.InstanceBackupStorage{{
				StorageRef: everest.ObjectRef{Name: "s3"},
			}},
		}
	}
	if len(doc.Spec.Backup.Storages) == 0 {
		doc.Spec.Backup.Storages = []everest.InstanceBackupStorage{{
			StorageRef: everest.ObjectRef{Name: "s3"},
		}}
	}
	doc.Spec.Backup.Storages[0].Schedules = schedules
	updated, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}

	return store.Write(ctx, map[string][]byte{path: updated}, "test schedule")
}

func instanceSchedules(t *testing.T, ctx context.Context, store *hub.MemoryStore, path string) []everest.InstanceBackupSchedule {
	t.Helper()
	body, err := store.Get(ctx, path)
	require.NoError(t, err)
	var doc instanceDoc
	require.NoError(t, yaml.Unmarshal(body, &doc))
	require.NotNil(t, doc.Spec.Backup)
	require.NotEmpty(t, doc.Spec.Backup.Storages)

	return doc.Spec.Backup.Storages[0].Schedules
}

type holdWriteStore struct {
	inner   *hub.MemoryStore
	entered chan struct{}
	release chan struct{}
}

func (s *holdWriteStore) Write(ctx context.Context, files map[string][]byte, message string) error {
	select {
	case <-s.entered:
	default:
		close(s.entered)
		<-s.release
	}

	return s.inner.Write(ctx, files, message)
}

func (s *holdWriteStore) Delete(ctx context.Context, paths []string, message string) error {
	return s.inner.Delete(ctx, paths, message)
}

func (s *holdWriteStore) Get(ctx context.Context, path string) ([]byte, error) {
	return s.inner.Get(ctx, path)
}

func (s *holdWriteStore) List(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.List(ctx, prefix)
}

func TestRestorePreservesFractionalHeartbeatSizing(t *testing.T) {
	svc, store, inv := newHub(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          now,
		Instances: []engine.Instance{{
			ID:          "spoke-a/pds.orders",
			Name:        "orders",
			Namespace:   "pds",
			Environment: "spoke-a",
			Profile:     "custom",
			Status:      engine.Status{Phase: engine.PhaseReady},
			Sizing:      engine.Sizing{Members: 3, CPUCores: 1.5, MemoryGB: 20.5, StorageGB: 20.5},
		}},
		Backups: []engine.Backup{{
			ID: "orders-ok", Instance: "orders", Status: engine.BackupSucceeded,
		}},
	})

	_, err := svc.RestoreToNewDatabase(ctx, "spoke-a/pds.orders", "orders-ok", "orders-restore-1")
	require.NoError(t, err)

	body, err := store.Get(ctx, "clusters/spoke-a/instances/orders-restore-1.yaml")
	require.NoError(t, err)
	got := string(body)
	assert.Contains(t, got, "1500m")
	assert.Contains(t, got, "20.5Gi")
	assert.NotContains(t, got, "cpu: \"1\"")
	assert.NotContains(t, got, "size: 20Gi")
}

// An agent older than private access reads a private load balancer as public
// and sends its password. Its beat carries no private access mark, so the hub
// keeps no password from it at all.
func TestHeartbeatWithoutThePrivateAccessMarkKeepsNoPassword(t *testing.T) {
	for name, tc := range map[string]struct {
		marked bool
		want   string
	}{
		"an older agent":  {marked: false, want: ""},
		"a current agent": {marked: true, want: "mysql://root:hunter2@a1b2.elb.amazonaws.com:3306"},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _, inv := newHub(t)
			_, ok, err := svc.AcceptHeartbeat(hub.Heartbeat{
				Environment:   "spoke-a",
				Namespace:     "pds",
				PrivateAccess: tc.marked,
				Instances: []engine.Instance{{
					ID:          hub.JoinID("spoke-a", "pds", "orders-pub"),
					Name:        "orders-pub",
					Namespace:   "pds",
					Environment: "spoke-a",
					Exposure:    engine.Exposure{External: true, Address: "a1b2.elb.amazonaws.com"},
					Connection: &engine.Connection{
						Host: "a1b2.elb.amazonaws.com",
						Port: 3306,
						URI:  "mysql://root:hunter2@a1b2.elb.amazonaws.com:3306",
					},
				}},
			}, nil)
			require.NoError(t, err)
			require.True(t, ok)

			snap, found := inv.Snapshot("spoke-a")
			require.True(t, found)
			require.Len(t, snap.Instances, 1)
			require.NotNil(t, snap.Instances[0].Connection)
			assert.Equal(t, tc.want, snap.Instances[0].Connection.URI)
			assert.Equal(t, "a1b2.elb.amazonaws.com", snap.Instances[0].Connection.Host, "the host is not a secret")
		})
	}
}

// Before this, an agent that read a private load balancer as public could
// leave a manifest-private database looking public with the safety net
// ranges as its allow list.
func TestGetOfAPrivateDatabaseAnOlderAgentReportsAsPublicHasNoRanges(t *testing.T) {
	svc, _, inv := newHub(t)
	_, err := svc.Create(context.Background(), engine.CreateInput{
		Name: "orders-priv", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		Instances: []engine.Instance{{
			ID:          hub.JoinID("spoke-a", "pds", "orders-priv"),
			Name:        "orders-priv",
			Namespace:   "pds",
			Environment: "spoke-a",
			Profile:     "dev",
			Exposure: engine.Exposure{
				External:     true,
				SourceRanges: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
				Address:      "internal-a1b2.elb.amazonaws.com",
			},
		}},
	})

	got, err := svc.Get(context.Background(), hub.JoinID("spoke-a", "pds", "orders-priv"))
	require.NoError(t, err)
	assert.Equal(t, engine.Exposure{
		External: true,
		Private:  true,
		Address:  "internal-a1b2.elb.amazonaws.com",
	}, got.Exposure)

	list, err := svc.List(context.Background(), "spoke-a")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Empty(t, list[0].Exposure.SourceRanges)
}

// TestCreateTurnsOnMonitoringTheSpokeReported puts the pmm-client on a new
// database when the spoke says it has a monitoring config to point it at.
func TestCreateTurnsOnMonitoringTheSpokeReported(t *testing.T) {
	svc, store, inv := newHub(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment:      "spoke-a",
		Namespace:        "pds",
		MonitoringConfig: "pmm",
		At:               now,
	})

	_, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)

	body, err := store.Get(ctx, "clusters/spoke-a/instances/orders-dev.yaml")
	require.NoError(t, err)
	var doc struct {
		Spec everest.InstanceSpec `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(body, &doc))
	monitoring, ok := doc.Spec.Components[everest.ComponentMonitoring]
	require.True(t, ok, "the manifest has no monitoring component:\n%s", body)
	assert.Equal(t, everest.ComponentTypePMM, monitoring.Type)
	assert.JSONEq(t, `{"monitoringConfigName":"pmm"}`, string(monitoring.Parameters))
}

// TestCreateLeavesMonitoringOffWithoutAConfig keeps a spoke that has no
// monitoring config from getting a component that names nothing.
func TestCreateLeavesMonitoringOffWithoutAConfig(t *testing.T) {
	svc, store, _ := newHub(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)

	body, err := store.Get(ctx, "clusters/spoke-a/instances/orders-dev.yaml")
	require.NoError(t, err)
	assert.NotContains(t, string(body), "monitoring")
}

// TestRestoreTurnsOnMonitoringTheSpokeReported gives a restored copy the same
// pmm-client a create would.
func TestRestoreTurnsOnMonitoringTheSpokeReported(t *testing.T) {
	svc, store, inv := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", MonitoringConfig: "pmm", At: now,
		Instances: []engine.Instance{*created},
		Backups: []engine.Backup{{
			ID: "orders-dev-ok", Instance: "orders-dev", Status: engine.BackupSucceeded,
		}},
	})

	_, err = svc.RestoreToNewDatabase(ctx, created.ID, "orders-dev-ok", "orders-restore-1")
	require.NoError(t, err)

	body, err := store.Get(ctx, "clusters/spoke-a/instances/orders-restore-1.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), "monitoringConfigName: pmm")
}

func TestAWrongNamespaceIsNotAReportedDatabaseWithAManifest(t *testing.T) {
	svc, _, inv := newHub(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
		Backups:   []engine.Backup{{ID: "orders-1", Instance: "orders", Status: engine.BackupSucceeded, CreatedAt: now}},
	})

	_, err = svc.Get(ctx, "spoke-a/other.orders")
	require.ErrorIs(t, err, engine.ErrNotFound)
	_, err = svc.Backups(ctx, "spoke-a/other.orders")
	require.ErrorIs(t, err, engine.ErrNotFound)

	got, err := svc.Get(ctx, "spoke-a/pds.orders")
	require.NoError(t, err)
	assert.Equal(t, "spoke-a/pds.orders", got.ID)
	backups, err := svc.Backups(ctx, "spoke-a/pds.orders")
	require.NoError(t, err)
	assert.Len(t, backups, 1)
}

func TestBackupsDoNotMatchAReportedDatabaseInAnotherNamespaceByName(t *testing.T) {
	svc, _, inv := newHub(t)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{{
			ID: "spoke-a/team.orders", Name: "orders", Namespace: "team", Environment: "spoke-a",
			Status: engine.Status{Phase: engine.PhaseReady},
		}},
		Backups: []engine.Backup{{ID: "orders-1", Instance: "orders", Status: engine.BackupSucceeded, CreatedAt: now}},
	})

	_, err := svc.Backups(context.Background(), "spoke-a/pds.orders")
	require.ErrorIs(t, err, engine.ErrNotFound)
}

func TestAWriteInAnotherNamespaceLeavesTheManifestAlone(t *testing.T) {
	svc, store, _ := newHub(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, engine.CreateInput{Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	path := "clusters/spoke-a/instances/orders.yaml"
	before, err := store.Get(ctx, path)
	require.NoError(t, err)
	wrong := "spoke-a/other.orders"
	cpu := 4

	require.ErrorIs(t, svc.Delete(ctx, wrong), engine.ErrNotFound)
	require.ErrorIs(t, svc.Scale(ctx, wrong, engine.ScaleInput{CPUCores: &cpu}), engine.ErrNotFound)
	_, err = svc.Backup(ctx, wrong)
	require.ErrorIs(t, err, engine.ErrNotFound)
	_, err = svc.SetBackupSchedule(ctx, wrong, &engine.ScheduleInput{Enabled: true, Period: engine.PeriodDaily, Hour: 3, RetentionCopies: 7})
	require.ErrorIs(t, err, engine.ErrNotFound)

	after, err := store.Get(ctx, path)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
}

func TestAPendingBackupBelongsToTheNamespaceItWasAskedIn(t *testing.T) {
	svc, _, inv := newHub(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{
			{ID: "spoke-a/ns-a.db1", Name: "db1", Namespace: "ns-a", Environment: "spoke-a"},
			{ID: "spoke-a/ns-b.db1", Name: "db1", Namespace: "ns-b", Environment: "spoke-a"},
		},
	})
	_, err := svc.Backup(ctx, "spoke-a/ns-a.db1")
	require.NoError(t, err)

	inA, err := svc.Backups(ctx, "spoke-a/ns-a.db1")
	require.NoError(t, err)
	inB, err := svc.Backups(ctx, "spoke-a/ns-b.db1")
	require.NoError(t, err)

	assert.Len(t, inA, 1)
	assert.Empty(t, inB)
}

// An agent that predates the removal of seeding still sends a reason with a
// failed result. The heartbeat decoder is not strict, so the result still reads.
func TestAWorkResultWithARetiredReasonStillDecodes(t *testing.T) {
	var got hub.WorkResult
	require.NoError(t, json.Unmarshal([]byte(`{"id":"w1","error":"text","reason":"source"}`), &got))

	assert.Equal(t, hub.WorkResult{ID: "w1", Error: "text"}, got)
}
