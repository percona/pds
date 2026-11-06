package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/client/pmm"
)

// Codes an environment agent sends when its PMM reading has no values. The agent
// sends a code rather than a sentence, so the words a customer reads come from
// the control plane they are looking at, whatever version the agent runs.
const (
	MonitoringNotConfigured = "not-configured"
	MonitoringUnreachable   = "unreachable"
	MonitoringUnavailable   = "unavailable"
	MonitoringNoTarget      = "no-target"
)

// MonitoringReading is the newest value of each chart for one database. A nil
// value is a series PMM had nothing for. Pending, when set, is one of the
// Monitoring codes and the values are then all nil.
type MonitoringReading struct {
	CPU            *float64 `json:"cpu,omitempty"`
	Memory         *float64 `json:"memory,omitempty"`
	Connections    *float64 `json:"connections,omitempty"`
	ReplicationLag *float64 `json:"replicationLag,omitempty"`
	Disk           *float64 `json:"disk,omitempty"`
	Pending        string   `json:"pending,omitempty"`
}

// value returns the reading for one chart.
func (r *MonitoringReading) value(kind MetricKind) *float64 {
	if r == nil {
		return nil
	}
	switch kind {
	case MetricCPU:
		return r.CPU
	case MetricMemory:
		return r.Memory
	case MetricConnections:
		return r.Connections
	case MetricReplicationLag:
		return r.ReplicationLag
	case MetricDisk:
		return r.Disk
	default:
		return nil
	}
}

func (r *MonitoringReading) set(kind MetricKind, value float64) {
	switch kind {
	case MetricCPU:
		r.CPU = &value
	case MetricMemory:
		r.Memory = &value
	case MetricConnections:
		r.Connections = &value
	case MetricReplicationLag:
		r.ReplicationLag = &value
	case MetricDisk:
		r.Disk = &value
	}
}

// latestSpan is how far back Latest looks. It covers a few scrapes, so one
// missed scrape still leaves a newest point to report.
const latestSpan = 5 * time.Minute

// Latest reads the newest value of every chart for one database, with the
// queries of its engine. A PMM that is missing, down or has no target is a
// reading with a Pending code rather than an error; only a PMM that answers
// with an error fails.
func (m *Metrics) Latest(ctx context.Context, database, namespace, engine string) (*MonitoringReading, error) {
	cluster, err := labelValue(database, ErrInvalidMetricTarget)
	if err != nil {
		return nil, err
	}
	namespace, err = labelValue(namespace, ErrInvalidMetricTarget)
	if err != nil {
		return nil, err
	}
	if m == nil || m.client == nil {
		return &MonitoringReading{Pending: MonitoringNotConfigured}, nil
	}

	queries := queriesFor(engine)
	_, step, rate := windowShape(MetricWindowHour)
	end := m.clock()()
	window := &MetricSnapshot{Start: end.Add(-latestSpan), End: end}

	probe, err := m.query(ctx, window, expand(queries.probe, namespace, cluster, rate, queries.dataDir), latestSpan)
	if err != nil {
		if code, ok := monitoringNotUp(err); ok {
			return &MonitoringReading{Pending: code}, nil
		}

		return nil, fmt.Errorf("metrics: probe %s: %w", database, err)
	}
	if len(probe) == 0 {
		return &MonitoringReading{Pending: MonitoringNoTarget}, nil
	}

	reading := &MonitoringReading{}
	for _, definition := range metricQueries {
		series, err := m.query(ctx, window, expand(queries.exprFor(definition), namespace, cluster, rate, queries.dataDir), step)
		if err != nil {
			if code, ok := monitoringNotUp(err); ok {
				return &MonitoringReading{Pending: code}, nil
			}

			return nil, fmt.Errorf("metrics: %s for %s: %w", definition.Kind, database, err)
		}
		if len(series) == 0 || len(series[0].Samples) == 0 {
			continue
		}
		samples := series[0].Samples
		reading.set(definition.Kind, samples[len(samples)-1].Value)
	}

	return reading, nil
}

func monitoringNotUp(err error) (string, bool) {
	switch {
	case errors.Is(err, pmm.ErrUnreachable):
		return MonitoringUnreachable, true
	case errors.Is(err, pmm.ErrUnavailable):
		return MonitoringUnavailable, true
	default:
		return "", false
	}
}

