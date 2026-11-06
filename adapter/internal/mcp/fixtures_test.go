package mcp_test

// What the API answers, with the fields the tools drop left in: a database
// carries its Team on a list across Teams, an environment says whether it has
// monitoring, and a ticket names its account and contact.

const (
	orders = "spoke-a/pds.orders-dev"
	ticket = "CS0061234"

	activityID = "00000000000000000007"
)

func databaseFixture() map[string]any {
	return map[string]any{
		"id": orders, "name": "orders-dev", "engine": "mysql", "profile": "dev",
		"namespace": "pds", "environment": "spoke-a", "status": "ready", "message": "",
		"members": 1, "cpu": 1, "memoryGb": 2, "storageGb": 20,
		"createdAt": "2026-09-08T12:00:00Z", "lastBackupAt": "2026-09-08T13:00:00Z",
		"backupSchedule": map[string]any{
			"name": "daily", "enabled": true, "cron": "0 2 * * *", "retentionCopies": 7,
			"period": "daily", "minute": 0, "hour": 2,
		},
		"connection": map[string]any{
			"host": "orders-dev.pds.svc", "port": 3306, "username": "root",
			"maskedUri": "mysql://root:••••••••@orders-dev.pds.svc:3306", "secretName": "orders-dev-secret",
			"serviceName": "orders-dev",
		},
		"access": map[string]any{"external": false, "visibility": "cluster"},
		"team":   "default",
	}
}

func databaseListFixture() map[string]any {
	return map[string]any{"databases": []any{databaseFixture()}}
}

func backupsFixture() map[string]any {
	return map[string]any{"backups": []any{
		map[string]any{
			"id": "orders-dev-1", "status": "succeeded", "sizeBytes": 1048576, "scheduled": true,
			"createdAt": "2026-09-08T13:00:00Z", "finishedAt": "2026-09-08T13:05:00Z",
		},
	}}
}

func metricsFixture() map[string]any {
	return map[string]any{
		"databaseId": orders, "window": "1h", "start": "2026-09-08T11:00:00Z", "end": "2026-09-08T12:00:00Z",
		"stepSeconds": 60, "pending": false,
		"metrics": []any{map[string]any{
			"kind": "cpu", "unit": "cores", "pending": false, "current": 0.4, "min": 0.1, "max": 0.9, "average": 0.5,
			"trend":  "steady",
			"points": []any{map[string]any{"t": "2026-09-08T11:00:00Z", "v": 0.4}},
		}},
	}
}

func activityFixture() map[string]any {
	return map[string]any{
		"entries": []any{map[string]any{
			"id": "7", "at": "2026-09-08T12:00:00Z", "actor": "ann", "client": "mcp", "agent": "Claude Code", "requestId": "mcp-0a1b2c3d",
			"action": "database.create", "databaseId": orders, "target": "orders-dev", "outcome": "succeeded",
			"detail": "mysql, dev", "resourceKind": "database", "resourceName": "orders-dev", "team": "default",
		}},
		"cursor": "7", "truncated": false,
	}
}

func environmentsFixture() map[string]any {
	return map[string]any{"hub": true, "environments": []any{map[string]any{
		"name": "spoke-a", "namespace": "pds", "status": "connected", "lastSeen": "2026-09-08T12:00:00Z",
		"team": "default", "monitoring": true,
	}}}
}

func ticketListFixture() map[string]any {
	return map[string]any{
		"tickets": []any{map[string]any{
			"number": ticket, "id": "abc", "state": "Open", "priority": "3", "account": "Acme", "contact": "Ann",
			"subject": "Orders is slow", "technology": "MySQL", "openedAt": "2026-09-08T12:00:00Z",
			"updatedAt": "2026-09-08T12:30:00Z", "externalUrl": "https://example.invalid/x",
		}},
		"portalUrl": "https://support.example/portal",
	}
}

func ticketFixture() map[string]any {
	one := ticketListFixture()["tickets"].([]any)[0].(map[string]any)
	one["description"] = "It stalls at noon."
	one["comments"] = []any{map[string]any{"at": "2026-09-08T12:10:00Z", "author": "Percona", "label": "Support", "body": "Looking."}}
	one["portalUrl"] = "https://support.example/portal/CS0061234"

	return one
}

// The history and alert routes answer more than the tools pass on: the ids of
// a row's owner, the console's actions, the labels of an alert.

