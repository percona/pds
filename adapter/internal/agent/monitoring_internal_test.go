package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

// A check that could not reach the database platform is no news. Only a
// confirmed miss stops naming the config, so a database created during an
// outage still gets the pmm-client.
func TestReportedMonitoringKeepsTheLastAnswerThroughAFailedCheck(t *testing.T) {
	cases := map[string]struct {
		exists bool
		err    error
		want   string
	}{
		"the platform is down": {err: errors.New("everest: 503"), want: "pmm"},
		"the config is gone":   {exists: false, want: ""},
		"the config is there":  {exists: true, want: "pmm"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
			calls := 0
			opts := Options{
				MonitoringConfig: "pmm",
				monitoring:       &monitoringSeen{},
				MonitoringExists: func(context.Context) (bool, error) {
					calls++
					if calls == 1 {
						return true, nil
					}

					return tc.exists, tc.err
				},
			}

			assert.Equal(t, "pmm", reportedMonitoring(context.Background(), opts, now))
			assert.Equal(t, tc.want, reportedMonitoring(context.Background(), opts, now.Add(2*monitoringRecheck)))
		})
	}
}

// A first check that fails reports nothing: there is no confirmed answer yet.
func TestReportedMonitoringNamesNothingBeforeAConfirmation(t *testing.T) {
	opts := Options{
		MonitoringConfig: "pmm",
		monitoring:       &monitoringSeen{},
		MonitoringExists: func(context.Context) (bool, error) { return false, errors.New("everest: timeout") },
	}

	assert.Empty(t, reportedMonitoring(context.Background(), opts, time.Now()))
}

// Reads run side by side, so a slow database early in the list does not use
// up the budget of the ones after it. Serially, three 60ms reads do not fit a
// 150ms budget and the last would go without a reading on every pass.
func TestMonitorUsageReadsDatabasesSideBySide(t *testing.T) {
	items := []engine.Instance{{ID: "a", Name: "a"}, {ID: "b", Name: "b"}, {ID: "c", Name: "c"}}
	usage := []hub.HeartbeatUsage{{DatabaseID: "a"}, {DatabaseID: "b"}, {DatabaseID: "c"}}
	opts := Options{
		MonitorTimeout: 150 * time.Millisecond,
		lastMonitor:    &map[string]monitorReading{},
		Monitor: func(ctx context.Context, item engine.Instance) *hub.HeartbeatMonitoring {
			select {
			case <-time.After(60 * time.Millisecond):
				return &hub.HeartbeatMonitoring{Pending: "read-" + item.Name}
			case <-ctx.Done():
				return &hub.HeartbeatMonitoring{Pending: "unavailable"}
			}
		},
	}

	monitorUsage(context.Background(), opts, items, usage, time.Now())

	for _, point := range usage {
		require.NotNil(t, point.Monitoring, point.DatabaseID)
		assert.Equal(t, "read-"+point.DatabaseID, point.Monitoring.Pending)
	}
	assert.Len(t, *opts.lastMonitor, 3)
}

// The workers never read more than monitorWorkers databases at once.
func TestMonitorUsageBoundsTheReadsInFlight(t *testing.T) {
	var items []engine.Instance
	var usage []hub.HeartbeatUsage
	for i := range 3 * monitorWorkers {
		id := string(rune('a' + i))
		items = append(items, engine.Instance{ID: id, Name: id})
		usage = append(usage, hub.HeartbeatUsage{DatabaseID: id})
	}
	var inFlight, peak atomic.Int32
	opts := Options{
		MonitorTimeout: time.Second,
		Monitor: func(context.Context, engine.Instance) *hub.HeartbeatMonitoring {
			now := inFlight.Add(1)
			for {
				old := peak.Load()
				if now <= old || peak.CompareAndSwap(old, now) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			inFlight.Add(-1)

			return &hub.HeartbeatMonitoring{}
		},
	}

	monitorUsage(context.Background(), opts, items, usage, time.Now())

	assert.LessOrEqual(t, peak.Load(), int32(monitorWorkers))
	for _, point := range usage {
		assert.NotNil(t, point.Monitoring, point.DatabaseID)
	}
}
