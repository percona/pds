package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

// failRun starts a run of the kind and marks it failed, the state a worker
// leaves when a step fails for good. The store tests share one database, so a
// worker claiming by kind could pick up another test's run; the row is set
// directly instead.
func failRun(t *testing.T, db *store.Store, team uint, environment, kind string) store.ProvisioningRun {
	t.Helper()
	run, err := db.Provisioning().StartWith(t.Context(), team, environment, kind, []string{"apply-release"}, "", time.Now())
	require.NoError(t, err)
	require.NoError(t, db.Gorm().WithContext(t.Context()).Model(&store.ProvisioningRun{}).Where("id = ?", run.ID).
		Updates(map[string]any{"status": store.RunFailed, "message": "Upgrade failed."}).Error)

	return run
}

func TestAFailedUpgradeRunDoesNotBlockTheNextRun(t *testing.T) {
	tests := []struct {
		name   string
		failed string
		next   string
	}{
		{"a rolled-back chart upgrade then a newer upgrade", store.KindUpgradePDS, store.KindUpgradePDS},
		{"a rolled-back chart upgrade then a Kubernetes update", store.KindUpgradePDS, store.KindUpgradeKubernetes},
		{"a failed Kubernetes update then a chart upgrade", store.KindUpgradeKubernetes, store.KindUpgradePDS},
		{"a failed Kubernetes update then another update", store.KindUpgradeKubernetes, store.KindUpgradeKubernetes},
		{"a failed chart upgrade then a rebuild", store.KindUpgradePDS, "rebuild"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, admin := newOrganizationWithAdmin(t)
			prov := db.Provisioning()
			failed := failRun(t, db, admin.TeamID, "orders", tc.failed)

			next, err := prov.StartWith(t.Context(), admin.TeamID, "orders", tc.next, []string{"apply-release"}, "", time.Now())

			require.NoError(t, err)
			assert.Equal(t, store.RunQueued, next.Status)
			got, err := prov.Run(t.Context(), failed.ID)
			require.NoError(t, err)
			assert.Equal(t, store.RunCancelled, got.Status, "the failed run is superseded, not left to hold the environment")
		})
	}
}

func TestAFailedRunOfAnyOtherKindStillHoldsTheEnvironment(t *testing.T) {
	for _, kind := range []string{"create", "rebuild"} {
		t.Run(kind, func(t *testing.T) {
			db, admin := newOrganizationWithAdmin(t)
			prov := db.Provisioning()
			failRun(t, db, admin.TeamID, "orders", kind)

			_, err := prov.StartWith(t.Context(), admin.TeamID, "orders", store.KindUpgradePDS, []string{"apply-release"}, "", time.Now())

			require.ErrorIs(t, err, store.ErrRunActive)
		})
	}
}

func TestARetryStillRevivesAFailedUpgradeRun(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	prov := db.Provisioning()
	failed := failRun(t, db, admin.TeamID, "orders", store.KindUpgradePDS)

	require.NoError(t, prov.Retry(t.Context(), admin.TeamID, "orders", time.Now()))

	got, err := prov.Run(t.Context(), failed.ID)
	require.NoError(t, err)
	assert.Equal(t, store.RunQueued, got.Status)
}

func TestInProgressIgnoresAFailedRun(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	prov := db.Provisioning()
	failRun(t, db, admin.TeamID, "orders", store.KindUpgradePDS)

	working, err := prov.InProgress(t.Context(), admin.TeamID, "orders")

	require.NoError(t, err)
	assert.False(t, working, "a failed run changes nothing, so the hourly check may read the environment")
}
