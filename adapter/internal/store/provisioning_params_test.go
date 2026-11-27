package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

func TestARunKeepsTheParametersItWasStartedWith(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	prov := db.Provisioning()

	started, err := prov.StartWith(t.Context(), admin.TeamID, "orders", "upgrade_pds", []string{"apply-release"}, `{"version":"0.2.0"}`, time.Now())
	require.NoError(t, err)

	got, err := prov.Run(t.Context(), started.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"version":"0.2.0"}`, got.Params)
}

func TestStartLeavesTheParametersEmpty(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	prov := db.Provisioning()

	started, err := prov.Start(t.Context(), admin.TeamID, "orders", "create", []string{"assume-role"}, time.Now())
	require.NoError(t, err)

	got, err := prov.Run(t.Context(), started.ID)
	require.NoError(t, err)
	assert.Empty(t, got.Params)
}

func TestAParameterisedRunStillHoldsTheEnvironment(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	prov := db.Provisioning()
	_, err := prov.StartWith(t.Context(), admin.TeamID, "orders", "upgrade_pds", []string{"apply-release"}, `{}`, time.Now())
	require.NoError(t, err)

	_, err = prov.StartWith(t.Context(), admin.TeamID, "orders", "upgrade_kubernetes", []string{"assume-role"}, `{}`, time.Now())

	require.ErrorIs(t, err, store.ErrRunActive)
}

func TestInProgressSaysWhetherARunIsChangingTheEnvironment(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	prov := db.Provisioning()

	busy, err := prov.InProgress(t.Context(), admin.TeamID, "orders")
	require.NoError(t, err)
	assert.False(t, busy)

	run, err := prov.Start(t.Context(), admin.TeamID, "orders", "create", []string{"assume-role"}, time.Now())
	require.NoError(t, err)
	busy, err = prov.InProgress(t.Context(), admin.TeamID, "orders")
	require.NoError(t, err)
	assert.True(t, busy)

	claimed, err := prov.Claim(t.Context(), "w1", []string{"create"}, time.Minute, time.Now())
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NoError(t, prov.EndRun(t.Context(), run.ID, "w1", store.RunSucceeded, time.Now()))
	busy, err = prov.InProgress(t.Context(), admin.TeamID, "orders")
	require.NoError(t, err)
	assert.False(t, busy)

	busy, err = prov.InProgress(t.Context(), admin.TeamID, "billing")
	require.NoError(t, err)
	assert.False(t, busy, "another environment is not busy")
}
