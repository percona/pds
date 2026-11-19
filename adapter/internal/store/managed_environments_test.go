package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

func managedInput(team uint, id, name string) store.NewManagedEnvironment {
	return store.NewManagedEnvironment{ID: id, TeamID: team, Name: name, ConnectionID: "aaaaaaaaaaaa", Region: "eu-west-1"}
}

func TestManagedEnvironmentIsReadBackByTeamAndName(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	me := db.ManagedEnvironments()

	created, err := me.Create(t.Context(), managedInput(admin.TeamID, "0123456789ab", "orders"), time.Now())
	require.NoError(t, err)

	got, err := me.Get(t.Context(), admin.TeamID, "orders")
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, "aaaaaaaaaaaa", got.ConnectionID)
	assert.Equal(t, "eu-west-1", got.Region)
	assert.False(t, got.StackFailureSeen)
}

func TestManagedEnvironmentNamesAreUniquePerTeam(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	other, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|grace", "Globex"), time.Now())
	require.NoError(t, err)
	me := db.ManagedEnvironments()

	_, err = me.Create(t.Context(), managedInput(admin.TeamID, "0123456789ab", "orders"), time.Now())
	require.NoError(t, err)

	_, err = me.Create(t.Context(), managedInput(admin.TeamID, "ba9876543210", "orders"), time.Now())
	require.ErrorIs(t, err, store.ErrManagedEnvironmentExists)

	_, err = me.Create(t.Context(), managedInput(other.TeamID, "ba9876543210", "orders"), time.Now())
	require.NoError(t, err, "another Team may use the name")
}

func TestAnotherTeamsManagedEnvironmentIsNotFound(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	other, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|grace", "Globex"), time.Now())
	require.NoError(t, err)
	me := db.ManagedEnvironments()
	_, err = me.Create(t.Context(), managedInput(admin.TeamID, "0123456789ab", "orders"), time.Now())
	require.NoError(t, err)

	_, err = me.Get(t.Context(), other.TeamID, "orders")
	require.ErrorIs(t, err, store.ErrManagedEnvironmentUnknown)
}

func TestStackFailureSeenIsRemembered(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	me := db.ManagedEnvironments()
	_, err := me.Create(t.Context(), managedInput(admin.TeamID, "0123456789ab", "orders"), time.Now())
	require.NoError(t, err)

	require.NoError(t, me.SetStackFailureSeen(t.Context(), admin.TeamID, "orders", true))
	got, err := me.Get(t.Context(), admin.TeamID, "orders")
	require.NoError(t, err)
	assert.True(t, got.StackFailureSeen)

	require.NoError(t, me.SetStackFailureSeen(t.Context(), admin.TeamID, "orders", false))
	got, err = me.Get(t.Context(), admin.TeamID, "orders")
	require.NoError(t, err)
	assert.False(t, got.StackFailureSeen)
}

func TestRemovingAManagedEnvironmentFreesItsName(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	me := db.ManagedEnvironments()
	_, err := me.Create(t.Context(), managedInput(admin.TeamID, "0123456789ab", "orders"), time.Now())
	require.NoError(t, err)

	require.NoError(t, me.Remove(t.Context(), admin.TeamID, "orders"))

	_, err = me.Create(t.Context(), managedInput(admin.TeamID, "ba9876543210", "orders"), time.Now())
	require.NoError(t, err)
}

func TestANewManagedEnvironmentHasTheDefaultMaintenanceWindowAndNoLifecycleState(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	me := db.ManagedEnvironments()
	_, err := me.Create(t.Context(), managedInput(admin.TeamID, "0123456789ab", "orders"), time.Now())
	require.NoError(t, err)

	got, err := me.Get(t.Context(), admin.TeamID, "orders")
	require.NoError(t, err)

	assert.Equal(t, store.MaintenanceWindow{Day: time.Sunday, Hour: 2, Zone: "UTC"}, got.Window())
	assert.Empty(t, got.ChartVersion)
	assert.Nil(t, got.ReadyAt)
	assert.Nil(t, got.LostAt)
	assert.Empty(t, got.DeferredVersion)
	assert.Empty(t, got.SkippedVersion)
	assert.Empty(t, got.Drift)
}

