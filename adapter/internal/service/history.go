package service

import (
	"context"
	"log/slog"
	"regexp"
	"time"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/engine"
)

// Kinds a history row can carry. Activity is the action kind of the same table.
const (
	HistoryAction     = "action"
	HistoryTransition = "transition"
	HistoryBackup     = "backup"
	HistoryRestore    = "restore"
	HistoryEvent      = "event"
)

// UsageInterval is how often one database keeps a sample. Faster heartbeats
// still arrive; the extra samples are dropped.
const UsageInterval = 5 * time.Minute

// UsageRetention is how long a usage sample is kept. It bounds the history a
// hub charts for a database on another environment.
const UsageRetention = 7 * 24 * time.Hour

// UsagePruner deletes usage samples taken before a cut, for every Team.
type UsagePruner interface {
	PruneUsage(ctx context.Context, before time.Time) (int64, error)
}

// UsageRetentionEvery is how often KeepUsageRetention sweeps.
const UsageRetentionEvery = time.Hour

// KeepUsageRetention prunes samples older than UsageRetention now and then on
// every tick until ctx is done. Heartbeats prune their own Team too, but a
// Team whose environments stopped reporting sends none, and this sweep is what
// still ages its rows out. A failed sweep is logged and tried on the next tick.
func KeepUsageRetention(ctx context.Context, pruner UsagePruner, every time.Duration, now func() time.Time, logger *slog.Logger) {
	if pruner == nil {
		return
	}
	if every <= 0 {
		every = UsageRetentionEvery
	}
	if now == nil {
		now = time.Now
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		removed, err := pruner.PruneUsage(ctx, now().Add(-UsageRetention))
		switch {
		case err != nil && ctx.Err() == nil:
			logger.WarnContext(ctx, "usage retention sweep failed", slog.String("cause", err.Error()))
		case removed > 0:
			logger.InfoContext(ctx, "usage retention sweep", slog.Int64("removed", removed))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// MaxJoinPhases is how many join steps one environment history response returns.
// Older rows stay in the database.
const MaxJoinPhases = 200

func clampPage(limit int) int {
	if limit <= 0 {
		return DefaultActivityLimit
	}
	if limit > MaxActivityEvents {
		return MaxActivityEvents
	}

	return limit
}

// HistoryEntry is one kept fact about a database.
type HistoryEntry struct {
	ID          string    `json:"id"`
	DatabaseID  string    `json:"databaseId,omitempty"`
	Environment string    `json:"environment,omitempty"`
	Kind        string    `json:"kind"`
	At          time.Time `json:"at"`
	Actor       string    `json:"actor,omitempty"`
	// UserID and TeamID say who acted and whose the row is. A row stores the
	// ids and never a name or an email, so a rename shows on old rows and a
	// removed User is told apart from an actor that never had a User row.
	UserID       uint      `json:"-"`
	TeamID       uint      `json:"-"`
	Client       string    `json:"client,omitempty"`
	Agent        string    `json:"agent,omitempty"`
	RequestID    string    `json:"requestId,omitempty"`
	Action       string    `json:"action,omitempty"`
	Target       string    `json:"target,omitempty"`
	Outcome      string    `json:"outcome,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	Error        string    `json:"error,omitempty"`
	ResourceKind string    `json:"resourceKind,omitempty"`
	ResourceName string    `json:"resourceName,omitempty"`
	FromPhase    string    `json:"fromPhase,omitempty"`
	ToPhase      string    `json:"toPhase,omitempty"`
	Message      string    `json:"message,omitempty"`
	ObjectID     string    `json:"objectId,omitempty"`
	Storage      string    `json:"storage,omitempty"`
	SizeBytes    int64     `json:"sizeBytes,omitempty"`
	StartedAt    time.Time `json:"startedAt,omitempty"`
	FinishedAt   time.Time `json:"finishedAt,omitempty"`
	DurationMS   int64     `json:"durationMs,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	EventType    string    `json:"eventType,omitempty"`
	Count        int32     `json:"count,omitempty"`
}

// HistoryQuery selects a page. Since and Until bound At. Cursor is an id and
// is exclusive, newest first.
type HistoryQuery struct {
	DatabaseID  string
	Environment string
	Kind        string
	Since       time.Time
	Until       time.Time
	Cursor      string
	Limit       int
}

// HistoryPage is one page of history, newest first.
type HistoryPage struct {
	Entries   []HistoryEntry `json:"entries"`
	Cursor    string         `json:"cursor,omitempty"`
	Truncated bool           `json:"truncated"`
}

// UsagePoint is one sample. Nil processor fields mean metrics-server did not
// answer; StorageBytes and Reason still do.
type UsagePoint struct {
	ID            string    `json:"id"`
	DatabaseID    string    `json:"databaseId"`
	Environment   string    `json:"environment,omitempty"`
	At            time.Time `json:"at"`
	CPUAvg        *float64  `json:"cpuAvg,omitempty"`
	CPUMax        *float64  `json:"cpuMax,omitempty"`
	MemoryAvg     *float64  `json:"memoryAvg,omitempty"`
	MemoryMax     *float64  `json:"memoryMax,omitempty"`
	StorageBytes  *int64    `json:"storageBytes,omitempty"`
	MetricsReason string    `json:"metricsReason,omitempty"`
	// Monitoring is the PMM reading the environment agent took with this sample.
	// Nil means the agent had no PMM to read from.
	Monitoring *MonitoringReading `json:"monitoring,omitempty"`
}

// UsageQuery selects samples for one database.
type UsageQuery struct {
	DatabaseID string
	Since      time.Time
	Until      time.Time
	Limit      int
}

// JoinStep is one phase report from the join script.
type JoinStep struct {
	Environment string    `json:"environment,omitempty"`
	RunID       string    `json:"runId,omitempty"`
	Phase       string    `json:"phase"`
	Outcome     string    `json:"outcome"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
	FinishedAt  time.Time `json:"finishedAt,omitempty"`
	DurationMS  int64     `json:"durationMs"`
	Output      string    `json:"output,omitempty"`
}

// JoinRun is every phase of one join, in the order they were reported.
type JoinRun struct {
	RunID  string     `json:"runId"`
	Phases []JoinStep `json:"phases"`
}

// PodEvent is one Kubernetes event a spoke includes on a heartbeat.
type PodEvent struct {
	DatabaseID string    `json:"databaseId"`
	At         time.Time `json:"at"`
	Type       string    `json:"type,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Message    string    `json:"message,omitempty"`
	Count      int32     `json:"count,omitempty"`
}

// HistoryBook keeps history, usage and join phases for one process.
type HistoryBook interface {
	RecordAction(ctx context.Context, e ActivityEvent)
	ListActions(ctx context.Context, q ActivityQuery) (ActivityPage, error)
	GetAction(ctx context.Context, id string) (ActivityEvent, error)
	RecordEntry(ctx context.Context, e HistoryEntry) error
	ListEntries(ctx context.Context, q HistoryQuery) (HistoryPage, error)
	RecordUsage(ctx context.Context, p UsagePoint) (bool, error)
	ListUsage(ctx context.Context, q UsageQuery) ([]UsagePoint, error)
	PruneUsage(ctx context.Context, before time.Time) (int64, error)
	RecordPhase(ctx context.Context, step JoinStep) error
	ListJoins(ctx context.Context, environment string) ([]JoinRun, error)
	Observe(ctx context.Context, environment string, prev, next []engine.Instance, backups []engine.Backup, restores []engine.Restore, events []PodEvent, usage []UsagePoint) error
	LastPhases(ctx context.Context) map[string]string
}

// TeamFallback names the Team a call with no request Team is recorded under.
// It is how a process that serves one Organization, or has no database, keeps
// the work it does outside any request. A process that serves every
// Organization has none, and records nothing rather than mixing customers.
type TeamFallback func(ctx context.Context) uint

// historyTeam returns the Team a history row belongs to: the request's, else
// the fallback's, else none.
func historyTeam(ctx context.Context, fallback TeamFallback) uint {
	if team, ok := auth.TeamFromContext(ctx); ok {
		return team
	}
	if fallback == nil {
		return 0
	}

	return fallback(ctx)
}

var (
	scrubValue     = `(?:"[^"\n]*"|'[^'\n]*'|\S+)`
	scrubAWSKey    = regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`)
	scrubAWSSecret = regexp.MustCompile(`(?i)(aws_secret_access_key\s*[=:]\s*)` + scrubValue)
	scrubAssign    = regexp.MustCompile(`(?i)\b(password|token|secret|bearer|api[_-]?key)\b(\s*[:=]\s*)` + scrubValue)
	scrubBearer    = regexp.MustCompile(`(?i)\bbearer\s+` + scrubValue)
	scrubNamed     = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:password|passwd|pwd|secret|token|apikey|api[_-]?key)[A-Za-z0-9_.-]*)([ \t]*[:=][ \t]*)` + scrubValue)
	scrubJWT       = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)
	scrubPEM       = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
	scrubKubeData  = regexp.MustCompile(`(?i)(client-certificate-data|client-key-data|certificate-authority-data):\s*\S+`)
)

// ScrubTail removes credentials from a join log before it is stored or sent.
// The patterns cover an AWS access key, a secret access key, a password or
// token assignment, a JWT, a PEM private key, and kubeconfig certificate data.
func ScrubTail(text string) string {
	text = scrubPEM.ReplaceAllString(text, "[redacted]")
	text = scrubKubeData.ReplaceAllString(text, "$1: [redacted]")
	text = scrubAWSSecret.ReplaceAllString(text, "${1}[redacted]")
	text = scrubNamed.ReplaceAllString(text, "${1}${2}[redacted]")
	text = scrubBearer.ReplaceAllString(text, "Bearer [redacted]")
	text = scrubAssign.ReplaceAllString(text, "${1}${2}[redacted]")
	text = scrubJWT.ReplaceAllString(text, "[redacted]")
	text = scrubAWSKey.ReplaceAllString(text, "[redacted]")

	return text
}

// DiffTransitions returns a row for every database whose phase changed.
func DiffTransitions(environment string, prev, next []engine.Instance, at time.Time) []HistoryEntry {
	was := map[string]string{}
	for _, item := range prev {
		if item.ID == "" {
			continue
		}
		was[item.ID] = item.Status.Phase
	}
	var out []HistoryEntry
	for _, item := range next {
		if item.ID == "" {
			continue
		}
		phase := item.Status.Phase
		from, ok := was[phaseKey(was, item.ID)]
		if !ok {
			from = ""
		}
		if from == phase {
			continue
		}
		out = append(out, HistoryEntry{
			DatabaseID:  item.ID,
			Environment: environment,
			Kind:        HistoryTransition,
			At:          at,
			FromPhase:   from,
			ToPhase:     phase,
			Message:     Sanitize(item.Status.Message),
		})
	}

	return out
}

func phaseKey(was map[string]string, id string) string {
	if _, ok := was[id]; ok {
		return id
	}

	return id
}

// BackupEntries turns a live backup list into history rows. Duration is finish
// minus start when both are set. The row is what remains after the object is
// gone.
func BackupEntries(environment, databaseID string, backups []engine.Backup, at time.Time) []HistoryEntry {
	var out []HistoryEntry
	for _, backup := range backups {
		if backup.ID == "" {
			continue
		}
		entry := HistoryEntry{
			DatabaseID:  databaseID,
			Environment: environment,
			Kind:        HistoryBackup,
			At:          at,
			ObjectID:    backup.ID,
			Outcome:     backup.Status,
			Message:     Sanitize(backup.Message),
			SizeBytes:   backup.SizeBytes,
		}
		if !backup.FinishOnly {
			entry.StartedAt = backup.CreatedAt
		}
		if !backup.FinishedAt.IsZero() {
			entry.FinishedAt = backup.FinishedAt
			if !backup.CreatedAt.IsZero() && !backup.FinishOnly {
				entry.DurationMS = backup.FinishedAt.Sub(backup.CreatedAt).Milliseconds()
			}
		}
		if entry.DatabaseID == "" {
			entry.DatabaseID = backup.Instance
		}
		out = append(out, entry)
	}

	return out
}

// RestoreEntries mirrors BackupEntries for restores.
func RestoreEntries(environment, databaseID string, restores []engine.Restore, at time.Time) []HistoryEntry {
	var out []HistoryEntry
	for _, restore := range restores {
		if restore.ID == "" {
			continue
		}
		entry := HistoryEntry{
			DatabaseID:  databaseID,
			Environment: environment,
			Kind:        HistoryRestore,
			At:          at,
			ObjectID:    restore.ID,
			Outcome:     restore.Status,
			Message:     Sanitize(restore.Message),
			StartedAt:   restore.CreatedAt,
		}
		if !restore.FinishedAt.IsZero() {
			entry.FinishedAt = restore.FinishedAt
			if !restore.CreatedAt.IsZero() {
				entry.DurationMS = restore.FinishedAt.Sub(restore.CreatedAt).Milliseconds()
			}
		}
		out = append(out, entry)
	}

	return out
}

// KeepUsage reports whether a sample is far enough after the previous one.
func KeepUsage(prev, at time.Time) bool {
	if prev.IsZero() {
		return true
	}

	return !at.Before(prev.Add(UsageInterval))
}
