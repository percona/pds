package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/client/pmm"
	"github.com/percona/pds/adapter/internal/engine/mongodb"
	"github.com/percona/pds/adapter/internal/engine/mysql"
	"github.com/percona/pds/adapter/internal/engine/postgresql"
	"github.com/percona/pds/adapter/internal/hub"
)

// Metrics errors the HTTP layer maps onto the API error contract.
var (
	// ErrUnknownMetricWindow means the caller asked for a window the adapter
	// does not serve. The toggle is fixed at 1h and 24h.
	ErrUnknownMetricWindow = errors.New("unknown metric window")
	// ErrUnknownMetric means the caller asked for a metric that does not exist.
	ErrUnknownMetric = errors.New("unknown metric")
	// ErrInvalidMetricTarget means the database name cannot be used as a label
	// value. It is a guard against building a query out of untrusted text.
	ErrInvalidMetricTarget = errors.New("invalid metric target")
)

// MetricWindow is one of the two windows the Metrics tab toggles between.
type MetricWindow string

const (
	// MetricWindowHour is the last hour at a one minute step.
	MetricWindowHour MetricWindow = "1h"
	// MetricWindowDay is the last day at a fifteen minute step.
	MetricWindowDay MetricWindow = "24h"
)

// MetricKind names one chart. These five are the ones the product ships.
type MetricKind string

const (
	MetricCPU            MetricKind = "cpu"
	MetricMemory         MetricKind = "memory"
	MetricConnections    MetricKind = "connections"
	MetricReplicationLag MetricKind = "replication-lag"
	MetricDisk           MetricKind = "disk"
)

// Units a series can carry. The UI formats a number from the unit, so a percent
// never renders with a seconds suffix.
const (
	UnitPercent = "percent"
	UnitCount   = "count"
	UnitSeconds = "seconds"
)

// Trends a series can report. They are the text equivalent of the chart shape,
// which is what makes a chart readable without seeing it.
const (
	TrendUp     = "up"
	TrendDown   = "down"
	TrendSteady = "steady"
)

// trendThreshold is the share of the larger endpoint a change has to clear
// before it counts as a direction. Below it, a series is steady: sampling noise
// is not a trend.
const trendThreshold = 0.05

// MetricPoint is one reading.
type MetricPoint struct {
	At    time.Time
	Value float64
}

// MetricSeries is one chart: the points plus the summary the UI renders as the
// chart's text equivalent.
//
// Pending is true when there is no PMM to read from yet, when PMM is
// unreachable or unavailable, or when PMM has no target for the database yet.
// A series that is not pending and has no points is a gap in the data, which
// the UI states rather than treating as a failure.
type MetricSeries struct {
	Kind    MetricKind
	Unit    string
	Pending bool
	Points  []MetricPoint
	Current float64
	Min     float64
	Max     float64
	Average float64
	Trend   string
}

// MetricSnapshot is one answer to GET /api/databases/:id/metrics.
type MetricSnapshot struct {
	Window        MetricWindow
	Start         time.Time
	End           time.Time
	Step          time.Duration
	Pending       bool
	PendingReason string
	Series        []MetricSeries
}

// MetricsRequest is what the HTTP layer asks for. Kinds empty means every kind.
type MetricsRequest struct {
	// Database is the instance name, which is also the PMM cluster label.
	Database string
	// Namespace is the Kubernetes namespace the database runs in. PMM 3 puts
	// it in front of the pod name in node_name.
	Namespace string
	// Engine picks the connection, replication, disk and probe queries. Blank
	// means MySQL, and an engine the service has no queries for gets none.
	Engine string
	Window MetricWindow
	Kinds  []MetricKind
}

// RangeQuerier is the slice of the PMM client the metrics service needs. The
// interface keeps the service testable and makes "no PMM configured" a nil
// value rather than a special case at every call site.
type RangeQuerier interface {
	QueryRange(ctx context.Context, q pmm.RangeQuery) ([]pmm.Series, error)
}

// Metrics reads database metrics from PMM.
type Metrics struct {
	client RangeQuerier
	now    func() time.Time
}

// MetricsOption configures a Metrics service.
type MetricsOption func(*Metrics)

// WithMetricsClock replaces the clock, so a test asserts an exact window.
func WithMetricsClock(now func() time.Time) MetricsOption {
	return func(m *Metrics) {
		if now != nil {
			m.now = now
		}
	}
}

