package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/agent"
	"github.com/percona/pds/adapter/internal/hub"
)

// helmRelease is the part of a Flux HelmRelease the agent reads.
func helmRelease(generation, observed int64, attempted string, history []map[string]any, conditions ...map[string]any) map[string]any {
	items := make([]any, 0, len(history))
	for _, entry := range history {
		items = append(items, entry)
	}
	conds := make([]any, 0, len(conditions))
	for _, c := range conditions {
		conds = append(conds, c)
	}
	status := map[string]any{"observedGeneration": observed, "history": items, "conditions": conds}
	if attempted != "" {
		status["lastAttemptedRevision"] = attempted
	}

	return map[string]any{"metadata": map[string]any{"generation": generation}, "status": status}
}

func deployed(version string) map[string]any {
	return map[string]any{"chartVersion": version, "status": "deployed"}
}

func ready(status string) map[string]any {
	return map[string]any{"type": "Ready", "status": status}
}

func readRelease(t *testing.T, object map[string]any) *hub.HeartbeatRelease {
	t.Helper()

	got, err := agent.FluxRelease(func(context.Context) (map[string]any, error) { return object, nil })(t.Context())
	require.NoError(t, err)

	return got
}

func TestAReleaseThatInstalledIsReportedReadyAtItsVersion(t *testing.T) {
	got := readRelease(t, helmRelease(1, 1, "0.1.0", []map[string]any{deployed("0.1.0")}, ready("True")))

	assert.Equal(t, &hub.HeartbeatRelease{Version: "0.1.0", State: hub.ReleaseReady}, got)
}

func TestAReleaseMidUpgradeIsReportedUpgradingAtTheVersionItStillRuns(t *testing.T) {
	got := readRelease(t, helmRelease(2, 2, "0.2.0",
		[]map[string]any{{"chartVersion": "0.2.0", "status": "pending-upgrade"}, {"chartVersion": "0.1.0", "status": "superseded"}},
		ready("Unknown")))

	assert.Equal(t, &hub.HeartbeatRelease{Version: "", Attempted: "0.2.0", State: hub.ReleaseUpgrading}, got,
		"no revision is deployed while the upgrade runs, and the report says so")
}

func TestAReleaseNewerThanWhatFluxLastLookedAtIsUpgradingNotReady(t *testing.T) {
	got := readRelease(t, helmRelease(3, 2, "0.1.0", []map[string]any{deployed("0.1.0")}, ready("True")))

	assert.Equal(t, &hub.HeartbeatRelease{Version: "0.1.0", State: hub.ReleaseUpgrading}, got)
}

func TestAnUpgradedReleaseIsReportedReadyAtTheNewVersion(t *testing.T) {
	got := readRelease(t, helmRelease(2, 2, "0.2.0",
		[]map[string]any{deployed("0.2.0"), {"chartVersion": "0.1.0", "status": "superseded"}}, ready("True")))

	assert.Equal(t, &hub.HeartbeatRelease{Version: "0.2.0", State: hub.ReleaseReady}, got)
}

func TestAFailedUpgradeFluxRolledBackIsReportedWithTheVersionThatFailed(t *testing.T) {
	got := readRelease(t, helmRelease(2, 2, "0.2.0",
		[]map[string]any{deployed("0.1.0"), {"chartVersion": "0.2.0", "status": "failed"}, {"chartVersion": "0.1.0", "status": "superseded"}},
		ready("False"), map[string]any{"type": "Remediated", "status": "True", "reason": "RollbackSucceeded"}))

	assert.Equal(t, &hub.HeartbeatRelease{Version: "0.1.0", Attempted: "0.2.0", State: hub.ReleaseRolledBack}, got)
}

func TestAReleaseThatHasNotInstalledYetIsNotReported(t *testing.T) {
	assert.Nil(t, readRelease(t, helmRelease(1, 0, "", nil)))
	assert.Nil(t, readRelease(t, nil), "no HelmRelease is no report, as on a connected environment")
}

func TestAReleaseThatCannotBeReadIsAnErrorNotAReport(t *testing.T) {
	got, err := agent.FluxRelease(func(context.Context) (map[string]any, error) { return nil, errors.New("forbidden") })(t.Context())

	require.Error(t, err)
	assert.Nil(t, got)
}

func TestTheBeatCarriesTheReleaseTheAgentReads(t *testing.T) {
	bodies := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case bodies <- body:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		_ = agent.Run(ctx, agent.Options{
			HubURL: srv.URL, Token: "secret", Environment: "spoke-a", Namespace: "pds", Interval: time.Hour,
			Release: func(context.Context) (*hub.HeartbeatRelease, error) {
				return &hub.HeartbeatRelease{Version: "0.2.0", State: hub.ReleaseReady}, nil
			},
		})
	}()

	var sent map[string]any
	require.NoError(t, json.Unmarshal([]byte(waitForBody(t, toStrings(bodies))), &sent))
	assert.Equal(t, map[string]any{"version": "0.2.0", "state": "ready"}, sent["release"])
}

func TestTheBeatStillGoesOutWhenTheReleaseCannotBeRead(t *testing.T) {
	bodies := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case bodies <- body:
		default:
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		_ = agent.Run(ctx, agent.Options{
			HubURL: srv.URL, Token: "secret", Environment: "spoke-a", Namespace: "pds", Interval: time.Hour,
			Release: func(context.Context) (*hub.HeartbeatRelease, error) { return nil, errors.New("forbidden") },
		})
	}()

	var sent map[string]any
	require.NoError(t, json.Unmarshal([]byte(waitForBody(t, toStrings(bodies))), &sent))
	assert.NotContains(t, sent, "release", "a failed read says nothing, so the hub keeps what it had")
}

func toStrings(in chan []byte) chan string {
	out := make(chan string, 1)
	go func() { out <- string(<-in) }()

	return out
}

func TestTheBeatKeepsTheBackupKeyAndGoesOutEvenWhenItFails(t *testing.T) {
	var calls atomic.Int32
	bodies := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case bodies <- body:
		default:
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		_ = agent.Run(ctx, agent.Options{
			HubURL: srv.URL, Token: "secret", Environment: "spoke-a", Namespace: "pds", Interval: time.Hour,
			BackupKey: func(context.Context) error { calls.Add(1); return errors.New("AccessDenied") },
		})
	}()

	waitForBody(t, toStrings(bodies))
	assert.EqualValues(t, 1, calls.Load(), "the beat reconciles the key once")
}
