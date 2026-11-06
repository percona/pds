package postgresql_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/oe"
	"github.com/percona/pds/adapter/internal/engine/postgresql"
)

// Compile-time checks: the PostgreSQL plugin satisfies both halves of the contract.
var (
	_ engine.Plugin          = (*postgresql.Plugin)(nil)
	_ engine.ReadWritePlugin = (*postgresql.Plugin)(nil)
)

// testBackupStorage is a BackupStorage name an operator registered themselves.
// Nothing in this repository creates one, so the plugin default is empty and a
// test that exercises backups has to opt in the way an operator does.
const testBackupStorage = "backups"

// newPlugin returns a plugin and the fake Everest behind it.
func newPlugin(t *testing.T) (*postgresql.Plugin, *fakeEverest) {
	t.Helper()

	fake := newFakeEverest()

	return postgresql.New(fake), fake
}

// newPluginWithBackups returns a plugin configured with a BackupStorage.
func newPluginWithBackups(t *testing.T) (*postgresql.Plugin, *fakeEverest) {
	t.Helper()

	fake := newFakeEverest()

	return postgresql.New(fake, postgresql.WithBackupStorage(testBackupStorage)), fake
}

func intPtr(v int) *int { return &v }

// seedProfile inserts a managed instance sized like a catalog profile. The
// plugin no longer creates instances; tests of scale, backup and delete still
// need one to start from.
func seedProfile(t *testing.T, fake *fakeEverest, name, profile string) string {
	t.Helper()

	row, err := oe.LookupProfile(profile)
	require.NoError(t, err)

	replicas := int32(row.Members)
	in := pgInstance(name, replicas, strconv.Itoa(row.CPUCores), fmt.Sprintf("%dGi", row.MemoryGB), fmt.Sprintf("%dGi", row.StorageGB))
	in.Metadata.Labels[oe.LabelProfile] = row.Name
	in.SetProxyComponent(everest.ComponentSpec{
		Type:     everest.ComponentTypePgBouncer,
		Replicas: everest.Ptr(oe.ProxyReplicasFor(replicas)),
	})
	if row.BackupSchedule == oe.BackupDaily {
		in.Spec.Backup = &everest.InstanceBackupSpec{
			Enabled:  true,
			ClassRef: everest.ObjectRef{Name: everest.BackupClassPostgreSQL},
			Storages: []everest.InstanceBackupStorage{{
				StorageRef: everest.ObjectRef{Name: testBackupStorage},
				Schedules:  []everest.InstanceBackupSchedule{oe.ScheduleSpec(engine.DefaultSchedule())},
			}},
		}
	}
	_, err = fake.CreateInstance(context.Background(), postgresql.DefaultNamespace, in)
	require.NoError(t, err)

	return postgresql.DefaultNamespace + "." + name
}

func listNamed(t *testing.T, p *postgresql.Plugin, id string) engine.Instance {
	t.Helper()

	items, err := p.List(context.Background())
	require.NoError(t, err)
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("instance %s was not listed", id)

	return engine.Instance{}
}

func pgInstance(name string, replicas int32, cpu, memory, storage string) *everest.Instance {
	in := &everest.Instance{}
	in.Metadata.Name = name
	in.Metadata.Labels = map[string]string{
		oe.LabelManagedBy: oe.ManagedBy,
	}
	in.Spec.ProviderRef = everest.ObjectRef{Name: everest.ProviderPostgreSQL}
	engine := everest.ComponentSpec{}
	if replicas != 0 {
		engine.Replicas = everest.Ptr(replicas)
	}
	if cpu != "" || memory != "" {
		engine.Resources = &everest.ResourceRequirements{
			Limits: &everest.ResourceList{CPU: cpu, Memory: memory},
		}
	}
	if storage != "" {
		engine.Storage = &everest.Storage{Size: storage}
	}
	in.SetEngineComponent(engine)

	return in
}

func TestEngineID(t *testing.T) {
	p, _ := newPlugin(t)
	assert.Equal(t, "postgresql", p.Engine())
}