// NewMetrics returns the metrics service. A nil client is a deployment without
// PMM_URL, and every read then answers pending instead of failing: a pending
// badge goes where a chart will be, never an error.
func NewMetrics(client RangeQuerier, opts ...MetricsOption) *Metrics {
	m := &Metrics{client: client, now: time.Now}
	for _, opt := range opts {
		opt(m)
	}

	return m
}

// metricQuery is the PromQL behind one chart. `$cluster` is the database name,
// `$members` the node_name pattern of its member pods, `$rate` the rate window
// the step calls for, and `$datadir` the engine's data volume. Connections and
// replication lag differ by engine, so their expressions live in engineQueries
// and Expr is empty.
//
// The expressions read PMM 3 metrics, which come from the PMM client sidecar
// the provider adds to every member pod: node_cpu_seconds_total,
// node_memory_* and node_filesystem_* from node_exporter, and the engine
// exporter's series for the rest.
//
// Each expression aggregates the members away, so one series comes back for a
// three member cluster and the chart is about the database, not a pod.
type metricQuery struct {
	Kind MetricKind
	Unit string
	Expr string
}

var metricQueries = []metricQuery{
	{
		Kind: MetricCPU,
		Unit: UnitPercent,
		// PMM 3's node_exporter series often omit cluster and carry only
		// node_name, which memberPattern matches.
		Expr: `avg(100 * (1 - avg by (node_name) (` +
			`rate(node_cpu_seconds_total{cluster="$cluster",mode="idle"}[$rate])` +
			` or rate(node_cpu_seconds_total{node_name=~"$members",mode="idle"}[$rate])` +
			`)))`,
	},
	{
		Kind: MetricMemory,
		Unit: UnitPercent,
		Expr: `avg(100 * (1 - (` +
			`node_memory_MemAvailable_bytes{cluster="$cluster"}` +
			` or node_memory_MemAvailable_bytes{node_name=~"$members"}` +
			`) / (` +
			`node_memory_MemTotal_bytes{cluster="$cluster"}` +
			` or node_memory_MemTotal_bytes{node_name=~"$members"}` +
			`)))`,
	},
	{
		Kind: MetricConnections,
		Unit: UnitCount,
	},
	{
		Kind: MetricReplicationLag,
		Unit: UnitSeconds,
	},
	{
		Kind: MetricDisk,
		Unit: UnitPercent,
		Expr: `max(100 * (1 - (` +
			`node_filesystem_avail_bytes{cluster="$cluster",mountpoint="$datadir"}` +
			` or node_filesystem_avail_bytes{node_name=~"$members",mountpoint="$datadir"}` +
			`) / (` +
			`node_filesystem_size_bytes{cluster="$cluster",mountpoint="$datadir"}` +
			` or node_filesystem_size_bytes{node_name=~"$members",mountpoint="$datadir"}` +
			`)))`,
	},
}

// engineQueries are the expressions and paths that differ between engines. The
// shared CPU and memory queries read node_exporter, which every engine's PMM
// client sidecar runs. An empty expression is a chart the engine has no query
// for, and it is not asked of PMM.
type engineQueries struct {
	connections string
	lag         string
	probe       string
	// dataDir is where the provider mounts the data volume, which is the
	// filesystem the disk chart is about. Empty means the disk chart has no
	// volume to read.
	dataDir string
}

// mysqlQueries read mysqld_exporter. PMM 3's `up` series do not carry the
// cluster label, but HAProxy and node_exporter do, so either of those is enough
// for the probe to leave pending.
var mysqlQueries = engineQueries{
	connections: `sum(mysql_global_status_threads_connected{cluster="$cluster"})`,
	lag:         `max(mysql_slave_status_seconds_behind_master{cluster="$cluster"})`,
	probe: `up{cluster="$cluster"} or haproxy_process_uptime_seconds{cluster="$cluster"}` +
		` or node_uname_info{cluster="$cluster"}`,
	dataDir: hub.MySQLDataDir,
}

