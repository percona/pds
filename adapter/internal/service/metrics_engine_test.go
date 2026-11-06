package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/service"
)

// A PostgreSQL target answers pg_up, and its charts read postgres_exporter and
// the /pgdata volume.
func postgresqlFake() *fakePMM {
	return &fakePMM{series: map[string][]float64{
		"pg_up{":                         {1, 1},
		"node_cpu_seconds_total":         {20, 24, 31},
		"node_memory_MemAvailable_bytes": {50, 52},
		"pg_stat_activity_count":         {7, 9},
		"pg_replication_lag_seconds":     {1.5, 0.5},
		"node_filesystem_avail_bytes":    {61, 62},
	}}
}

func TestForDatabaseChartsAPostgreSQLDatabase(t *testing.T) {
	fake := postgresqlFake()
	in := request()
	in.Engine = "postgresql"

	got, err := newMetrics(t, fake).ForDatabase(context.Background(), in)
	require.NoError(t, err)
	require.False(t, got.Pending)

	current := map[service.MetricKind]float64{}
	for _, series := range got.Series {
		assert.False(t, series.Pending, "%s should not be pending", series.Kind)
		require.NotEmpty(t, series.Points, "%s should have points", series.Kind)
		current[series.Kind] = series.Current
	}
	assert.InDelta(t, 9, current[service.MetricConnections], 0.001)
	assert.InDelta(t, 0.5, current[service.MetricReplicationLag], 0.001)

	assert.Equal(t, `pg_up{cluster="orders-dev"} or node_uname_info{cluster="orders-dev"}`, fake.queries[0])
	joined := strings.Join(fake.queries, "\n")
	assert.Contains(t, joined, `sum(pg_stat_activity_count{cluster="orders-dev"})`)
	assert.Contains(t, joined, `pg_replication_lag_seconds{cluster="orders-dev"}`)
	assert.Contains(t, joined, `mountpoint="/pgdata"`)
	assert.NotContains(t, joined, "mysql")
}

func TestForDatabaseKeepsTheMySQLQueries(t *testing.T) {
	for name, engine := range map[string]string{"blank": "", "mysql": "mysql", "unknown": "mongodb"} {
		t.Run(name, func(t *testing.T) {
			fake := liveFake()
			in := request()
			in.Engine = engine

			_, err := newMetrics(t, fake).ForDatabase(context.Background(), in)
			require.NoError(t, err)

			assert.Equal(t,
				`up{cluster="orders-dev"} or haproxy_process_uptime_seconds{cluster="orders-dev"}`+
					` or node_uname_info{cluster="orders-dev"}`,
				fake.queries[0])
			joined := strings.Join(fake.queries, "\n")
			assert.Contains(t, joined, `sum(mysql_global_status_threads_connected{cluster="orders-dev"})`)
			assert.Contains(t, joined, `max(mysql_slave_status_seconds_behind_master{cluster="orders-dev"})`)
			assert.Contains(t, joined, `mountpoint="/var/lib/mysql"`)
			assert.NotContains(t, joined, "pg_")
		})
	}
}

func TestForDatabaseIsPendingWhilePostgreSQLHasNoTarget(t *testing.T) {
	fake := &fakePMM{}
	in := request()
	in.Engine = "postgresql"

	got, err := newMetrics(t, fake).ForDatabase(context.Background(), in)
	require.NoError(t, err)

	assert.True(t, got.Pending)
	assert.Len(t, fake.queries, 1)
}

func TestLatestReadsAPostgreSQLDatabase(t *testing.T) {
	fake := postgresqlFake()

	got, err := newMetrics(t, fake).Latest(context.Background(), "orders-dev", "pds", "postgresql")
	require.NoError(t, err)

	assert.Empty(t, got.Pending)
	require.NotNil(t, got.Connections)
	assert.InDelta(t, 9, *got.Connections, 0.001)
	require.NotNil(t, got.ReplicationLag)
	assert.InDelta(t, 0.5, *got.ReplicationLag, 0.001)
	require.NotNil(t, got.Disk)
	assert.Contains(t, fake.queries[0], `pg_up{cluster="orders-dev"}`)
	joined := strings.Join(fake.queries, "\n")
	assert.Contains(t, joined, `mountpoint="/pgdata"`)
	assert.NotContains(t, joined, "mysql")
}

// PMM 3 does not put the cluster label on every exporter's series, so a
// PostgreSQL target node_exporter already reports for is not pending.
func TestForDatabaseFindsAPostgreSQLTargetThroughNodeExporter(t *testing.T) {
	fake := &fakePMM{series: map[string][]float64{
		"node_uname_info":        {1, 1},
		"node_cpu_seconds_total": {20, 24, 31},
	}}
	in := request()
	in.Engine = "postgresql"

	got, err := newMetrics(t, fake).ForDatabase(context.Background(), in)
	require.NoError(t, err)

	assert.False(t, got.Pending)
}

// postgres_exporter's replication collector names the series
// pg_replication_lag_seconds, and PMM's own query names it pg_replication_lag.
func TestForDatabaseReadsPostgreSQLLagUnderEitherName(t *testing.T) {
	fake := &fakePMM{series: map[string][]float64{
		"pg_up{":              {1, 1},
		"pg_replication_lag{": {2, 3},
	}}
	in := request()
	in.Engine = "postgresql"
	in.Kinds = []service.MetricKind{service.MetricReplicationLag}

	got, err := newMetrics(t, fake).ForDatabase(context.Background(), in)
	require.NoError(t, err)

	require.Len(t, got.Series, 1)
	assert.InDelta(t, 3, got.Series[0].Current, 0.001)
}

// The two lag series answer differently on a replica of an idle primary: the
// collector's reads 0 once everything received is replayed, PMM's keeps
// counting since the last replayed commit. Each is aggregated on its own, so
// PMM's is read only when the collector's is absent and never outweighs it.
func TestForDatabaseReadsPMMsPostgreSQLLagOnlyWithoutTheCollectors(t *testing.T) {
	fake := &fakePMM{series: map[string][]float64{"pg_up{": {1, 1}}}
	in := request()
	in.Engine = "postgresql"
	in.Kinds = []service.MetricKind{service.MetricReplicationLag}

	_, err := newMetrics(t, fake).ForDatabase(context.Background(), in)
	require.NoError(t, err)

	require.Len(t, fake.queries, 2)
	assert.Equal(t,
		`max(pg_replication_lag_seconds{cluster="orders-dev"}) or max(pg_replication_lag{cluster="orders-dev"})`,
		fake.queries[1])
}
