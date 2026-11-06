package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/agent"
	"github.com/percona/pds/adapter/internal/client/pmm"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/service"
)

// fakeAlertmanager answers Grafana's Alertmanager route and remembers what it
// was asked.
type fakeAlertmanager struct {
	mu     sync.Mutex
	status int
	body   string
	paths  []string
	query  url.Values
	auth   string
}

func (f *fakeAlertmanager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.query = r.URL.Query()
	f.auth = r.Header.Get("Authorization")
	status, body := f.status, f.body
	f.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func newAlertSource(t *testing.T, f *fakeAlertmanager) func(context.Context) (pmm.AlertList, error) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	client, err := pmm.New(pmm.Options{BaseURL: srv.URL, Token: "glsa_token"})
	require.NoError(t, err)

	return client.Alerts
}

// firstBeat runs the agent against a fake hub and returns the raw body of the
// first heartbeat it posts.
func firstBeat(t *testing.T, opts agent.Options) []byte {
	t.Helper()
	bodies := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		select {
		case bodies <- body:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts.HubURL = srv.URL
	opts.Environment = "spoke-a"
	opts.Namespace = "pds"
	opts.Interval = time.Hour
	go func() { _ = agent.Run(ctx, opts) }()

	select {
	case body := <-bodies:
		return body
	case <-time.After(5 * time.Second):
		t.Fatal("the agent posted no heartbeat")
		return nil
	}
}

func decodeAlerts(t *testing.T, body []byte) *hub.HeartbeatAlerts {
	t.Helper()
	var beat hub.Heartbeat
	require.NoError(t, json.Unmarshal(body, &beat))

	return beat.Alerts
}

func alertJSON(fingerprint string, labels map[string]string, annotations map[string]string, startsAt time.Time) string {
	l, _ := json.Marshal(labels)
	a, _ := json.Marshal(annotations)

	return fmt.Sprintf(`{"fingerprint":%q,"labels":%s,"annotations":%s,"startsAt":%q,"status":{"state":"active"}}`,
		fingerprint, l, a, startsAt.Format(time.RFC3339))
}

func TestBeatCarriesFiringAlertsWithMappedSeverityAndAllowedLabels(t *testing.T) {
	started := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	f := &fakeAlertmanager{body: "[" + strings.Join([]string{
		alertJSON("a1", map[string]string{
			"alertname": "PDS service down", "pds_rule": "service_down", "severity": "error",
			"cluster": "orders", "namespace": "pds", "node_name": "ip-10-0-0-1",
			"service_name": "orders-mysql", "instance": "10.0.0.5:9104",
			"grafana_folder": "PDS", "password": "hunter2",
		}, map[string]string{"summary": "orders is down"}, started),
		alertJSON("a2", map[string]string{"alertname": "Custom disk"}, nil, started),
	}, ",") + "]"}

	body := firstBeat(t, agent.Options{Alerts: newAlertSource(t, f)})
	alerts := decodeAlerts(t, body)

	require.NotNil(t, alerts)
	assert.Empty(t, alerts.Pending)
	assert.Equal(t, 2, alerts.Total)
	assert.False(t, alerts.At.IsZero())
	require.Len(t, alerts.Items, 2)

	first := alerts.Items[0]
	assert.Equal(t, "a1", first.Fingerprint)
	assert.Equal(t, "service_down", first.Rule)
	assert.Equal(t, hub.AlertCritical, first.Severity, "error is critical")
	assert.Equal(t, "orders is down", first.Summary)
	assert.True(t, first.StartsAt.Equal(started))
	assert.Equal(t, map[string]string{
		"alertname": "PDS service down", "pds_rule": "service_down", "severity": "error",
		"cluster": "orders", "namespace": "pds", "node_name": "ip-10-0-0-1",
		"service_name": "orders-mysql", "instance": "10.0.0.5:9104",
	}, first.Labels, "only allow-listed labels leave the environment")
	assert.NotContains(t, string(body), "hunter2")

	second := alerts.Items[1]
	assert.Equal(t, "Custom disk", second.Rule, "a rule without pds_rule is named by its alert name")
	assert.Equal(t, "Custom disk", second.Summary, "no summary annotation falls back to the alert name")
	assert.Equal(t, hub.AlertWarning, second.Severity, "a missing severity is a warning")
}

func TestSeverityMapping(t *testing.T) {
	tests := map[string]string{
		"critical": hub.AlertCritical, "error": hub.AlertCritical, "CRITICAL": hub.AlertCritical,
		"warning": hub.AlertWarning, "info": hub.AlertInfo, "notice": hub.AlertInfo,
		"page": hub.AlertWarning, "": hub.AlertWarning,
	}
	for label, want := range tests {
		t.Run("label="+label, func(t *testing.T) {
			f := &fakeAlertmanager{body: "[" + alertJSON("x", map[string]string{"alertname": "A", "severity": label}, nil, time.Now()) + "]"}
			alerts := decodeAlerts(t, firstBeat(t, agent.Options{Alerts: newAlertSource(t, f)}))
			require.NotNil(t, alerts)
			require.Len(t, alerts.Items, 1)
			assert.Equal(t, want, alerts.Items[0].Severity)
		})
	}
}

func TestBeatCapsAlertsAtTwoHundredCriticalsFirstThenLongestFiring(t *testing.T) {
	base := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	var items []string
	// 230 warnings that started in order, then 20 criticals that started later
	// than every warning: severity ranks before age.
	for i := range 230 {
		items = append(items, alertJSON(fmt.Sprintf("w%03d", i),
			map[string]string{"alertname": "W", "severity": "warning"}, nil, base.Add(time.Duration(i)*time.Minute)))
	}
	for i := range 20 {
		items = append(items, alertJSON(fmt.Sprintf("c%03d", i),
			map[string]string{"alertname": "C", "severity": "critical"}, nil, base.Add(time.Duration(1000+i)*time.Minute)))
	}
	f := &fakeAlertmanager{body: "[" + strings.Join(items, ",") + "]"}

	alerts := decodeAlerts(t, firstBeat(t, agent.Options{Alerts: newAlertSource(t, f)}))

	require.NotNil(t, alerts)
	assert.Equal(t, 250, alerts.Total, "the total says what was cut")
	require.Len(t, alerts.Items, 200)
	for i := range 20 {
		assert.Equal(t, fmt.Sprintf("c%03d", i), alerts.Items[i].Fingerprint, "criticals come first, longest firing first")
	}
	assert.Equal(t, "w000", alerts.Items[20].Fingerprint, "then warnings, longest firing first")
	assert.Equal(t, "w179", alerts.Items[199].Fingerprint)
}

func TestAlertsStayInsideTheHeartbeatBodyLimit(t *testing.T) {
	// JSON writes & and < as six bytes each, so the same number of
	// characters weighs six times as much on the wire.
	for name, filler := range map[string]string{
		"plain":   strings.Repeat("x", 1500),
		"escaped": strings.Repeat("&<", 750),
	} {
		t.Run(name, func(t *testing.T) {
			var items []string
			for i := range 250 {
				items = append(items, alertJSON(fmt.Sprintf("f%03d", i), map[string]string{
					"alertname": filler, "pds_rule": filler, "severity": "critical", "cluster": filler, "namespace": filler,
					"node_name": filler, "service_name": filler, "instance": filler,
				}, map[string]string{"summary": filler}, time.Now()))
			}
			f := &fakeAlertmanager{body: "[" + strings.Join(items, ",") + "]"}

			body := firstBeat(t, agent.Options{Alerts: newAlertSource(t, f)})

			assert.Less(t, len(body), 700<<10, "the heartbeat handler refuses a body of 1 MiB, and the rest of the beat needs room")
			alerts := decodeAlerts(t, body)
			require.NotNil(t, alerts)
			assert.Equal(t, 250, alerts.Total, "the total says alerts were cut")
			assert.NotEmpty(t, alerts.Items)
			assert.Less(t, len(alerts.Items), 200)
		})
	}
}

func TestAlertsQueryAsksForActiveUnsilencedUninhibitedOnly(t *testing.T) {
	f := &fakeAlertmanager{body: "[]"}

	firstBeat(t, agent.Options{Alerts: newAlertSource(t, f)})

	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, []string{"/graph/api/alertmanager/grafana/api/v2/alerts"}, f.paths)
	assert.Equal(t, "true", f.query.Get("active"))
	assert.Equal(t, "false", f.query.Get("silenced"))
	assert.Equal(t, "false", f.query.Get("inhibited"))
	assert.Equal(t, "Bearer glsa_token", f.auth, "the agent reads with its existing PMM token")
}

