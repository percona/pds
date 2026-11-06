package hub

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

type provisionedRule struct {
	UID          string            `json:"uid"`
	Title        string            `json:"title"`
	Condition    string            `json:"condition"`
	For          string            `json:"for"`
	NoDataState  string            `json:"noDataState"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	Data         []ruleQuery       `json:"data"`
	ExecErrState string            `json:"execErrState"`
}

type ruleQuery struct {
	RefID         string         `json:"refId"`
	DatasourceUID string         `json:"datasourceUid"`
	TimeRange     map[string]int `json:"relativeTimeRange"`
	Model         map[string]any `json:"model"`
}

type provisionedRules struct {
	APIVersion int `json:"apiVersion"`
	Groups     []struct {
		OrgID    int               `json:"orgId"`
		Name     string            `json:"name"`
		Folder   string            `json:"folder"`
		Interval string            `json:"interval"`
		Rules    []provisionedRule `json:"rules"`
	} `json:"groups"`
}

func readRules(t *testing.T) provisionedRules {
	t.Helper()
	files, err := pmmFiles("spoke-a", DefaultPMMSettings(), "1.9.1", true)
	require.NoError(t, err)

	var cm struct {
		Kind     string                `json:"kind"`
		Metadata struct{ Name string } `json:"metadata"`
		Data     map[string]string     `json:"data"`
	}
	require.NoError(t, yaml.Unmarshal(files[pmmDir("spoke-a")+"/alert-rules.yaml"], &cm))
	require.Equal(t, "ConfigMap", cm.Kind)
	require.Equal(t, "pds-alert-rules", cm.Metadata.Name)
	require.Len(t, cm.Data, 1)

	var out provisionedRules
	for _, body := range cm.Data {
		require.NoError(t, yaml.UnmarshalStrict([]byte(body), &out))
	}

	return out
}

func allRules(t *testing.T) map[string]provisionedRule {
	t.Helper()
	byID := map[string]provisionedRule{}
	for _, g := range readRules(t).Groups {
		for _, r := range g.Rules {
			byID[g.Name+"/"+r.Labels["pds_rule"]] = r
		}
	}

	return byID
}

func TestPMMFilesIncludeTheAlertRulesInTheKustomization(t *testing.T) {
	files, err := pmmFiles("spoke-a", DefaultPMMSettings(), "1.9.1", true)
	require.NoError(t, err)

	var index struct {
		Resources []string `json:"resources"`
	}
	require.NoError(t, yaml.Unmarshal(files[pmmDir("spoke-a")+"/kustomization.yaml"], &index))
	assert.Contains(t, index.Resources, "alert-rules.yaml")
}

func TestAlertRulesCoverBothEngines(t *testing.T) {
	rules := allRules(t)

	want := map[string]string{
		"MySQL/service_down":          "critical",
		"MySQL/disk_used":             "warning",
		"MySQL/replication_lag":       "warning",
		"MySQL/connections_used":      "warning",
		"PostgreSQL/service_down":     "critical",
		"PostgreSQL/disk_used":        "warning",
		"PostgreSQL/replication_lag":  "warning",
		"PostgreSQL/connections_used": "warning",
		"Nodes/node_down":             "critical",
	}
	assert.Len(t, rules, len(want))
	for id, severity := range want {
		rule, ok := rules[id]
		require.True(t, ok, "rule %s is missing", id)
		assert.Equal(t, severity, rule.Labels["severity"], id)
	}
}

func TestAlertRuleUIDsAreUnique(t *testing.T) {
	seen := map[string]string{}
	for id, rule := range allRules(t) {
		prev, dup := seen[rule.UID]
		assert.False(t, dup, "%s and %s share uid %s", id, prev, rule.UID)
		seen[rule.UID] = id
	}
	assert.Contains(t, seen, "pds-service_down", "the MySQL rule keeps its uid across the upgrade")
}

func TestPostgreSQLRulesReadPostgreSQLSeries(t *testing.T) {
	rules := allRules(t)
	expr := func(id string) string { return rules[id].Data[0].Model["expr"].(string) }

	assert.Contains(t, expr("PostgreSQL/service_down"), "pg_up")
	assert.Contains(t, expr("PostgreSQL/disk_used"), `mountpoint="/pgdata"`)
	assert.Contains(t, expr("PostgreSQL/replication_lag"), "pg_replication_lag_seconds")
	assert.Contains(t, expr("PostgreSQL/connections_used"), "pg_stat_activity_count")
	assert.Contains(t, expr("PostgreSQL/connections_used"), "pg_settings_max_connections")
	for id, rule := range rules {
		if strings.HasPrefix(id, "PostgreSQL/") {
			assert.NotContains(t, rule.Data[0].Model["expr"], "mysql", id)
		}
	}
}

func TestAlertRulesAreProvisionedIntoAFolderPMMKeepsReadOnly(t *testing.T) {
	doc := readRules(t)

	assert.Equal(t, 1, doc.APIVersion)
	require.NotEmpty(t, doc.Groups)
	for _, g := range doc.Groups {
		assert.Equal(t, 1, g.OrgID)
		assert.Equal(t, "PDS", g.Folder)
		assert.NotEmpty(t, g.Name)
		assert.NotEmpty(t, g.Interval)
	}
}

var (
	byClause     = regexp.MustCompile(`\bby \(([^)]*)\)`)
	labelReplace = regexp.MustCompile(`label_replace\(`)
	replaceArgs  = regexp.MustCompile(`"([a-z_]+)", "([^"]*)", "([a-z_]+)", "([^"]*)"\)`)
)

// outputLabels applies the label_replace calls in expr to a series that
// carries only node_name, the way PMM 3's node_exporter series arrive.
func outputLabels(t *testing.T, expr, nodeName string) map[string]string {
	t.Helper()
	labels := map[string]string{"node_name": nodeName}
	calls := replaceArgs.FindAllStringSubmatch(expr, -1)
	require.Len(t, calls, len(labelReplace.FindAllString(expr, -1)), "every label_replace is well formed")
	// The innermost call runs first, and its arguments are written first.
	for _, call := range calls {
		dst, replacement, src, pattern := call[1], call[2], call[3], call[4]
		re := regexp.MustCompile("^(?:" + pattern + ")$")
		m := re.FindStringSubmatchIndex(labels[src])
		if m == nil {
			continue
		}
		labels[dst] = string(re.ExpandString(nil, replacement, labels[src], m))
	}

	return labels
}

func TestEveryAlertRuleCanBeMappedToADatabase(t *testing.T) {
	members := map[string]string{
		"MySQL":      "db-orders-pxc-0",
		"PostgreSQL": "db-orders-instance1-x7k2-0",
		"Nodes":      "db-orders-instance1-x7k2-0",
	}
	for id, rule := range allRules(t) {
		query := rule.Data[0].Model["expr"].(string)

		by := byClause.FindStringSubmatch(query)
		require.NotNil(t, by, "%s must aggregate by labels", id)
		kept := strings.Split(strings.ReplaceAll(by[1], " ", ""), ",")
		assert.Contains(t, kept, "cluster", "%s must keep the cluster label", id)
		assert.Contains(t, kept, "namespace", "%s must keep the namespace label", id)

		// Node series carry no cluster or namespace, so the expression has to
		// derive them from the node name before it groups. Every PostgreSQL
		// expression does, because its series do not reliably carry a namespace.
		if strings.Contains(query, "node_name") {
			got := outputLabels(t, query, members[strings.Split(id, "/")[0]])
			assert.Equal(t, "orders", got["cluster"], "%s derives the database", id)
			assert.Equal(t, "db", got["namespace"], "%s derives the namespace", id)
		}
		if strings.HasPrefix(id, "PostgreSQL/") {
			assert.Contains(t, query, "node_name", "%s derives its labels from the node name", id)
		}
	}
}

func TestNodeDownMapsMembersOfBothEngines(t *testing.T) {
	query := allRules(t)["Nodes/node_down"].Data[0].Model["expr"].(string)

	for _, node := range []string{"db-orders-pxc-2", "db-orders-instance1-x7k2-0", "db-orders-instance-ab12-1"} {
		got := outputLabels(t, query, node)
		assert.Equal(t, "orders", got["cluster"], node)
		assert.Equal(t, "db", got["namespace"], node)
	}
}

func TestEveryAlertRuleHasAWellFormedPipeline(t *testing.T) {
	for id, rule := range allRules(t) {
		require.Len(t, rule.Data, 3, id)
		assert.Equal(t, "A", rule.Data[0].RefID, id)
		assert.Equal(t, pmmMetricsDatasourceUID, rule.Data[0].DatasourceUID, id)
		assert.Equal(t, "__expr__", rule.Data[1].DatasourceUID, id)
		assert.Equal(t, "__expr__", rule.Data[2].DatasourceUID, id)
		assert.Equal(t, rule.Data[2].RefID, rule.Condition, id)
		assert.NotEmpty(t, rule.UID, id)
		assert.NotEmpty(t, rule.Annotations["summary"], id)
		// A target that never reported must stay quiet, so no data reads as OK.
		assert.Equal(t, "OK", rule.NoDataState, id)
	}
}

func TestAlertRuleThresholdsMatchTheCatalog(t *testing.T) {
	rules := allRules(t)
	threshold := func(id string) any {
		model := rules[id].Data[2].Model
		conds := model["conditions"].([]any)
		evaluator := conds[0].(map[string]any)["evaluator"].(map[string]any)

		return evaluator["params"].([]any)[0]
	}

	assert.EqualValues(t, 1, threshold("MySQL/service_down"))
	assert.EqualValues(t, 1, threshold("Nodes/node_down"))
	assert.EqualValues(t, 85, threshold("MySQL/disk_used"))
	assert.EqualValues(t, 30, threshold("MySQL/replication_lag"))
	assert.EqualValues(t, 90, threshold("MySQL/connections_used"))
	assert.EqualValues(t, 1, threshold("PostgreSQL/service_down"))
	assert.EqualValues(t, 85, threshold("PostgreSQL/disk_used"))
	assert.EqualValues(t, 30, threshold("PostgreSQL/replication_lag"))
	assert.EqualValues(t, 90, threshold("PostgreSQL/connections_used"))
	assert.Contains(t, rules["MySQL/connections_used"].Data[0].Model["expr"], "max_connections")
	assert.Contains(t, rules["MySQL/disk_used"].Data[0].Model["expr"], "/var/lib/mysql")
}

func TestPMMHelmReleaseMountsTheRulesBesideTheChartConfigVolume(t *testing.T) {
	files, err := pmmFiles("spoke-a", DefaultPMMSettings(), "1.9.1", true)
	require.NoError(t, err)

	var doc struct {
		Spec struct {
			Values struct {
				ExtraVolumes []struct {
					Name      string         `json:"name"`
					EmptyDir  map[string]any `json:"emptyDir"`
					ConfigMap struct {
						Name string `json:"name"`
					} `json:"configMap"`
				} `json:"extraVolumes"`
				ExtraVolumeMounts []struct {
					Name      string `json:"name"`
					MountPath string `json:"mountPath"`
					ReadOnly  bool   `json:"readOnly"`
				} `json:"extraVolumeMounts"`
				PodAnnotations map[string]string `json:"podAnnotations"`
			} `json:"values"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(files[pmmDir("spoke-a")+"/helmrelease.yaml"], &doc))
	v := doc.Spec.Values

	volumes := map[string]string{}
	for _, vol := range v.ExtraVolumes {
		volumes[vol.Name] = vol.ConfigMap.Name
		if vol.Name == "pmm-config" {
			assert.NotNil(t, vol.EmptyDir, "the chart's config volume stays an emptyDir")
		}
	}
	assert.Contains(t, volumes, "pmm-config")
	assert.Equal(t, "pds-alert-rules", volumes["pds-alert-rules"])

	mounts := map[string]string{}
	for _, m := range v.ExtraVolumeMounts {
		mounts[m.Name] = m.MountPath
		if m.Name == "pds-alert-rules" {
			assert.True(t, m.ReadOnly)
		}
	}
	assert.Equal(t, "/usr/local/percona/pmm/config", mounts["pmm-config"])
	assert.Equal(t, "/usr/share/grafana/conf/provisioning/alerting", mounts["pds-alert-rules"])
	assert.NotEmpty(t, v.PodAnnotations["pds.percona.com/alert-rules"], "a rule change must roll PMM")
}

