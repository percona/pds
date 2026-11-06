package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/store"
)

// DBBook stores history in the adapter database, one Team at a time.
type DBBook struct {
	lazy     *store.Lazy
	fallback TeamFallback
}

// NewDBBook returns a book backed by the adapter database. fallback is the
// Team a process that serves one Organization records under when a request
// has none.
func NewDBBook(lazy *store.Lazy, fallback TeamFallback) *DBBook {
	return &DBBook{lazy: lazy, fallback: fallback}
}

func (b *DBBook) state(ctx context.Context) (*store.HubState, bool) {
	if b == nil || b.lazy == nil {
		return nil, false
	}
	team := historyTeam(ctx, b.fallback)
	if team == 0 {
		return nil, false
	}
	st := b.lazy.For(team).State()
	if st == nil {
		return nil, false
	}

	return st, true
}

// UserNames reads the display name of Users by id, for Activity to show who
// acted. Users are global, so any team's state answers.
func (b *DBBook) UserNames(ctx context.Context, ids []uint) (map[uint]string, error) {
	st, ok := b.state(ctx)
	if !ok {
		return nil, ErrActivityUnavailable
	}

	return st.UserNames(ctx, ids)
}

func (b *DBBook) RecordAction(ctx context.Context, e ActivityEvent) {
	if strings.TrimSpace(e.Action) == "" {
		return
	}
	if err := b.RecordEntry(ctx, actionEntry(e)); err != nil {
		log.FromContext(ctx).Warn("history write failed, event dropped", "action", e.Action, "error", err)
	}
}

func (b *DBBook) ListActions(ctx context.Context, q ActivityQuery) (ActivityPage, error) {
	st, ok := b.state(ctx)
	if !ok {
		return ActivityPage{}, ErrActivityUnavailable
	}
	limit := clampPage(q.Limit)
	var minID uint64
	if q.Since != "" {
		_, _ = fmt.Sscan(strings.TrimLeft(q.Since, "0"), &minID)
	}
	rows, err := st.ListHistory(ctx, store.HistoryFilter{
		DatabaseID: q.DatabaseID,
		Kind:       HistoryAction,
		MinID:      minID,
		Limit:      limit + 1,
	})
	if err != nil {
		return ActivityPage{}, err
	}
	newest, err := st.NewestActionID(ctx)
	if err != nil {
		return ActivityPage{}, err
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	entries := make([]ActivityEvent, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, entryAction(fromRow(row)))
	}
	cursorText := ""
	if newest > 0 {
		cursorText = fmt.Sprintf("%020d", newest)
	}

	return ActivityPage{Entries: entries, Cursor: cursorText, Truncated: truncated}, nil
}

func (b *DBBook) GetAction(ctx context.Context, id string) (ActivityEvent, error) {
	st, ok := b.state(ctx)
	if !ok {
		return ActivityEvent{}, ErrActivityUnavailable
	}
	var rowID uint64
	_, _ = fmt.Sscan(strings.TrimLeft(id, "0"), &rowID)
	if rowID == 0 {
		return ActivityEvent{}, ErrActivityEventNotFound
	}
	row, err := st.ActionByID(ctx, rowID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || strings.Contains(err.Error(), "record not found") {
			return ActivityEvent{}, ErrActivityEventNotFound
		}
		return ActivityEvent{}, err
	}
	if fmt.Sprintf("%020d", row.ID) != id && row.ID != rowID {
		return ActivityEvent{}, ErrActivityEventNotFound
	}

	return entryAction(fromRow(row)), nil
}

func (b *DBBook) RecordEntry(ctx context.Context, e HistoryEntry) error {
	if strings.TrimSpace(e.Kind) == "" {
		return nil
	}
	st, ok := b.state(ctx)
	if !ok {
		return ErrActivityUnavailable
	}
	e.Detail = Sanitize(clip(e.Detail))
	e.Error = Sanitize(clip(e.Error))
	e.Message = Sanitize(clip(e.Message))
	_, err := st.InsertHistory(ctx, toRow(e))

	return err
}

func (b *DBBook) ListEntries(ctx context.Context, q HistoryQuery) (HistoryPage, error) {
	st, ok := b.state(ctx)
	if !ok {
		return HistoryPage{}, ErrActivityUnavailable
	}
	limit := clampPage(q.Limit)
	var cursor uint64
	_, _ = fmt.Sscan(q.Cursor, &cursor)
	rows, err := st.ListHistory(ctx, store.HistoryFilter{
		DatabaseID:  q.DatabaseID,
		Environment: q.Environment,
		Kind:        q.Kind,
		Since:       q.Since,
		Until:       q.Until,
		Cursor:      cursor,
		Limit:       limit + 1,
	})
	if err != nil {
		return HistoryPage{}, err
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	entries := make([]HistoryEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, fromRow(row))
	}
	next := ""
	if truncated && len(entries) > 0 {
		next = entries[len(entries)-1].ID
	}

	return HistoryPage{Entries: entries, Cursor: next, Truncated: truncated}, nil
}

