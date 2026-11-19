package store_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

func newCloudToken(team uint, hash string, ttl time.Duration) store.NewCloudToken {
	return store.NewCloudToken{TeamID: team, Hash: hash, AllowedRegions: []string{"eu-west-1"}, ExpiresAt: time.Now().Add(ttl)}
}

func enrollInput(hash, account, id string) store.CloudEnrollment {
	return store.CloudEnrollment{
		TokenHash: hash, AccountID: account, Alias: "acme", StackRegion: "eu-west-1",
		NewID: id, NewExternalID: "ext-" + id, NewStackName: "pds-" + id,
	}
}

func TestEnrollingTwiceFromOneAccountReturnsTheSameConnection(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	cc := db.CloudConnections()
	require.NoError(t, cc.CreateToken(t.Context(), newCloudToken(admin.TeamID, "h1", time.Hour)))

	first, err := cc.Enroll(t.Context(), enrollInput("h1", "123456789012", "aaaaaaaaaaaa"), time.Now())
	require.NoError(t, err)
	again, err := cc.Enroll(t.Context(), enrollInput("h1", "123456789012", "bbbbbbbbbbbb"), time.Now())
	require.NoError(t, err)

	assert.Equal(t, "aaaaaaaaaaaa", first.ID)
	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, first.ExternalID, again.ExternalID)
	assert.Equal(t, first.StackName, again.StackName)
	assert.Equal(t, store.CloudPending, again.State)
	assert.Equal(t, []string{"eu-west-1"}, again.AllowedRegions)
}

func TestTwoEnrollmentsAtOnceMakeOneConnection(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	cc := db.CloudConnections()
	require.NoError(t, cc.CreateToken(t.Context(), newCloudToken(admin.TeamID, "h1", time.Hour)))

	var wg sync.WaitGroup
	ids := make([]string, 2)
	errs := make([]error, 2)
	for n, id := range []string{"aaaaaaaaaaaa", "bbbbbbbbbbbb"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var row store.CloudConnection
			row, errs[n] = cc.Enroll(t.Context(), enrollInput("h1", "123456789012", id), time.Now())
			ids[n] = row.ID
		}()
	}
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, ids[0], ids[1])
}

func TestAnotherTeamWithTheSameAccountGetsItsOwnConnection(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	other, err := db.Identity().CreateTeam(t.Context(), admin.OrganizationID, "payments", time.Now())
	require.NoError(t, err)
	cc := db.CloudConnections()
	require.NoError(t, cc.CreateToken(t.Context(), newCloudToken(admin.TeamID, "h1", time.Hour)))
	require.NoError(t, cc.CreateToken(t.Context(), newCloudToken(other.ID, "h2", time.Hour)))

	a, err := cc.Enroll(t.Context(), enrollInput("h1", "123456789012", "aaaaaaaaaaaa"), time.Now())
	require.NoError(t, err)
	b, err := cc.Enroll(t.Context(), enrollInput("h2", "123456789012", "bbbbbbbbbbbb"), time.Now())
	require.NoError(t, err)

	assert.NotEqual(t, a.ID, b.ID)
	assert.NotEqual(t, a.ExternalID, b.ExternalID)

	_, err = cc.Get(t.Context(), other.ID, a.ID)
	assert.ErrorIs(t, err, store.ErrCloudConnectionUnknown, "another Team's connection reads as missing")
}

func TestATokenNeverEnrollsOnceExpiredRevokedOrUnknown(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	cc := db.CloudConnections()
	require.NoError(t, cc.CreateToken(t.Context(), newCloudToken(admin.TeamID, "old", -time.Minute)))
	require.NoError(t, cc.CreateToken(t.Context(), newCloudToken(admin.TeamID, "revoked", time.Hour)))
	require.NoError(t, cc.RevokeToken(t.Context(), admin.TeamID, "revoked", time.Now()))

	for _, hash := range []string{"old", "revoked", "nobody"} {
		_, err := cc.Enroll(t.Context(), enrollInput(hash, "123456789012", "aaaaaaaaaaaa"), time.Now())
		assert.ErrorIs(t, err, store.ErrCloudTokenInvalid, hash)
	}
}

func TestATokenBindsToTheFirstAccountItEnrolls(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	cc := db.CloudConnections()
	require.NoError(t, cc.CreateToken(t.Context(), newCloudToken(admin.TeamID, "h1", time.Hour)))
	_, err := cc.Enroll(t.Context(), enrollInput("h1", "123456789012", "aaaaaaaaaaaa"), time.Now())
	require.NoError(t, err)

	_, err = cc.Enroll(t.Context(), enrollInput("h1", "999999999999", "bbbbbbbbbbbb"), time.Now())

	assert.ErrorIs(t, err, store.ErrCloudTokenAccount)
}

