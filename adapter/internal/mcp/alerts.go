package mcp

import (
	"context"

	"github.com/google/jsonschema-go/jsonschema"
)

const (
	defaultAlertLimit = 25
	maxAlertLimit     = 100
)

type alert struct {
	ID          string `json:"id"`
	Environment string `json:"environment"`
	DatabaseID  string `json:"databaseId,omitempty" jsonschema:"The database the alert is about. Absent when it maps to none."`
	Rule        string `json:"rule"`
	Severity    string `json:"severity"`
	Summary     string `json:"summary"`
	StartsAt    string `json:"startsAt"`
}

// alertEnvironment says whether an environment's alerts can be trusted.
type alertEnvironment struct {
	Name   string `json:"name"`
	State  string `json:"state" jsonschema:"One of live, stale or off. Only live means monitoring was read recently, so only then does an empty list mean nothing is firing."`
	Reason string `json:"reason,omitempty" jsonschema:"Why the alerts cannot be trusted. Present unless state is live."`
	ReadAt string `json:"readAt,omitempty" jsonschema:"When monitoring was last read. Absent when the environment never reported."`
	Total  int    `json:"total" jsonschema:"How many alerts are firing in the environment."`
	Shown  int    `json:"shown" jsonschema:"How many of them the control plane lists. Fewer than total means some were cut."`
	Note   string `json:"note,omitempty"`
}

type alerts struct {
	Environments []alertEnvironment `json:"environments"`
	Alerts       []alert            `json:"alerts" jsonschema:"Critical first, then oldest first."`
	Criticals    int                `json:"criticals" jsonschema:"How many critical alerts the control plane listed, before the limit."`
	Total        int                `json:"total" jsonschema:"How many alerts the control plane listed, before the limit."`
	Truncated    bool               `json:"truncated" jsonschema:"True when the limit left alerts out."`
}

type alertsInput struct {
	Limit *int `json:"limit,omitempty" jsonschema:"How many alerts to return, from 1 to 100. Defaults to 25."`
}

type databaseAlerts struct {
	DatabaseID  string           `json:"databaseId"`
	Environment alertEnvironment `json:"environment" jsonschema:"The environment the database runs in, so an empty list can be told apart from an unreadable one."`
	Alerts      []alert          `json:"alerts" jsonschema:"Critical first, then oldest first."`
}

func alertReads() []tool {
	return []tool{
		define(spec{
			name:  "list_alerts",
			title: "List alerts",
			description: "List the alerts firing on every environment, critical first, with the state of each environment's monitoring. " +
				"An empty list means nothing is firing only for an environment whose state is live: read environments before saying all is well. " +
				"limit is 1 to 100 (default 25). Each alert names its database when it maps to one: use get_database for details.",
			annotations: readOnly(),
			tune: func(input, _ *jsonschema.Schema) {
				bounds(input.Properties["limit"], 1, maxAlertLimit)
			},
		}, listAlerts),

		define(spec{
			name:  "get_database_alerts",
			title: "Get database alerts",
			description: "Get the alerts firing on one database, critical first, with the state of the environment it runs in. " +
				"An empty list means nothing is firing only when the environment state is live.",
			annotations: readOnly(),
		}, getDatabaseAlerts),
	}
}

func listAlerts(ctx context.Context, c *call, in alertsInput) (alerts, error) {
	var out alerts
	if err := c.get(ctx, "/api/alerts", nil, &out); err != nil {
		return alerts{}, err
	}

	out.Environments = orEmpty(out.Environments)
	out.Alerts = orEmpty(out.Alerts)
	out.Total = len(out.Alerts)
	if limit := limitOr(in.Limit, defaultAlertLimit); len(out.Alerts) > limit {
		out.Alerts, out.Truncated = out.Alerts[:limit], true
	}

	return out, nil
}

func getDatabaseAlerts(ctx context.Context, c *call, in databaseInput) (databaseAlerts, error) {
	path, err := databasePath(in.ID)
	if err != nil {
		return databaseAlerts{}, err
	}

	var out databaseAlerts
	if err := c.get(ctx, "/api/databases/"+path+"/alerts", nil, &out); err != nil {
		return databaseAlerts{}, err
	}
	out.Alerts = orEmpty(out.Alerts)

	return out, nil
}