func TestListIncludesLastBackupAt(t *testing.T) {
	p, fake := newPlugin(t)
	seedProfile(t, fake, "orders", "dev")

	finished := time.Date(2026, time.August, 26, 12, 1, 8, 0, time.UTC)
	fake.seedBackup(postgresql.DefaultNamespace, "orders", "orders-1", everest.BackupStateSucceeded, "1", finished)

	got, err := p.List(context.Background())
	require.NoError(t, err)

	require.Len(t, got, 1)
	require.NotNil(t, got[0].LastBackupAt, "the list Last backup cell reads this")
	assert.True(t, got[0].LastBackupAt.Equal(finished), "got %s", got[0].LastBackupAt)
}

func TestMalformedIDIsRejected(t *testing.T) {
	p, fake := newPlugin(t)

	for _, id := range []string{"", "orders", "ns.a.b", "ns.", ".orders", "   "} {
		err := p.Delete(context.Background(), id)

		require.Error(t, err, "id %q", id)
		assert.True(t, errors.Is(err, oe.ErrInvalidID), "id %q: want ErrInvalidID, got %v", id, err)
	}

	assert.Empty(t, fake.seen(), "a malformed ID never reaches Everest")
}

func TestListIncludesConnectionOnceReady(t *testing.T) {
	p, fake := newPlugin(t)
	created := seedProfile(t, fake, "orders", "dev")
	fake.seedCredentials(postgresql.DefaultNamespace, "orders")
	fake.seedStatus(postgresql.DefaultNamespace, "orders", "orders-pgbouncer.pds", 5432, everest.PhaseReady)

	got, err := p.List(context.Background())
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, created, got[0].ID)
	require.NotNil(t, got[0].Connection, "the list is what the Ready pill reads")
	assert.Equal(t, "orders-pgbouncer.pds", got[0].Connection.Host)
	assert.Equal(t, 5432, got[0].Connection.Port)
}

func TestListSkipsCredentialsUntilReady(t *testing.T) {
	p, fake := newPlugin(t)
	seedProfile(t, fake, "orders", "dev")

	got, err := p.List(context.Background())
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Nil(t, got[0].Connection)
	for _, call := range fake.seen() {
		assert.NotContains(t, call, "GetInstanceConnection")
	}
}

func TestListReturnsInstancesByName(t *testing.T) {
	p, fake := newPlugin(t)
	for _, name := range []string{"orders", "checkout", "billing"} {
		seedProfile(t, fake, name, "dev")
	}

	got, err := p.List(context.Background())
	require.NoError(t, err)

	require.Len(t, got, 3)
	assert.Equal(t, []string{"billing", "checkout", "orders"},
		[]string{got[0].Name, got[1].Name, got[2].Name})
}

// TestListSkipsOtherEngines keeps a MySQL instance in the same namespace
// from being described by the PostgreSQL plugin.
func TestListSkipsOtherEngines(t *testing.T) {
	p, fake := newPlugin(t)
	seedProfile(t, fake, "orders", "dev")

	other := &everest.Instance{}
	other.Metadata.Name = "reporting"
	other.Metadata.Labels = map[string]string{
		oe.LabelManagedBy: oe.ManagedBy,
	}
	other.Spec.ProviderRef = everest.ObjectRef{Name: everest.ProviderPXC}
	_, err := fake.CreateInstance(context.Background(), postgresql.DefaultNamespace, other)
	require.NoError(t, err)

	got, err := p.List(context.Background())
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, "orders", got[0].Name)
}

// TestListReportsAManagedInstanceWithoutAProfileAsCustom covers a Percona Database Service
// managed instance with no profile label. Size still round-trips; the
// profile reads as custom rather than inventing one.
func TestListReportsAManagedInstanceWithoutAProfileAsCustom(t *testing.T) {
	p, fake := newPlugin(t)

	other := pgInstance("hand-rolled", 3, "1500m", "3Gi", "50Gi")
	_, err := fake.CreateInstance(context.Background(), postgresql.DefaultNamespace, other)
	require.NoError(t, err)

	got, err := p.List(context.Background())
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, oe.ProfileCustom, got[0].Profile)
	assert.InDelta(t, 1.5, got[0].Sizing.CPUCores, 0.001)
	assert.InDelta(t, 3.0, got[0].Sizing.MemoryGB, 0.001)
	assert.InDelta(t, 50.0, got[0].Sizing.StorageGB, 0.001)
}

