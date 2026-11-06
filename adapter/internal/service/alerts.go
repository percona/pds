package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

// States an environment's alerts can be in. Only live means PMM was read on
// the newest heartbeat, so a live environment with no alerts is the one case
// where nothing is wrong.
const (
	AlertsLive  = "live"
	AlertsStale = "stale"
	AlertsOff   = "off"
)

// Kinds of console action an alert or an unavailable environment names. The
// console turns a kind into a link; the label is what the person reads.
const (
	ActionOpenDatabase     = "open_database"
	ActionViewEnvironment  = "view_environment"
	ActionScaleStorage     = "scale_storage"
	ActionViewMetrics      = "view_metrics"
	ActionScaleResources   = "scale_resources"
	ActionRetryBackup      = "retry_backup"
	ActionSetUpMonitoring  = "set_up_monitoring"
	ActionOpenMonitoring   = "open_monitoring"
	ActionUpdateConnection = "update_connection"
)

// RuleBackupFailed is the one alert the control plane computes itself.
const RuleBackupFailed = "backup_failed"

// pendingOutdated marks an environment whose agent sent no alerts reading.
const pendingOutdated = "outdated"

// AlertAction is the one thing the console offers beside an alert.
type AlertAction struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// Alert is one firing alert in one environment.
type Alert struct {
	ID          string `json:"id"`
	Environment string `json:"environment"`
	// DatabaseID is empty for an alert that maps to no database.
	DatabaseID string            `json:"databaseId,omitempty"`
	Rule       string            `json:"rule"`
	Severity   string            `json:"severity"`
	Summary    string            `json:"summary"`
	StartsAt   time.Time         `json:"startsAt"`
	Labels     map[string]string `json:"labels"`
	Action     AlertAction       `json:"action"`
}

// AlertEnvironment says whether an environment's alerts can be trusted. Reason
// and Action are set whenever State is not live.
type AlertEnvironment struct {
	Name    string       `json:"name"`
	State   string       `json:"state"`
	Pending string       `json:"pending,omitempty"`
	Reason  string       `json:"reason,omitempty"`
	Action  *AlertAction `json:"action,omitempty"`
	// ReadAt is when monitoring was last read, or when the environment last
	// reported when it never carried a reading. Nil means it never reported.
	ReadAt *time.Time `json:"readAt"`
	// Total counts every firing alert the environment holds and Shown the ones
	// in the list, so Total above Shown means some were cut.
	Total int    `json:"total"`
	Shown int    `json:"shown"`
	Note  string `json:"note,omitempty"`
}

// AlertReport is every environment's state and firing alerts for one team.
type AlertReport struct {
	Environments []AlertEnvironment `json:"environments"`
	Alerts       []Alert            `json:"alerts"`
	// Criticals counts the critical alerts in Alerts, which feeds the sidebar.
	Criticals int `json:"criticals"`
}

// DatabaseAlerts is one database's alerts with the state of the environment it
// runs in, so an empty list can be told apart from an unreadable one.
type DatabaseAlerts struct {
	DatabaseID  string           `json:"databaseId"`
	Environment AlertEnvironment `json:"environment"`
	Alerts      []Alert          `json:"alerts"`
}

// Alerts is the firing alerts of every environment this team has.
func (d *Database) Alerts(ctx context.Context) (*AlertReport, error) {
	h, err := d.hubService(ctx)
	if err != nil {
		return nil, err
	}

	return BuildAlertReport(h.AlertSources(), h.Now()), nil
}