func historyFixture() map[string]any {
	return map[string]any{
		"entries": []any{
			map[string]any{
				"id": "00000000000000000009", "databaseId": orders, "environment": "spoke-a", "kind": "backup",
				"at": "2026-09-08T13:05:00Z", "outcome": "succeeded", "objectId": "orders-dev-1", "sizeBytes": 1048576,
				"startedAt": "2026-09-08T13:00:00Z", "finishedAt": "2026-09-08T13:05:00Z", "durationMs": 300000,
			},
			map[string]any{
				"id": "00000000000000000008", "databaseId": orders, "environment": "spoke-a", "kind": "transition",
				"at": "2026-09-08T12:30:00Z", "fromPhase": "pending", "toPhase": "ready", "message": "Ready.",
			},
			map[string]any{
				"id": "00000000000000000007", "databaseId": orders, "environment": "spoke-a", "kind": "action",
				"at": "2026-09-08T12:00:00Z", "actor": "ann", "client": "mcp", "agent": "Claude Code", "requestId": "mcp-0a1b2c3d",
				"action": "database.create", "target": "orders-dev", "outcome": "accepted", "detail": "mysql, dev",
				"resourceKind": "database", "resourceName": "orders-dev",
			},
		},
		"cursor": "00000000000000000007", "truncated": true,
	}
}

func usageFixture() map[string]any {
	return map[string]any{"samples": []any{
		map[string]any{
			"id": "00000000000000000002", "databaseId": orders, "environment": "spoke-a", "at": "2026-09-08T12:05:00Z",
			"cpuAvg": 0.25, "cpuMax": 0.5, "memoryAvg": 536870912, "memoryMax": 671088640, "storageBytes": 21474836480,
			"monitoring": map[string]any{"cpu": 0.3},
		},
		map[string]any{
			"id": "00000000000000000001", "databaseId": orders, "environment": "spoke-a", "at": "2026-09-08T12:00:00Z",
			"storageBytes": 21474836480, "metricsReason": "metrics-server did not answer",
		},
	}}
}

func environmentHistoryFixture() map[string]any {
	return map[string]any{"runs": []any{map[string]any{"runId": "run-1", "phases": []any{
		map[string]any{
			"environment": "spoke-a", "runId": "run-1", "phase": "preflight", "outcome": "ok",
			"startedAt": "2026-09-08T11:00:00Z", "finishedAt": "2026-09-08T11:00:02Z", "durationMs": 2000,
		},
		map[string]any{
			"environment": "spoke-a", "runId": "run-1", "phase": "install", "outcome": "failed",
			"startedAt": "2026-09-08T11:00:02Z", "finishedAt": "2026-09-08T11:01:00Z", "durationMs": 58000,
			"output": "the release did not become ready",
		},
	}}}}
}

func alertFixture(id, rule, severity, summary, startsAt, databaseID string) map[string]any {
	return map[string]any{
		"id": "spoke-a/" + id, "environment": "spoke-a", "databaseId": databaseID, "rule": rule, "severity": severity,
		"summary": summary, "startsAt": startsAt, "labels": map[string]any{"cluster": "orders-dev"},
		"action": map[string]any{"kind": "open_database", "label": "Open database"},
	}
}

func alertsFixture() map[string]any {
	return map[string]any{
		"environments": []any{
			map[string]any{
				"name": "spoke-a", "state": "live", "readAt": "2026-09-08T12:00:00Z", "total": 2, "shown": 2,
			},
			map[string]any{
				"name": "spoke-b", "state": "off", "pending": "not-configured", "readAt": nil, "total": 0, "shown": 0,
				"reason": "Monitoring is not set up on this environment.",
				"action": map[string]any{"kind": "set_up_monitoring", "label": "Set up monitoring"},
			},
		},
		"alerts": []any{
			alertFixture("f1", "service_down", "critical", "orders-dev is down", "2026-09-08T11:55:00Z", orders),
			alertFixture("f2", "disk_high", "warning", "Disk is 90% full", "2026-09-08T11:00:00Z", ""),
		},
		"criticals": 1,
	}
}

func databaseAlertsFixture() map[string]any {
	return map[string]any{
		"databaseId": orders,
		"environment": map[string]any{
			"name": "spoke-a", "state": "live", "readAt": "2026-09-08T12:00:00Z", "total": 2, "shown": 2,
		},
		"alerts": []any{alertFixture("f1", "service_down", "critical", "orders-dev is down", "2026-09-08T11:55:00Z", orders)},
	}
}

func activityDetailFixture() map[string]any {
	return map[string]any{
		"event": map[string]any{
			"id": activityID, "at": "2026-09-08T12:00:00Z", "actor": "ann", "client": "mcp", "agent": "Claude Code", "requestId": "mcp-0a1b2c3d",
			"action": "database.create", "databaseId": orders, "target": "orders-dev", "outcome": "accepted",
			"detail": "mysql, dev", "resourceKind": "database", "resourceName": "orders-dev", "team": "default",
		},
		"resource": map[string]any{
			"kind": "database", "name": "orders-dev", "namespace": "pds", "state": "ready",
			"startedAt": "2026-09-08T12:00:01Z", "endedAt": "2026-09-08T12:30:00Z",
		},
		"timeline": []any{
			map[string]any{"at": "2026-09-08T12:00:00Z", "source": "request", "type": "accepted", "message": "database.create"},
			map[string]any{
				"at": "2026-09-08T12:10:00Z", "source": "kubernetes", "type": "Normal", "reason": "Created",
				"message": "Created pod orders-dev-0", "count": 1,
			},
		},
		"notes": []any{"The database is ready."}, "truncated": false,
	}
}
