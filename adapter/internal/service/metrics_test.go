package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/pmm"
	"github.com/percona/pds/adapter/internal/service"
)

// testNow is the fixed clock every metrics test reads, so a window is an exact
// pair of timestamps rather than "roughly an hour ago".
var testNow = time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

// fakePMM serves matrices chosen by a substring of the PromQL expression, so a
// test says "connections returns these points" without repeating the query.
type fakePMM struct {
	// series maps a substring of the query to the values of one series.
	series map[string][]float64
	// empty lists substrings whose query matches nothing, which is how PMM
	// reports a metric it has no data for.
	empty []string
	// status, when not zero, is returned instead of a matrix.
	status int
	body   string
	// closed shuts the server down before the client is handed out, which is
	// what a PMM that is not running looks like: the connection is refused
	// before there is a status to read.
	closed bool
	// unavailableAfter, when not zero, answers 503 once that many queries have
	// been served, which is a PMM that goes away partway through a snapshot.
	unavailableAfter int
	// nodes maps a node_name to the value of its node_exporter series, which
	// carry no cluster label: that is how PMM 3 reports a member pod. A query
	// that selects by node_name answers the average of the nodes its matchers
	// select. Giving a database's own members one value makes avg and max
	// agree, so the fake stays out of which one a chart uses.
	nodes map[string]float64

	queries []string
}

func (f *fakePMM) client(t *testing.T) *pmm.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		f.queries = append(f.queries, query)

		w.Header().Set("Content-Type", "application/json")

		if f.unavailableAfter != 0 && len(f.queries) > f.unavailableAfter {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(f.body))

			return
		}

		body, err := f.matrix(query)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"status":"error","errorType":"bad_data","error":%q}`, err.Error())

			return
		}

		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	client, err := pmm.New(pmm.Options{BaseURL: srv.URL})
	require.NoError(t, err)

	if f.closed {
		srv.Close()
	}

	return client
}

// matrix renders the response for one query: a series when a key matches, an
// empty result otherwise. A node_name matcher PMM could not parse is an error.
func (f *fakePMM) matrix(query string) ([]byte, error) {
	for _, blank := range f.empty {
		if strings.Contains(query, blank) {
			return emptyMatrix(), nil
		}
	}

	value, selected, err := f.byNodeName(query)
	if err != nil {
		return nil, err
	}
	if selected {
		return matrixOf([]float64{value}), nil
	}

	for key, values := range f.series {
		if strings.Contains(query, key) {
			return matrixOf(values), nil
		}
	}

	return emptyMatrix(), nil
}

var nodeNameMatcher = regexp.MustCompile(`node_name=~("(?:[^"\\]|\\.)*")`)

// byNodeName averages the nodes the query's node_name matchers select. PromQL
// reads the matcher as a Go style string and anchors the regex at both ends,
// so the fake does the same.
func (f *fakePMM) byNodeName(query string) (float64, bool, error) {
	matchers := nodeNameMatcher.FindAllStringSubmatch(query, -1)
	if len(f.nodes) == 0 || len(matchers) == 0 {
		return 0, false, nil
	}

	selected := map[string]bool{}
	for _, matcher := range matchers {
		pattern, err := strconv.Unquote(matcher[1])
		if err != nil {
			return 0, false, fmt.Errorf("node_name matcher %s: %w", matcher[1], err)
		}
		re, err := regexp.Compile("^(?:" + pattern + ")$")
		if err != nil {
			return 0, false, fmt.Errorf("node_name matcher %s: %w", matcher[1], err)
		}
		for node := range f.nodes {
			if re.MatchString(node) {
				selected[node] = true
			}
		}
	}
	if len(selected) == 0 {
		return 0, false, nil
	}

	total := 0.0
	for node := range selected {
		total += f.nodes[node]
	}

	return total / float64(len(selected)), true, nil
}

