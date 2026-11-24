package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

const (
	// pmmMetricsDatasourceUID is the uid PMM gives its Metrics datasource, the
	// VictoriaMetrics behind PMM. A provisioned rule names its datasource by uid.
	pmmMetricsDatasourceUID = "PA58DA793C7250F1B"
	// pmmRulesName names the ConfigMap, its volume and its pod annotation.
	pmmRulesName = "pds-alert-rules"
	// pmmRulesFile is the one file the ConfigMap holds.
	pmmRulesFile = "pds-rules.yaml"
	// pmmRulesMountPath is where Grafana reads alerting provisioning files. The
	// mount replaces that directory, so only the PDS rules load from it.
	pmmRulesMountPath = "/usr/share/grafana/conf/provisioning/alerting"
	// pmmRulesFolder is the read-only folder the rules appear in.
	pmmRulesFolder = "PDS"
	// MySQLDataDir is where the PXC provider mounts the data volume.
	MySQLDataDir = "/var/lib/mysql"
	// PostgreSQLDataDir is where the PostgreSQL provider mounts it.
	PostgreSQLDataDir = "/pgdata"
	// MongoDBDataDir is where the MongoDB provider mounts the data volume.
	MongoDBDataDir = "/data/db"
	// MongoDBReplicaSet is the replica set name the MongoDB provider gives a
	// database, which its member pods carry: <database>-rs0-<n>.
	MongoDBReplicaSet = "rs0"
	// MemberNodeSuffix is what a member's node_name ends with after the
	// database name, as a regex with no capture group: -pxc-<n> for a PXC
	// member, -<instance set>-<suffix>-<n> for a PostgreSQL one, -rs0-<n> for a
	// MongoDB one. The suffix is the four random characters the PostgreSQL
	// operator names the StatefulSet with. It is held to four so the pxc-0 and
	// haproxy-0 pods of a database named orders-instance2 do not read as
	// members of orders.
	MemberNodeSuffix = `-(?:pxc-[0-9]+|instance[0-9]*-[a-z0-9]{4}-[0-9]+|` + MongoDBReplicaSet + `-[0-9]+)`
)

// byDatabase derives the namespace and cluster labels from node_name for a
// series that does not carry them. PMM 3 labels node_exporter series only with
// node_name, which the providers set to <namespace>-<database> followed by
// MemberNodeSuffix. The namespace is the part before the first hyphen, so a
// namespace that contains a hyphen is mis-parsed: my-ns-db-pxc-0 reads as
// namespace my and cluster ns-db, and the series maps to no database.
func byDatabase(selector string) string {
	return `label_replace(label_replace(` + selector +
		`, "namespace", "$1", "node_name", "([^-]+)-.+` + MemberNodeSuffix + `")` +
		`, "cluster", "$1", "node_name", "[^-]+-(.+)` + MemberNodeSuffix + `")`
}

// pmmRule is one curated alert rule. Every expression keeps the cluster label,
// which is the database name, and the namespace label, so the control plane can
// map a firing alert to a database.
type pmmRule struct {
	// group is the rule group the rule loads in: an engine's name, or
	// pmmSharedGroup for a rule that reads series every engine reports.
	group     string
	id        string
	title     string
	severity  string
	hold      string
	expr      string
	op        string
	threshold float64
	summary   string
}

const (
	pmmMySQLGroup      = "MySQL"
	pmmPostgreSQLGroup = "PostgreSQL"
	pmmMongoDBGroup    = "MongoDB"
	// pmmSharedGroup holds the rules about the nodes under a database, which
	// look the same for every engine.
	pmmSharedGroup = "Nodes"
)