// postgresqlQueries read postgres_exporter. Its replication collector names
// the lag series pg_replication_lag_seconds and PMM's own query names it
// pg_replication_lag. The collector's reads 0 on a replica that has replayed
// everything it received, while PMM's keeps growing on a replica of an idle
// primary, so the two are aggregated apart and PMM's answers only when the
// collector's is absent. The probe leaves pending on node_exporter too, as the
// MySQL one does.
var postgresqlQueries = engineQueries{
	connections: `sum(pg_stat_activity_count{cluster="$cluster"})`,
	lag:         `max(pg_replication_lag_seconds{cluster="$cluster"}) or max(pg_replication_lag{cluster="$cluster"})`,
	probe:       `pg_up{cluster="$cluster"} or node_uname_info{cluster="$cluster"}`,
	dataDir:     hub.PostgreSQLDataDir,
}

// mongodbQueries read mongodb_exporter, with the names in hub, which the alert
// rules read too. Connections count the open ones across the members. The lag
// series exists once per secondary, so a one member replica set has none and
// its chart is an empty series. The probe leaves pending on node_exporter too,
// as the others do.
var mongodbQueries = engineQueries{
	connections: `sum(` + hub.MongoDBConnectionsMetric + `{cluster="$cluster",` +
		hub.MongoDBConnectionsStateLabel + `="` + hub.MongoDBConnectionsOpen + `"})`,
	lag:     `max(` + hub.MongoDBReplicationLagMetric + `{cluster="$cluster"})`,
	probe:   hub.MongoDBUpMetric + `{cluster="$cluster"} or node_uname_info{cluster="$cluster"}`,
	dataDir: hub.MongoDBDataDir,
}

// nodeQueries are what an engine the service has no queries for gets: a probe
// on node_exporter, which every PMM client sidecar runs, and nothing else. It
// shows no chart another engine's series would have filled with the wrong data.
var nodeQueries = engineQueries{probe: `node_uname_info{cluster="$cluster"}`}

// queriesFor picks the engine's queries. A blank engine is MySQL, which is what
// an agent that predates the field reports for.
func queriesFor(engine string) engineQueries {
	switch engine {
	case "", mysql.EngineID:
		return mysqlQueries
	case postgresql.EngineID:
		return postgresqlQueries
	case mongodb.EngineID:
		return mongodbQueries
	default:
		return nodeQueries
	}
}

// exprFor is the expression behind one chart for this engine, empty when the
// engine has none.
func (q engineQueries) exprFor(definition metricQuery) string {
	switch definition.Kind {
	case MetricConnections:
		return q.connections
	case MetricReplicationLag:
		return q.lag
	case MetricDisk:
		if q.dataDir == "" {
			return ""
		}

		return definition.Expr
	default:
		return definition.Expr
	}
}

// queryTarget is what an expression's placeholders are filled with.
type queryTarget struct {
	namespace string
	cluster   string
	rate      string
}

// queryChart asks PMM for one chart. An expression the engine has none of is
// an empty answer, the same as a series PMM has no data for.
func (m *Metrics) queryChart(
	ctx context.Context,
	snapshot *MetricSnapshot,
	queries engineQueries,
	definition metricQuery,
	target queryTarget,
	step time.Duration,
) ([]pmm.Series, error) {
	expr := queries.exprFor(definition)
	if expr == "" {
		return nil, nil
	}

	return m.query(ctx, snapshot, expand(expr, target.namespace, target.cluster, target.rate, queries.dataDir), step)
}

