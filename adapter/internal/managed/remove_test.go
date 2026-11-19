package managed_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/store"
)

type fakeRecords struct {
	rec store.ManagedEnvironment
	err error
}

func (f fakeRecords) Get(_ context.Context, team uint, name string) (store.ManagedEnvironment, error) {
	if f.err != nil || team != f.rec.TeamID || name != f.rec.Name {
		return store.ManagedEnvironment{}, errors.Join(store.ErrManagedEnvironmentUnknown, f.err)
	}

	return f.rec, nil
}

type fakeConnections struct {
	state string
	err   error
}

func (f fakeConnections) Get(_ context.Context, team uint, id string) (store.CloudConnection, error) {
	return store.CloudConnection{ID: id, TeamID: team, AccountID: "111122223333", State: f.state}, f.err
}

type fakeAbandoner struct {
	messages []string
	err      error
}

func (f *fakeAbandoner) Abandon(_ context.Context, _ uint, environment, message string, _ time.Time) (store.ProvisioningRun, error) {
	f.messages = append(f.messages, environment+": "+message)

	return store.ProvisioningRun{Kind: store.KindDelete, Status: store.RunSucceeded}, f.err
}

type removalLab struct {
	runs      *fakeRuns
	abandoner *fakeAbandoner
	forgotten []string
	forgetErr error
	conn      fakeConnections
	remover   *managed.Remover
	scope     auth.Scope
}

func newRemovalLab(connectionState string) *removalLab {
	l := &removalLab{runs: &fakeRuns{}, abandoner: &fakeAbandoner{}, conn: fakeConnections{state: connectionState}}
	rec := store.ManagedEnvironment{ID: "0123456789ab", TeamID: 7, Name: "orders", ConnectionID: "aaaaaaaaaaaa", Region: "eu-west-1"}
	l.remover = managed.NewRemover(fakeRecords{rec: rec}, l.conn, l.runs, l.abandoner,
		func(_ context.Context, _ uint, name string) error {
			l.forgotten = append(l.forgotten, name)

			return l.forgetErr
		})
	l.scope = auth.Scope{Team: auth.Team{ID: 7}}

	return l
}

func TestDeleteStartsTheDeleteRunOfTheTeamsEnvironment(t *testing.T) {
	l := newRemovalLab(store.CloudReady)

	run, err := l.remover.Delete(t.Context(), l.scope, "orders")
	require.NoError(t, err)

	assert.Equal(t, []string{"delete/orders"}, l.runs.started)
	assert.Equal(t, managed.KindDelete, run.Kind)
	assert.Empty(t, l.forgotten, "the run forgets the environment once AWS is clean")
}

func TestDeleteOfAnotherTeamsEnvironmentIsRefusedAsUnknown(t *testing.T) {
	l := newRemovalLab(store.CloudReady)
	l.scope.Team.ID = 8

	_, err := l.remover.Delete(t.Context(), l.scope, "orders")

	require.ErrorIs(t, err, store.ErrManagedEnvironmentUnknown)
	assert.Empty(t, l.runs.started)
}

func TestDeletePassesOnARunThatIsAlreadyAskedFor(t *testing.T) {
	l := newRemovalLab(store.CloudReady)
	l.runs.err = store.ErrRunActive

	_, err := l.remover.Delete(t.Context(), l.scope, "orders")

	require.ErrorIs(t, err, store.ErrRunActive)
}

func TestRemoveFromPDSForgetsAnEnvironmentWhoseConnectionIsUnreachable(t *testing.T) {
	l := newRemovalLab(store.CloudUnreachable)

	require.NoError(t, l.remover.RemoveFromPDS(t.Context(), l.scope, "orders"))

	assert.Equal(t, []string{"orders"}, l.forgotten)
	require.Len(t, l.abandoner.messages, 1)
	assert.Contains(t, l.abandoner.messages[0], "Removed from PDS only")
	assert.Empty(t, l.runs.started, "nothing is deleted in AWS")
}

func TestRemoveFromPDSIsRefusedWhileTheConnectionWorks(t *testing.T) {
	l := newRemovalLab(store.CloudReady)

	err := l.remover.RemoveFromPDS(t.Context(), l.scope, "orders")

	require.ErrorIs(t, err, managed.ErrConnectionReachable)
	assert.Empty(t, l.forgotten)
	assert.Empty(t, l.abandoner.messages)
}

func TestRemoveFromPDSStopsWhenARunIsWorkingOnTheEnvironment(t *testing.T) {
	l := newRemovalLab(store.CloudUnreachable)
	l.abandoner.err = store.ErrRunActive

	err := l.remover.RemoveFromPDS(t.Context(), l.scope, "orders")

	require.ErrorIs(t, err, store.ErrRunActive)
	assert.Empty(t, l.forgotten, "the record stays until nothing is working on it")
}

func TestRemoveFromPDSAllowsAConnectionThatNoLongerExists(t *testing.T) {
	l := newRemovalLab(store.CloudReady)
	l.remover = managed.NewRemover(
		fakeRecords{rec: store.ManagedEnvironment{ID: "0123456789ab", TeamID: 7, Name: "orders", ConnectionID: "gone"}},
		fakeConnections{err: store.ErrCloudConnectionUnknown}, l.runs, l.abandoner,
		func(_ context.Context, _ uint, name string) error {
			l.forgotten = append(l.forgotten, name)
			return nil
		})

	require.NoError(t, l.remover.RemoveFromPDS(t.Context(), l.scope, "orders"))

	assert.Equal(t, []string{"orders"}, l.forgotten)
}

func TestRemainsNamesWhatStaysInAWSAfterRemovingFromPDSOnly(t *testing.T) {
	l := newRemovalLab(store.CloudUnreachable)

	got, err := l.remover.Remains(t.Context(), l.scope, "orders")
	require.NoError(t, err)

	assert.Equal(t, managed.Remains{
		AccountID: "111122223333", Region: "eu-west-1", StackName: "pds-env-0123456789ab",
		ClusterName: "pds-0123456789ab", Bucket: "pds-111122223333-0123456789ab",
	}, got)
}
