package api_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A MongoDB rule keeps the cluster and namespace labels, which carry the
// database name, so two MongoDB databases in one environment are told apart and
// each alert gets the action every engine's alert gets.
func TestMongoDBAlertsMapToTheirOwnDatabaseWithTheSameActions(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")
	at := lab.now
	orders := map[string]string{"cluster": "orders", "namespace": "pds", "service_name": "orders-rs0-0"}
	reports := map[string]string{"cluster": "reports", "namespace": "pds", "node_name": "pds-reports-rs0-1"}
	lab.beat("prod-eu", []string{"orders", "reports"}, nil, liveAlerts(at,
		firing("o-down", "service_down", "critical", at.Add(-time.Minute), orders),
		firing("r-disk", "disk_used", "warning", at.Add(-time.Minute), reports),
		firing("r-lag", "replication_lag", "warning", at.Add(-time.Minute), reports),
		firing("o-conn", "connections_used", "warning", at.Add(-time.Minute), orders),
	))

	body := lab.all(t)

	byRule := map[string]alertRow{}
	for _, a := range body.Alerts {
		byRule[a.Rule] = a
	}
	require.Len(t, byRule, 4)
	assert.Equal(t, "prod-eu/pds.orders", byRule["service_down"].DatabaseID)
	assert.Equal(t, "Open database", byRule["service_down"].Action.Label)
	assert.Equal(t, "prod-eu/pds.reports", byRule["disk_used"].DatabaseID)
	assert.Equal(t, "Scale storage", byRule["disk_used"].Action.Label)
	assert.Equal(t, "prod-eu/pds.reports", byRule["replication_lag"].DatabaseID)
	assert.Equal(t, "View metrics", byRule["replication_lag"].Action.Label)
	assert.Equal(t, "prod-eu/pds.orders", byRule["connections_used"].DatabaseID)
	assert.Equal(t, "Scale resources", byRule["connections_used"].Action.Label)
}