func TestListEmpty(t *testing.T) {
	p, _ := newPlugin(t)

	got, err := p.List(context.Background())

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestScaleWritesTheNewSpecBackToEverest(t *testing.T) {
	p, fake := newPlugin(t)
	created := seedProfile(t, fake, "orders", "dev")

	require.NoError(t, p.Scale(context.Background(), created, engine.ScaleInput{
		CPUCores:  intPtr(2),
		MemoryGB:  intPtr(4),
		StorageGB: intPtr(40),
	}))

	stored := fake.stored(postgresql.DefaultNamespace, "orders")
	assert.Equal(t, "2", stored.EngineComponent().CPU())
	assert.Equal(t, "4Gi", stored.EngineComponent().Memory())
	assert.Equal(t, "40Gi", stored.EngineComponent().StorageSize())
	assert.Equal(t, int32(1), stored.EngineComponent().ReplicaCount(),
		"a scale that leaves members out leaves them alone")
}

// TestScaleReplicasEnablesHA covers the HA rule: 1 to 3 members is what turns
// a single node database into an HA cluster, and the proxy tier follows so the
// result matches what a create with an HA profile would have produced.
func TestScaleReplicasEnablesHA(t *testing.T) {
	p, fake := newPlugin(t)
	created := seedProfile(t, fake, "orders", "dev")

	require.NoError(t, p.Scale(context.Background(), created, engine.ScaleInput{Replicas: intPtr(3)}))

	stored := fake.stored(postgresql.DefaultNamespace, "orders")
	assert.Equal(t, int32(3), stored.EngineComponent().ReplicaCount())
	assert.Equal(t, "1", stored.EngineComponent().CPU(), "the rest of the spec is untouched")

	assert.Equal(t, int32(3), stored.ProxyComponent().ReplicaCount(), "the proxy tier follows the engine tier")

	got := listNamed(t, p, created)
	assert.Equal(t, 3, got.Sizing.Members)
}

// TestScaleReplicasBackToOne is the same rule in reverse: an HA cluster can go
// back to a single member, and the proxy tier shrinks with it.
func TestScaleReplicasBackToOne(t *testing.T) {
	p, fake := newPlugin(t)
	created := seedProfile(t, fake, "orders", "prod")

	require.NoError(t, p.Scale(context.Background(), created, engine.ScaleInput{Replicas: intPtr(1)}))

	stored := fake.stored(postgresql.DefaultNamespace, "orders")
	assert.Equal(t, int32(1), stored.EngineComponent().ReplicaCount())
	assert.Equal(t, int32(1), stored.ProxyComponent().ReplicaCount())
}

// TestScaleRejectsMemberCountsWithoutQuorum covers the values a Galera cluster
// cannot run: an even member count has no quorum, so it is refused rather than
// handed to the operator.
func TestScaleRejectsMemberCountsWithoutQuorum(t *testing.T) {
	for _, replicas := range []int{0, 2, 4, 6, 7} {
		t.Run(fmt.Sprintf("%d members", replicas), func(t *testing.T) {
			p, fake := newPlugin(t)
			created := seedProfile(t, fake, "orders", "dev")

			err := p.Scale(context.Background(), created, engine.ScaleInput{Replicas: intPtr(replicas)})

			require.Error(t, err)
			assert.True(t, errors.Is(err, oe.ErrOutOfBounds), "want ErrOutOfBounds, got %v", err)

			var choice *engine.ChoiceError
			require.True(t, errors.As(err, &choice))
			assert.Equal(t, "Members must be 1, 3, or 5.", choice.UserMessage())

			stored := fake.stored(postgresql.DefaultNamespace, "orders")
			assert.Equal(t, int32(1), stored.EngineComponent().ReplicaCount(), "the stored spec was left alone")
		})
	}
}

// TestScalePreservesUntouchedFields proves a partial scale is a partial edit of
// the live spec, not a rebuild from a profile.
func TestScalePreservesUntouchedFields(t *testing.T) {
	p, fake := newPluginWithBackups(t)
	created := seedProfile(t, fake, "orders", "prod")

	require.NoError(t, p.Scale(context.Background(), created, engine.ScaleInput{StorageGB: intPtr(400)}))

	stored := fake.stored(postgresql.DefaultNamespace, "orders")
	assert.Equal(t, "4", stored.EngineComponent().CPU())
	assert.Equal(t, "16Gi", stored.EngineComponent().Memory())
	assert.Equal(t, "400Gi", stored.EngineComponent().StorageSize())
	assert.NotNil(t, stored.Spec.Backup, "the backup configuration survives a scale")
	assert.Equal(t, "pgbouncer", stored.ProxyComponent().Type, "the proxy tier survives a scale")
}

// TestScaleRejectsStorageShrink covers the storage rule: disks grow, never shrink.
func TestScaleRejectsStorageShrink(t *testing.T) {
	p, fake := newPlugin(t)
	created := seedProfile(t, fake, "orders", "staging")

	err := p.Scale(context.Background(), created, engine.ScaleInput{StorageGB: intPtr(50)})

	require.Error(t, err)
	assert.True(t, errors.Is(err, oe.ErrStorageShrink), "want ErrStorageShrink, got %v", err)

	stored := fake.stored(postgresql.DefaultNamespace, "orders")
	assert.Equal(t, "100Gi", stored.EngineComponent().StorageSize(), "the stored spec was left alone")
}

func TestScaleAllowsSameStorage(t *testing.T) {
	p, fake := newPlugin(t)
	created := seedProfile(t, fake, "orders", "staging")

	assert.NoError(t, p.Scale(context.Background(), created, engine.ScaleInput{StorageGB: intPtr(100)}))
}

func TestScaleRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		in      engine.ScaleInput
		wantErr error
	}{
		{
			name:    "cpu below the minimum",
			in:      engine.ScaleInput{CPUCores: intPtr(oe.MinCPUCores - 1)},
			wantErr: oe.ErrOutOfBounds,
		},
		{
			name:    "cpu above the maximum",
			in:      engine.ScaleInput{CPUCores: intPtr(oe.MaxCPUCores + 1)},
			wantErr: oe.ErrOutOfBounds,
		},
		{
			name:    "memory above the maximum",
			in:      engine.ScaleInput{MemoryGB: intPtr(oe.MaxMemoryGB + 1)},
			wantErr: oe.ErrOutOfBounds,
		},
		{
			name:    "storage above the maximum",
			in:      engine.ScaleInput{StorageGB: intPtr(oe.MaxStorageGB + 1)},
			wantErr: oe.ErrOutOfBounds,
		},
		{
			name:    "no field set",
			in:      engine.ScaleInput{},
			wantErr: oe.ErrEmptyScale,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, fake := newPlugin(t)
			created := seedProfile(t, fake, "orders", "prod")

			err := p.Scale(context.Background(), created, tt.in)

			require.Error(t, err)
			assert.True(t, errors.Is(err, tt.wantErr), "want %v, got %v", tt.wantErr, err)
		})
	}
}

