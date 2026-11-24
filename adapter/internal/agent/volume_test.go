package agent_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/agent"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

// TestBeatDoesNotReportReadyWhileTheVolumeCannotGrow covers a PostgreSQL
// database whose storage class refuses expansion: the operator records a
// warning and carries on, and the provider reports Ready at the size the spec
// asks for. The beat must say the size is not in place.
func TestBeatDoesNotReportReadyWhileTheVolumeCannotGrow(t *testing.T) {
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
	go func() {
		_ = agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Namespace:   "pds",
			Interval:    time.Hour,
			Databases:   dbs,
			Collect: func(_ context.Context, _ string, items []engine.Instance) ([]hub.HeartbeatEvent, []hub.HeartbeatUsage) {
				return []hub.HeartbeatEvent{{
					DatabaseID: items[0].ID, At: time.Now().UTC(), Type: "Warning",
					Reason:  "PersistentVolumeError",
					Message: "persistent volume resizing requires a storage class that supports resize",
				}}, nil
			},
		})
	}()

	beat := <-beats
	require.Len(t, beat.Instances, 2)
	assert.Equal(t, engine.PhaseUpdating, beat.Instances[0].Status.Phase)
	assert.Contains(t, beat.Instances[0].Status.Message, "storage class")
	assert.Equal(t, engine.PhaseReady, beat.Instances[1].Status.Phase)
}

// TestBeatReportsTheVolumeSizeTheOperatorHolds covers a MongoDB database whose
// storage class refuses expansion: the Instance spec keeps the requested size
// and stays Ready, so the beat carries the size read from the operator instead.
func TestBeatReportsTheVolumeSizeTheOperatorHolds(t *testing.T) {
	dbs := &listStub{instances: []engine.Instance{
		{Name: "orders", Namespace: "pds", Engine: "mongodb", Sizing: engine.Sizing{StorageGB: 10}, Status: engine.Status{Phase: engine.PhaseReady}},
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
	go func() {
		_ = agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Namespace:   "pds",
			Interval:    time.Hour,
			Databases:   dbs,
			Volumes: func(_ context.Context, items []engine.Instance) {
				items[0].Sizing.StorageGB = 5
			},
		})
	}()

	beat := <-beats
	require.Len(t, beat.Instances, 1)
	assert.Equal(t, 5.0, beat.Instances[0].Sizing.StorageGB)
	assert.Equal(t, engine.PhaseReady, beat.Instances[0].Status.Phase)
}
