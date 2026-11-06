package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/hub"
)

// A delete and a scale only exist in the queue until the spoke's next beat. A
// hub restart in that window used to drop them silently: the UI had already
// said the change was accepted, and nothing ever carried it.
func TestQueuedWorkSurvivesAHubRestart(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewConfigMapWorkPersist(kv, "everest-system", "pds-hub-work")

	q := hub.NewWorkQueue()
	q.SetPersist(persist)
	require.NoError(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"}))
	cpu := 4
	require.NoError(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkScale, Database: "pds.orders-dev", CPUCores: &cpu}))

	restarted := hub.NewWorkQueue()
	restarted.SetPersist(persist)
	require.NoError(t, restarted.Load(ctx))

	got := restarted.Take("spoke-a")
	require.Len(t, got, 2)
	assert.Equal(t, hub.WorkDelete, got[0].Kind)
	assert.Equal(t, hub.WorkScale, got[1].Kind)
	require.NotNil(t, got[1].CPUCores)
	assert.Equal(t, 4, *got[1].CPUCores)
}

// Work the spoke already ran must not come back after a restart, or a restart
// re-deletes a database somebody recreated.
func TestAcknowledgedWorkDoesNotSurviveARestart(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewConfigMapWorkPersist(kv, "everest-system", "pds-hub-work")

	q := hub.NewWorkQueue()
	q.SetPersist(persist)
	require.NoError(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"}))

	sent := q.Take("spoke-a")
	require.Len(t, sent, 1)
	require.NoError(t, q.Ack("spoke-a", []hub.WorkResult{{ID: sent[0].ID}}))

	restarted := hub.NewWorkQueue()
	restarted.SetPersist(persist)
	require.NoError(t, restarted.Load(ctx))

	assert.Empty(t, restarted.Take("spoke-a"))
}

// Work handed to a spoke that never acknowledged is still owed, so a restart
// has to bring it back rather than treat handing it over as doing it.
func TestWorkInFlightAtRestartComesBack(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewConfigMapWorkPersist(kv, "everest-system", "pds-hub-work")

	q := hub.NewWorkQueue()
	q.SetPersist(persist)
	require.NoError(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"}))
	require.Len(t, q.Take("spoke-a"), 1)

	restarted := hub.NewWorkQueue()
	restarted.SetPersist(persist)
	require.NoError(t, restarted.Load(ctx))

	assert.Len(t, restarted.Take("spoke-a"), 1)
}

func TestAckDropsWorkRestoredAsQueuedAfterARestart(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewConfigMapWorkPersist(kv, "everest-system", "pds-hub-work")

	q := hub.NewWorkQueue()
	q.SetPersist(persist)
	require.NoError(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"}))
	sent := q.Take("spoke-a")
	require.Len(t, sent, 1)

	restarted := hub.NewWorkQueue()
	restarted.SetPersist(persist)
	require.NoError(t, restarted.Load(ctx))
	require.NoError(t, restarted.Ack("spoke-a", []hub.WorkResult{{ID: sent[0].ID}}))

	assert.Empty(t, restarted.Take("spoke-a"), "a retained ack must not redeliver a delete after restart")
}

// A queue with nowhere to write still works, which is what the tests that
// keep the queue in memory rely on.
func TestAQueueWithoutPersistStillWorks(t *testing.T) {
	q := hub.NewWorkQueue()
	require.NoError(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"}))

	assert.Len(t, q.Take("spoke-a"), 1)
	assert.NoError(t, q.Load(context.Background()))
}

// failingPersist is a store the adapter is not allowed to read, which is what
// an older chart looks like: the Role does not name the new ConfigMap yet.
type failingPersist struct{ loads int }

func (f *failingPersist) Load(context.Context) (map[string][]hub.Work, error) {
	f.loads++

	return nil, assert.AnError
}

func (f *failingPersist) Save(context.Context, string, []hub.Work) error { return assert.AnError }

type forbiddenPersist struct{}

func (forbiddenPersist) Load(context.Context) (map[string][]hub.Work, error) {
	return nil, kube.ErrForbidden
}

func (forbiddenPersist) Save(context.Context, string, []hub.Work) error {
	return kube.ErrForbidden
}

func TestATransientWorkLoadErrorDoesNotDetach(t *testing.T) {
	p := &failingPersist{}
	q := hub.NewWorkQueue()
	q.SetPersist(p)

	require.Error(t, q.Load(context.Background()), "a timeout must fail startup rather than go memory-only")
	require.Error(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"}))
	assert.Empty(t, q.Take("spoke-a"), "persist stays attached so a failed save cannot accept work")
}

func TestForbiddenWorkConfigMapDoesNotStopTheHub(t *testing.T) {
	q := hub.NewWorkQueue()
	q.SetPersist(forbiddenPersist{})

	require.NoError(t, q.Load(context.Background()), "Forbidden must not be fatal")
	require.NoError(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"}))
	assert.Len(t, q.Take("spoke-a"), 1)
}

// Once reading is refused, writing will be too, so the queue detaches rather
// than warning on every push for the life of the process.
func TestAnUnreadableQueueStopsTryingToWrite(t *testing.T) {
	q := hub.NewWorkQueue()
	q.SetPersist(forbiddenPersist{})
	require.NoError(t, q.Load(context.Background()))

	require.NoError(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"}))
	assert.Len(t, q.Take("spoke-a"), 1)
}

// Disconnecting an environment and connecting it again used to hand the new spoke
// the previous registration's delete and scale work on its first heartbeat.
func TestClearDropsQueuedAndInFlightWork(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewConfigMapWorkPersist(kv, "everest-system", "pds-hub-work")

	q := hub.NewWorkQueue()
	q.SetPersist(persist)
	require.NoError(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"}))
	require.NoError(t, q.Push("spoke-b", hub.Work{Kind: hub.WorkBackup, Database: "pds.other", BackupID: "other-1"}))
	require.Len(t, q.Take("spoke-a"), 1)

	require.NoError(t, q.Clear("spoke-a"))

	assert.Empty(t, q.Take("spoke-a"))
	assert.Equal(t, 0, q.Pending("spoke-a"))
	require.Len(t, q.Take("spoke-b"), 1, "clearing one environment must not drop another")

	restarted := hub.NewWorkQueue()
	restarted.SetPersist(persist)
	require.NoError(t, restarted.Load(ctx))
	assert.Empty(t, restarted.Take("spoke-a"))
}

type saveFailPersist struct{}

func (saveFailPersist) Load(context.Context) (map[string][]hub.Work, error) {
	return map[string][]hub.Work{}, nil
}

func (saveFailPersist) Save(context.Context, string, []hub.Work) error {
	return assert.AnError
}

// A save failure used to log and still return the item as queued, so delete
// and scale callers answered success for work a restart would drop.
func TestPushDoesNotAcceptWorkWhenPersistFails(t *testing.T) {
	q := hub.NewWorkQueue()
	q.SetPersist(saveFailPersist{})

	err := q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"})
	require.Error(t, err)
	assert.Empty(t, q.Take("spoke-a"), "a failed save must not leave work in memory")
}

func TestClearLeavesWorkWhenPersistFails(t *testing.T) {
	kv := &memKV{}
	persist := hub.NewConfigMapWorkPersist(kv, "everest-system", "pds-hub-work")
	q := hub.NewWorkQueue()
	q.SetPersist(persist)
	require.NoError(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"}))

	q.SetPersist(saveFailPersist{})
	require.Error(t, q.Clear("spoke-a"))
	assert.Len(t, q.Take("spoke-a"), 1, "clear must restore memory when the empty queue cannot be saved")
}

func TestAckLeavesWorkWhenPersistFails(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewConfigMapWorkPersist(kv, "everest-system", "pds-hub-work")
	q := hub.NewWorkQueue()
	q.SetPersist(persist)
	require.NoError(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"}))
	sent := q.Take("spoke-a")
	require.Len(t, sent, 1)

	q.SetPersist(saveFailPersist{})
	require.Error(t, q.Ack("spoke-a", []hub.WorkResult{{ID: sent[0].ID}}))
	assert.Equal(t, 1, q.Pending("spoke-a"), "ack must restore in-flight work when the save fails")

	restarted := hub.NewWorkQueue()
	restarted.SetPersist(persist)
	require.NoError(t, restarted.Load(ctx))
	assert.Len(t, restarted.Take("spoke-a"), 1, "a failed ack must not drop the ConfigMap entry")
}

func TestAckLeavesQueuedWorkWhenPersistFails(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewConfigMapWorkPersist(kv, "everest-system", "pds-hub-work")
	q := hub.NewWorkQueue()
	q.SetPersist(persist)
	require.NoError(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"}))
	sent := q.Take("spoke-a")
	require.Len(t, sent, 1)

	restarted := hub.NewWorkQueue()
	restarted.SetPersist(persist)
	require.NoError(t, restarted.Load(ctx))
	restarted.SetPersist(saveFailPersist{})
	require.Error(t, restarted.Ack("spoke-a", []hub.WorkResult{{ID: sent[0].ID}}))
	assert.Len(t, restarted.Take("spoke-a"), 1, "ack must restore queued work when the save fails")
}

func TestLoadSkipsCorruptEnvironmentJSONAndKeepsTheRest(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewConfigMapWorkPersist(kv, "everest-system", "pds-hub-work")
	require.NoError(t, persist.Save(ctx, "spoke-a", []hub.Work{{
		ID: "wrk_keep", Kind: hub.WorkDelete, Database: "pds.orders-dev",
	}}))
	require.NoError(t, kv.PutConfigMap(ctx, "everest-system", "pds-hub-work", map[string]string{
		"spoke-b": "{not-json",
	}))

	loaded, err := persist.Load(ctx)
	require.NoError(t, err)
	require.Contains(t, loaded, "spoke-a")
	assert.Equal(t, "wrk_keep", loaded["spoke-a"][0].ID)
	assert.NotContains(t, loaded, "spoke-b")
}

type countingWorkPersist struct {
	saves int
}

func (c *countingWorkPersist) Load(context.Context) (map[string][]hub.Work, error) {
	return map[string][]hub.Work{}, nil
}

func (c *countingWorkPersist) Save(context.Context, string, []hub.Work) error {
	c.saves++

	return nil
}

func TestTakeDoesNotRewriteAnUnchangedQueue(t *testing.T) {
	p := &countingWorkPersist{}
	q := hub.NewWorkQueue()
	q.SetPersist(p)
	require.NoError(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"}))
	afterPush := p.saves
	require.Len(t, q.Take("spoke-a"), 1)
	assert.Equal(t, afterPush, p.saves, "Take must not persist a queue whose owed set did not change")
	q.Take("spoke-b")
	assert.Equal(t, afterPush, p.saves, "an empty heartbeat must not write the work ConfigMap")
}

type blockingWorkPersist struct{}

func (blockingWorkPersist) Load(context.Context) (map[string][]hub.Work, error) {
	return map[string][]hub.Work{}, nil
}

func (blockingWorkPersist) Save(ctx context.Context, _ string, _ []hub.Work) error {
	<-ctx.Done()

	return ctx.Err()
}

func TestWorkSaveIsBounded(t *testing.T) {
	q := hub.NewWorkQueue()
	q.SetPersist(blockingWorkPersist{})

	done := make(chan error, 1)
	go func() {
		done <- q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"})
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(8 * time.Second):
		t.Fatal("work persist held the queue mutex without a deadline")
	}
}

func TestADelayedAckCannotDropWorkMintedAfterAnEmptyRestart(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewConfigMapWorkPersist(kv, "everest-system", "pds-hub-work")

	q := hub.NewWorkQueue()
	q.SetPersist(persist)
	require.NoError(t, q.Push("spoke-a", hub.Work{Kind: hub.WorkDelete, Database: "pds.orders-dev"}))
	sent := q.Take("spoke-a")
	require.Len(t, sent, 1)
	oldID := sent[0].ID
	require.NoError(t, q.Ack("spoke-a", []hub.WorkResult{{ID: oldID}}))

	restarted := hub.NewWorkQueue()
	restarted.SetPersist(persist)
	require.NoError(t, restarted.Load(ctx))
	require.NoError(t, restarted.Push("spoke-a", hub.Work{Kind: hub.WorkBackup, Database: "pds.orders-dev", BackupID: "orders-dev-1"}))
	handed := restarted.Take("spoke-a")
	require.Len(t, handed, 1)
	assert.NotEqual(t, oldID, handed[0].ID)

	require.NoError(t, restarted.Ack("spoke-a", []hub.WorkResult{{ID: oldID}}))
	assert.Equal(t, 1, restarted.Pending("spoke-a"), "an ack for a previous item must not drop later work")
}
