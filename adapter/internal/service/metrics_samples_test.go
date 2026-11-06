package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/service"
)

func num(value float64) *float64 { return &value }

func TestLatestReadsTheNewestValueOfEverySeries(t *testing.T) {
	fake := liveFake()
	metrics := newMetrics(t, fake)

	got, err := metrics.Latest(context.Background(), "orders-dev", "pds", "mysql")
	require.NoError(t, err)

	require.NotNil(t, got)
	assert.Empty(t, got.Pending)
	require.NotNil(t, got.CPU)
	assert.InDelta(t, 31, *got.CPU, 0.001)
	require.NotNil(t, got.Connections)
	assert.InDelta(t, 14, *got.Connections, 0.001)
	require.NotNil(t, got.ReplicationLag)
	assert.InDelta(t, 0.25, *got.ReplicationLag, 0.001)
	require.NotNil(t, got.Disk)
	assert.InDelta(t, 42, *got.Disk, 0.001)

	// A probe and then one query per chart, each over a short window so the
	// newest point is the only one that matters.
	assert.Len(t, fake.queries, 6)
}

func TestLatestLeavesASeriesWithNoDataNil(t *testing.T) {
	fake := liveFake()
	fake.empty = []string{"mysql_slave_status_seconds_behind_master"}
	metrics := newMetrics(t, fake)

	got, err := metrics.Latest(context.Background(), "orders-dev", "pds", "mysql")
	require.NoError(t, err)

	assert.Nil(t, got.ReplicationLag)
	assert.NotNil(t, got.CPU)
}

func TestLatestSaysWhyThereIsNoReading(t *testing.T) {
	cases := map[string]struct {
		fake *fakePMM
		want string
	}{
		"no PMM configured": {fake: nil, want: service.MonitoringNotConfigured},
		"PMM refuses the connection": {
			fake: &fakePMM{closed: true}, want: service.MonitoringUnreachable,
		},
		"PMM is starting": {
			fake: &fakePMM{status: 503}, want: service.MonitoringUnavailable,
		},
		"PMM has no target": {fake: &fakePMM{}, want: service.MonitoringNoTarget},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := newMetrics(t, tc.fake).Latest(context.Background(), "orders-dev", "pds", "mysql")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Pending)
			assert.Nil(t, got.CPU)
		})
	}
}

func TestLatestFailsWhenPMMRejectsTheQuery(t *testing.T) {
	_, err := newMetrics(t, &fakePMM{status: 400, body: `{"status":"error"}`}).
		Latest(context.Background(), "orders-dev", "pds", "mysql")

	require.Error(t, err)
}

func TestLatestRefusesANameThatIsNotALabel(t *testing.T) {
	_, err := newMetrics(t, liveFake()).Latest(context.Background(), `orders"}`, "pds", "mysql")

	require.ErrorIs(t, err, service.ErrInvalidMetricTarget)
}

func TestLatestRefusesANamespaceThatIsNotALabel(t *testing.T) {
	_, err := newMetrics(t, liveFake()).Latest(context.Background(), "orders", `pds"}`, "mysql")

	require.ErrorIs(t, err, service.ErrInvalidMetricTarget)
}

// The agent reads this on every sample, and the hub charts what it sends, so a
// reading that blends in another database's pods is what every chart shows.
func TestLatestReadsOnlyItsOwnMembers(t *testing.T) {
	for name, nodes := range memberNodes {
		t.Run(name, func(t *testing.T) {
			fake := &fakePMM{series: map[string][]float64{"up{": {1}}, nodes: nodes}

			got, err := newMetrics(t, fake).Latest(context.Background(), "orders", "db", "mysql")
			require.NoError(t, err)

			for kind, value := range map[string]*float64{"cpu": got.CPU, "memory": got.Memory, "disk": got.Disk} {
				require.NotNil(t, value, kind)
				assert.InDelta(t, 20, *value, 0.001, kind)
			}
		})
	}
}

// sample is one stored reading, minutes before testNow.
func sample(minutesAgo int, reading *service.MonitoringReading) service.UsagePoint {
	return service.UsagePoint{
		DatabaseID: "spoke-a/pds.orders",
		At:         testNow.Add(-time.Duration(minutesAgo) * time.Minute),
		Monitoring: reading,
	}
}