// pmmRuleCatalog is the rules of each engine and the shared ones.
// A rule id is the same across engines for the same failure, because the
// control plane picks a customer's action from the id.
var pmmRuleCatalog = []pmmRule{
	{
		group: pmmMySQLGroup, id: "service_down", title: "MySQL is down", severity: "critical", hold: "2m",
		expr:      `min by (cluster, namespace, service_name) (mysql_up)`,
		op:        "lt",
		threshold: 1,
		summary:   "MySQL is not responding on {{ $labels.service_name }}.",
	},
	{
		group: pmmSharedGroup, id: "node_down", title: "Database node is down", severity: "critical", hold: "2m",
		expr:      `min by (cluster, namespace, node_name) (` + byDatabase(`up{job=~"node_exporter.*"}`) + `)`,
		op:        "lt",
		threshold: 1,
		summary:   "The node {{ $labels.node_name }} stopped reporting.",
	},
	{
		group: pmmMySQLGroup, id: "disk_used", title: "Data volume is almost full", severity: "warning", hold: "10m",
		expr:      diskUsedExpr(MySQLDataDir),
		op:        "gt",
		threshold: 85,
		summary:   "The data volume on {{ $labels.node_name }} is over 85% full.",
	},
	{
		group: pmmMySQLGroup, id: "replication_lag", title: "Replication is behind", severity: "warning", hold: "5m",
		expr:      `max by (cluster, namespace, service_name) (mysql_slave_status_seconds_behind_master)`,
		op:        "gt",
		threshold: 30,
		summary:   "{{ $labels.service_name }} is more than 30 seconds behind.",
	},
	{
		group: pmmMySQLGroup, id: "connections_used", title: "Connections are nearly exhausted", severity: "warning", hold: "5m",
		expr: `max by (cluster, namespace, service_name) (100 * ` +
			`mysql_global_status_threads_connected / mysql_global_variables_max_connections)`,
		op:        "gt",
		threshold: 90,
		summary:   "{{ $labels.service_name }} uses over 90% of its allowed connections.",
	},
	{
		// PMM adds no namespace label of its own, so every PostgreSQL
		// expression derives both labels from node_name.
		group: pmmPostgreSQLGroup, id: "service_down", title: "PostgreSQL is down", severity: "critical", hold: "2m",
		expr:      `min by (cluster, namespace, service_name) (` + byDatabase(`pg_up`) + `)`,
		op:        "lt",
		threshold: 1,
		summary:   "PostgreSQL is not responding on {{ $labels.service_name }}.",
	},
	{
		group: pmmPostgreSQLGroup, id: "disk_used", title: "PostgreSQL data volume is almost full", severity: "warning", hold: "10m",
		expr:      diskUsedExpr(PostgreSQLDataDir),
		op:        "gt",
		threshold: 85,
		summary:   "The data volume on {{ $labels.node_name }} is over 85% full.",
	},
	{
		group: pmmPostgreSQLGroup, id: "replication_lag", title: "PostgreSQL replication is behind", severity: "warning", hold: "5m",
		expr:      `max by (cluster, namespace, service_name) (` + byDatabase(`pg_replication_lag_seconds`) + `)`,
		op:        "gt",
		threshold: 30,
		summary:   "{{ $labels.service_name }} is more than 30 seconds behind.",
	},
	{
		group: pmmPostgreSQLGroup, id: "connections_used", title: "PostgreSQL connections are nearly exhausted", severity: "warning", hold: "5m",
		expr: `max by (cluster, namespace, service_name) (100 * ` +
			`sum by (cluster, namespace, service_name) (` + byDatabase(`pg_stat_activity_count`) + `) / ` +
			`max by (cluster, namespace, service_name) (` + byDatabase(`pg_settings_max_connections`) + `))`,
		op:        "gt",
		threshold: 90,
		summary:   "{{ $labels.service_name }} uses over 90% of its allowed connections.",
	},
	{
		// mongodb_up is 1 while the exporter reaches mongod. Like PostgreSQL's,
		// every expression derives its labels from node_name.
		group: pmmMongoDBGroup, id: "service_down", title: "MongoDB is down", severity: "critical", hold: "2m",
		expr:      `min by (cluster, namespace, service_name) (` + byDatabase(MongoDBUpMetric) + `)`,
		op:        "lt",
		threshold: 1,
		summary:   "MongoDB is not responding on {{ $labels.service_name }}.",
	},
	{
		group: pmmMongoDBGroup, id: "disk_used", title: "MongoDB data volume is almost full", severity: "warning", hold: "10m",
		expr:      diskUsedExpr(MongoDBDataDir),
		op:        "gt",
		threshold: 85,
		summary:   "The data volume on {{ $labels.node_name }} is over 85% full.",
	},
	{
		// A replica set of one member has no lag series, so the rule reads no
		// data there and stays quiet.
		group: pmmMongoDBGroup, id: "replication_lag", title: "MongoDB replication is behind", severity: "warning", hold: "5m",
		expr:      `max by (cluster, namespace, service_name) (` + byDatabase(MongoDBReplicationLagMetric) + `)`,
		op:        "gt",
		threshold: 30,
		summary:   "{{ $labels.service_name }} is more than 30 seconds behind.",
	},
	{
		group: pmmMongoDBGroup, id: "connections_used", title: "MongoDB connections are nearly exhausted", severity: "warning", hold: "5m",
		expr: `max by (cluster, namespace, service_name) (100 * ` +
			`sum by (cluster, namespace, service_name) (` +
			byDatabase(mongoDBConnections(MongoDBConnectionsOpen)) + `) / ` +
			`sum by (cluster, namespace, service_name) (` +
			byDatabase(mongoDBConnections(MongoDBConnectionsOpen, MongoDBConnectionsAvailable)) + `))`,
		op:        "gt",
		threshold: 90,
		summary:   "{{ $labels.service_name }} uses over 90% of its allowed connections.",
	},
}

