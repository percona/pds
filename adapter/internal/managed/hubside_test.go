package managed_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/managed"
)

type fakeTeamHub struct {
	enrolled  []string
	enrollErr error
	files     map[string][]byte
	message   string
	committed string
	connected map[string]bool
	forgotten []string
	forgetErr error
}

func (f *fakeTeamHub) EnrollManaged(_ context.Context, name string) (hub.ManagedEnrollment, error) {
	f.enrolled = append(f.enrolled, name)

	return hub.ManagedEnrollment{AgentToken: "agt_x", Identity: "key", Pub: "pub", Namespace: "pds", Repo: "acme-orders"}, f.enrollErr
}

func (f *fakeTeamHub) CommitFiles(_ context.Context, environment string, files map[string][]byte, message string) error {
	f.committed, f.files, f.message = environment, files, message

	return nil
}

func (f *fakeTeamHub) Disconnect(_ context.Context, environment string) (bool, error) {
	f.forgotten = append(f.forgotten, environment)

	return false, f.forgetErr
}

func (f *fakeTeamHub) EnvironmentConnected(environment string) bool { return f.connected[environment] }

func newHubSide(t *testing.T, teams map[uint]*fakeTeamHub) *managed.HubSide {
	t.Helper()

	return managed.NewHubSide(
		func(_ context.Context, team uint) (managed.TeamHub, error) {
			h, ok := teams[team]
			if !ok {
				return nil, errors.New("no hub for the team")
			}

			return h, nil
		},
		func(repo string) string { return "ssh://git@github.com/acme/" + repo + ".git" },
	)
}

func TestHubSideJoinGivesTheStepWhatTheEnvironmentNeeds(t *testing.T) {
	team := &fakeTeamHub{}
	side := newHubSide(t, map[uint]*fakeTeamHub{7: team})

	join, err := side.Join(t.Context(), 7, "orders")

	require.NoError(t, err)
	assert.Equal(t, managed.Join{
		Environment: "orders", AgentToken: "agt_x", GitURL: "ssh://git@github.com/acme/acme-orders.git",
		GitIdentity: "key", GitIdentityPub: "pub", Namespace: "pds",
	}, join)
	assert.Equal(t, []string{"orders"}, team.enrolled)
}

func TestHubSideJoinFailsWithoutAGitAddress(t *testing.T) {
	side := managed.NewHubSide(
		func(context.Context, uint) (managed.TeamHub, error) { return &fakeTeamHub{}, nil },
		func(string) string { return "" },
	)

	_, err := side.Join(t.Context(), 7, "orders")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Git")
}

func TestHubSideAnswersForTheTeamThatOwnsTheEnvironment(t *testing.T) {
	mine, theirs := &fakeTeamHub{connected: map[string]bool{"orders": true}}, &fakeTeamHub{}
	side := newHubSide(t, map[uint]*fakeTeamHub{7: mine, 8: theirs})

	require.NoError(t, side.WriteRelease(t.Context(), 7, "orders", map[string][]byte{"clusters/orders/x.yaml": []byte("x")}))
	connected, err := side.Connected(t.Context(), 7, "orders")
	require.NoError(t, err)
	other, err := side.Connected(t.Context(), 8, "orders")
	require.NoError(t, err)

	assert.Equal(t, "orders", mine.committed)
	assert.Contains(t, mine.message, "orders")
	assert.Empty(t, theirs.committed)
	assert.True(t, connected)
	assert.False(t, other, "another Team's environment of the same name is another environment")
}

func TestHubSidePassesOnAHubItCannotReach(t *testing.T) {
	side := newHubSide(t, map[uint]*fakeTeamHub{})

	_, err := side.Join(t.Context(), 9, "orders")
	assert.Error(t, err)
	assert.Error(t, side.WriteRelease(t.Context(), 9, "orders", nil))
	_, err = side.Connected(t.Context(), 9, "orders")
	assert.Error(t, err)
}

func TestHubSideDisconnectRemovesTheEnvironmentFromItsTeamsHub(t *testing.T) {
	mine, theirs := &fakeTeamHub{}, &fakeTeamHub{}
	side := newHubSide(t, map[uint]*fakeTeamHub{7: mine, 8: theirs})

	require.NoError(t, side.Disconnect(t.Context(), 7, "orders"))

	assert.Equal(t, []string{"orders"}, mine.forgotten)
	assert.Empty(t, theirs.forgotten)
}

func TestHubSideDisconnectIsDoneForAnEnvironmentTheHubDoesNotKnow(t *testing.T) {
	team := &fakeTeamHub{forgetErr: fmt.Errorf("%w: %q", engine.ErrNotFound, "orders")}
	side := newHubSide(t, map[uint]*fakeTeamHub{7: team})

	require.NoError(t, side.Disconnect(t.Context(), 7, "orders"), "a rerun after the hub forgot it has nothing left to do")

	team.forgetErr = errors.New("the Git host is down")
	require.Error(t, side.Disconnect(t.Context(), 7, "orders"))
}