// ForDatabase reads one snapshot. It never fails because PMM has nothing to
// report; it fails only when PMM answers with an error.
func (m *Metrics) ForDatabase(ctx context.Context, in MetricsRequest) (*MetricSnapshot, error) {
	cluster, err := labelValue(in.Database, ErrInvalidMetricTarget)
	if err != nil {
		return nil, err
	}
	namespace, err := labelValue(in.Namespace, ErrInvalidMetricTarget)
	if err != nil {
		return nil, err
	}

	window, err := ParseMetricWindow(string(in.Window))
	if err != nil {
		return nil, err
	}

	kinds := in.Kinds
	if len(kinds) == 0 {
		kinds = AllMetricKinds()
	}

	queries := queriesFor(in.Engine)
	span, step, rate := windowShape(window)
	end := m.clock()()
	snapshot := &MetricSnapshot{
		Window: window,
		Start:  end.Add(-span),
		End:    end,
		Step:   step,
	}

	if m == nil || m.client == nil {
		return pending(snapshot, kinds, "Monitoring is not connected yet."), nil
	}

	// The probe covers the window in one or two evaluations, which is enough to
	// tell a registered target from one PMM has never seen.
	probe, err := m.query(ctx, snapshot, expand(queries.probe, namespace, cluster, rate, queries.dataDir), span)
	if err != nil {
		if reason, ok := pmmNotUp(err); ok {
			return pending(snapshot, kinds, reason), nil
		}

		return nil, fmt.Errorf("metrics: probe %s: %w", in.Database, err)
	}
	if len(probe) == 0 {
		return pending(snapshot, kinds, "Monitoring has no target for this database yet."), nil
	}

	// Charts are collected before they are attached, so a PMM that goes away
	// halfway through answers pending rather than a grid that is part drawn.
	series := make([]MetricSeries, 0, len(kinds))

	for _, kind := range kinds {
		definition, ok := queryFor(kind)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownMetric, kind)
		}

		points, err := m.queryChart(ctx, snapshot, queries, definition, queryTarget{namespace, cluster, rate}, step)
		if err != nil {
			if reason, ok := pmmNotUp(err); ok {
				return pending(snapshot, kinds, reason), nil
			}

			return nil, fmt.Errorf("metrics: %s for %s: %w", kind, in.Database, err)
		}

		series = append(series, summarize(definition, points))
	}

	snapshot.Series = series

	return snapshot, nil
}

// pmmNotUp reports whether a failed PMM read means PMM is not up, and the
// reason to show in its place. A PMM that is not there is the same state as
// one that has no target yet: a pending badge goes where the chart will be.
// A PMM that answered, by rejecting the query or the token, is a real failure
// and stays one.
func pmmNotUp(err error) (string, bool) {
	switch {
	case errors.Is(err, pmm.ErrUnreachable):
		return "Monitoring is not reachable yet.", true
	case errors.Is(err, pmm.ErrUnavailable):
		return "Monitoring is not answering queries yet.", true
	default:
		return "", false
	}
}

// clock returns the time source. A nil service is a deployment that never built
// one, which still has to answer pending rather than panic.
func (m *Metrics) clock() func() time.Time {
	if m == nil || m.now == nil {
		return time.Now
	}

	return m.now
}

// query runs one range query over the snapshot window.
func (m *Metrics) query(
	ctx context.Context,
	snapshot *MetricSnapshot,
	expr string,
	step time.Duration,
) ([]pmm.Series, error) {
	return m.client.QueryRange(ctx, pmm.RangeQuery{
		Query: expr,
		Start: snapshot.Start,
		End:   snapshot.End,
		Step:  step,
	})
}

// summarize turns the PMM answer into one chart. The expressions aggregate the
// members away, so the first series is the whole database; anything after it
// would be a query that stopped aggregating, and taking the first keeps the
// chart honest rather than mixing series.
func summarize(definition metricQuery, series []pmm.Series) MetricSeries {
	out := MetricSeries{Kind: definition.Kind, Unit: definition.Unit, Trend: TrendSteady}
	if len(series) == 0 || len(series[0].Samples) == 0 {
		return out
	}

	samples := series[0].Samples
	out.Points = make([]MetricPoint, 0, len(samples))
	out.Min = samples[0].Value
	out.Max = samples[0].Value

	total := 0.0
	for _, sample := range samples {
		out.Points = append(out.Points, MetricPoint{At: sample.At, Value: sample.Value})
		out.Min = min(out.Min, sample.Value)
		out.Max = max(out.Max, sample.Value)
		total += sample.Value
	}

	out.Current = samples[len(samples)-1].Value
	out.Average = total / float64(len(samples))
	out.Trend = trendOf(samples)

	return out
}

// trendOf compares the ends of a series. The threshold is relative, so a
// connection count moving from 2 to 3 reads as a trend while a CPU line
// wobbling by half a percent does not.
func trendOf(samples []pmm.Sample) string {
	if len(samples) < 2 {
		return TrendSteady
	}

	first := samples[0].Value
	last := samples[len(samples)-1].Value
	change := last - first
	scale := max(abs(first), abs(last))

	if scale == 0 || abs(change) < scale*trendThreshold {
		return TrendSteady
	}
	if change > 0 {
		return TrendUp
	}

	return TrendDown
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}

	return value
}