func emptyMatrix() []byte {
	return []byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`)
}

// matrixOf builds a one series matrix whose points sit one minute apart and end
// at testNow, which is what a range query over the last hour returns.
func matrixOf(values []float64) []byte {
	points := make([]string, 0, len(values))
	base := testNow.Add(-time.Duration(len(values)) * time.Minute)

	for i, value := range values {
		at := base.Add(time.Duration(i+1) * time.Minute)
		points = append(points, fmt.Sprintf(`[%d,"%s"]`, at.Unix(), strconvFloat(value)))
	}

	return []byte(fmt.Sprintf(
		`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"cluster":"orders-dev"},"values":[%s]}]}}`,
		strings.Join(points, ","),
	))
}

func strconvFloat(value float64) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}

	return string(encoded)
}

func newMetrics(t *testing.T, fake *fakePMM) *service.Metrics {
	t.Helper()

	var client service.RangeQuerier
	if fake != nil {
		client = fake.client(t)
	}

	return service.NewMetrics(client, service.WithMetricsClock(func() time.Time { return testNow }))
}

func request() service.MetricsRequest {
	return service.MetricsRequest{Database: "orders-dev", Namespace: "pds", Window: service.MetricWindowHour}
}

// A live target, so the probe answers and every metric is asked for.
func liveFake() *fakePMM {
	return &fakePMM{series: map[string][]float64{
		"up{":                                      {1, 1},
		"node_cpu_seconds_total":                   {20, 24, 31},
		"node_memory_MemAvailable_bytes":           {50, 52},
		"mysql_global_status_threads_connected":    {12, 14},
		"mysql_slave_status_seconds_behind_master": {0.5, 0.25},
		"node_filesystem_avail_bytes":              {41, 42},
	}}
}

func TestForDatabaseIsPendingWithoutAPMMConnection(t *testing.T) {
	metrics := newMetrics(t, nil)

	got, err := metrics.ForDatabase(context.Background(), request())
	require.NoError(t, err)

	assert.True(t, got.Pending)
	assert.Equal(t, "Monitoring is not connected yet.", got.PendingReason)
	require.Len(t, got.Series, 5)

	for _, series := range got.Series {
		assert.True(t, series.Pending, "%s should be pending", series.Kind)
		assert.Empty(t, series.Points)
	}
}

// A deployment that never built the service still has to answer, because the
// metrics route is mounted whether PMM is wired or not.
func TestForDatabaseIsPendingOnANilService(t *testing.T) {
	var metrics *service.Metrics

	got, err := metrics.ForDatabase(context.Background(), request())
	require.NoError(t, err)

	assert.True(t, got.Pending)
	assert.Len(t, got.Series, 5)
}

func TestForDatabaseIsPendingWhilePMMHasNoTarget(t *testing.T) {
	fake := &fakePMM{}
	metrics := newMetrics(t, fake)

	got, err := metrics.ForDatabase(context.Background(), request())
	require.NoError(t, err)

	assert.True(t, got.Pending)
	assert.Contains(t, got.PendingReason, "target")

	// The probe answered nothing, so the five metric queries are not worth
	// making: one call decides the whole snapshot.
	assert.Len(t, fake.queries, 1)
	assert.Contains(t, fake.queries[0], `cluster="orders-dev"`)
}

func TestForDatabaseReturnsEveryMetricOnceTheTargetIsUp(t *testing.T) {
	metrics := newMetrics(t, liveFake())

	got, err := metrics.ForDatabase(context.Background(), request())
	require.NoError(t, err)
	require.False(t, got.Pending)

	kinds := make([]service.MetricKind, 0, len(got.Series))
	for _, series := range got.Series {
		kinds = append(kinds, series.Kind)
	}

	assert.Equal(t, []service.MetricKind{
		service.MetricCPU,
		service.MetricMemory,
		service.MetricConnections,
		service.MetricReplicationLag,
		service.MetricDisk,
	}, kinds)
}

func TestForDatabaseSummarizesASeries(t *testing.T) {
	metrics := newMetrics(t, liveFake())

	got, err := metrics.ForDatabase(context.Background(), request())
	require.NoError(t, err)

	cpu := seriesOf(t, got, service.MetricCPU)
	assert.False(t, cpu.Pending)
	assert.Equal(t, service.UnitPercent, cpu.Unit)
	require.Len(t, cpu.Points, 3)
	assert.InDelta(t, 31, cpu.Current, 0.0001)
	assert.InDelta(t, 20, cpu.Min, 0.0001)
	assert.InDelta(t, 31, cpu.Max, 0.0001)
	assert.InDelta(t, 25, cpu.Average, 0.0001)
	assert.Equal(t, service.TrendUp, cpu.Trend)
}

func TestForDatabaseReportsTheUnitPerMetric(t *testing.T) {
	metrics := newMetrics(t, liveFake())

	got, err := metrics.ForDatabase(context.Background(), request())
	require.NoError(t, err)

	assert.Equal(t, service.UnitPercent, seriesOf(t, got, service.MetricMemory).Unit)
	assert.Equal(t, service.UnitCount, seriesOf(t, got, service.MetricConnections).Unit)
	assert.Equal(t, service.UnitSeconds, seriesOf(t, got, service.MetricReplicationLag).Unit)
	assert.Equal(t, service.UnitPercent, seriesOf(t, got, service.MetricDisk).Unit)
}

func TestForDatabaseTrends(t *testing.T) {
	tests := []struct {
		name   string
		values []float64
		want   string
	}{
		{name: "rising", values: []float64{10, 40}, want: service.TrendUp},
		{name: "falling", values: []float64{40, 10}, want: service.TrendDown},
		{name: "flat", values: []float64{20, 20}, want: service.TrendSteady},
		{name: "noise", values: []float64{20, 20.4}, want: service.TrendSteady},
		{name: "single point", values: []float64{20}, want: service.TrendSteady},
		{name: "all zero", values: []float64{0, 0}, want: service.TrendSteady},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := liveFake()
			fake.series["mysql_global_status_threads_connected"] = test.values
			metrics := newMetrics(t, fake)

			got, err := metrics.ForDatabase(context.Background(), request())
			require.NoError(t, err)

			assert.Equal(t, test.want, seriesOf(t, got, service.MetricConnections).Trend)
		})
	}
}

// A live target with nothing recorded for one metric is a gap in the data, not
// a target PMM has yet to register. Async replication on a PXC cluster with no
// replica is the normal case.
func TestForDatabaseSeparatesAnEmptyMetricFromAPendingTarget(t *testing.T) {
	fake := liveFake()
	fake.empty = []string{"mysql_slave_status_seconds_behind_master"}
	metrics := newMetrics(t, fake)

	got, err := metrics.ForDatabase(context.Background(), request())
	require.NoError(t, err)

	assert.False(t, got.Pending)

	lag := seriesOf(t, got, service.MetricReplicationLag)
	assert.False(t, lag.Pending)
	assert.Empty(t, lag.Points)
}

func TestForDatabaseWindows(t *testing.T) {
	tests := []struct {
		window service.MetricWindow
		start  time.Time
		step   time.Duration
	}{
		{window: service.MetricWindowHour, start: testNow.Add(-time.Hour), step: time.Minute},
		{window: service.MetricWindowDay, start: testNow.Add(-24 * time.Hour), step: 15 * time.Minute},
	}

	for _, test := range tests {
		t.Run(string(test.window), func(t *testing.T) {
			metrics := newMetrics(t, liveFake())

			got, err := metrics.ForDatabase(context.Background(), service.MetricsRequest{
				Database:  "orders-dev",
				Namespace: "pds",
				Window:    test.window,
			})
			require.NoError(t, err)

			assert.Equal(t, test.window, got.Window)
			assert.Equal(t, test.start, got.Start)
			assert.Equal(t, testNow, got.End)
			assert.Equal(t, test.step, got.Step)
		})
	}
}

func TestForDatabaseAsksOnlyForTheKindsRequested(t *testing.T) {
	fake := liveFake()
	metrics := newMetrics(t, fake)

	got, err := metrics.ForDatabase(context.Background(), service.MetricsRequest{
		Database:  "orders-dev",
		Namespace: "pds",
		Window:    service.MetricWindowHour,
		Kinds:     []service.MetricKind{service.MetricCPU},
	})
	require.NoError(t, err)

	require.Len(t, got.Series, 1)
	assert.Equal(t, service.MetricCPU, got.Series[0].Kind)

	// The probe plus one metric, not the probe plus five.
	assert.Len(t, fake.queries, 2)
}

func TestForDatabaseScopesEveryQueryToTheDatabase(t *testing.T) {
	fake := liveFake()
	metrics := newMetrics(t, fake)

	_, err := metrics.ForDatabase(context.Background(), request())
	require.NoError(t, err)

	require.Len(t, fake.queries, 6)
	for _, query := range fake.queries {
		assert.Contains(t, query, `cluster="orders-dev"`)
	}

	var cpuQuery string
	for _, query := range fake.queries {
		if strings.Contains(query, "node_cpu_seconds_total") {
			cpuQuery = query
			break
		}
	}
	require.NotEmpty(t, cpuQuery)
	assert.Contains(t, cpuQuery, `node_name=~"^pds-orders-dev-(`)
}