// TestScaleWhileABackupRunsIsAConflict is the 409 case on scale. Scaling
// restarts pods, so a backup that is already running would be lost.
func TestScaleWhileABackupRunsIsAConflict(t *testing.T) {
	tests := []struct {
		state       string
		wantBusy    bool
		wantStorage string
	}{
		{state: everest.BackupStateRunning, wantBusy: true, wantStorage: "100Gi"},
		{state: everest.BackupStatePending, wantBusy: true, wantStorage: "100Gi"},
		{state: everest.BackupStateSucceeded, wantBusy: false, wantStorage: "200Gi"},
		{state: everest.BackupStateFailed, wantBusy: false, wantStorage: "200Gi"},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			p, fake := newPlugin(t)
			created := seedProfile(t, fake, "orders", "staging")
			fake.seedBackup(postgresql.DefaultNamespace, "orders", "orders-1", tt.state, "1Gi", time.Now().UTC())

			err := p.Scale(context.Background(), created, engine.ScaleInput{StorageGB: intPtr(200)})

			if tt.wantBusy {
				require.Error(t, err)
				assert.True(t, errors.Is(err, oe.ErrBusy), "want ErrBusy, got %v", err)

				var busy *engine.BusyError
				require.True(t, errors.As(err, &busy))
				assert.Equal(t, "backup", busy.Operation)
			} else {
				require.NoError(t, err)
			}

			stored := fake.stored(postgresql.DefaultNamespace, "orders")
			assert.Equal(t, tt.wantStorage, stored.EngineComponent().StorageSize())
		})
	}
}

