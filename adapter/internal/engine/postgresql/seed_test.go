package postgresql_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/postgresql"
	"github.com/percona/pds/adapter/internal/platform"
)

// fakeObjects is the Kubernetes API a seed reads and writes, keyed by kind and
// name, and it records what was created and deleted. createErr makes a create
// store the object and still return an error, the way a client timeout does
// after the API server accepted the request; heldAfterCreate replaces what the
// server holds in that case.
type fakeObjects struct {
	objects         map[string]*unstructured.Unstructured
	created         []*unstructured.Unstructured
	deleted         []string
	failGet         error
	failCreate      error
	createErr       error
	heldAfterCreate *unstructured.Unstructured
	deleteErr       error
	onCreate        func()
}

func (f *fakeObjects) key(gvr schema.GroupVersionResource, name string) string {
	return gvr.Resource + "/" + name
}

func (f *fakeObjects) GetObject(ctx context.Context, gvr schema.GroupVersionResource, _, name string) (*unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.failGet != nil {
		return nil, f.failGet
	}
	if obj, ok := f.objects[f.key(gvr, name)]; ok {
		return obj.DeepCopy(), nil
	}

	return nil, kube.ErrObjectNotFound
}

func (f *fakeObjects) CreateObject(_ context.Context, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	if f.failCreate != nil {
		return f.failCreate
	}
	f.created = append(f.created, obj)
	f.objects[f.key(gvr, obj.GetName())] = obj
	if f.heldAfterCreate != nil {
		f.objects[f.key(gvr, obj.GetName())] = f.heldAfterCreate
	}
	if f.onCreate != nil {
		f.onCreate()
	}

	return f.createErr
}

func (f *fakeObjects) DeleteObject(ctx context.Context, gvr schema.GroupVersionResource, _, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.objects[f.key(gvr, name)]; !ok {
		return kube.ErrObjectNotFound
	}
	delete(f.objects, f.key(gvr, name))
	f.deleted = append(f.deleted, name)

	return nil
}

func sourcePG() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "pgv2.percona.com/v2",
		"kind":       "PerconaPGCluster",
		"metadata":   map[string]any{"name": "pgsolo", "namespace": "pds", "uid": "cluster-uid"},
		"spec": map[string]any{
			"crVersion":       "3.0.0",
			"image":           "percona/percona-distribution-postgresql:18.4-1",
			"postgresVersion": int64(18),
			"port":            int64(5432),
			"instances":       []any{map[string]any{"name": "instance1", "replicas": int64(1)}},
			"proxy":           map[string]any{"pgBouncer": map[string]any{"replicas": int64(1)}},
			"pmm":             map[string]any{"enabled": true},
			"backups": map[string]any{
				"enabled": true,
				"pgbackrest": map[string]any{
					"configuration": []any{map[string]any{"secret": map[string]any{"name": "pgsolo-pgbackrest-repo1-creds"}}},
					"global": map[string]any{
						"repo1-path":               "/pgbackrest/source-uid/repo1",
						"repo1-s3-uri-style":       "path",
						"repo1-storage-verify-tls": "n",
					},
				},
			},
		},
	}}
}

func succeededBackup() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "pgv2.percona.com/v2",
		"kind":       "PerconaPGBackup",
		"metadata":   map[string]any{"name": "pgsolo-20261006210946-c539", "namespace": "pds"},
		"spec":       map[string]any{"pgCluster": "pgsolo", "repoName": "repo1"},
		"status": map[string]any{
			"state":       "Succeeded",
			"backupName":  "20261006-211028F",
			"destination": "s3://pds-backups/pgbackrest/source-uid/repo1",
			"repo": map[string]any{
				"name": "repo1",
				"s3": map[string]any{
					"bucket":   "pds-backups",
					"endpoint": "http://minio:9000",
					"region":   "us-east-1",
				},
			},
		},
	}}
}

func newFake() *fakeObjects {
	return &fakeObjects{objects: map[string]*unstructured.Unstructured{
		"perconapgclusters/pgsolo":                    sourcePG(),
		"perconapgbackups/pgsolo-20261006210946-c539": succeededBackup(),
	}}
}