func TestLifecycleStateIsKeptPerEnvironment(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	me := db.ManagedEnvironments()
	now := time.Now().UTC().Truncate(time.Second)
	_, err := me.Create(t.Context(), managedInput(admin.TeamID, "0123456789ab", "orders"), now)
	require.NoError(t, err)
	_, err = me.Create(t.Context(), managedInput(admin.TeamID, "ba9876543210", "billing"), now)
	require.NoError(t, err)

	require.NoError(t, me.SetChartVersion(t.Context(), admin.TeamID, "orders", "0.1.0"))
	require.NoError(t, me.SetMaintenanceWindow(t.Context(), admin.TeamID, "orders", store.MaintenanceWindow{Day: time.Friday, Hour: 22, Zone: "Europe/Berlin"}))
	require.NoError(t, me.SetDeferral(t.Context(), admin.TeamID, "orders", "0.2.0", now.Add(48*time.Hour)))
	require.NoError(t, me.SetSkippedVersion(t.Context(), admin.TeamID, "orders", "0.2.0"))
	drift := []store.DriftItem{{Resource: "PrivateRoute", Detail: "Changed outside PDS"}}
	require.NoError(t, me.SetDrift(t.Context(), admin.TeamID, "orders", drift, now))

	got, err := me.Get(t.Context(), admin.TeamID, "orders")
	require.NoError(t, err)
	assert.Equal(t, "0.1.0", got.ChartVersion)
	assert.Equal(t, store.MaintenanceWindow{Day: time.Friday, Hour: 22, Zone: "Europe/Berlin"}, got.Window())
	assert.Equal(t, "0.2.0", got.DeferredVersion)
	assert.WithinDuration(t, now.Add(48*time.Hour), *got.DeferredUntil, time.Second)
	assert.Equal(t, "0.2.0", got.SkippedVersion)
	assert.Equal(t, drift, got.Drift)
	assert.WithinDuration(t, now, *got.DriftCheckedAt, time.Second)

	other, err := me.Get(t.Context(), admin.TeamID, "billing")
	require.NoError(t, err)
	assert.Empty(t, other.ChartVersion)
	assert.Empty(t, other.Drift)
}

func TestMarkReadyClearsLostAndKeepsTheFirstReadyTime(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	me := db.ManagedEnvironments()
	first := time.Now().UTC().Truncate(time.Second)
	_, err := me.Create(t.Context(), managedInput(admin.TeamID, "0123456789ab", "orders"), first)
	require.NoError(t, err)

	require.NoError(t, me.MarkReady(t.Context(), admin.TeamID, "orders", first))
	require.NoError(t, me.SetLost(t.Context(), admin.TeamID, "orders", true, first.Add(time.Hour)))
	require.NoError(t, me.SetLost(t.Context(), admin.TeamID, "orders", true, first.Add(2*time.Hour)))
	lost, err := me.Get(t.Context(), admin.TeamID, "orders")
	require.NoError(t, err)
	require.NotNil(t, lost.LostAt)
	assert.WithinDuration(t, first.Add(time.Hour), *lost.LostAt, time.Second, "it stays lost since the first time it was seen")

	require.NoError(t, me.MarkReady(t.Context(), admin.TeamID, "orders", first.Add(3*time.Hour)))
	got, err := me.Get(t.Context(), admin.TeamID, "orders")
	require.NoError(t, err)
	assert.Nil(t, got.LostAt)
	assert.WithinDuration(t, first, *got.ReadyAt, time.Second)
}

func TestLifecycleUpdatesOfAnotherTeamsEnvironmentAreNotFound(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	me := db.ManagedEnvironments()

	assert.ErrorIs(t, me.SetChartVersion(t.Context(), admin.TeamID, "ghost", "0.1.0"), store.ErrManagedEnvironmentUnknown)
	assert.ErrorIs(t, me.SetLost(t.Context(), admin.TeamID, "ghost", true, time.Now()), store.ErrManagedEnvironmentUnknown)
}

func TestAllListsEveryTeamsManagedEnvironments(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	other, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|grace", "Globex"), time.Now())
	require.NoError(t, err)
	me := db.ManagedEnvironments()
	_, err = me.Create(t.Context(), managedInput(admin.TeamID, "0123456789ab", "orders"), time.Now())
	require.NoError(t, err)
	_, err = me.Create(t.Context(), managedInput(other.TeamID, "ba9876543210", "orders"), time.Now())
	require.NoError(t, err)

	all, err := me.All(t.Context())

	require.NoError(t, err)
	var ids []string
	for _, e := range all {
		ids = append(ids, e.ID)
	}
	assert.Contains(t, ids, "0123456789ab")
	assert.Contains(t, ids, "ba9876543210")
}