// pending fills a snapshot with empty charts and the reason there is nothing to
// draw yet.
func pending(snapshot *MetricSnapshot, kinds []MetricKind, reason string) *MetricSnapshot {
	snapshot.Pending = true
	snapshot.PendingReason = reason
	snapshot.Series = make([]MetricSeries, 0, len(kinds))

	for _, kind := range kinds {
		definition, ok := queryFor(kind)
		if !ok {
			continue
		}

		snapshot.Series = append(snapshot.Series, MetricSeries{
			Kind:    definition.Kind,
			Unit:    definition.Unit,
			Pending: true,
			Trend:   TrendSteady,
		})
	}

	return snapshot
}

// windowShape returns the span, the step between points, and the rate window
// for a window. The rate window is wider than the step so a counter rate has
// enough samples to be stable.
func windowShape(window MetricWindow) (span, step time.Duration, rate string) {
	if window == MetricWindowDay {
		return 24 * time.Hour, 15 * time.Minute, "30m"
	}

	return time.Hour, time.Minute, "5m"
}

func queryFor(kind MetricKind) (metricQuery, bool) {
	for _, definition := range metricQueries {
		if definition.Kind == kind {
			return definition, true
		}
	}

	return metricQuery{}, false
}

// AllMetricKinds returns every kind in the order the Metrics tab renders them.
func AllMetricKinds() []MetricKind {
	kinds := make([]MetricKind, 0, len(metricQueries))
	for _, definition := range metricQueries {
		kinds = append(kinds, definition.Kind)
	}

	return kinds
}

// ParseMetricWindow reads the `window` query parameter. Blank means the default
// the Overview minis use.
func ParseMetricWindow(raw string) (MetricWindow, error) {
	switch MetricWindow(strings.TrimSpace(raw)) {
	case "", MetricWindowHour:
		return MetricWindowHour, nil
	case MetricWindowDay:
		return MetricWindowDay, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownMetricWindow, raw)
	}
}

// ParseMetricKinds reads the `metrics` query parameter, a comma separated list.
// Blank means every kind, which is what the Metrics tab asks for.
func ParseMetricKinds(raw string) ([]MetricKind, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return AllMetricKinds(), nil
	}

	parts := strings.Split(trimmed, ",")
	kinds := make([]MetricKind, 0, len(parts))

	for _, part := range parts {
		kind := MetricKind(strings.TrimSpace(part))
		if _, ok := queryFor(kind); !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownMetric, part)
		}

		kinds = append(kinds, kind)
	}

	return kinds, nil
}

// expand fills the placeholders in an expression. Only the adapter writes
// expressions; the only values that come from outside are the database name
// and its namespace, and labelValue has already checked both.
func expand(expr, namespace, cluster, rate, dataDir string) string {
	return strings.NewReplacer(
		"$cluster", cluster,
		"$members", memberPattern(namespace, cluster),
		"$rate", rate,
		"$datadir", dataDir,
	).Replace(expr)
}

// memberPattern matches the node_name PMM 3 gives a member pod of one database:
// the namespace, a hyphen, then the pod name, which is <database>-pxc-<n> for a
// PXC member, <database>-<instance set>-<suffix>-<n> for a PostgreSQL one and
// <database>-rs0-<n> for a MongoDB one.
// It is anchored at both ends and starts at the namespace, so orders does not
// match the pods of my-orders, or of an orders in another namespace.
//
// The member suffix, and why its random part is held to four characters, is
// hub.MemberNodeSuffix: the alert rules derive the database from the same name.
//
// The names are regex literals, so a dot labelValue lets through matches only
// a dot. The backslashes QuoteMeta adds are doubled because the pattern sits
// inside a PromQL string.
func memberPattern(namespace, cluster string) string {
	literal := func(name string) string {
		return strings.ReplaceAll(regexp.QuoteMeta(name), `\`, `\\`)
	}

	return "^" + literal(namespace) + "-" + literal(cluster) + hub.MemberNodeSuffix + "$"
}

// labelValue checks that a name can be pasted into a label matcher, reporting
// the sentinel the calling feature maps onto a message. Instance names are
// DNS-1123, so this rejects nothing a real database has, and it stops a crafted
// id from rewriting the query around it.
func labelValue(name string, invalid error) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("%w: name must not be blank", invalid)
	}
	if len(trimmed) > 253 {
		return "", fmt.Errorf("%w: name is longer than 253 characters", invalid)
	}

	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '.', r == '_':
		default:
			return "", fmt.Errorf("%w: %q is not a label value", invalid, name)
		}
	}

	return trimmed, nil
}