// mongoDBConnections selects the connections series in the given states. The
// states are fixed names, never a customer's input.
func mongoDBConnections(states ...string) string {
	matcher := `="` + states[0] + `"`
	if len(states) > 1 {
		matcher = `=~"` + strings.Join(states, "|") + `"`
	}

	return MongoDBConnectionsMetric + `{` + MongoDBConnectionsStateLabel + matcher + `}`
}

// diskUsedExpr is the percentage of the data volume mounted at dir that is in
// use, per node.
func diskUsedExpr(dir string) string {
	return `max by (cluster, namespace, node_name) (` + byDatabase(`100 * (1 - `+
		`node_filesystem_avail_bytes{mountpoint="`+dir+`"} / `+
		`node_filesystem_size_bytes{mountpoint="`+dir+`"})`) + `)`
}

type grafanaRuleQuery struct {
	RefID             string         `json:"refId"`
	DatasourceUID     string         `json:"datasourceUid"`
	RelativeTimeRange map[string]int `json:"relativeTimeRange,omitempty"`
	Model             map[string]any `json:"model"`
}

type grafanaRule struct {
	UID          string             `json:"uid"`
	Title        string             `json:"title"`
	Condition    string             `json:"condition"`
	Data         []grafanaRuleQuery `json:"data"`
	For          string             `json:"for"`
	NoDataState  string             `json:"noDataState"`
	ExecErrState string             `json:"execErrState"`
	Labels       map[string]string  `json:"labels"`
	Annotations  map[string]string  `json:"annotations"`
}

type grafanaRuleGroup struct {
	OrgID    int           `json:"orgId"`
	Name     string        `json:"name"`
	Folder   string        `json:"folder"`
	Interval string        `json:"interval"`
	Rules    []grafanaRule `json:"rules"`
}

type grafanaRuleFile struct {
	APIVersion int                `json:"apiVersion"`
	Groups     []grafanaRuleGroup `json:"groups"`
}

