package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestChatToolLabelNamesWhatTheToolDoes(t *testing.T) {
	for tool, label := range map[string]string{
		"list_databases": "Listing databases", "get_database": "Reading status", "get_metrics": "Reading metrics",
		"list_backups": "Reading backups", "get_database_history": "Reading history", "get_database_usage": "Reading usage",
		"get_environment_history": "Reading environment history", "list_environments": "Listing environments",
		"list_activity": "Reading activity", "get_activity": "Reading activity",
		"list_alerts": "Reading alerts", "get_database_alerts": "Reading alerts",
		"list_support_tickets": "Listing tickets", "get_support_ticket": "Reading a ticket",
		"wait_for_database": "Waiting", "wait_for_backup": "Waiting", "wait_for_environment": "Waiting",
		"get_environment": "Reading an environment", "list_cloud_connections": "Reading AWS accounts", "get_cloud_connection": "Reading AWS accounts",
		"something_new": "Reading data",
	} {
		assert.Equal(t, label, chatToolLabel(tool), tool)
	}
}