func (b *DBBook) RecordUsage(ctx context.Context, p UsagePoint) (bool, error) {
	if p.DatabaseID == "" {
		return false, nil
	}
	st, ok := b.state(ctx)
	if !ok {
		return false, ErrActivityUnavailable
	}
	if p.At.IsZero() {
		p.At = time.Now().UTC()
	}
	row := store.UsageSample{
		DatabaseID:    p.DatabaseID,
		Environment:   p.Environment,
		At:            p.At,
		CPUAvg:        p.CPUAvg,
		CPUMax:        p.CPUMax,
		MemoryAvg:     p.MemoryAvg,
		MemoryMax:     p.MemoryMax,
		StorageBytes:  p.StorageBytes,
		MetricsReason: Sanitize(p.MetricsReason),
	}
	if reading := p.Monitoring; reading != nil {
		row.Monitored = true
		row.MonitorCPU = reading.CPU
		row.MonitorMemory = reading.Memory
		row.MonitorConnections = reading.Connections
		row.MonitorReplicationLag = reading.ReplicationLag
		row.MonitorDisk = reading.Disk
		row.MonitorPending = monitoringCode(reading.Pending)
	}
	_, kept, err := st.InsertUsageIfDue(ctx, row, UsageInterval)

	return kept, err
}

func (b *DBBook) ListUsage(ctx context.Context, q UsageQuery) ([]UsagePoint, error) {
	st, ok := b.state(ctx)
	if !ok {
		return nil, ErrActivityUnavailable
	}
	rows, err := st.ListUsage(ctx, q.DatabaseID, q.Since, q.Until, clampPage(q.Limit))
	if err != nil {
		return nil, err
	}
	out := make([]UsagePoint, 0, len(rows))
	for _, row := range rows {
		var reading *MonitoringReading
		if row.Monitored {
			reading = &MonitoringReading{
				CPU:            row.MonitorCPU,
				Memory:         row.MonitorMemory,
				Connections:    row.MonitorConnections,
				ReplicationLag: row.MonitorReplicationLag,
				Disk:           row.MonitorDisk,
				Pending:        row.MonitorPending,
			}
		}
		out = append(out, UsagePoint{
			Monitoring:    reading,
			ID:            fmt.Sprintf("%020d", row.ID),
			DatabaseID:    row.DatabaseID,
			Environment:   row.Environment,
			At:            row.At,
			CPUAvg:        row.CPUAvg,
			CPUMax:        row.CPUMax,
			MemoryAvg:     row.MemoryAvg,
			MemoryMax:     row.MemoryMax,
			StorageBytes:  row.StorageBytes,
			MetricsReason: row.MetricsReason,
		})
	}

	return out, nil
}

func (b *DBBook) PruneUsage(ctx context.Context, before time.Time) (int64, error) {
	st, ok := b.state(ctx)
	if !ok {
		return 0, ErrActivityUnavailable
	}

	return st.PruneUsage(ctx, before)
}

func (b *DBBook) RecordPhase(ctx context.Context, step JoinStep) error {
	if step.Environment == "" || step.Phase == "" {
		return nil
	}
	st, ok := b.state(ctx)
	if !ok {
		return ErrActivityUnavailable
	}
	if step.StartedAt.IsZero() {
		step.StartedAt = time.Now().UTC()
	}
	var finished *time.Time
	if !step.FinishedAt.IsZero() {
		t := step.FinishedAt.UTC()
		finished = &t
	}

	return st.UpsertJoinPhase(ctx, store.JoinPhase{
		Environment: step.Environment,
		RunID:       step.RunID,
		Phase:       step.Phase,
		Outcome:     step.Outcome,
		StartedAt:   step.StartedAt.UTC(),
		FinishedAt:  finished,
		DurationMS:  step.DurationMS,
		Output:      clipTail(ScrubTail(step.Output)),
	})
}

func (b *DBBook) ListJoins(ctx context.Context, environment string) ([]JoinRun, error) {
	st, ok := b.state(ctx)
	if !ok {
		return nil, ErrActivityUnavailable
	}
	rows, err := st.ListJoinPhases(ctx, environment)
	if err != nil {
		return nil, err
	}
	var order []string
	grouped := map[string][]JoinStep{}
	for _, row := range rows {
		if _, seen := grouped[row.RunID]; !seen {
			order = append(order, row.RunID)
		}
		step := JoinStep{
			Environment: row.Environment, RunID: row.RunID, Phase: row.Phase, Outcome: row.Outcome,
			StartedAt: row.StartedAt, DurationMS: row.DurationMS, Output: row.Output,
		}
		if row.FinishedAt != nil {
			step.FinishedAt = *row.FinishedAt
		}
		grouped[row.RunID] = append(grouped[row.RunID], step)
	}
	out := make([]JoinRun, 0, len(order))
	for _, id := range order {
		out = append(out, JoinRun{RunID: id, Phases: grouped[id]})
	}

	return out, nil
}

func (b *DBBook) LastPhases(ctx context.Context) map[string]string {
	st, ok := b.state(ctx)
	if !ok {
		return map[string]string{}
	}
	phases, err := st.LastTransitionPhase(ctx)
	if err != nil {
		return map[string]string{}
	}

	return phases
}

