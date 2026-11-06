package hub_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

func TestJoinAndSplitHubID(t *testing.T) {
	id := hub.JoinID("spoke-a", "pds", "orders-dev")
	assert.Equal(t, "spoke-a/pds.orders-dev", id)

	environment, ns, name, err := hub.SplitID(id)
	require.NoError(t, err)
	assert.Equal(t, "spoke-a", environment)
	assert.Equal(t, "pds", ns)
	assert.Equal(t, "orders-dev", name)
}

func TestSplitIDRequiresAnEnvironment(t *testing.T) {
	_, _, _, err := hub.SplitID("pds.orders-dev")
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrInvalidID)
	assert.NotErrorIs(t, err, hub.ErrUnreachable)
}

func TestSplitIDRejectsJunk(t *testing.T) {
	_, _, _, err := hub.SplitID("no-dot")
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrInvalidID)
}