// uid keeps the MySQL and shared rules' original uids, so an upgrade replaces
// them in place, and prefixes the PostgreSQL and MongoDB ones, whose ids repeat
// MySQL's.
func (r pmmRule) uid() string {
	switch r.group {
	case pmmPostgreSQLGroup:
		return "pds-postgresql-" + r.id
	case pmmMongoDBGroup:
		return "pds-mongodb-" + r.id
	}

	return "pds-" + r.id
}

// toGrafana renders a rule as the query, reduce and threshold steps Grafana
// evaluates. A rule fires only on a series that reports a bad value. A target
// that disappears leaves no series, and no data reads as OK on purpose: a
// presence check cannot tell a pod that vanished from a database PMM never
// monitored or one that was just deleted, and a false critical on either is
// worse than a late one. A pod that stops reporting is caught when its exporter
// answers 0, and monitoring that is not reporting is the heartbeat's to say.
func (r pmmRule) toGrafana() grafanaRule {
	return grafanaRule{
		UID:       r.uid(),
		Title:     r.title,
		Condition: "C",
		Data: []grafanaRuleQuery{
			{
				RefID:             "A",
				DatasourceUID:     pmmMetricsDatasourceUID,
				RelativeTimeRange: map[string]int{"from": 600, "to": 0},
				Model: map[string]any{
					"refId":         "A",
					"expr":          r.expr,
					"instant":       true,
					"intervalMs":    1000,
					"maxDataPoints": 43200,
				},
			},
			{
				RefID:         "B",
				DatasourceUID: "__expr__",
				Model: map[string]any{
					"refId":      "B",
					"type":       "reduce",
					"expression": "A",
					"reducer":    "last",
					"settings":   map[string]any{"mode": "dropNN"},
				},
			},
			{
				RefID:         "C",
				DatasourceUID: "__expr__",
				Model: map[string]any{
					"refId":      "C",
					"type":       "threshold",
					"expression": "B",
					"conditions": []any{map[string]any{
						"evaluator": map[string]any{"type": r.op, "params": []float64{r.threshold}},
					}},
				},
			},
		},
		For:          r.hold,
		NoDataState:  "OK",
		ExecErrState: "OK",
		Labels:       map[string]string{"pds_rule": r.id, "severity": r.severity},
		Annotations:  map[string]string{"summary": r.summary},
	}
}

// pmmRulesYAML is the Grafana alerting provisioning file for the curated
// MySQL, PostgreSQL and MongoDB rules. Provisioned rules are read-only in PMM. Dollar
// signs stay single: Grafana 12 does not expand them in alert rules, so
// doubling would load a literal $$1 into the expression.
func pmmRulesYAML() ([]byte, error) {
	var groups []grafanaRuleGroup
	for _, name := range []string{pmmMySQLGroup, pmmPostgreSQLGroup, pmmMongoDBGroup, pmmSharedGroup} {
		var rules []grafanaRule
		for _, r := range pmmRuleCatalog {
			if r.group == name {
				rules = append(rules, r.toGrafana())
			}
		}
		groups = append(groups, grafanaRuleGroup{
			OrgID:    1,
			Name:     name,
			Folder:   pmmRulesFolder,
			Interval: "1m",
			Rules:    rules,
		})
	}

	return yaml.Marshal(grafanaRuleFile{APIVersion: 1, Groups: groups})
}

// pmmRulesConfigMap wraps the rules file in the ConfigMap PMM mounts, and
// returns a short digest of the file. The digest goes on the pod, because
// Grafana reads provisioning files at start and a changed rule must roll PMM.
func pmmRulesConfigMap() (manifest []byte, digest string, err error) {
	body, err := pmmRulesYAML()
	if err != nil {
		return nil, "", fmt.Errorf("render alert rules: %w", err)
	}
	manifest, err = yaml.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": pmmRulesName},
		"data":       map[string]string{pmmRulesFile: string(body)},
	})
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)

	return manifest, hex.EncodeToString(sum[:8]), nil
}
