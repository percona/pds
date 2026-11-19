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
