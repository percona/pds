package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

const instancesIndex = "clusters/spoke-a/instances/kustomization.yaml"

func indexText(t *testing.T, store hub.Store) string {
	t.Helper()
	body, err := store.Get(context.Background(), instancesIndex)
	require.NoError(t, err)

	return string(body)
}

func credentialsWork(t *testing.T, svc *hub.Service) []hub.Work {
	t.Helper()
	var out []hub.Work
	for _, item := range svc.TakeWork("spoke-a") {
		if item.Kind == hub.WorkCredentials {
			out = append(out, item)
		}
	}

	return out
}

func createDev(t *testing.T, svc *hub.Service, name string) {
	t.Helper()
	_, err := svc.Create(context.Background(), engine.CreateInput{
		Name: name, Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.NoError(t, err)
}

// Flux applies what the index lists, and the operator generates the users
// Secret the moment the cluster exists. A create that listed the Instance
// straight away let the operator pick passwords the exporter cannot read.
func TestCreateHoldsTheInstanceUntilTheAgentPreparesItsCredentials(t *testing.T) {
	svc, store, _ := newHub(t)

	createDev(t, svc, "orders")

	_, err := store.Get(context.Background(), "clusters/spoke-a/instances/orders.yaml")
	require.NoError(t, err, "the manifest is in Git so the database lists as pending")
	assert.NotContains(t, indexText(t, store), "orders.yaml")
	work := credentialsWork(t, svc)
	require.Len(t, work, 1)
	assert.Equal(t, "pds.orders", work[0].Database)

	_, known, err := svc.AcceptHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds",
		Results: []hub.WorkResult{{ID: work[0].ID}},
	}, nil)
	require.NoError(t, err)
	require.True(t, known)

	assert.Contains(t, indexText(t, store), "orders.yaml")
	assert.Empty(t, credentialsWork(t, svc))
}

// An agent that could not write the Secret, or one too old to know the kind,
// answers with an error. The database is released anyway: the operator then
// generates the passwords as it did before, and a create never waits forever.
func TestAFailedCredentialsAnswerStillReleasesTheInstance(t *testing.T) {
	svc, store, _ := newHub(t)
	createDev(t, svc, "orders")
	work := credentialsWork(t, svc)
	require.Len(t, work, 1)

	svc.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds",
		Results: []hub.WorkResult{{ID: work[0].ID, Error: "unknown kind credentials"}},
	})

	assert.Contains(t, indexText(t, store), "orders.yaml")
}

func TestAnotherChangeToTheIndexKeepsAHeldInstanceOut(t *testing.T) {
	svc, store, _ := newHub(t)
	createDev(t, svc, "orders")
	createDev(t, svc, "billing")
	work := credentialsWork(t, svc)
	require.Len(t, work, 2)
	var orders hub.Work
	for _, item := range work {
		if item.Database == "pds.orders" {
			orders = item
		}
	}

	svc.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds",
		Results: []hub.WorkResult{{ID: orders.ID}},
	})

	index := indexText(t, store)
	assert.Contains(t, index, "orders.yaml")
	assert.NotContains(t, index, "billing.yaml")
}

// A restore's users Secret is copied from its source by the provider, because
// the restored data carries the source's users. A Secret written ahead of it
// would win, and the restored database would refuse its own operator.
func TestARestoreIsNotHeld(t *testing.T) {
	svc, store, inv := newHub(t)
	createDev(t, svc, "orders")
	work := credentialsWork(t, svc)
	require.Len(t, work, 1)
	svc.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds",
		Results: []hub.WorkResult{{ID: work[0].ID}},
	})
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{{
			ID: "spoke-a/pds.orders", Name: "orders", Namespace: "pds", Environment: "spoke-a",
			Status: engine.Status{Phase: engine.PhaseReady},
		}},
		Backups: []engine.Backup{{ID: "orders-ok", Instance: "orders", Status: engine.BackupSucceeded}},
	})

	_, err := svc.RestoreToNewDatabase(context.Background(), "spoke-a/pds.orders", "orders-ok", "orders-restored")
	require.NoError(t, err)

	assert.Contains(t, indexText(t, store), "orders-restored.yaml")
	assert.Empty(t, credentialsWork(t, svc))
}

// Only the MySQL operator reads the users Secret the agent writes. A
// PostgreSQL database would get a Secret nothing reads and that its delete
// leaves behind, so its create asks the agent for nothing and is listed at once.
func TestAPostgreSQLCreateIsNotHeld(t *testing.T) {
	svc, store, _ := newHub(t)

	_, err := createPostgreSQL(t, svc, engine.CreateInput{})
	require.NoError(t, err)

	assert.Contains(t, indexText(t, store), "reports.yaml")
	assert.Empty(t, credentialsWork(t, svc))
}