// DatabaseAlerts is the firing alerts of one database.
func (d *Database) DatabaseAlerts(ctx context.Context, id string) (*DatabaseAlerts, error) {
	h, err := d.hubService(ctx)
	if err != nil {
		return nil, err
	}
	sources := h.AlertSources()
	if !reportsDatabase(sources, id) {
		// A database still pending has no heartbeat entry yet. Get finds it
		// through its manifest, which exists only in the environment's own
		// namespace. The ID check also refuses a reported instance that carries
		// no namespace and so matched by name, under an id other than this one.
		inst, err := h.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if inst.ID != id {
			return nil, fmt.Errorf("%w: %q", engine.ErrNotFound, id)
		}
	}
	environment, _, _, err := hub.SplitID(id)
	if err != nil {
		return nil, err
	}
	report := BuildAlertReport(sources, h.Now())
	out := &DatabaseAlerts{DatabaseID: id, Alerts: []Alert{}}
	found := false
	for _, env := range report.Environments {
		if env.Name == environment {
			out.Environment, found = env, true
		}
	}
	if !found {
		return nil, fmt.Errorf("%w: %q", engine.ErrNotFound, id)
	}
	for _, alert := range report.Alerts {
		if alert.DatabaseID == id {
			out.Alerts = append(out.Alerts, alert)
		}
	}

	return out, nil
}

// reportsDatabase is whether any environment's last heartbeat lists a database
// with exactly this id. Same-named databases in one environment differ only by
// namespace, so the id is the only match that tells them apart.
func reportsDatabase(sources []hub.AlertSource, id string) bool {
	for _, src := range sources {
		for _, inst := range src.Beat.Instances {
			if inst.ID == id {
				return true
			}
		}
	}

	return false
}

// BuildAlertReport reads what each environment last reported. An environment
// whose heartbeat is stale contributes no alerts, because what it last said is
// not what is firing now. An environment whose monitoring could not be read
// still contributes backup_failed, which comes from its heartbeat.
func BuildAlertReport(sources []hub.AlertSource, now time.Time) *AlertReport {
	report := &AlertReport{Environments: []AlertEnvironment{}, Alerts: []Alert{}}
	for _, src := range sources {
		env, alerts := buildEnvironment(src, now)
		report.Environments = append(report.Environments, env)
		report.Alerts = append(report.Alerts, alerts...)
	}
	sort.SliceStable(report.Alerts, func(i, j int) bool {
		a, b := report.Alerts[i], report.Alerts[j]
		if ra, rb := severityRank(a.Severity), severityRank(b.Severity); ra != rb {
			return ra < rb
		}
		if !a.StartsAt.Equal(b.StartsAt) {
			return a.StartsAt.Before(b.StartsAt)
		}

		return a.ID < b.ID
	})
	for _, alert := range report.Alerts {
		if alert.Severity == hub.AlertCritical {
			report.Criticals++
		}
	}

	return report
}

func severityRank(severity string) int {
	switch severity {
	case hub.AlertCritical:
		return 0
	case hub.AlertWarning:
		return 1
	default:
		return 2
	}
}

func buildEnvironment(src hub.AlertSource, now time.Time) (AlertEnvironment, []Alert) {
	name := src.Environment.Name
	env := AlertEnvironment{Name: name}
	if src.Seen {
		readAt := src.Beat.At.UTC()
		if src.Beat.Alerts != nil && !src.Beat.Alerts.At.IsZero() {
			readAt = src.Beat.Alerts.At.UTC()
		}
		env.ReadAt = &readAt
	}

	if src.Environment.Status != hub.StatusConnected || !src.Seen {
		env.State = AlertsStale
		env.Reason, env.Action = staleCopy(name, src, now)

		return env, nil
	}

	var alerts []Alert
	reading := src.Beat.Alerts
	switch {
	case reading == nil:
		env.State, env.Pending = AlertsOff, pendingOutdated
	case reading.Pending != "":
		env.State, env.Pending = AlertsOff, monitoringCode(reading.Pending)
	default:
		env.State = AlertsLive
	}
	if env.State == AlertsOff {
		env.Reason, env.Action = offCopy(name, env.Pending)
	}
	if env.State == AlertsLive {
		env.Total = reading.Total
		if env.Total < len(reading.Items) {
			env.Total = len(reading.Items)
		}
		for _, item := range reading.Items {
			alerts = append(alerts, firingAlert(name, item, src.Beat.Instances))
		}
	}
	backups := failedBackups(name, src.Beat)
	alerts = append(alerts, backups...)
	env.Shown = len(alerts)
	env.Total += len(backups)
	if env.Total > env.Shown {
		env.Note = fmt.Sprintf(alertCopy("truncated"), env.Shown, env.Total, name)
	}

	return env, alerts
}