// TestScaleMapsAnEverestConflict covers the other conflict: Everest itself
// refusing the write because the object moved under us or the operator has the
// instance locked. It is a 409 for the same reason, not a downstream failure.
func TestScaleMapsAnEverestConflict(t *testing.T) {
	p, fake := newPlugin(t)
	created := seedProfile(t, fake, "orders", "dev")

	fake.mu.Lock()
	fake.failUpdateWith = &everest.APIError{Status: 409, Message: "the object has been modified"}
	fake.mu.Unlock()

	err := p.Scale(context.Background(), created, engine.ScaleInput{CPUCores: intPtr(2)})

	require.Error(t, err)
	assert.True(t, errors.Is(err, oe.ErrBusy), "want ErrBusy, got %v", err)
	assert.True(t, errors.Is(err, everest.ErrConflict), "the Everest cause is preserved for the log")
}

func TestScaleUnknownInstance(t *testing.T) {
	p, _ := newPlugin(t)

	err := p.Scale(context.Background(), "pds.missing", engine.ScaleInput{CPUCores: intPtr(2)})

	require.Error(t, err)
	assert.True(t, errors.Is(err, oe.ErrInstanceNotFound), "want ErrInstanceNotFound, got %v", err)
}

func TestBackupCreatesABackupObject(t *testing.T) {
	p, fake := newPluginWithBackups(t)
	created := seedProfile(t, fake, "orders", "dev")

	got, err := p.Backup(context.Background(), created)
	require.NoError(t, err)

	assert.Equal(t, engine.BackupPending, got.Status)
	assert.Contains(t, got.ID, "orders-")

	backups := fake.backups[postgresql.DefaultNamespace+"/orders"]
	require.Len(t, backups, 1)
	assert.Equal(t, "orders", backups[0].Spec.InstanceName())
	assert.Equal(t, testBackupStorage, backups[0].Spec.StorageRef.Name)
}

func TestBackupNamedUsesTheCallerID(t *testing.T) {
	p, fake := newPluginWithBackups(t)
	created := seedProfile(t, fake, "orders", "dev")

	got, err := p.BackupNamed(context.Background(), created, "orders-20260908120000")
	require.NoError(t, err)
	assert.Equal(t, "orders-20260908120000", got.ID)

	backups := fake.backups[postgresql.DefaultNamespace+"/orders"]
	require.Len(t, backups, 1)
	assert.Equal(t, "orders-20260908120000", backups[0].Metadata.Name)
}

func TestBackupNamedTreatsAnExistingIDAsSuccess(t *testing.T) {
	p, fake := newPluginWithBackups(t)
	created := seedProfile(t, fake, "orders", "dev")

	first, err := p.BackupNamed(context.Background(), created, "orders-retry")
	require.NoError(t, err)

	again, err := p.BackupNamed(context.Background(), created, "orders-retry")
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, engine.BackupPending, again.Status)

	backups := fake.backups[postgresql.DefaultNamespace+"/orders"]
	require.Len(t, backups, 1)
}

// TestBackupWithoutStorageIsRefused is the shipped default: no BackupStorage
// exists, so a request Everest would reject is refused here instead.
func TestBackupWithoutStorageIsRefused(t *testing.T) {
	p, fake := newPlugin(t)
	created := seedProfile(t, fake, "orders", "dev")

	_, err := p.Backup(context.Background(), created)

	require.Error(t, err)
	assert.True(t, errors.Is(err, oe.ErrBackupsUnavailable), "want ErrBackupsUnavailable, got %v", err)
	assert.Empty(t, fake.backups[postgresql.DefaultNamespace+"/orders"])
}

// TestBackupNameFitsTheLabelLimit keeps a long database name from producing an
// object name Kubernetes refuses.
func TestBackupNameFitsTheLabelLimit(t *testing.T) {
	p, fake := newPluginWithBackups(t)
	long := "a-very-long-database-name-that-uses-most-of-the-dns-label-limit"
	require.Len(t, long, 63)

	created := seedProfile(t, fake, long, "dev")

	got, err := p.Backup(context.Background(), created)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(got.ID), 63)
}

func TestBackupUnknownInstance(t *testing.T) {
	p, _ := newPluginWithBackups(t)

	_, err := p.Backup(context.Background(), "pds.missing")

	require.Error(t, err)
	assert.True(t, errors.Is(err, oe.ErrInstanceNotFound), "want ErrInstanceNotFound, got %v", err)
}

