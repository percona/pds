package agent_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/agent"
)

func recordingHub(t *testing.T) (*httptest.Server, chan string) {
	t.Helper()

	bodies := make(chan string, 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies <- string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"work":[]}`))
	}))
	t.Cleanup(srv.Close)

	return srv, bodies
}

func runWithAddress(t *testing.T, srv *httptest.Server, read func(context.Context) (string, error)) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = agent.Run(ctx, agent.Options{
			HubURL:            srv.URL,
			Environment:       "spoke-a",
			Namespace:         "pds",
			Interval:          10 * time.Millisecond,
			Databases:         &stubDatabases{},
			MonitoringAddress: read,
		})
	}()
}

func TestBeatReportsTheMonitoringAddress(t *testing.T) {
	srv, bodies := recordingHub(t)
	runWithAddress(t, srv, func(context.Context) (string, error) { return "pmm.elb.example.com", nil })

	assert.Contains(t, waitForBody(t, bodies), `"monitoringAddress":"pmm.elb.example.com"`)
}

// Monitoring that is not exposed has no address, and the field is left out
// rather than sent empty.
func TestBeatLeavesOutAnAddressThereIsNone(t *testing.T) {
	srv, bodies := recordingHub(t)
	runWithAddress(t, srv, func(context.Context) (string, error) { return "", nil })

	assert.NotContains(t, waitForBody(t, bodies), "monitoringAddress")
}

func TestBeatWithoutAnAddressReaderSendsNone(t *testing.T) {
	srv, bodies := recordingHub(t)
	runWithAddress(t, srv, nil)

	assert.NotContains(t, waitForBody(t, bodies), "monitoringAddress")
}

// One failed read of the Service must not turn a working address back into
// "pending" for everyone looking at the panel.
func TestBeatKeepsTheLastAddressWhenTheReadFails(t *testing.T) {
	srv, bodies := recordingHub(t)
	var calls atomic.Int32
	runWithAddress(t, srv, func(context.Context) (string, error) {
		if calls.Add(1) == 1 {
			return "pmm.elb.example.com", nil
		}

		return "", errors.New("the API server is busy")
	})

	require.Contains(t, waitForBody(t, bodies), `"monitoringAddress":"pmm.elb.example.com"`)
	for range 3 {
		assert.Contains(t, waitForBody(t, bodies), `"monitoringAddress":"pmm.elb.example.com"`)
	}
}

// An empty answer is not a failure: the load balancer was removed, and the
// hub should stop showing an address that no longer exists.
func TestBeatDropsTheAddressOnceTheServiceHasNone(t *testing.T) {
	srv, bodies := recordingHub(t)
	var calls atomic.Int32
	runWithAddress(t, srv, func(context.Context) (string, error) {
		if calls.Add(1) == 1 {
			return "pmm.elb.example.com", nil
		}

		return "", nil
	})

	waitForBody(t, bodies)
	var last string
	for range 3 {
		last = waitForBody(t, bodies)
	}
	assert.False(t, strings.Contains(last, "monitoringAddress"), last)
}
