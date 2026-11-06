package mcp

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

const (
	defaultActivityLimit = 50
	maxActivityLimit     = 500
)

type metricPoint struct {
	T string  `json:"t"`
	V float64 `json:"v"`
}

type metric struct {
	Kind    string        `json:"kind" jsonschema:"One of cpu, memory, connections, replication-lag or disk."`
	Unit    string        `json:"unit"`
	Pending bool          `json:"pending" jsonschema:"True when monitoring has no values for this metric yet."`
	Current float64       `json:"current"`
	Min     float64       `json:"min"`
	Max     float64       `json:"max"`
	Average float64       `json:"average"`
	Trend   string        `json:"trend" jsonschema:"One of up, down or steady."`
	Points  []metricPoint `json:"points,omitempty" jsonschema:"Only present when includePoints is true."`
}

type metrics struct {
	DatabaseID    string   `json:"databaseId"`
	Window        string   `json:"window"`
	Start         string   `json:"start"`
	End           string   `json:"end"`
	StepSeconds   int      `json:"stepSeconds"`
	Pending       bool     `json:"pending" jsonschema:"True when monitoring has no values yet. Values are then zero."`
	PendingReason string   `json:"pendingReason,omitempty"`
	Metrics       []metric `json:"metrics"`
}

type metricsInput struct {
	ID            string   `json:"id" jsonschema:"A database id, copied exactly from list_databases."`
	Window        string   `json:"window,omitempty" jsonschema:"The time span. Defaults to 1h."`
	Metrics       []string `json:"metrics,omitempty" jsonschema:"Which metrics to return. Defaults to all of them."`
	IncludePoints bool     `json:"includePoints,omitempty" jsonschema:"Return the full series of each metric. Defaults to false."`
}

type activityEntry struct {
	ID           string `json:"id"`
	At           string `json:"at"`
	Actor        string `json:"actor"`
	RequestID    string `json:"requestId,omitempty"`
	Action       string `json:"action"`
	DatabaseID   string `json:"databaseId,omitempty"`
	Target       string `json:"target"`
	Outcome      string `json:"outcome"`
	Detail       string `json:"detail,omitempty"`
	Error        string `json:"error,omitempty"`
	ResourceKind string `json:"resourceKind,omitempty"`
	ResourceName string `json:"resourceName,omitempty"`
	Client       string `json:"client,omitempty" jsonschema:"How the change was made: mcp for an external agent, chat for the console Chat. Absent for a person in the console."`
	Agent        string `json:"agent,omitempty" jsonschema:"The program that made the change, such as Cursor or Claude Code, or Anthropic for the Chat."`
}

type activity struct {
	Entries   []activityEntry `json:"entries"`
	Cursor    string          `json:"cursor" jsonschema:"Pass this as since to get only newer entries."`
	Truncated bool            `json:"truncated"`
}

type activityInput struct {
	DatabaseID string `json:"databaseId,omitempty" jsonschema:"Only the changes to this database."`
	Limit      *int   `json:"limit,omitempty" jsonschema:"How many entries to return, from 1 to 500. Defaults to 50."`
	Since      string `json:"since,omitempty" jsonschema:"The cursor from an earlier call, to get only newer entries."`
}

func observabilityReads() []tool {
	return []tool{
		define(spec{
			name:  "get_metrics",
			title: "Get metrics",
			description: "Get resource metrics for a database over the last hour or day. " +
				"Returns current, minimum, maximum and average values and a trend for each metric. " +
				"Values are zero while pending is true, so read pending before trusting a number. " +
				"Ask for points only when you need the series. " +
				"On a control plane with several environments, metrics come from usage samples sent by each environment and may be pending.",
			annotations: readOnly(),
			tune: func(input, _ *jsonschema.Schema) {
				input.Properties["window"].Enum = []any{"1h", "24h"}
				input.Properties["metrics"].Items.Enum = []any{"cpu", "memory", "connections", "replication-lag", "disk"}
			},
		}, getMetrics),

		define(spec{
			name:  "list_activity",
			title: "List activity",
			description: "List recent changes made through the control plane, newest first. " +
				"Entries are written when a change is requested and do not update later, so an accepted entry does not mean the change finished. " +
				"Use wait_for_database or wait_for_backup to follow progress.",
			annotations: readOnly(),
			tune: func(input, _ *jsonschema.Schema) {
				bounds(input.Properties["limit"], 1, maxActivityLimit)
			},
		}, listActivity),
	}
}

// bounds sets the inclusive range of a number the types leave open.
func bounds(schema *jsonschema.Schema, lowest, highest float64) {
	schema.Minimum, schema.Maximum = &lowest, &highest
}

func getMetrics(ctx context.Context, c *call, in metricsInput) (metrics, error) {
	path, err := databasePath(in.ID)
	if err != nil {
		return metrics{}, err
	}

	query := url.Values{}
	if in.Window != "" {
		query.Set("window", in.Window)
	}
	if len(in.Metrics) > 0 {
		query.Set("metrics", strings.Join(in.Metrics, ","))
	}

	var out metrics
	if err := c.get(ctx, "/api/databases/"+path+"/metrics", query, &out); err != nil {
		return metrics{}, err
	}

	out.Metrics = orEmpty(out.Metrics)
	if !in.IncludePoints {
		for i := range out.Metrics {
			out.Metrics[i].Points = nil
		}
	}

	return out, nil
}

func listActivity(ctx context.Context, c *call, in activityInput) (activity, error) {
	query := url.Values{}
	if in.DatabaseID != "" {
		// Checked like every other id, though it travels in the query string.
		if _, err := databasePath(in.DatabaseID); err != nil {
			return activity{}, err
		}
		query.Set("databaseId", in.DatabaseID)
	}
	limit := defaultActivityLimit
	if in.Limit != nil {
		limit = *in.Limit
	}
	query.Set("limit", strconv.Itoa(limit))
	if in.Since != "" {
		query.Set("since", in.Since)
	}

	var out activity
	if err := c.get(ctx, "/api/activity", query, &out); err != nil {
		return activity{}, err
	}
	out.Entries = orEmpty(out.Entries)

	return out, nil
}
