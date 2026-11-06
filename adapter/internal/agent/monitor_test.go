package agent_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/agent"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

// TestBeatCarriesTheMonitoringSampleWithTheUsageSample reads the local PMM
// once per usage sample, so the query load follows the five minute throttle
// rather than the beat interval.
func TestBeatCarriesTheMonitoringSampleWithTheUsageSample(t *testing.T) {
	dbs := &listStub{instances: []engine.Instance{{
		Name: "orders", Namespace: "pds", Status: engine.Status{Phase: engine.PhaseReady},
	}}}

	beats := make(chan hub.Heartbeat, 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		var beat hub.Heartbeat
		assert.NoError(t, json.Unmarshal(body, &beat))
		select {
		case beats <- beat:
		default:
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"work":[]}`))
	}))
	t.Cleanup(srv.Close)

	cpu := 41.5
	var reads atomic.Int32
	var monitored []string
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		done <- agent.Run(ctx, agent.Options{
			HubURL:           srv.URL,
			Environment:      "spoke-a",
			Namespace:        "pds",
			MonitoringConfig: "pmm",
			Interval:         5 * time.Millisecond,
			Databases:        dbs,
			Collect: func(_ context.Context, _ string, items []engine.Instance) ([]hub.HeartbeatEvent, []hub.HeartbeatUsage) {
				usage := make([]hub.HeartbeatUsage, 0, len(items))
				for _, item := range items {
					usage = append(usage, hub.HeartbeatUsage{DatabaseID: item.ID, At: time.Now().UTC()})
				}

				return nil, usage
			},
			Monitor: func(_ context.Context, item engine.Instance) *hub.HeartbeatMonitoring {
				reads.Add(1)
				monitored = append(monitored, item.Name)

				return &hub.HeartbeatMonitoring{CPU: &cpu}
			},
		})
	}()

	first := <-beats
	assert.Equal(t, "pmm", first.MonitoringConfig)
	require.Len(t, first.Usage, 1)
	require.NotNil(t, first.Usage[0].Monitoring)
	assert.InDelta(t, cpu, *first.Usage[0].Monitoring.CPU, 0.001)

	// Later beats inside the throttle carry no sample and read nothing.
	for range 3 {
		later := <-beats
		assert.Empty(t, later.Usage)
		assert.Equal(t, "pmm", later.MonitoringConfig)
	}
	cancel()
	require.NoError(t, <-done)
	assert.Equal(t, int32(1), reads.Load())
	assert.Equal(t, []string{"orders"}, monitored)
}

// TestBeatWithoutAMonitorSendsUsageAlone keeps an agent with no local PMM on
// the beat it always sent.
func TestBeatWithoutAMonitorSendsUsageAlone(t *testing.T) {
	dbs := &listStub{instances: []engine.Instance{{
		Name: "orders", Namespace: "pds", Status: engine.Status{Phase: engine.PhaseReady},
	}}}

	beats := make(chan hub.Heartbeat, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var beat hub.Heartbeat
		_ = json.Unmarshal(body, &beat)
		select {
		case beats <- beat:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Namespace:   "pds",
			Interval:    time.Hour,
			Databases:   dbs,
			Collect: func(_ context.Context, _ string, items []engine.Instance) ([]hub.HeartbeatEvent, []hub.HeartbeatUsage) {
				return nil, []hub.HeartbeatUsage{{DatabaseID: items[0].ID, At: time.Now().UTC()}}
			},
		})
	}()

	beat := <-beats
	assert.Empty(t, beat.MonitoringConfig)
	require.Len(t, beat.Usage, 1)
	assert.Nil(t, beat.Usage[0].Monitoring)
}

// TestBeatDoesNotWaitOutAHungMonitor bounds the whole PMM pass, so a PMM that
// hangs cannot hold the heartbeat past the budget.
func TestBeatDoesNotWaitOutAHungMonitor(t *testing.T) {
	dbs := &listStub{instances: []engine.Instance{
		{Name: "orders", Namespace: "pds", Status: engine.Status{Phase: engine.PhaseReady}},
		{Name: "billing", Namespace: "pds", Status: engine.Status{Phase: engine.PhaseReady}},
	}}

	beats := make(chan hub.Heartbeat, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var beat hub.Heartbeat
		_ = json.Unmarshal(body, &beat)
		select {
		case beats <- beat:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	started := time.Now()
	go func() {
		_ = agent.Run(ctx, agent.Options{
			HubURL:         srv.URL,
			Environment:    "spoke-a",
			Namespace:      "pds",
			Interval:       time.Hour,
			MonitorTimeout: 50 * time.Millisecond,
			Databases:      dbs,
			Collect: func(_ context.Context, _ string, items []engine.Instance) ([]hub.HeartbeatEvent, []hub.HeartbeatUsage) {
				usage := make([]hub.HeartbeatUsage, 0, len(items))
				for _, item := range items {
					usage = append(usage, hub.HeartbeatUsage{DatabaseID: item.ID, At: time.Now().UTC()})
				}

				return nil, usage
			},
			Monitor: func(ctx context.Context, _ engine.Instance) *hub.HeartbeatMonitoring {
				<-ctx.Done()

				return &hub.HeartbeatMonitoring{Pending: "unavailable"}
			},
		})
	}()

	select {
	case beat := <-beats:
		assert.Less(t, time.Since(started), 2*time.Second)
		require.Len(t, beat.Usage, 2)
		for _, point := range beat.Usage {
			require.NotNil(t, point.Monitoring)
			assert.Equal(t, "unavailable", point.Monitoring.Pending)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a hung monitor held the heartbeat")
	}
}

// TestBeatReusesTheReadingWhileTheHubIsDown keeps a hub outage from turning
// every retried beat into a fresh round of PMM queries.
func TestBeatReusesTheReadingWhileTheHubIsDown(t *testing.T) {
	dbs := &listStub{instances: []engine.Instance{{
		Name: "orders", Namespace: "pds", Status: engine.Status{Phase: engine.PhaseReady},
	}}}

	var posts atomic.Int32
	accepted := make(chan hub.Heartbeat, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if posts.Add(1) <= 3 {
			w.WriteHeader(http.StatusBadGateway)

			return
		}
		body, _ := io.ReadAll(r.Body)
		var beat hub.Heartbeat
		_ = json.Unmarshal(body, &beat)
		select {
		case accepted <- beat:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cpu := 12.0
	var reads atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Namespace:   "pds",
			Interval:    time.Millisecond,
			Databases:   dbs,
			Collect: func(_ context.Context, _ string, items []engine.Instance) ([]hub.HeartbeatEvent, []hub.HeartbeatUsage) {
				return nil, []hub.HeartbeatUsage{{DatabaseID: items[0].ID, At: time.Now().UTC()}}
			},
			Monitor: func(context.Context, engine.Instance) *hub.HeartbeatMonitoring {
				reads.Add(1)

				return &hub.HeartbeatMonitoring{CPU: &cpu}
			},
		})
	}()

	select {
	case beat := <-accepted:
		require.Len(t, beat.Usage, 1)
		require.NotNil(t, beat.Usage[0].Monitoring)
		assert.InDelta(t, cpu, *beat.Usage[0].Monitoring.CPU, 0.001)
	case <-time.After(20 * time.Second):
		t.Fatal("the hub never accepted a beat")
	}
	assert.Equal(t, int32(1), reads.Load(), "four beats went out and PMM was read once")
}

// TestBeatNamesOnlyAMonitoringConfigThatExists keeps the hub from writing
// manifests that point the pmm-client at an object the environment does not have
// yet, and checks the object at most once a minute.
func TestBeatNamesOnlyAMonitoringConfigThatExists(t *testing.T) {
	beats := make(chan hub.Heartbeat, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var beat hub.Heartbeat
		_ = json.Unmarshal(body, &beat)
		select {
		case beats <- beat:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var exists atomic.Bool
	var checks atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = agent.Run(ctx, agent.Options{
			HubURL:           srv.URL,
			Environment:      "spoke-a",
			Namespace:        "pds",
			Interval:         5 * time.Millisecond,
			MonitoringConfig: "pmm",
			MonitoringExists: func(context.Context) (bool, error) {
				checks.Add(1)

				return exists.Load(), nil
			},
		})
	}()

	for range 3 {
		beat := <-beats
		assert.Empty(t, beat.MonitoringConfig, "the config does not exist yet")
	}
	assert.Equal(t, int32(1), checks.Load(), "a missing config is looked up once a minute, not every beat")
}

func TestBeatNamesTheMonitoringConfigOnceItExists(t *testing.T) {
	beats := make(chan hub.Heartbeat, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var beat hub.Heartbeat
		_ = json.Unmarshal(body, &beat)
		select {
		case beats <- beat:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = agent.Run(ctx, agent.Options{
			HubURL:           srv.URL,
			Environment:      "spoke-a",
			Namespace:        "pds",
			Interval:         time.Hour,
			MonitoringConfig: "pmm",
			MonitoringExists: func(context.Context) (bool, error) { return true, nil },
		})
	}()

	assert.Equal(t, "pmm", (<-beats).MonitoringConfig)
}
