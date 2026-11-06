package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/percona/pds/adapter/internal/client/pmm"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/service"
)

const (
	// maxAlerts bounds what one beat carries. The heartbeat body is capped at
	// 1 MiB, and an environment with more firing alerts than this has a
	// problem the first screenful already shows.
	maxAlerts = 200
	// maxAlertsBytes bounds the encoded size of the items in one beat, well
	// under the body limit so the instances in the same beat keep their room.
	maxAlertsBytes = 400 << 10
	// maxLabelValue and maxSummary keep one alert small whatever a rule
	// writes into its labels and annotations.
	maxLabelValue = 200
	maxSummary    = 300
)

// alertLabels are the labels an alert keeps when it leaves the environment. A
// customer's own rules can put anything in a label, so the rest stay behind.
var alertLabels = []string{
	"alertname", "pds_rule", "severity", "cluster", "namespace",
	"node_name", "service_name", "instance",
}

// readAlerts reads the environment's firing alerts. A PMM that cannot be read
// is a pending reason on the beat and never an error, so the heartbeat that
// carries the instances still goes out.
func readAlerts(ctx context.Context, opts Options, now time.Time) *hub.HeartbeatAlerts {
	out := &hub.HeartbeatAlerts{At: now, Items: []hub.HeartbeatAlert{}}
	if opts.Alerts == nil {
		out.Pending = service.MonitoringNotConfigured

		return out
	}
	list, err := opts.Alerts(ctx)
	if err != nil {
		opts.Logger.WarnContext(ctx, "alerts read failed", "cause", err.Error())
		out.Pending = service.MonitoringUnavailable
		if errors.Is(err, pmm.ErrUnreachable) {
			out.Pending = service.MonitoringUnreachable
		}

		return out
	}

	sorted := append([]pmm.Alert(nil), list.Items...)
	sort.Slice(sorted, func(i, j int) bool { return pmm.AlertBefore(sorted[i], sorted[j]) })
	items := make([]hub.HeartbeatAlert, 0, len(sorted))
	for _, alert := range sorted {
		items = append(items, alertItem(alert))
	}
	out.Total = list.Total
	out.Items = withinBudget(items)

	return out
}

// withinBudget keeps the leading items that fit maxAlerts and maxAlertsBytes.
// The budget counts the JSON a beat carries, where & < and > weigh six bytes
// each, so it holds whatever characters a rule writes.
func withinBudget(items []hub.HeartbeatAlert) []hub.HeartbeatAlert {
	var size int
	for i, item := range items {
		if i == maxAlerts {
			return items[:i]
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return items[:i]
		}
		size += len(encoded) + 1
		if size > maxAlertsBytes {
			return items[:i]
		}
	}

	return items
}

func alertItem(alert pmm.Alert) hub.HeartbeatAlert {
	labels := make(map[string]string, len(alertLabels))
	for _, key := range alertLabels {
		if value, ok := alert.Labels[key]; ok {
			labels[key] = truncate(value, maxLabelValue)
		}
	}
	rule := alert.Labels["pds_rule"]
	if rule == "" {
		rule = alert.Labels["alertname"]
	}
	summary := alert.Annotations["summary"]
	if summary == "" {
		summary = alert.Labels["alertname"]
	}

	return hub.HeartbeatAlert{
		Fingerprint: alert.Fingerprint,
		Rule:        truncate(rule, maxLabelValue),
		Severity:    mapSeverity(alert.Labels["severity"]),
		Summary:     truncate(summary, maxSummary),
		StartsAt:    alert.StartsAt.UTC(),
		Labels:      labels,
	}
}

func mapSeverity(label string) string {
	switch pmm.SeverityRank(label) {
	case pmm.RankCritical:
		return hub.AlertCritical
	case pmm.RankInfo:
		return hub.AlertInfo
	default:
		return hub.AlertWarning
	}
}

// truncate cuts to n bytes on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}

	return s[:n]
}