func TestBackupsAreNewestFirst(t *testing.T) {
	p, fake := newPlugin(t)
	created := seedProfile(t, fake, "orders", "prod")

	older := time.Date(2026, time.August, 24, 9, 15, 0, 0, time.UTC)
	newer := time.Date(2026, time.August, 25, 9, 15, 0, 0, time.UTC)
	fake.seedBackup(postgresql.DefaultNamespace, "orders", "orders-older", everest.BackupStateSucceeded, "1289748", older)
	fake.seedBackup(postgresql.DefaultNamespace, "orders", "orders-newer", everest.BackupStateRunning, "", newer)

	got, err := p.Backups(context.Background(), created)
	require.NoError(t, err)

	require.Len(t, got, 2)
	assert.Equal(t, "orders-newer", got[0].ID)
	assert.Equal(t, engine.BackupRunning, got[0].Status)
	assert.Equal(t, "orders-older", got[1].ID)
	assert.Equal(t, engine.BackupSucceeded, got[1].Status)
	assert.Equal(t, int64(1289748), got[1].SizeBytes)
}

// The namespace backup collection answers 405 on some OpenEverest versions
// and only accepts POST there, so listing goes through each instance.
func TestNamespaceBackupsListsEachInstanceAndNeverTheNamespace(t *testing.T) {
	p, fake := newPlugin(t)
	seedProfile(t, fake, "orders", "prod")
	seedProfile(t, fake, "billing", "prod")

	older := time.Date(2026, time.August, 24, 9, 15, 0, 0, time.UTC)
	newer := time.Date(2026, time.August, 25, 9, 15, 0, 0, time.UTC)
	fake.seedBackup(postgresql.DefaultNamespace, "orders", "orders-older", everest.BackupStateSucceeded, "1289748", older)
	fake.seedBackup(postgresql.DefaultNamespace, "billing", "billing-newer", everest.BackupStateSucceeded, "", newer)
	fake.resetCalls()

	got, err := p.NamespaceBackups(context.Background(), postgresql.DefaultNamespace)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "billing-newer", got[0].ID)
	assert.Equal(t, "billing", got[0].Instance)
	assert.Equal(t, "orders-older", got[1].ID)
	assert.Equal(t, "orders", got[1].Instance)
	assert.ElementsMatch(t, []string{
		"ListInstances " + postgresql.DefaultNamespace,
		"ListInstanceBackups " + postgresql.DefaultNamespace + "/orders",
		"ListInstanceBackups " + postgresql.DefaultNamespace + "/billing",
	}, fake.seen())
}

func TestNamespaceBackupsSkipsAnInstanceDeletedMidListing(t *testing.T) {
	p, fake := newPlugin(t)
	seedProfile(t, fake, "orders", "prod")
	seedProfile(t, fake, "billing", "prod")
	fake.seedBackup(postgresql.DefaultNamespace, "orders", "orders-one", everest.BackupStateSucceeded, "", time.Now())
	fake.seedBackup(postgresql.DefaultNamespace, "billing", "billing-one", everest.BackupStateSucceeded, "", time.Now())
	fake.vanishedInstance = "billing"

	got, err := p.NamespaceBackups(context.Background(), postgresql.DefaultNamespace)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "orders-one", got[0].ID)
}

func TestNamespaceBackupsFailsWhenAnInstanceListFails(t *testing.T) {
	p, fake := newPlugin(t)
	seedProfile(t, fake, "orders", "prod")
	fake.failWith = &everest.APIError{Status: http.StatusInternalServerError}

	_, err := p.NamespaceBackups(context.Background(), postgresql.DefaultNamespace)
	var apiErr *everest.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusInternalServerError, apiErr.Status)
}

// TestBackupsSeparateScheduledFromOnDemand is what the Type column in the
// backup list reads. A scheduled backup carries spec.scheduleName; an
// on-demand backup does not.
func TestBackupsSeparateScheduledFromOnDemand(t *testing.T) {
	p, fake := newPluginWithBackups(t)
	created := seedProfile(t, fake, "orders", "prod")

	at := time.Date(2026, time.August, 25, 2, 0, 0, 0, time.UTC)
	fake.seedScheduledBackup(postgresql.DefaultNamespace, "orders",
		"orders-daily-20260825020000", everest.BackupStateSucceeded, "2Gi", at)

	onDemand, err := p.Backup(context.Background(), created)
	require.NoError(t, err)
	assert.False(t, onDemand.Scheduled)

	got, err := p.Backups(context.Background(), created)
	require.NoError(t, err)
	require.Len(t, got, 2)

	byID := map[string]engine.Backup{}
	for _, backup := range got {
		byID[backup.ID] = backup
	}

	require.Contains(t, byID, "orders-daily-20260825020000")
	assert.True(t, byID["orders-daily-20260825020000"].Scheduled)
	assert.False(t, byID[onDemand.ID].Scheduled)
}

