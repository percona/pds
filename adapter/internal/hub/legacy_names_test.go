package hub_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

func TestAHeartbeatThatNamesItsClusterStillDecodes(t *testing.T) {
	var beat hub.Heartbeat

	require.NoError(t, json.Unmarshal([]byte(`{"cluster":"spoke-a","namespace":"pds"}`), &beat))

	assert.Equal(t, "spoke-a", beat.Environment)
	assert.Equal(t, "pds", beat.Namespace)
}

func TestAHeartbeatThatNamesItsEnvironmentWinsOverTheOldField(t *testing.T) {
	var beat hub.Heartbeat

	require.NoError(t, json.Unmarshal([]byte(`{"cluster":"old","environment":"spoke-a"}`), &beat))

	assert.Equal(t, "spoke-a", beat.Environment)
}

func TestAHeartbeatIsWrittenWithOnlyTheEnvironmentField(t *testing.T) {
	body, err := json.Marshal(hub.Heartbeat{Environment: "spoke-a"})
	require.NoError(t, err)

	assert.Contains(t, string(body), `"environment":"spoke-a"`)
	assert.NotContains(t, string(body), `"cluster"`)
}

func TestAGrantThatNamesItsClusterStillDecodes(t *testing.T) {
	var grant hub.Grant

	require.NoError(t, json.Unmarshal([]byte(`{"cluster":"spoke-a","role":"agent"}`), &grant))

	assert.Equal(t, "spoke-a", grant.Environment)
	assert.Equal(t, "agent", grant.Role)
}

// A manifest written before the rename carries the old label. Listing reads
// the environment from where the manifest sits, so the database still shows
// under it, and the next rewrite moves the label to the new key.
func TestAnInstanceLabeledWithTheOldClusterKeyStillListsAndIsRelabeledOnScale(t *testing.T) {
	svc, store, _ := newHub(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	path := "clusters/spoke-a/instances/orders-dev.yaml"
	body, err := store.Get(ctx, path)
	require.NoError(t, err)
	legacy := strings.Replace(string(body), "pds.percona.com/environment:", "pds.percona.com/cluster:", 1)
	require.NotEqual(t, string(body), legacy)
	require.NoError(t, store.Write(ctx, map[string][]byte{path: []byte(legacy)}, "relabel"))

	listed, err := svc.List(ctx, "spoke-a")
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, created.ID, listed[0].ID)

	memory := 4
	require.NoError(t, svc.Scale(ctx, created.ID, engine.ScaleInput{MemoryGB: &memory}))

	rewritten, err := store.Get(ctx, path)
	require.NoError(t, err)
	assert.Contains(t, string(rewritten), "pds.percona.com/environment: spoke-a")
	assert.NotContains(t, string(rewritten), "pds.percona.com/cluster")
}