func TestReachingReadyRevokesTheConnectionsTokensOnce(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	cc := db.CloudConnections()
	require.NoError(t, cc.CreateToken(t.Context(), newCloudToken(admin.TeamID, "h1", time.Hour)))
	row, err := cc.Enroll(t.Context(), enrollInput("h1", "123456789012", "aaaaaaaaaaaa"), time.Now())
	require.NoError(t, err)

	saved, err := cc.SaveCheck(t.Context(), row.ID, store.CloudCheck{State: store.CloudReady, TemplateVersion: 1}, time.Now())
	require.NoError(t, err)
	require.NotNil(t, saved.ReadyAt)

	_, err = cc.Enroll(t.Context(), enrollInput("h1", "123456789012", "aaaaaaaaaaaa"), time.Now())
	assert.ErrorIs(t, err, store.ErrCloudTokenInvalid, "the token stops working once the connection is Ready")

	later := time.Now().Add(time.Hour)
	again, err := cc.SaveCheck(t.Context(), row.ID, store.CloudCheck{State: store.CloudUnreachable, Reason: "gone"}, later)
	require.NoError(t, err)
	assert.Equal(t, store.CloudUnreachable, again.State)
	assert.WithinDuration(t, saved.ReadyAt.UTC(), again.ReadyAt.UTC(), time.Millisecond, "ReadyAt keeps the first time")
}

func TestCancelDropsAPendingConnectionAndItsTokensButNotAReadyOne(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	cc := db.CloudConnections()
	require.NoError(t, cc.CreateToken(t.Context(), newCloudToken(admin.TeamID, "h1", time.Hour)))
	pending, err := cc.Enroll(t.Context(), enrollInput("h1", "123456789012", "aaaaaaaaaaaa"), time.Now())
	require.NoError(t, err)
	require.NoError(t, cc.CreateToken(t.Context(), newCloudToken(admin.TeamID, "h2", time.Hour)))
	ready, err := cc.Enroll(t.Context(), enrollInput("h2", "210987654321", "bbbbbbbbbbbb"), time.Now())
	require.NoError(t, err)
	_, err = cc.SaveCheck(t.Context(), ready.ID, store.CloudCheck{State: store.CloudReady}, time.Now())
	require.NoError(t, err)

	require.NoError(t, cc.Cancel(t.Context(), admin.TeamID, pending.ID, time.Now()))
	_, err = cc.Get(t.Context(), admin.TeamID, pending.ID)
	assert.ErrorIs(t, err, store.ErrCloudConnectionUnknown)
	_, err = cc.Enroll(t.Context(), enrollInput("h1", "123456789012", "cccccccccccc"), time.Now())
	assert.ErrorIs(t, err, store.ErrCloudTokenInvalid)

	assert.ErrorIs(t, cc.Cancel(t.Context(), admin.TeamID, ready.ID, time.Now()), store.ErrCloudConnectionReady)
}

func TestEventsKeepStepsAndHeartbeatsOnlyTouchTheConnection(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	cc := db.CloudConnections()
	require.NoError(t, cc.CreateToken(t.Context(), newCloudToken(admin.TeamID, "h1", time.Hour)))
	row, err := cc.Enroll(t.Context(), enrollInput("h1", "123456789012", "aaaaaaaaaaaa"), time.Now())
	require.NoError(t, err)
	start := time.Now()

	require.NoError(t, cc.RecordEvent(t.Context(), row.ID, store.CloudEvent{Step: "stack", Status: store.CloudStepStarted}, start))
	require.NoError(t, cc.RecordEvent(t.Context(), row.ID, store.CloudEvent{Step: "stack", Status: store.CloudStepHeartbeat}, start.Add(30*time.Second)))
	require.NoError(t, cc.RecordEvent(t.Context(), row.ID, store.CloudEvent{Step: "stack", Status: store.CloudStepFailed, Detail: "boom"}, start.Add(time.Minute)))

	events, err := cc.Events(t.Context(), admin.TeamID, row.ID)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, store.CloudStepStarted, events[0].Status)
	assert.Equal(t, "boom", events[1].Detail)

	got, err := cc.Get(t.Context(), admin.TeamID, row.ID)
	require.NoError(t, err)
	require.NotNil(t, got.SignalAt)
	assert.WithinDuration(t, start.Add(time.Minute), *got.SignalAt, time.Second)
}
