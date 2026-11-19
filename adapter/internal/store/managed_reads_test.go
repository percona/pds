package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

func TestListReturnsTheTeamsManagedEnvironmentsByName(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	other, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|grace", "Globex"), time.Now())
	require.NoError(t, err)
	me := db.ManagedEnvironments()
	for _, in := range []store.NewManagedEnvironment{
		managedInput(admin.TeamID, "000000000002", "payments"),
		managedInput(admin.TeamID, "000000000001", "orders"),
		managedInput(other.TeamID, "000000000003", "ledger"),
	} {
		_, err = me.Create(t.Context(), in, time.Now())
		require.NoError(t, err)
	}

	got, err := me.List(t.Context(), admin.TeamID)
	require.NoError(t, err)

	require.Len(t, got, 2)
	assert.Equal(t, "orders", got[0].Name)
	assert.Equal(t, "payments", got[1].Name)
}

func TestLatestRunIsTheNewestOfTheEnvironmentAndCarriesItsRefusal(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	prov := db.Provisioning()
	now := time.Now().UTC().Truncate(time.Millisecond)

	_, err := prov.Latest(t.Context(), admin.TeamID, "orders")
	require.ErrorIs(t, err, store.ErrRunNotFound)

	first, err := prov.Start(t.Context(), admin.TeamID, "orders", "create", []string{"one", "two"}, now)
	require.NoError(t, err)
	claimed, err := prov.Claim(t.Context(), "w1", []string{"create"}, time.Minute, now)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, first.ID, claimed.ID)
	require.NoError(t, prov.BeginStep(t.Context(), first.ID, "w1", 0, now))
	require.NoError(t, prov.EndStep(t.Context(), first.ID, "w1", 0, store.StepEnd{
		StepStatus: store.StepFailed, RunStatus: store.RunFailed, Failure: "AWS refused it.", Message: "AWS refused it.",
		Refusal: store.StepRefusal{Reason: "quota", Service: "Amazon EC2", Item: "Elastic IP addresses per Region", Region: "eu-west-1"},
	}, now))

	latest, err := prov.Latest(t.Context(), admin.TeamID, "orders")
	require.NoError(t, err)
	assert.Equal(t, first.ID, latest.ID)
	steps, err := prov.Steps(t.Context(), latest.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StepRefusal{Reason: "quota", Service: "Amazon EC2", Item: "Elastic IP addresses per Region", Region: "eu-west-1"},
		steps[0].Refusal)
	assert.Empty(t, steps[1].Refusal)

	require.NoError(t, prov.Retry(t.Context(), admin.TeamID, "orders", now))
	steps, err = prov.Steps(t.Context(), latest.ID)
	require.NoError(t, err)
	assert.Empty(t, steps[0].Refusal, "a retry clears the refusal with the failure")

	_, err = prov.Latest(t.Context(), admin.TeamID+100, "orders")
	require.ErrorIs(t, err, store.ErrRunNotFound, "another Team has no such run")
}

func TestRevokingATokenByItsPublicIDReturnsItAndStaysInsideTheTeam(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	cc := db.CloudConnections()
	hash := "0123456789abcdef0123456789abcdef"
	require.NoError(t, cc.CreateToken(t.Context(), newCloudToken(admin.TeamID, hash, time.Hour)))
	_, err := cc.Enroll(t.Context(), enrollInput(hash, "123456789012", "aaaaaaaaaaaa"), time.Now())
	require.NoError(t, err)

	_, err = cc.RevokeTokenByID(t.Context(), admin.TeamID+100, hash[:16], time.Now())
	require.ErrorIs(t, err, store.ErrCloudTokenInvalid, "another Team cannot cancel it")

	token, err := cc.RevokeTokenByID(t.Context(), admin.TeamID, hash[:16], time.Now())
	require.NoError(t, err)
	assert.Equal(t, "aaaaaaaaaaaa", token.ConnectionID)

	_, err = cc.TokenTeam(t.Context(), hash, time.Now())
	require.ErrorIs(t, err, store.ErrCloudTokenInvalid)
}

func TestRemoveForgetsAReadyConnectionWithItsTokensAndEvents(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	cc := db.CloudConnections()
	require.NoError(t, cc.CreateToken(t.Context(), newCloudToken(admin.TeamID, "h1", time.Hour)))
	_, err := cc.Enroll(t.Context(), enrollInput("h1", "123456789012", "aaaaaaaaaaaa"), time.Now())
	require.NoError(t, err)
	_, err = cc.SaveCheck(t.Context(), "aaaaaaaaaaaa", store.CloudCheck{State: store.CloudReady, TemplateVersion: 1}, time.Now())
	require.NoError(t, err)

	require.ErrorIs(t, cc.Remove(t.Context(), admin.TeamID+100, "aaaaaaaaaaaa"), store.ErrCloudConnectionUnknown)
	require.NoError(t, cc.Remove(t.Context(), admin.TeamID, "aaaaaaaaaaaa"))

	_, err = cc.Get(t.Context(), admin.TeamID, "aaaaaaaaaaaa")
	require.ErrorIs(t, err, store.ErrCloudConnectionUnknown)
}