func TestPMMSettingsStillReadBackWithTheRulesMounted(t *testing.T) {
	s := DefaultPMMSettings()
	s.StorageGi = 20
	files, err := pmmFiles("spoke-a", s, "1.9.1", true)
	require.NoError(t, err)

	got, err := parsePMMSettings(files[pmmDir("spoke-a")+"/helmrelease.yaml"])
	require.NoError(t, err)
	assert.Equal(t, s, got)
}

// Grafana 12 loads alert rule strings as written, so a doubled dollar sign
// would reach the query as a literal $$1.
func TestAlertRulesFileKeepsDollarSignsSingle(t *testing.T) {
	body, err := pmmRulesYAML()
	require.NoError(t, err)

	text := string(body)
	assert.NotContains(t, text, "$$")
	assert.Contains(t, text, `"$1"`)
	assert.Contains(t, text, "{{ $labels.service_name }}")
	assert.Contains(t, text, "{{ $labels.node_name }}")
}

func TestAlertRuleTitlesAreUniqueAcrossTheFolder(t *testing.T) {
	seen := map[string]string{}
	for id, rule := range allRules(t) {
		prev, dup := seen[rule.Title]
		assert.False(t, dup, "%s and %s share the title %q in one folder", id, prev, rule.Title)
		seen[rule.Title] = id
	}
}