// memberNodes are node_exporter series as PMM 3 reports them, with only a
// node_name of <namespace>-<pod>. The members of orders in namespace db read
// 20; everything else reads 90, so a chart that blends in another database's
// pods moves off 20. The pods of orders-instance2 read like a PostgreSQL
// member of orders, with pxc or haproxy where the four character suffix goes.
var memberNodes = map[string]map[string]float64{
	"PXC members": {
		"db-orders-pxc-0":               20,
		"db-orders-pxc-1":               20,
		"db-my-orders-pxc-0":            90,
		"other-orders-pxc-0":            90,
		"db-orders-instance2-pxc-0":     90,
		"db-orders-instance2-haproxy-0": 90,
	},
	"PostgreSQL members": {
		"db-orders-instance1-b2c4-0":    20,
		"db-my-orders-instance1-x7z9-0": 90,
		"other-orders-instance1-b2c4-0": 90,
	},
}

func TestForDatabaseChartsOnlyItsOwnMembers(t *testing.T) {
	for name, nodes := range memberNodes {
		t.Run(name, func(t *testing.T) {
			fake := &fakePMM{series: map[string][]float64{"up{": {1}}, nodes: nodes}

			got, err := newMetrics(t, fake).ForDatabase(context.Background(), service.MetricsRequest{
				Database: "orders", Namespace: "db", Window: service.MetricWindowHour,
			})
			require.NoError(t, err)

			for _, kind := range []service.MetricKind{service.MetricCPU, service.MetricMemory, service.MetricDisk} {
				assert.InDelta(t, 20, seriesOf(t, got, kind).Current, 0.001, kind)
			}
		})
	}
}