func firingAlert(environment string, item hub.HeartbeatAlert, instances []engine.Instance) Alert {
	labels := item.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	databaseID := mapToDatabase(labels, instances)

	return Alert{
		ID:          environment + "/" + item.Fingerprint,
		Environment: environment,
		DatabaseID:  databaseID,
		Rule:        item.Rule,
		Severity:    item.Severity,
		Summary:     item.Summary,
		StartsAt:    item.StartsAt.UTC(),
		Labels:      labels,
		Action:      ruleAction(item.Rule, databaseID != ""),
	}
}

// mapToDatabase finds the database an alert is about by the environment label,
// which carries the database name, and the namespace label. An alert with no
// namespace maps only when exactly one database has that name, so it is never
// pinned on the wrong one.
func mapToDatabase(labels map[string]string, instances []engine.Instance) string {
	name := labels["cluster"]
	if name == "" {
		return ""
	}
	namespace := labels["namespace"]
	var found []string
	for _, inst := range instances {
		if inst.Name == name && (namespace == "" || inst.Namespace == namespace) {
			found = append(found, inst.ID)
		}
	}
	if len(found) != 1 {
		return ""
	}

	return found[0]
}

// failedBackups is one backup_failed alert for each database whose newest
// finished backup failed. A backup still running does not clear it: the last
// answer is the one that matters until a newer one finishes.
//
// A backup names its database but not its namespace, so databases that share a
// name in one environment cannot be told apart. For those, any failed finished
// backup of that name raises the alert on each of them: a success belonging to
// a sibling must never hide a failure, so the alert may outlast a retry until
// the failed backup leaves the heartbeat.
func failedBackups(environment string, beat hub.Heartbeat) []Alert {
	named := map[string]int{}
	for _, inst := range beat.Instances {
		named[inst.Name]++
	}
	var out []Alert
	for _, inst := range beat.Instances {
		var newest *engine.Backup
		var newestAt time.Time
		var newestFailed *engine.Backup
		var newestFailedAt time.Time
		for i := range beat.Backups {
			b := &beat.Backups[i]
			if b.Instance != inst.Name || (b.Status != engine.BackupSucceeded && b.Status != engine.BackupFailed) {
				continue
			}
			at := b.FinishedAt
			if at.IsZero() {
				at = b.CreatedAt
			}
			if newest == nil || at.After(newestAt) {
				newest, newestAt = b, at
			}
			if b.Status == engine.BackupFailed && (newestFailed == nil || at.After(newestFailedAt)) {
				newestFailed, newestFailedAt = b, at
			}
		}
		failed, failedAt := newest, newestAt
		if named[inst.Name] > 1 {
			failed, failedAt = newestFailed, newestFailedAt
		}
		if failed == nil || failed.Status != engine.BackupFailed {
			continue
		}
		out = append(out, Alert{
			ID:          environment + "/" + RuleBackupFailed + "/" + inst.ID,
			Environment: environment,
			DatabaseID:  inst.ID,
			Rule:        RuleBackupFailed,
			Severity:    hub.AlertWarning,
			Summary:     fmt.Sprintf(alertCopy("backup-failed"), inst.Name),
			StartsAt:    failedAt.UTC(),
			Labels:      map[string]string{"cluster": inst.Name, "namespace": inst.Namespace},
			Action:      ruleAction(RuleBackupFailed, true),
		})
	}

	return out
}