func (b *DBBook) sameTransition(ctx context.Context, entry HistoryEntry) (bool, error) {
	st, ok := b.state(ctx)
	if !ok {
		return false, ErrActivityUnavailable
	}
	rows, err := st.ListHistory(ctx, store.HistoryFilter{
		DatabaseID: entry.DatabaseID, Kind: HistoryTransition, Limit: 1,
	})
	if err != nil || len(rows) == 0 {
		return false, err
	}

	return rows[0].FromPhase == entry.FromPhase && rows[0].ToPhase == entry.ToPhase, nil
}

func (b *DBBook) Observe(
	ctx context.Context,
	environment string,
	prev, next []engine.Instance,
	backups []engine.Backup,
	restores []engine.Restore,
	events []PodEvent,
	usage []UsagePoint,
) error {
	now := time.Now().UTC()
	var failed error
	note := func(err error, kind string) {
		if err == nil || failed != nil {
			return
		}
		log.FromContext(ctx).Warn("history write failed, event dropped", "kind", kind, "error", err)
		failed = err
	}
	for _, entry := range DiffTransitions(environment, prev, next, now) {
		same, err := b.sameTransition(ctx, entry)
		if err != nil {
			note(err, entry.Kind)
			continue
		}
		if same {
			continue
		}
		note(b.RecordEntry(ctx, entry), entry.Kind)
	}
	for _, entry := range BackupEntries(environment, "", backups, now) {
		note(b.RecordEntry(ctx, entry), entry.Kind)
	}
	for _, entry := range RestoreEntries(environment, "", restores, now) {
		note(b.RecordEntry(ctx, entry), entry.Kind)
	}
	for _, event := range events {
		note(b.RecordEntry(ctx, HistoryEntry{
			DatabaseID:  event.DatabaseID,
			Environment: environment,
			Kind:        HistoryEvent,
			At:          event.At,
			EventType:   event.Type,
			Reason:      event.Reason,
			Message:     Sanitize(event.Message),
			Count:       event.Count,
		}), HistoryEvent)
	}
	for _, point := range usage {
		point.Environment = environment
		if _, err := b.RecordUsage(ctx, point); err != nil {
			note(err, "usage")
		}
	}

	return failed
}

func toRow(e HistoryEntry) store.HistoryRow {
	var started, finished *time.Time
	if !e.StartedAt.IsZero() {
		t := e.StartedAt.UTC()
		started = &t
	}
	if !e.FinishedAt.IsZero() {
		t := e.FinishedAt.UTC()
		finished = &t
	}

	return store.HistoryRow{
		DatabaseID:   e.DatabaseID,
		Environment:  e.Environment,
		Kind:         e.Kind,
		At:           e.At,
		Actor:        e.Actor,
		UserID:       optionalID(e.UserID),
		Client:       e.Client,
		Agent:        e.Agent,
		RequestID:    e.RequestID,
		Action:       e.Action,
		Target:       e.Target,
		Outcome:      e.Outcome,
		Detail:       e.Detail,
		Error:        e.Error,
		ResourceKind: e.ResourceKind,
		ResourceName: e.ResourceName,
		FromPhase:    e.FromPhase,
		ToPhase:      e.ToPhase,
		Message:      e.Message,
		ObjectID:     e.ObjectID,
		Storage:      e.Storage,
		SizeBytes:    e.SizeBytes,
		StartedAt:    started,
		FinishedAt:   finished,
		DurationMS:   e.DurationMS,
		Reason:       e.Reason,
		EventType:    e.EventType,
		Count:        e.Count,
	}
}

func fromRow(row store.HistoryRow) HistoryEntry {
	e := HistoryEntry{
		ID:           fmt.Sprintf("%020d", row.ID),
		DatabaseID:   row.DatabaseID,
		Environment:  row.Environment,
		Kind:         row.Kind,
		At:           row.At,
		Actor:        row.Actor,
		UserID:       valueOfID(row.UserID),
		TeamID:       row.TeamID,
		Client:       row.Client,
		Agent:        row.Agent,
		RequestID:    row.RequestID,
		Action:       row.Action,
		Target:       row.Target,
		Outcome:      row.Outcome,
		Detail:       row.Detail,
		Error:        row.Error,
		ResourceKind: row.ResourceKind,
		ResourceName: row.ResourceName,
		FromPhase:    row.FromPhase,
		ToPhase:      row.ToPhase,
		Message:      row.Message,
		ObjectID:     row.ObjectID,
		Storage:      row.Storage,
		SizeBytes:    row.SizeBytes,
		DurationMS:   row.DurationMS,
		Reason:       row.Reason,
		EventType:    row.EventType,
		Count:        row.Count,
	}
	if row.StartedAt != nil {
		e.StartedAt = *row.StartedAt
	}
	if row.FinishedAt != nil {
		e.FinishedAt = *row.FinishedAt
	}

	return e
}

// optionalID stores an id of zero as NULL, which is what a row with no User
// or no Team holds.
func optionalID(id uint) *uint {
	if id == 0 {
		return nil
	}

	return &id
}

func valueOfID(id *uint) uint {
	if id == nil {
		return 0
	}

	return *id
}