// Instance names are DNS labels, so a dot never reaches PMM from a real
// database, but the label check lets one through and a regex reads it as any
// character.
func TestForDatabaseReadsADotInANameLiterally(t *testing.T) {
	fake := &fakePMM{series: map[string][]float64{"up{": {1}}, nodes: map[string]float64{
		"db.v2-orders.v2-pxc-0": 20,
		"dbxv2-ordersxv2-pxc-0": 90,
	}}

	got, err := newMetrics(t, fake).ForDatabase(context.Background(), service.MetricsRequest{
		Database: "orders.v2", Namespace: "db.v2", Window: service.MetricWindowHour,
	})
	require.NoError(t, err)

	assert.InDelta(t, 20, seriesOf(t, got, service.MetricCPU).Current, 0.001)
}

func TestForDatabaseRejectsANameThatCannotBeALabel(t *testing.T) {
	metrics := newMetrics(t, liveFake())

	_, err := metrics.ForDatabase(context.Background(), service.MetricsRequest{
		Database: `orders" or up{`,
		Window:   service.MetricWindowHour,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, service.ErrInvalidMetricTarget)
}

func TestForDatabaseRejectsANamespaceThatCannotBeALabel(t *testing.T) {
	_, err := newMetrics(t, liveFake()).ForDatabase(context.Background(), service.MetricsRequest{
		Database:  "orders",
		Namespace: `pds")`,
		Window:    service.MetricWindowHour,
	})

	require.ErrorIs(t, err, service.ErrInvalidMetricTarget)
}

func TestForDatabaseReportsAPMMFailure(t *testing.T) {
	fake := &fakePMM{
		status: http.StatusInternalServerError,
		body:   `{"status":"error","error":"storage is not ready"}`,
	}
	metrics := newMetrics(t, fake)

	_, err := metrics.ForDatabase(context.Background(), request())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage is not ready")
}

// A PMM that is not up is the same state as one with no target: a pending
// badge, not an error. A create never waited on PMM in the first place.
func TestForDatabaseIsPendingWhenPMMIsUnreachable(t *testing.T) {
	snapshot, err := newMetrics(t, &fakePMM{closed: true}).ForDatabase(context.Background(), request())
	require.NoError(t, err)

	assert.True(t, snapshot.Pending)
	assert.Equal(t, "Monitoring is not reachable yet.", snapshot.PendingReason)
	assert.Len(t, snapshot.Series, len(service.AllMetricKinds()))

	for _, series := range snapshot.Series {
		assert.True(t, series.Pending, string(series.Kind))
		assert.Empty(t, series.Points, string(series.Kind))
	}
}

// PMM's own nginx answers a gateway status while Grafana and the metrics store
// boot, which is a PMM that is not up rather than one refusing the query.
func TestForDatabaseIsPendingWhenPMMIsNotServingYet(t *testing.T) {
	for _, status := range []int{
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		fake := &fakePMM{status: status, body: `{"status":"error","error":"upstream is down"}`}

		snapshot, err := newMetrics(t, fake).ForDatabase(context.Background(), request())
		require.NoError(t, err, "status %d", status)

		assert.True(t, snapshot.Pending, "status %d", status)
		assert.Equal(t, "Monitoring is not answering queries yet.", snapshot.PendingReason)
	}
}

// The probe answers and then PMM goes away, so the charts collected so far are
// dropped: half a grid reads like a quiet cluster.
func TestForDatabaseIsPendingWhenPMMGoesAwayAfterTheProbe(t *testing.T) {
	fake := liveFake()
	fake.unavailableAfter = 1

	snapshot, err := newMetrics(t, fake).ForDatabase(context.Background(), request())
	require.NoError(t, err)

	assert.True(t, snapshot.Pending)
	assert.Equal(t, "Monitoring is not answering queries yet.", snapshot.PendingReason)

	for _, series := range snapshot.Series {
		assert.True(t, series.Pending, string(series.Kind))
	}
}

func TestParseMetricWindow(t *testing.T) {
	tests := []struct {
		raw     string
		want    service.MetricWindow
		wantErr bool
	}{
		{raw: "", want: service.MetricWindowHour},
		{raw: "1h", want: service.MetricWindowHour},
		{raw: "24h", want: service.MetricWindowDay},
		{raw: "7d", wantErr: true},
		{raw: "1H", wantErr: true},
	}

	for _, test := range tests {
		t.Run("window "+test.raw, func(t *testing.T) {
			got, err := service.ParseMetricWindow(test.raw)
			if test.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, service.ErrUnknownMetricWindow)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestParseMetricKinds(t *testing.T) {
	all, err := service.ParseMetricKinds("")
	require.NoError(t, err)
	assert.Len(t, all, 5)

	some, err := service.ParseMetricKinds("cpu,connections")
	require.NoError(t, err)
	assert.Equal(t, []service.MetricKind{service.MetricCPU, service.MetricConnections}, some)

	_, err = service.ParseMetricKinds("cpu,disk-io")
	require.Error(t, err)
	assert.ErrorIs(t, err, service.ErrUnknownMetric)
}

func seriesOf(t *testing.T, snapshot *service.MetricSnapshot, kind service.MetricKind) service.MetricSeries {
	t.Helper()

	for _, series := range snapshot.Series {
		if series.Kind == kind {
			return series
		}
	}

	t.Fatalf("no %s series in snapshot", kind)

	return service.MetricSeries{}
}