// TestBackupsNormalizeEverestStates covers the state vocabulary the UI renders
// as one set of pills, and the size string Everest reports in bytes.
func TestBackupsNormalizeEverestStates(t *testing.T) {
	tests := []struct {
		everestState string
		size         string
		want         string
		wantBytes    int64
	}{
		{everestState: everest.BackupStatePending, want: engine.BackupPending},
		{everestState: everest.BackupStateRunning, want: engine.BackupRunning},
		{everestState: everest.BackupStateSucceeded, size: "2Gi", want: engine.BackupSucceeded, wantBytes: 2147483648},
		{everestState: everest.BackupStateFailed, want: engine.BackupFailed},
		{everestState: everest.BackupStateDeleting, want: engine.BackupDeleting},
		{everestState: "", want: engine.BackupPending},
		{everestState: "SomethingNew", want: engine.BackupUnknown},
		{everestState: everest.BackupStateSucceeded, size: "not a size", want: engine.BackupSucceeded},
	}

	for _, tt := range tests {
		t.Run(tt.everestState+" "+tt.size, func(t *testing.T) {
			p, fake := newPlugin(t)
			created := seedProfile(t, fake, "orders", "dev")

			at := time.Date(2026, time.August, 25, 9, 15, 0, 0, time.UTC)
			fake.seedBackup(postgresql.DefaultNamespace, "orders", "orders-1", tt.everestState, tt.size, at)

			got, err := p.Backups(context.Background(), created)
			require.NoError(t, err)

			require.Len(t, got, 1)
			assert.Equal(t, tt.want, got[0].Status)
			assert.Equal(t, tt.wantBytes, got[0].SizeBytes)
		})
	}
}

// TestSizingReadsKubernetesQuantities covers the quantity spellings an operator
// or a hand-edited CR can put in a spec. An unreadable quantity reads as zero:
// one bad value must not hide the instance, or every other instance in the list.
func TestSizingReadsKubernetesQuantities(t *testing.T) {
	tests := []struct {
		name          string
		cpu           string
		memory        string
		storage       string
		wantCPU       float64
		wantMemoryGB  float64
		wantStorageGB float64
	}{
		{
			name: "whole units", cpu: "4", memory: "16Gi", storage: "250Gi",
			wantCPU: 4, wantMemoryGB: 16, wantStorageGB: 250,
		},
		{
			name: "millicores", cpu: "500m", memory: "512Mi", storage: "10Gi",
			wantCPU: 0.5, wantMemoryGB: 0.5, wantStorageGB: 10,
		},
		{
			// A developer who writes 8G means eight gigabytes, so the
			// adapter reports 8 rather than the 7.45 a strict SI
			// reading of the byte count would give.
			name: "decimal units", cpu: "2.5", memory: "8G", storage: "1T",
			wantCPU: 2.5, wantMemoryGB: 8, wantStorageGB: 1024,
		},
		{
			name: "decimal memory next to binary storage", cpu: "1", memory: "2G", storage: "20Gi",
			wantCPU: 1, wantMemoryGB: 2, wantStorageGB: 20,
		},
		{
			name: "mebibytes and megabytes", cpu: "1", memory: "2048Mi", storage: "2048M",
			wantCPU: 1, wantMemoryGB: 2, wantStorageGB: 2,
		},
		{
			name: "plain bytes", cpu: "1", memory: "2147483648", storage: "21474836480",
			wantCPU: 1, wantMemoryGB: 2, wantStorageGB: 20,
		},
		{
			name: "unreadable reads as zero", cpu: "lots", memory: "plenty", storage: "",
			wantCPU: 0, wantMemoryGB: 0, wantStorageGB: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, fake := newPlugin(t)

			in := pgInstance("orders", 3, tt.cpu, tt.memory, tt.storage)
			_, err := fake.CreateInstance(context.Background(), postgresql.DefaultNamespace, in)
			require.NoError(t, err)

			got := listNamed(t, p, "pds.orders")

			assert.Equal(t, 3, got.Sizing.Members)
			assert.InDelta(t, tt.wantCPU, got.Sizing.CPUCores, 0.01)
			assert.InDelta(t, tt.wantMemoryGB, got.Sizing.MemoryGB, 0.01)
			assert.InDelta(t, tt.wantStorageGB, got.Sizing.StorageGB, 0.01)
		})
	}
}

