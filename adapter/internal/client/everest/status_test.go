package everest_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
)

func TestReadyCountsReadTheEngineComponent(t *testing.T) {
	var status everest.InstanceStatus
	require.NoError(t, json.Unmarshal([]byte(`{"components":[
		{"name":"proxy","selector":"app=p","replicas":5,"readyReplicas":5},
		{"name":"engine","selector":"app=e","replicas":3,"readyReplicas":2}
	]}`), &status))

	ready, total := status.ReadyCounts()

	assert.Equal(t, int32(2), ready)
	assert.Equal(t, int32(3), total, "the proxy has more pods and is still not the engine")
}

func TestReadyCountsWithoutAnEngineComponent(t *testing.T) {
	ready, total := everest.InstanceStatus{}.ReadyCounts()

	assert.Zero(t, ready)
	assert.Zero(t, total)
}

func TestReadyCountsOfAPartialEngineStatus(t *testing.T) {
	var status everest.InstanceStatus
	require.NoError(t, json.Unmarshal([]byte(`{"components":[{"name":"engine"}]}`), &status))

	ready, total := status.ReadyCounts()

	assert.Zero(t, ready)
	assert.Zero(t, total)
}

func TestSchedulingBlockerReadsTheTransitionTimeFromTheWire(t *testing.T) {
	var status everest.InstanceStatus
	require.NoError(t, json.Unmarshal([]byte(`{"conditions":[
		{"type":"PodsReady","status":"False","lastTransitionTime":"2026-10-19T10:00:00Z"},
		{"type":"PodsScheduled","status":"False","reason":"Unschedulable","lastTransitionTime":"2026-10-19T10:00:00Z"}
	]}`), &status))

	held := time.Date(2026, 10, 19, 10, 2, 0, 0, time.UTC)

	blocker, ok := status.SchedulingBlocker(2*time.Minute, held)
	assert.True(t, ok, "a hold of exactly the grace period counts")
	assert.Equal(t, "Unschedulable", blocker.Reason)

	_, ok = status.SchedulingBlocker(2*time.Minute, held.Add(-time.Second))
	assert.False(t, ok, "one second short of the grace period does not")
}

func TestSchedulingBlockerIgnoresAConditionWithoutATransitionTime(t *testing.T) {
	var status everest.InstanceStatus
	require.NoError(t, json.Unmarshal([]byte(`{"conditions":[{"type":"PodsScheduled","status":"False"}]}`), &status))

	_, ok := status.SchedulingBlocker(time.Minute, time.Now())

	assert.False(t, ok)
}