func field(t *testing.T, obj *unstructured.Unstructured, path ...string) any {
	t.Helper()
	value, found, err := unstructured.NestedFieldNoCopy(obj.Object, path...)
	require.NoError(t, err)
	require.True(t, found, "missing %v", path)

	return value
}

// The new cluster is created before its Instance, with the dataSource that
// restores the backup's label out of the source's repository, so the operator
// bootstraps it from that backup and never from an empty database.
func TestSeedCreatesTheDatabaseWithADataSourceFromTheBackup(t *testing.T) {
	objects := newFake()

	err := postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	require.NoError(t, err)
	require.Len(t, objects.created, 1)
	created := objects.created[0]
	assert.Equal(t, "pgcopy", created.GetName())
	assert.Equal(t, "pds", created.GetNamespace())
	assert.Equal(t, "PerconaPGCluster", created.GetKind())

	source := field(t, created, "spec", "dataSource", "pgbackrest").(map[string]any)
	assert.Equal(t, "db", source["stanza"])
	assert.Equal(t, []any{"--set=20261006-211028F", "--type=immediate"}, source["options"])
	assert.Equal(t, map[string]any{
		"name": "repo1",
		"s3":   map[string]any{"bucket": "pds-backups", "endpoint": "http://minio:9000", "region": "us-east-1"},
	}, source["repo"])
	assert.Equal(t, map[string]any{
		"repo1-path":               "/pgbackrest/source-uid/repo1",
		"repo1-s3-uri-style":       "path",
		"repo1-storage-verify-tls": "n",
	}, source["global"], "the repository path is the source's, which is read and never written")
	assert.Equal(t,
		[]any{map[string]any{"secret": map[string]any{"name": "pgsolo-pgbackrest-repo1-creds"}}},
		source["configuration"], "the restore reads the source's S3 credentials")
}