// monitoringUnknown is what a code this control plane does not know is stored
// as. It reads as no readings, which is true whatever the agent meant.
const monitoringUnknown = "unknown"

// monitoringCode is the code to store for what an agent sent. Only known codes
// are kept as sent; anything else becomes monitoringUnknown, so no agent can
// put text into the column that is too long or not valid UTF-8.
func monitoringCode(code string) string {
	switch code = strings.TrimSpace(code); code {
	case "", MonitoringNotConfigured, MonitoringUnreachable, MonitoringUnavailable, MonitoringNoTarget:
		return code
	default:
		return monitoringUnknown
	}
}

// monitoringPendingReason is the sentence for a code. A code this control plane
// does not know reads as no readings, which is true whatever the agent meant.
func monitoringPendingReason(code string) string {
	switch code {
	case MonitoringNotConfigured:
		return "Monitoring is not set up in this environment yet."
	case MonitoringUnreachable:
		return "Monitoring is not reachable in this environment yet."
	case MonitoringUnavailable:
		return "Monitoring is not serving readings in this environment yet."
	case MonitoringNoTarget:
		return "Monitoring has no target for this database yet."
	default:
		return noMonitoringReadings
	}
}

const noMonitoringReadings = "Monitoring has no readings for this database yet."

// Now is the service clock. A nil service reads the wall clock.
func (m *Metrics) Now() time.Time {
	return m.clock()()
}

// FromSamples is MetricsFromSamples at the service clock.
func (m *Metrics) FromSamples(points []UsagePoint, in MetricsRequest) (*MetricSnapshot, error) {
	return MetricsFromSamples(points, in, m.Now())
}

// MetricWindowSpan is how far back a window reaches.
func MetricWindowSpan(window MetricWindow) time.Duration {
	span, _, _ := windowShape(window)

	return span
}

// MetricsFromSamples builds the metrics answer out of the readings environment
// agents posted, which is how a control plane with no PMM of its own charts a
// database on another environment. The answer has the same shape ForDatabase
// gives, so the Metrics tab draws both the same way.
//
// The newest sample in the window decides whether the charts are pending: a
// database whose target went away is pending now even if it charted earlier.
func MetricsFromSamples(points []UsagePoint, in MetricsRequest, now time.Time) (*MetricSnapshot, error) {
	window, err := ParseMetricWindow(string(in.Window))
	if err != nil {
		return nil, err
	}
	kinds := in.Kinds
	if len(kinds) == 0 {
		kinds = AllMetricKinds()
	}

	span, _, _ := windowShape(window)
	snapshot := &MetricSnapshot{
		Window: window,
		Start:  now.Add(-span),
		End:    now,
		Step:   UsageInterval,
	}

	inWindow := make([]UsagePoint, 0, len(points))
	for _, point := range points {
		if point.At.Before(snapshot.Start) || point.At.After(now) {
			continue
		}
		inWindow = append(inWindow, point)
	}
	sort.SliceStable(inWindow, func(i, j int) bool { return inWindow[i].At.Before(inWindow[j].At) })

	if len(inWindow) == 0 {
		return pending(snapshot, kinds, noMonitoringReadings), nil
	}
	newest := inWindow[len(inWindow)-1].Monitoring
	if newest == nil {
		return pending(snapshot, kinds, monitoringPendingReason(MonitoringNotConfigured)), nil
	}
	if newest.Pending != "" {
		return pending(snapshot, kinds, monitoringPendingReason(newest.Pending)), nil
	}

	snapshot.Series = make([]MetricSeries, 0, len(kinds))
	for _, kind := range kinds {
		definition, ok := queryFor(kind)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownMetric, kind)
		}
		var samples []pmm.Sample
		for _, point := range inWindow {
			if value := point.Monitoring.value(kind); value != nil {
				samples = append(samples, pmm.Sample{At: point.At, Value: *value})
			}
		}
		var series []pmm.Series
		if len(samples) > 0 {
			series = []pmm.Series{{Samples: samples}}
		}
		snapshot.Series = append(snapshot.Series, summarize(definition, series))
	}

	return snapshot, nil
}