func TestOriginalAlertRuleUIDsAreKept(t *testing.T) {
	want := map[string]string{
		"MySQL/service_down":          "pds-service_down",
		"MySQL/disk_used":             "pds-disk_used",
		"MySQL/replication_lag":       "pds-replication_lag",
		"MySQL/connections_used":      "pds-connections_used",
		"Nodes/node_down":             "pds-node_down",
		"PostgreSQL/service_down":     "pds-postgresql-service_down",
		"PostgreSQL/disk_used":        "pds-postgresql-disk_used",
		"PostgreSQL/replication_lag":  "pds-postgresql-replication_lag",
		"PostgreSQL/connections_used": "pds-postgresql-connections_used",
	}
	rules := allRules(t)
	for id, uid := range want {
		assert.Equal(t, uid, rules[id].UID, id)
	}
}

func TestEveryAlertRuleFiresOnTheBadSide(t *testing.T) {
	rules := allRules(t)
	want := map[string]string{
		"service_down":     "lt",
		"node_down":        "lt",
		"disk_used":        "gt",
		"replication_lag":  "gt",
		"connections_used": "gt",
	}
	for id, rule := range rules {
		evaluator := rule.Data[2].Model["conditions"].([]any)[0].(map[string]any)["evaluator"].(map[string]any)
		assert.Equal(t, want[rule.Labels["pds_rule"]], evaluator["type"], id)
	}
}