// The operator rejects enabled backups with no repository, and the provider
// turns them on when it takes the cluster over. The copy must not inherit the
// source's monitoring or its repository settings, which belong to the source.
func TestSeedKeepsTheSourcesShapeAndLeavesBackupsOff(t *testing.T) {
	objects := newFake()

	require.NoError(t, postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy"))

	created := objects.created[0]
	assert.Equal(t, "3.0.0", field(t, created, "spec", "crVersion"))
	assert.Equal(t, int64(18), field(t, created, "spec", "postgresVersion"))
	assert.Equal(t, "percona/percona-distribution-postgresql:18.4-1", field(t, created, "spec", "image"))
	assert.Len(t, field(t, created, "spec", "instances"), 1)
	assert.Equal(t, map[string]any{"enabled": false}, field(t, created, "spec", "backups"))
	_, hasPMM, _ := unstructured.NestedFieldNoCopy(created.Object, "spec", "pmm")
	assert.False(t, hasPMM)
	assert.Empty(t, created.GetOwnerReferences())
}

func TestSeedRefusesABackupOfAnotherDatabase(t *testing.T) {
	objects := newFake()
	require.NoError(t, unstructured.SetNestedField(objects.objects["perconapgbackups/pgsolo-20261006210946-c539"].Object,
		"someone-else", "spec", "pgCluster"))

	err := postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	require.ErrorIs(t, err, engine.ErrBackupNotFound)
	assert.Empty(t, objects.created)
}

func TestSeedRefusesABackupThatIsNotFinished(t *testing.T) {
	objects := newFake()
	require.NoError(t, unstructured.SetNestedField(objects.objects["perconapgbackups/pgsolo-20261006210946-c539"].Object,
		"Running", "status", "state"))

	err := postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	require.ErrorIs(t, err, engine.ErrBackupUnusable)
	assert.Empty(t, objects.created)
}

func TestSeedReportsAMissingBackup(t *testing.T) {
	err := postgresql.Seed(context.Background(), newFake(), "pds", "pgsolo", "gone", "pgcopy")

	require.ErrorIs(t, err, engine.ErrBackupNotFound)
}

func TestSeedReportsAMissingSource(t *testing.T) {
	objects := newFake()
	delete(objects.objects, "perconapgclusters/pgsolo")

	err := postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	require.ErrorIs(t, err, engine.ErrNotFound)
}

// A seed the hub offers again after a lost acknowledgement finds its cluster
// and does nothing, rather than restoring over a database that already runs.
func TestSeedIsIdempotent(t *testing.T) {
	objects := newFake()
	ctx := context.Background()
	require.NoError(t, postgresql.Seed(ctx, objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy"))

	require.NoError(t, postgresql.Seed(ctx, objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy"))

	assert.Len(t, objects.created, 1)
}

// Adding a dataSource to a cluster makes the operator bootstrap it again and
// discard what it holds, so a cluster of the copy's name that this seed did not
// create is refused rather than restored over.
func TestSeedRefusesADatabaseItDidNotCreate(t *testing.T) {
	objects := newFake()
	existing := sourcePG()
	existing.SetName("pgcopy")
	objects.objects["perconapgclusters/pgcopy"] = existing

	err := postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	require.ErrorIs(t, err, engine.ErrExists)
	assert.Empty(t, objects.created)
}

// A cluster an earlier seed left behind restores the backup that seed named.
// Reusing it for another backup would hand the developer the wrong data.
func TestSeedRefusesACopyThatRestoresAnotherBackup(t *testing.T) {
	objects := newFake()
	ctx := context.Background()
	require.NoError(t, postgresql.Seed(ctx, objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy"))
	later := succeededBackup()
	later.SetName("pgsolo-20261007090000-a1b2")
	require.NoError(t, unstructured.SetNestedField(later.Object, "20261007-090012F", "status", "backupName"))
	objects.objects["perconapgbackups/pgsolo-20261007090000-a1b2"] = later

	err := postgresql.Seed(ctx, objects, "pds", "pgsolo", "pgsolo-20261007090000-a1b2", "pgcopy")

	require.ErrorIs(t, err, engine.ErrExists)
	assert.Len(t, objects.created, 1)
}

// A cluster on its way out takes whatever restores into it along, so the
// Instance that would adopt it is not released.
func TestSeedRefusesACopyThatIsBeingDeleted(t *testing.T) {
	objects := newFake()
	ctx := context.Background()
	require.NoError(t, postgresql.Seed(ctx, objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy"))
	deleting := metav1.Now()
	objects.objects["perconapgclusters/pgcopy"].SetDeletionTimestamp(&deleting)

	err := postgresql.Seed(ctx, objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	require.ErrorIs(t, err, engine.ErrExists)
}

// The copy gets no load balancer of its own: a second one is a second bill,
// and the Instance that adopts the copy asks for none.
func TestSeedLeavesTheSourcesLoadBalancerBehind(t *testing.T) {
	objects := newFake()
	require.NoError(t, unstructured.SetNestedField(objects.objects["perconapgclusters/pgsolo"].Object,
		map[string]any{"type": "LoadBalancer"}, "spec", "proxy", "pgBouncer", "expose"))

	require.NoError(t, postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy"))

	created := objects.created[0]
	assert.Equal(t, int64(1), field(t, created, "spec", "proxy", "pgBouncer", "replicas"))
	_, exposed, _ := unstructured.NestedFieldNoCopy(created.Object, "spec", "proxy", "pgBouncer", "expose")
	assert.False(t, exposed)
}

func TestSeedKeepsTheCauseOfAReadFailure(t *testing.T) {
	cause := errors.New("api down")
	objects := newFake()
	objects.failGet = cause

	err := postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	require.ErrorIs(t, err, cause)
}

// An environment that cannot read the operator's objects cannot seed at all,
// which is a different failure from a backup that is missing.
func TestSeedReportsAnEnvironmentThatCannotReachTheOperatorsObjects(t *testing.T) {
	for name, cause := range map[string]error{
		"forbidden":     kube.ErrObjectForbidden,
		"no api access": platform.ErrUnavailable,
	} {
		t.Run(name, func(t *testing.T) {
			objects := newFake()
			objects.failGet = cause

			err := postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

			require.ErrorIs(t, err, engine.ErrSeedUnavailable)
			require.ErrorIs(t, err, cause, "the log keeps the cause")
		})
	}
}

func TestSeedDoesNotCallAnAPIFailureUnavailable(t *testing.T) {
	objects := newFake()
	objects.failGet = errors.New("api down")

	err := postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	assert.NotErrorIs(t, err, engine.ErrSeedUnavailable)
}

// A ServiceAccount that may read the operator's objects but not create one
// cannot seed either, and asking the developer to restore again cannot help.
func TestSeedReportsAnEnvironmentThatMayReadButNotCreate(t *testing.T) {
	objects := newFake()
	objects.objects = map[string]*unstructured.Unstructured{
		"perconapgbackups/pgsolo-20261006210946-c539": succeededBackup(),
		"perconapgclusters/pgsolo":                    sourcePG(),
	}
	objects.failCreate = kube.ErrObjectForbidden

	err := postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	require.ErrorIs(t, err, engine.ErrSeedUnavailable)
}

var errTimeout = errors.New("client timeout")

// A create the API server accepted but the caller saw fail leaves a cluster
// that restores and uses storage with no Instance to adopt it, so the seed
// removes it before reporting the failure.
func TestSeedRemovesTheCopyWhenTheCreateErrorsAfterTheServerMadeIt(t *testing.T) {
	objects := newFake()
	objects.createErr = errTimeout

	err := postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	require.ErrorIs(t, err, errTimeout)
	assert.Equal(t, []string{"pgcopy"}, objects.deleted)
	assert.NotContains(t, objects.objects, "perconapgclusters/pgcopy")
}

// The caller's deadline is usually what expired, so the cleanup must not
// inherit it.
func TestSeedRemovesTheCopyEvenWhenTheCallersContextIsDone(t *testing.T) {
	objects := newFake()
	objects.createErr = context.DeadlineExceeded
	ctx, cancel := context.WithCancel(context.Background())
	objects.onCreate = cancel

	err := postgresql.Seed(ctx, objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, []string{"pgcopy"}, objects.deleted)
}

func TestSeedDeletesNothingWhenTheCreateNeverReachedTheServer(t *testing.T) {
	objects := newFake()
	objects.createErr = errTimeout
	objects.heldAfterCreate = nil
	objects.onCreate = func() { delete(objects.objects, "perconapgclusters/pgcopy") }

	err := postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	require.ErrorIs(t, err, errTimeout)
	assert.Empty(t, objects.deleted)
}

// Only a cluster this seed made is removed: one that restores another backup,
// or that appeared under the name from elsewhere, stays.
func TestSeedKeepsACopyItDidNotCreate(t *testing.T) {
	objects := newFake()
	objects.createErr = errTimeout
	other := sourcePG()
	other.SetName("pgcopy")
	objects.heldAfterCreate = other

	err := postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	require.ErrorIs(t, err, errTimeout)
	assert.Empty(t, objects.deleted)
	assert.Contains(t, objects.objects, "perconapgclusters/pgcopy")
}

// A create refused because the name is taken did not make the cluster that is
// there.
func TestSeedKeepsTheCopyWhenTheCreateFoundTheNameTaken(t *testing.T) {
	objects := newFake()
	objects.createErr = kube.ErrObjectExists

	err := postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	require.ErrorIs(t, err, kube.ErrObjectExists)
	assert.Empty(t, objects.deleted)
}

func TestSeedReportsBothFailuresWhenTheCleanupFails(t *testing.T) {
	objects := newFake()
	objects.createErr = errTimeout
	objects.deleteErr = kube.ErrObjectForbidden

	err := postgresql.Seed(context.Background(), objects, "pds", "pgsolo", "pgsolo-20261006210946-c539", "pgcopy")

	require.ErrorIs(t, err, errTimeout)
	require.ErrorIs(t, err, kube.ErrObjectForbidden)
}