// TestSizingWithoutResourcesIsZero covers a cluster whose engine carries no
// resources block, which is what a partially created CR looks like.
func TestSizingWithoutResourcesIsZero(t *testing.T) {
	p, fake := newPlugin(t)

	in := pgInstance("orders", 0, "", "", "")
	_, err := fake.CreateInstance(context.Background(), postgresql.DefaultNamespace, in)
	require.NoError(t, err)

	got := listNamed(t, p, "pds.orders")

	assert.Equal(t, engine.Sizing{}, got.Sizing)
	assert.Equal(t, engine.EngineResources{}, got.Spec.Components.Engine.Resources)
}

func TestBackupsUnknownInstance(t *testing.T) {
	p, _ := newPlugin(t)

	_, err := p.Backups(context.Background(), "pds.missing")

	require.Error(t, err)
	assert.True(t, errors.Is(err, oe.ErrInstanceNotFound), "want ErrInstanceNotFound, got %v", err)
}

func TestDeleteRemovesTheInstance(t *testing.T) {
	p, fake := newPlugin(t)
	created := seedProfile(t, fake, "orders", "dev")

	require.NoError(t, p.Delete(context.Background(), created))
	assert.Nil(t, fake.stored(postgresql.DefaultNamespace, "orders"))
}

func TestDeleteUnknownInstance(t *testing.T) {
	p, _ := newPlugin(t)

	err := p.Delete(context.Background(), "pds.missing")

	require.Error(t, err)
	assert.True(t, errors.Is(err, oe.ErrInstanceNotFound), "want ErrInstanceNotFound, got %v", err)
}

// TestEverestFailurePropagates proves a downstream outage is never swallowed.
// Every call must surface it so the HTTP layer can answer 502.
func TestEverestFailurePropagates(t *testing.T) {
	boom := errors.New("everest: dial tcp: connection refused")

	tests := []struct {
		name string
		call func(*postgresql.Plugin) error
	}{
		{
			name: "list",
			call: func(p *postgresql.Plugin) error {
				_, err := p.List(context.Background())

				return err
			},
		},
		{
			name: "scale",
			call: func(p *postgresql.Plugin) error {
				return p.Scale(context.Background(), "pds.orders", engine.ScaleInput{CPUCores: intPtr(2)})
			},
		},
		{
			name: "backup",
			call: func(p *postgresql.Plugin) error {
				_, err := p.Backup(context.Background(), "pds.orders")

				return err
			},
		},
		{
			name: "backups",
			call: func(p *postgresql.Plugin) error {
				_, err := p.Backups(context.Background(), "pds.orders")

				return err
			},
		},
		{
			name: "delete",
			call: func(p *postgresql.Plugin) error {
				return p.Delete(context.Background(), "pds.orders")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeEverest()
			fake.failWith = boom

			err := tt.call(postgresql.New(fake, postgresql.WithBackupStorage(testBackupStorage)))

			require.Error(t, err)
			assert.ErrorIs(t, err, boom)
			assert.NotErrorIs(t, err, oe.ErrInstanceNotFound)
		})
	}
}

// TestConcurrentAccess runs the plugin the way the HTTP layer does, from many
// goroutines at once, so -race proves nothing is shared unsafely.
func TestConcurrentAccess(t *testing.T) {
	p, fake := newPlugin(t)
	const workers = 8

	ids := make([]string, workers)
	for i := range workers {
		ids[i] = seedProfile(t, fake, fmt.Sprintf("orders-%d", i), "dev")
	}

	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			assert.NoError(t, p.Scale(context.Background(), ids[i], engine.ScaleInput{StorageGB: intPtr(40)}))

			items, err := p.List(context.Background())
			assert.NoError(t, err)
			assert.NotEmpty(t, items)
		}(i)
	}
	wg.Wait()
}