// ruleAction is the console action for a rule. A rule about one database
// falls back to its environment when the alert maps to none.
func ruleAction(rule string, mapped bool) AlertAction {
	if rule == "node_down" || !mapped {
		return AlertAction{Kind: ActionViewEnvironment, Label: alertCopy("view-environment")}
	}
	switch rule {
	case "disk_used":
		return AlertAction{Kind: ActionScaleStorage, Label: alertCopy("scale-storage")}
	case "replication_lag":
		return AlertAction{Kind: ActionViewMetrics, Label: alertCopy("view-metrics")}
	case "connections_used":
		return AlertAction{Kind: ActionScaleResources, Label: alertCopy("scale-resources")}
	case RuleBackupFailed:
		return AlertAction{Kind: ActionRetryBackup, Label: alertCopy("retry-backup")}
	default:
		return AlertAction{Kind: ActionOpenDatabase, Label: alertCopy("open-database")}
	}
}

func staleCopy(name string, src hub.AlertSource, now time.Time) (string, *AlertAction) {
	action := &AlertAction{Kind: ActionViewEnvironment, Label: alertCopy("view-environment")}
	if !src.Seen {
		return fmt.Sprintf(alertCopy("never-reported"), name), action
	}

	return fmt.Sprintf(alertCopy("stale"), name, elapsed(now.Sub(src.Beat.At))), action
}

func offCopy(name, pending string) (string, *AlertAction) {
	switch pending {
	case MonitoringNotConfigured:
		return fmt.Sprintf(alertCopy("not-set-up"), name),
			&AlertAction{Kind: ActionSetUpMonitoring, Label: alertCopy("set-up-monitoring")}
	case MonitoringUnreachable:
		return fmt.Sprintf(alertCopy("not-ready"), name),
			&AlertAction{Kind: ActionOpenMonitoring, Label: alertCopy("open-monitoring")}
	case pendingOutdated:
		return fmt.Sprintf(alertCopy("outdated"), name),
			&AlertAction{Kind: ActionUpdateConnection, Label: alertCopy("update-connection")}
	default:
		return fmt.Sprintf(alertCopy("not-serving"), name),
			&AlertAction{Kind: ActionOpenMonitoring, Label: alertCopy("open-monitoring")}
	}
}

// alertCopy is the words a customer reads for an alert state, an action and
// the notes around them. The %s and %d are filled by the caller.
func alertCopy(key string) string {
	switch key {
	case "not-set-up":
		return "Alerts are off in %s because monitoring is not set up. Set up monitoring for this environment."
	case "not-ready":
		return "Alerts are off in %s because monitoring is not ready yet. Check again in a minute."
	case "not-serving":
		return "Alerts are off in %s because monitoring is not serving alerts. Open monitoring to see why."
	case "outdated":
		return "Alerts are off in %s because it has not sent alerts yet. Update the connection to this environment."
	case "stale":
		return "Alerts are not current in %s because it has not reported in %s. Check that the Kubernetes cluster is running."
	case "never-reported":
		return "Alerts are not available in %s because it has not reported in yet. Finish connecting it to see alerts."
	case "truncated":
		return "Showing %d of %d alerts in %s. Open monitoring for the rest."
	case "backup-failed":
		return "The newest backup of %s failed."
	case "open-database":
		return "Open database"
	case "view-environment":
		return "View environment"
	case "scale-storage":
		return "Scale storage"
	case "view-metrics":
		return "View metrics"
	case "scale-resources":
		return "Scale resources"
	case "retry-backup":
		return "Retry backup"
	case "set-up-monitoring":
		return "Set up monitoring"
	case "open-monitoring":
		return "Open monitoring"
	case "update-connection":
		return "Update connection"
	default:
		return ""
	}
}

// elapsed is a duration a customer reads: whole minutes, hours or days.
func elapsed(d time.Duration) string {
	unit := func(n int, word string) string {
		if n == 1 {
			return "1 " + word
		}

		return fmt.Sprintf("%d %ss", n, word)
	}
	switch {
	case d < time.Hour:
		return unit(max(int(d/time.Minute), 1), "minute")
	case d < 48*time.Hour:
		return unit(int(d/time.Hour), "hour")
	default:
		return unit(int(d/(24*time.Hour)), "day")
	}
}