func TestNoAlertsIsAnEmptyListNotAnAbsentField(t *testing.T) {
	f := &fakeAlertmanager{body: "[]"}

	body := firstBeat(t, agent.Options{Alerts: newAlertSource(t, f)})

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Contains(t, raw, "alerts")
	assert.JSONEq(t, `[]`, string(decodeRawField(t, raw["alerts"])["items"]))
	alerts := decodeAlerts(t, body)
	assert.Equal(t, 0, alerts.Total)
	assert.Empty(t, alerts.Pending)
}

func decodeRawField(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var out map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &out))

	return out
}

func TestAnOldAgentBeatHasNoAlertsField(t *testing.T) {
	var beat hub.Heartbeat
	require.NoError(t, json.Unmarshal([]byte(`{"environment":"spoke-a","namespace":"pds"}`), &beat))
	assert.Nil(t, beat.Alerts, "a beat without the field must read differently from an empty list")

	raw, err := json.Marshal(hub.Heartbeat{Environment: "spoke-a"})
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "alerts")
}

func TestUnreachablePMMSetsAPendingReasonAndTheBeatStillPosts(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	client, err := pmm.New(pmm.Options{BaseURL: srv.URL})
	require.NoError(t, err)
	srv.Close()

	body := firstBeat(t, agent.Options{Alerts: client.Alerts})

	alerts := decodeAlerts(t, body)
	require.NotNil(t, alerts)
	assert.Equal(t, service.MonitoringUnreachable, alerts.Pending)
	assert.Empty(t, alerts.Items)
	assert.Zero(t, alerts.Total)
}

