package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
)

const hubOrdersMetrics = "/api/databases/spoke-a/pds.orders-dev/metrics"

// beatHubWithUsage sends one heartbeat listing orders-dev and carrying the
// given usage samples, the way the environment agent posts them.
func beatHubWithUsage(t *testing.T, srv http.Handler, token string, usage []map[string]any) {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"environment": "spoke-a",
		"namespace":   "pds",
		"at":          time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		"instances":   []map[string]any{reportedOrdersDev(nil)},
		"usage":       usage,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

type hubMetricsBody struct {
	DatabaseID    string `json:"databaseId"`
	Window        string `json:"window"`
	StepSeconds   int    `json:"stepSeconds"`
	Pending       bool   `json:"pending"`
	PendingReason string `json:"pendingReason"`
	Metrics       []struct {
		Kind    string  `json:"kind"`
		Pending bool    `json:"pending"`
		Current float64 `json:"current"`
		Points  []struct {
			At    time.Time `json:"t"`
			Value float64   `json:"v"`
		} `json:"points"`
	} `json:"metrics"`
}

func getHubMetrics(t *testing.T, srv http.Handler, path string) hubMetricsBody {
	t.Helper()

	rec := do(t, srv, http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body hubMetricsBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	return body
}

func TestHubChartsTheReadingsTheAgentPosted(t *testing.T) {
	book := service.NewMemoryBook(fixedTeam(1))
	srv := newHubTestServerWithBook(t, "secret", book)
	now := time.Now().UTC()

	beatHubWithUsage(t, srv, "secret", []map[string]any{{
		"databaseId": "spoke-a/pds.orders-dev",
		"at":         now.Add(-10 * time.Minute),
		"monitoring": map[string]any{"cpu": 12.5, "memory": 40, "connections": 3, "replicationLag": 0, "disk": 20},
	}})
	beatHubWithUsage(t, srv, "secret", []map[string]any{{
		"databaseId": "spoke-a/pds.orders-dev",
		"at":         now.Add(-time.Minute),
		"monitoring": map[string]any{"cpu": 30, "memory": 41, "connections": 5, "replicationLag": 0.5, "disk": 21},
	}})

	body := getHubMetrics(t, srv, hubOrdersMetrics)
	assert.Equal(t, "spoke-a/pds.orders-dev", body.DatabaseID)
	assert.Equal(t, "1h", body.Window)
	assert.Equal(t, 300, body.StepSeconds)
	assert.False(t, body.Pending, body.PendingReason)
	require.Len(t, body.Metrics, 5)
	kinds := make([]string, 0, len(body.Metrics))
	for _, metric := range body.Metrics {
		kinds = append(kinds, metric.Kind)
		assert.False(t, metric.Pending)
		assert.Len(t, metric.Points, 2, metric.Kind)
	}
	assert.Equal(t, []string{"cpu", "memory", "connections", "replication-lag", "disk"}, kinds)
	assert.InDelta(t, 30, body.Metrics[0].Current, 0.001)
	assert.InDelta(t, 5, body.Metrics[2].Current, 0.001)

	day := getHubMetrics(t, srv, hubOrdersMetrics+"?window=24h&metrics=cpu")
	assert.Equal(t, "24h", day.Window)
	require.Len(t, day.Metrics, 1)
	assert.Equal(t, "cpu", day.Metrics[0].Kind)
}

func TestHubMetricsArePending(t *testing.T) {
	cases := map[string]struct {
		usage []map[string]any
		want  string
	}{
		"no samples yet": {
			want: "Monitoring has no readings for this database yet.",
		},
		"the environment has no monitoring": {
			usage: []map[string]any{{"databaseId": "spoke-a/pds.orders-dev", "at": time.Now().UTC()}},
			want:  "Monitoring is not set up in this environment yet.",
		},
		"monitoring is not up": {
			usage: []map[string]any{{
				"databaseId": "spoke-a/pds.orders-dev", "at": time.Now().UTC(),
				"monitoring": map[string]any{"pending": service.MonitoringUnavailable},
			}},
			want: "Monitoring is not serving readings in this environment yet.",
		},
		"no target": {
			usage: []map[string]any{{
				"databaseId": "spoke-a/pds.orders-dev", "at": time.Now().UTC(),
				"monitoring": map[string]any{"pending": service.MonitoringNoTarget},
			}},
			want: "Monitoring has no target for this database yet.",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := newHubTestServerWithBook(t, "secret", service.NewMemoryBook(fixedTeam(1)))
			beatHubWithUsage(t, srv, "secret", tc.usage)

			body := getHubMetrics(t, srv, hubOrdersMetrics)
			assert.True(t, body.Pending)
			assert.Equal(t, tc.want, body.PendingReason)
			require.Len(t, body.Metrics, 5)
			for _, metric := range body.Metrics {
				assert.True(t, metric.Pending)
				assert.Empty(t, metric.Points)
			}
		})
	}
}

func TestHubMetricsOfAnUnknownDatabaseAreNotFound(t *testing.T) {
	srv := newHubTestServerWithBook(t, "secret", service.NewMemoryBook(fixedTeam(1)))
	beatHubWithUsage(t, srv, "secret", nil)

	rec := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.missing/metrics", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHeartbeatPrunesSamplesPastTheRetention(t *testing.T) {
	book := service.NewMemoryBook(fixedTeam(1))
	ctx := context.Background()
	old := time.Now().UTC().Add(-service.UsageRetention - time.Hour)
	kept, err := book.RecordUsage(ctx, service.UsagePoint{DatabaseID: "spoke-a/pds.orders-dev", At: old})
	require.NoError(t, err)
	require.True(t, kept)

	srv := newHubTestServerWithBook(t, "secret", book)
	beatHubWithUsage(t, srv, "secret", []map[string]any{{
		"databaseId": "spoke-a/pds.orders-dev", "at": time.Now().UTC(),
		"monitoring": map[string]any{"cpu": 10},
	}})

	var samples []service.UsagePoint
	require.Eventually(t, func() bool {
		samples, err = book.ListUsage(ctx, service.UsageQuery{DatabaseID: "spoke-a/pds.orders-dev"})

		return err == nil && len(samples) == 1
	}, 2*time.Second, 10*time.Millisecond, "the week old sample is gone and the new one stayed")
	require.NotNil(t, samples[0].Monitoring)
	assert.InDelta(t, 10, *samples[0].Monitoring.CPU, 0.001)
}

// TestHeartbeatPrunesAtMostOncePerHour keeps a busy hub from deleting on every
// beat: a sample that ages out between two close beats stays until the next
// hourly prune.
func TestHeartbeatPrunesAtMostOncePerHour(t *testing.T) {
	book := service.NewMemoryBook(fixedTeam(1))
	ctx := context.Background()
	old := time.Now().UTC().Add(-service.UsageRetention - time.Hour)
	listOld := func(id string) []service.UsagePoint {
		samples, err := book.ListUsage(ctx, service.UsageQuery{DatabaseID: id})
		require.NoError(t, err)

		return samples
	}

	// The first beat's prune runs beside it. A sentinel it has to delete says
	// when it has finished, so nothing below races it.
	_, err := book.RecordUsage(ctx, service.UsagePoint{DatabaseID: "spoke-a/pds.sentinel", At: old})
	require.NoError(t, err)
	srv := newHubTestServerWithBook(t, "secret", book)
	beatHubWithUsage(t, srv, "secret", nil)
	require.Eventually(t, func() bool { return len(listOld("spoke-a/pds.sentinel")) == 0 },
		2*time.Second, 5*time.Millisecond, "the first beat pruned")

	_, err = book.RecordUsage(ctx, service.UsagePoint{DatabaseID: "spoke-a/pds.orders-dev", At: old})
	require.NoError(t, err)
	beatHubWithUsage(t, srv, "secret", nil)

	assert.Never(t, func() bool { return len(listOld("spoke-a/pds.orders-dev")) == 0 },
		200*time.Millisecond, 10*time.Millisecond, "the second beat inside the hour did not prune")
}

// TestHubMetricsFailWhenTheHubCannotBeBuilt keeps a hub that failed to load
// from answering with a pending chart read from a PMM it does not have.
func TestHubMetricsFailWhenTheHubCannotBeBuilt(t *testing.T) {
	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: time.Now().UTC(),
		Instances: []engine.Instance{{
			ID: "spoke-a/pds.orders-dev", Name: "orders-dev", Namespace: "pds", Environment: "spoke-a",
			Engine: "mysql", Status: engine.Status{Phase: engine.PhaseReady},
		}},
	})
	hubSvc := hub.NewService(hub.NewMemoryStore(), inv, hub.Options{Namespace: "pds"})
	var calls atomic.Int32
	databases := service.NewDatabase(engine.NewRegistry(), service.WithHubFor(func(context.Context) (*hub.Service, error) {
		// The lookup finds the database; the hub then fails to load.
		if calls.Add(1) == 1 {
			return hubSvc, nil
		}

		return nil, errors.New("hydrate team hub: store is down")
	}))
	srv := api.NewServer(api.Deps{
		Config:    testConfig(),
		Logger:    log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:      auth.NewStub(),
		Databases: databases,
	})

	rec := do(t, srv, http.MethodGet, hubOrdersMetrics, nil)
	assert.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "store is down")
}