func TestAlertRuleHoldTimesMatchTheCatalog(t *testing.T) {
	want := map[string]string{
		"service_down":     "2m",
		"node_down":        "2m",
		"disk_used":        "10m",
		"replication_lag":  "5m",
		"connections_used": "5m",
	}
	for id, rule := range allRules(t) {
		assert.Equal(t, want[rule.Labels["pds_rule"]], rule.For, id)
	}
}

// A mistyped series or a percentage that is really a ratio reads as no data,
// which reads as healthy, so the PostgreSQL expressions are pinned whole.
func TestPostgreSQLRuleExpressionsArePinned(t *testing.T) {
	rules := allRules(t)
	expr := func(id string) string { return rules[id].Data[0].Model["expr"].(string) }
	derive := func(selector string) string {
		return `label_replace(label_replace(` + selector +
			`, "namespace", "$1", "node_name", "([^-]+)-.+-(?:pxc-[0-9]+|instance[0-9]*-[a-z0-9]{4}-[0-9]+)")` +
			`, "cluster", "$1", "node_name", "[^-]+-(.+)-(?:pxc-[0-9]+|instance[0-9]*-[a-z0-9]{4}-[0-9]+)")`
	}

	assert.Equal(t, `min by (cluster, namespace, service_name) (`+derive(`pg_up`)+`)`,
		expr("PostgreSQL/service_down"))
	assert.Equal(t, `max by (cluster, namespace, service_name) (`+derive(`pg_replication_lag_seconds`)+`)`,
		expr("PostgreSQL/replication_lag"))
	assert.Equal(t, `max by (cluster, namespace, service_name) (100 * `+
		`sum by (cluster, namespace, service_name) (`+derive(`pg_stat_activity_count`)+`) / `+
		`max by (cluster, namespace, service_name) (`+derive(`pg_settings_max_connections`)+`))`,
		expr("PostgreSQL/connections_used"))
	assert.Equal(t, `max by (cluster, namespace, node_name) (`+derive(`100 * (1 - `+
		`node_filesystem_avail_bytes{mountpoint="/pgdata"} / `+
		`node_filesystem_size_bytes{mountpoint="/pgdata"})`)+`)`,
		expr("PostgreSQL/disk_used"))
}

// byDatabase must read what service.memberPattern reads: the same node names
// are members of the same database.
func TestByDatabaseDerivesTheDatabaseFromAMemberNodeName(t *testing.T) {
	cases := map[string][2]string{
		"db-orders-pxc-1":            {"db", "orders"},
		"db-orders-pxc-12":           {"db", "orders"},
		"db-orders-instance1-x7k2-0": {"db", "orders"},
		"db-orders-instance-ab12-1":  {"db", "orders"},
		"db-orders-instance2-pxc-0":  {"db", "orders-instance2"},
		"db-pxc-1-instance1-x7k2-0":  {"db", "pxc-1"},
	}
	for node, want := range cases {
		got := outputLabels(t, byDatabase("up"), node)
		assert.Equal(t, want[0], got["namespace"], node)
		assert.Equal(t, want[1], got["cluster"], node)
	}
}

func TestByDatabaseLeavesNonMembersAlone(t *testing.T) {
	for _, node := range []string{
		"pmm-server",
		"db-orders-haproxy-0",
		"db-orders-instance2-haproxy-0",
		"db-orders-instance1-x7k2",
		"db-orders-instance1-xyz-0",
		"db-orders-instance1-x7k2x-0",
		"db-orders-instance1-X7K2-0",
		"db-orders-pgbouncer-7d9c5b6f4-xk2lp",
	} {
		got := outputLabels(t, byDatabase("up"), node)
		assert.NotContains(t, got, "cluster", node)
		assert.NotContains(t, got, "namespace", node)
	}
}
