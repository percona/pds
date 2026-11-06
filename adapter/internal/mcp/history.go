package mcp

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

const (
	defaultHistoryLimit = 20
	maxHistoryLimit     = 100
	// maxPhaseOutput is how much of a join step's log a result carries. The
	// end of a log is where a step says why it stopped.
	maxPhaseOutput = 500
)

// historyKinds are the kinds of fact the history of a database keeps.
var historyKinds = []any{"action", "transition", "backup", "restore", "event"}

type historyEntry struct {
	ID         string `json:"id"`
	At         string `json:"at"`
	Kind       string `json:"kind" jsonschema:"One of action, transition, backup, restore or event."`
	Actor      string `json:"actor,omitempty"`
	Client     string `json:"client,omitempty" jsonschema:"How the change was made: mcp for an external agent, chat for the console Chat. Absent for a person in the console."`
	Agent      string `json:"agent,omitempty" jsonschema:"The program that made the change, such as Cursor or Claude Code, or Anthropic for the Chat."`
	Action     string `json:"action,omitempty"`
	Target     string `json:"target,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Error      string `json:"error,omitempty"`
	FromPhase  string `json:"fromPhase,omitempty" jsonschema:"The status the database left. Set on a transition."`
	ToPhase    string `json:"toPhase,omitempty" jsonschema:"The status the database reached. Set on a transition."`
	Message    string `json:"message,omitempty"`
	Reason     string `json:"reason,omitempty"`
	EventType  string `json:"eventType,omitempty"`
	Count      int32  `json:"count,omitempty" jsonschema:"How many times an event happened."`
	ObjectID   string `json:"objectId,omitempty" jsonschema:"The backup id on a backup or restore."`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
	DurationMS int64  `json:"durationMs,omitempty"`
}

type history struct {
	Entries   []historyEntry `json:"entries"`
	Cursor    string         `json:"cursor,omitempty" jsonschema:"Pass this as cursor to get the next, older page. Present only when truncated is true."`
	Truncated bool           `json:"truncated" jsonschema:"True when older entries remain."`
}

type historyInput struct {
	ID     string `json:"id" jsonschema:"A database id, copied exactly from list_databases."`
	Kind   string `json:"kind,omitempty" jsonschema:"Only this kind of entry."`
	Since  string `json:"since,omitempty" jsonschema:"Only entries at or after this time, as an RFC 3339 timestamp."`
	Until  string `json:"until,omitempty" jsonschema:"Only entries at or before this time, as an RFC 3339 timestamp."`
	Cursor string `json:"cursor,omitempty" jsonschema:"The cursor from an earlier call, to get the next page."`
	Limit  *int   `json:"limit,omitempty" jsonschema:"How many entries to return, from 1 to 100. Defaults to 20."`
}

type usageSample struct {
	At            string   `json:"at"`
	CPUAvg        *float64 `json:"cpuAvg,omitempty" jsonschema:"Average CPU use, in cores."`
	CPUMax        *float64 `json:"cpuMax,omitempty" jsonschema:"Highest CPU use, in cores."`
	MemoryAvg     *float64 `json:"memoryAvg,omitempty" jsonschema:"Average memory use, in bytes."`
	MemoryMax     *float64 `json:"memoryMax,omitempty" jsonschema:"Highest memory use, in bytes."`
	StorageBytes  *int64   `json:"storageBytes,omitempty" jsonschema:"Storage provisioned for the database, in bytes."`
	MetricsReason string   `json:"metricsReason,omitempty" jsonschema:"Why the CPU and memory figures are missing."`
}

type usage struct {
	Samples []usageSample `json:"samples" jsonschema:"Newest first."`
}

type usageInput struct {
	ID    string `json:"id" jsonschema:"A database id, copied exactly from list_databases."`
	Since string `json:"since,omitempty" jsonschema:"Only samples at or after this time, as an RFC 3339 timestamp."`
	Until string `json:"until,omitempty" jsonschema:"Only samples at or before this time, as an RFC 3339 timestamp."`
	Limit *int   `json:"limit,omitempty" jsonschema:"How many samples to return, from 1 to 100. Defaults to 20."`
}

type joinPhase struct {
	Phase      string `json:"phase"`
	Outcome    string `json:"outcome"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	DurationMS int64  `json:"durationMs"`
	Output     string `json:"output,omitempty" jsonschema:"The end of the step's log, at most 500 characters."`
}

type joinRun struct {
	RunID  string      `json:"runId"`
	Phases []joinPhase `json:"phases" jsonschema:"In the order the steps ran."`
}

type environmentHistory struct {
	Runs []joinRun `json:"runs" jsonschema:"Oldest run first."`
}

type environmentHistoryInput struct {
	Name string `json:"name" jsonschema:"An environment name, copied exactly from list_environments."`
}

type activityResource struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	State     string `json:"state"`
	Message   string `json:"message,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	EndedAt   string `json:"endedAt,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
}