// A database deleted before the agent answered never reaches the environment,
// so writing a Secret for it there would leave passwords nothing uses.
func TestDeletingAHeldDatabaseDropsItsCredentialsWork(t *testing.T) {
	svc, store, _ := newHub(t)
	createDev(t, svc, "orders")

	require.NoError(t, svc.Delete(context.Background(), "spoke-a/pds.orders"))

	assert.Empty(t, credentialsWork(t, svc))
	assert.NotContains(t, indexText(t, store), "orders.yaml")
}

// A release is a Git write. When it fails, the answer is not acknowledged, so
// the agent is asked again and the release is tried again with it.
func TestAReleaseThatCannotWriteIsTriedAgain(t *testing.T) {
	base := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	clock := base
	backing := hub.NewMemoryStore()
	store := &failWriteStore{Store: backing}
	svc := hubOverStore(t, store)
	svc.SetWorkClock(func() time.Time { return clock })
	createDev(t, svc, "orders")
	work := credentialsWork(t, svc)
	require.Len(t, work, 1)

	store.failSuffix = "kustomization.yaml"
	svc.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds",
		Results: []hub.WorkResult{{ID: work[0].ID}},
	})
	assert.NotContains(t, indexText(t, backing), "orders.yaml")

	store.failSuffix = ""
	clock = clock.Add(5 * time.Minute)
	again := credentialsWork(t, svc)
	require.Len(t, again, 1)
	assert.Equal(t, work[0].ID, again[0].ID)

	svc.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds",
		Results: []hub.WorkResult{{ID: again[0].ID}},
	})
	assert.Contains(t, indexText(t, backing), "orders.yaml")
}

// releaseCredentials answers every credentials item the way an agent that
// wrote the Secret does, so a test about something else starts from a database
// the index lists.
func releaseCredentials(t *testing.T, svc *hub.Service) {
	t.Helper()
	var results []hub.WorkResult
	for _, item := range svc.TakeWork("spoke-a") {
		require.Equal(t, hub.WorkCredentials, item.Kind)
		results = append(results, hub.WorkResult{ID: item.ID})
	}
	svc.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", Results: results})
}

// toggleSavePersist keeps the queue in memory and fails saves while failSave
// is set, so a test can lose one acknowledgement and keep the rest.
type toggleSavePersist struct {
	saved    map[string][]hub.Work
	failSave bool
}

func (p *toggleSavePersist) Load(context.Context) (map[string][]hub.Work, error) {
	out := map[string][]hub.Work{}
	for environment, items := range p.saved {
		out[environment] = append([]hub.Work(nil), items...)
	}

	return out, nil
}

func (p *toggleSavePersist) Save(_ context.Context, environment string, items []hub.Work) error {
	if p.failSave {
		return assert.AnError
	}
	p.saved[environment] = append([]hub.Work(nil), items...)

	return nil
}

// Flux deletes what the index stops listing. A release whose acknowledgement
// was lost leaves its item owed after the index already lists the database,
// and the next rewrite must not take that running database back out.
func TestAListedDatabaseStaysListedWhenItsCredentialsItemIsStillOwed(t *testing.T) {
	svc, store, _ := newHub(t)
	persist := &toggleSavePersist{saved: map[string][]hub.Work{}}
	require.NoError(t, svc.SetWorkPersist(context.Background(), persist))
	createDev(t, svc, "orders")
	work := credentialsWork(t, svc)
	require.Len(t, work, 1)

	persist.failSave = true
	svc.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds",
		Results: []hub.WorkResult{{ID: work[0].ID}},
	})
	require.Contains(t, indexText(t, store), "orders.yaml")

	persist.failSave = false
	createDev(t, svc, "billing")

	assert.Contains(t, indexText(t, store), "orders.yaml")
	assert.NotContains(t, indexText(t, store), "billing.yaml")
}

// The credentials item is queued after the manifest is written. When it cannot
// be saved, the manifest goes too, or the retry would answer 409 for a
// database that exists nowhere.
func TestACreateWhoseCredentialsCannotBeQueuedLeavesNoManifest(t *testing.T) {
	svc, store, _ := newHub(t)
	persist := &toggleSavePersist{saved: map[string][]hub.Work{}, failSave: true}
	require.NoError(t, svc.SetWorkPersist(context.Background(), persist))

	_, err := svc.Create(context.Background(), engine.CreateInput{
		Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})

	require.ErrorIs(t, err, hub.ErrPersist)
	_, getErr := store.Get(context.Background(), "clusters/spoke-a/instances/orders.yaml")
	require.ErrorIs(t, getErr, hub.ErrNotFound)
}