func TestPMMErrorsSetAPendingReason(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{"not serving yet", http.StatusServiceUnavailable, service.MonitoringUnavailable},
		{"token refused", http.StatusUnauthorized, service.MonitoringUnavailable},
		{"unexpected failure", http.StatusInternalServerError, service.MonitoringUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeAlertmanager{status: tt.status, body: `{"message":"no"}`}
			alerts := decodeAlerts(t, firstBeat(t, agent.Options{Alerts: newAlertSource(t, f)}))
			require.NotNil(t, alerts)
			assert.Equal(t, tt.want, alerts.Pending)
			assert.Empty(t, alerts.Items)
		})
	}
}

func TestNoMonitoringSetsItsOwnPendingReason(t *testing.T) {
	alerts := decodeAlerts(t, firstBeat(t, agent.Options{}))

	require.NotNil(t, alerts)
	assert.Equal(t, service.MonitoringNotConfigured, alerts.Pending)
	assert.Empty(t, alerts.Items)
}

func TestAHangingPMMDelaysTheBeatByTheMonitoringBudgetAtMost(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	client, err := pmm.New(pmm.Options{BaseURL: srv.URL})
	require.NoError(t, err)

	start := time.Now()
	body := firstBeat(t, agent.Options{Alerts: client.Alerts, MonitorTimeout: 100 * time.Millisecond})

	assert.Less(t, time.Since(start), 3*time.Second)
	alerts := decodeAlerts(t, body)
	require.NotNil(t, alerts)
	assert.Equal(t, service.MonitoringUnreachable, alerts.Pending)
}

func TestACriticalLateInAHugeListStillLeadsTheBeat(t *testing.T) {
	base := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	var items []string
	for i := range 2500 {
		if i == 2300 {
			items = append(items, alertJSON("crit", map[string]string{"alertname": "C", "severity": "critical"}, nil, base.Add(time.Hour)))
		}
		items = append(items, alertJSON(fmt.Sprintf("w%04d", i),
			map[string]string{"alertname": "W", "severity": "warning"}, nil, base.Add(time.Duration(i)*time.Second)))
	}
	f := &fakeAlertmanager{body: "[" + strings.Join(items, ",") + "]"}

	alerts := decodeAlerts(t, firstBeat(t, agent.Options{Alerts: newAlertSource(t, f)}))

	require.NotNil(t, alerts)
	assert.Equal(t, 2501, alerts.Total)
	require.NotEmpty(t, alerts.Items)
	assert.Equal(t, "crit", alerts.Items[0].Fingerprint)
}