func TestMetricsFromSamplesChartsTheStoredReadings(t *testing.T) {
	// Newest first, the order the book lists them in.
	points := []service.UsagePoint{
		sample(0, &service.MonitoringReading{CPU: num(30), Connections: num(9)}),
		sample(5, &service.MonitoringReading{CPU: num(20), Connections: num(8)}),
		sample(10, &service.MonitoringReading{CPU: num(10)}),
	}

	got, err := service.MetricsFromSamples(points, service.MetricsRequest{Window: service.MetricWindowHour}, testNow)
	require.NoError(t, err)

	assert.False(t, got.Pending)
	assert.Equal(t, service.MetricWindowHour, got.Window)
	assert.Equal(t, testNow.Add(-time.Hour), got.Start)
	assert.Equal(t, testNow, got.End)
	assert.Equal(t, service.UsageInterval, got.Step)
	require.Len(t, got.Series, 5)

	cpu := got.Series[0]
	assert.Equal(t, service.MetricCPU, cpu.Kind)
	assert.Equal(t, service.UnitPercent, cpu.Unit)
	require.Len(t, cpu.Points, 3)
	assert.Equal(t, testNow.Add(-10*time.Minute), cpu.Points[0].At, "points run oldest first")
	assert.InDelta(t, 30, cpu.Current, 0.001)
	assert.InDelta(t, 10, cpu.Min, 0.001)
	assert.InDelta(t, 30, cpu.Max, 0.001)
	assert.InDelta(t, 20, cpu.Average, 0.001)
	assert.Equal(t, service.TrendUp, cpu.Trend)

	connections := got.Series[2]
	assert.Equal(t, service.MetricConnections, connections.Kind)
	assert.Len(t, connections.Points, 2, "a reading without the series is a gap, not a zero")

	lag := got.Series[3]
	assert.False(t, lag.Pending)
	assert.Empty(t, lag.Points)
}

func TestMetricsFromSamplesAnswersOnlyTheKindsAskedFor(t *testing.T) {
	points := []service.UsagePoint{sample(0, &service.MonitoringReading{CPU: num(30), Disk: num(40)})}

	got, err := service.MetricsFromSamples(points, service.MetricsRequest{
		Window: service.MetricWindowDay,
		Kinds:  []service.MetricKind{service.MetricDisk},
	}, testNow)
	require.NoError(t, err)

	assert.Equal(t, testNow.Add(-24*time.Hour), got.Start)
	require.Len(t, got.Series, 1)
	assert.Equal(t, service.MetricDisk, got.Series[0].Kind)
	assert.InDelta(t, 40, got.Series[0].Current, 0.001)
}

func TestMetricsFromSamplesIsPending(t *testing.T) {
	cases := map[string]struct {
		points []service.UsagePoint
		want   string
	}{
		"no samples yet": {
			points: nil,
			want:   "Monitoring has no readings for this database yet.",
		},
		"the agent has no monitoring": {
			points: []service.UsagePoint{sample(0, nil)},
			want:   "Monitoring is not set up in this environment yet.",
		},
		"monitoring is not configured": {
			points: []service.UsagePoint{sample(0, &service.MonitoringReading{Pending: service.MonitoringNotConfigured})},
			want:   "Monitoring is not set up in this environment yet.",
		},
		"monitoring is not reachable": {
			points: []service.UsagePoint{sample(0, &service.MonitoringReading{Pending: service.MonitoringUnreachable})},
			want:   "Monitoring is not reachable in this environment yet.",
		},
		"monitoring is starting": {
			points: []service.UsagePoint{sample(0, &service.MonitoringReading{Pending: service.MonitoringUnavailable})},
			want:   "Monitoring is not serving readings in this environment yet.",
		},
		"no target": {
			// An older reading does not count: the newest one says the target
			// went away.
			points: []service.UsagePoint{
				sample(0, &service.MonitoringReading{Pending: service.MonitoringNoTarget}),
				sample(5, &service.MonitoringReading{CPU: num(12)}),
			},
			want: "Monitoring has no target for this database yet.",
		},
		"a code this hub does not know": {
			points: []service.UsagePoint{sample(0, &service.MonitoringReading{Pending: "something-new"})},
			want:   "Monitoring has no readings for this database yet.",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := service.MetricsFromSamples(tc.points, service.MetricsRequest{}, testNow)
			require.NoError(t, err)

			assert.True(t, got.Pending)
			assert.Equal(t, tc.want, got.PendingReason)
			require.Len(t, got.Series, 5)
			for _, series := range got.Series {
				assert.True(t, series.Pending)
				assert.Empty(t, series.Points)
			}
		})
	}
}

func TestMetricsFromSamplesRejectsAnUnknownWindow(t *testing.T) {
	_, err := service.MetricsFromSamples(nil, service.MetricsRequest{Window: "7d"}, testNow)

	require.ErrorIs(t, err, service.ErrUnknownMetricWindow)
}