type timelineEntry struct {
	At      string `json:"at"`
	Source  string `json:"source"`
	Type    string `json:"type"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	Count   int32  `json:"count,omitempty"`
}

type activityDetail struct {
	Event     activityEntry     `json:"event" jsonschema:"The change as it was requested."`
	Resource  *activityResource `json:"resource,omitempty" jsonschema:"What the change made, as it is now. It can differ from the event: a request that succeeded may have started work that failed."`
	Timeline  []timelineEntry   `json:"timeline" jsonschema:"Oldest first."`
	Notes     []string          `json:"notes,omitempty"`
	Truncated bool              `json:"truncated" jsonschema:"True when the oldest timeline entries were left out."`
}

type activityDetailInput struct {
	ID string `json:"id" jsonschema:"An activity id, copied exactly from list_activity."`
}

func historyReads() []tool {
	return []tool{
		define(spec{
			name:  "get_database_history",
			title: "Get database history",
			description: "Get what happened to one database, newest first: changes made through the control plane, status changes, backups, restores and events. " +
				"Filter by kind, since and until (RFC 3339 times). " +
				"limit is 1 to 100 (default 20). When truncated is true, pass the cursor to get older entries. " +
				"Use it to explain why a database changed status or when a backup failed.",
			annotations: readOnly(),
			tune: func(input, _ *jsonschema.Schema) {
				bounds(input.Properties["limit"], 1, maxHistoryLimit)
				input.Properties["kind"].Enum = historyKinds
			},
		}, getDatabaseHistory),

		define(spec{
			name:  "get_database_usage",
			title: "Get database usage",
			description: "Get CPU, memory and storage samples for one database, newest first. " +
				"Samples are taken about every five minutes and kept for seven days, and they work when monitoring is off. " +
				"CPU is in cores and memory and storage are in bytes. " +
				"A sample with a metricsReason has no CPU or memory figures. " +
				"limit is 1 to 100 (default 20): narrow with since and until (RFC 3339 times) for a longer span.",
			annotations: readOnly(),
			tune: func(input, _ *jsonschema.Schema) {
				bounds(input.Properties["limit"], 1, maxHistoryLimit)
			},
		}, getDatabaseUsage),

		define(spec{
			name:  "get_environment_history",
			title: "Get environment history",
			description: "Get the steps of each attempt to connect an environment, with how each step ended and the end of its log. " +
				"Use it to find out why an environment did not connect. " +
				"name is an environment name from list_environments.",
			annotations: readOnly(),
		}, getEnvironmentHistory),

		define(spec{
			name:  "get_activity",
			title: "Get an activity entry",
			description: "Get one change from the activity log with what is known about the work it started: the object's state now and a timeline of events. " +
				"id is an entry id from list_activity. " +
				"The log keeps only the most recent changes, so an older entry may be gone.",
			annotations: readOnly(),
			tune: func(input, _ *jsonschema.Schema) {
				input.Properties["id"].Pattern = `^[A-Za-z0-9_-]{1,64}$`
			},
		}, getActivity),
	}
}

func getDatabaseHistory(ctx context.Context, c *call, in historyInput) (history, error) {
	path, err := databasePath(in.ID)
	if err != nil {
		return history{}, err
	}

	query := url.Values{"limit": {strconv.Itoa(limitOr(in.Limit, defaultHistoryLimit))}}
	for key, value := range map[string]string{"kind": in.Kind, "since": in.Since, "until": in.Until, "cursor": in.Cursor} {
		if value != "" {
			query.Set(key, value)
		}
	}

	var out history
	if err := c.get(ctx, "/api/databases/"+path+"/history", query, &out); err != nil {
		return history{}, err
	}
	out.Entries = orEmpty(out.Entries)

	return out, nil
}

func getDatabaseUsage(ctx context.Context, c *call, in usageInput) (usage, error) {
	path, err := databasePath(in.ID)
	if err != nil {
		return usage{}, err
	}

	query := url.Values{"limit": {strconv.Itoa(limitOr(in.Limit, defaultHistoryLimit))}}
	if in.Since != "" {
		query.Set("since", in.Since)
	}
	if in.Until != "" {
		query.Set("until", in.Until)
	}

	var out usage
	if err := c.get(ctx, "/api/databases/"+path+"/usage", query, &out); err != nil {
		return usage{}, err
	}
	out.Samples = orEmpty(out.Samples)

	return out, nil
}

func getEnvironmentHistory(ctx context.Context, c *call, in environmentHistoryInput) (environmentHistory, error) {
	path, err := environmentPath(in.Name)
	if err != nil {
		return environmentHistory{}, err
	}

	var out environmentHistory
	if err := c.get(ctx, "/api/environments/"+path+"/history", nil, &out); err != nil {
		return environmentHistory{}, hintedAs(err, msgEnvironmentNotFoundHint)
	}
	out.Runs = orEmpty(out.Runs)
	for i := range out.Runs {
		out.Runs[i].Phases = orEmpty(out.Runs[i].Phases)
		for j := range out.Runs[i].Phases {
			phase := &out.Runs[i].Phases[j]
			phase.StartedAt, phase.FinishedAt = set(phase.StartedAt), set(phase.FinishedAt)
			phase.Output = tail(phase.Output, maxPhaseOutput)
		}
	}

	return out, nil
}

func getActivity(ctx context.Context, c *call, in activityDetailInput) (activityDetail, error) {
	var out activityDetail
	if err := c.get(ctx, "/api/activity/"+url.PathEscape(in.ID), nil, &out); err != nil {
		return activityDetail{}, hintedAs(err, msgActivityNotFoundHint)
	}
	out.Timeline = orEmpty(out.Timeline)

	return out, nil
}

// limitOr is the limit the caller chose, or the tool's own default.
func limitOr(limit *int, fallback int) int {
	if limit == nil {
		return fallback
	}

	return *limit
}

// set is a time the API recorded, or empty when it sent Go's zero time, which
// it does for a step that has not finished.
func set(at string) string {
	if strings.HasPrefix(at, "0001-01-01") {
		return ""
	}

	return at
}

// tail is the last n characters of text, cut on a character boundary.
func tail(text string, n int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= n {
		return string(runes)
	}

	return string(runes[len(runes)-n:])
}
