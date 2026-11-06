package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/log"
)

// MemoryBook keeps history in process memory, one set of rows per Team.
// It is what tests use, and what a process without a database falls back to.
type MemoryBook struct {
	fallback TeamFallback

	mu      sync.Mutex
	next    uint64
	entries []storedEntry
	usage   []storedUsage
	phases  []storedPhase
}

type storedEntry struct {
	team  uint
	entry HistoryEntry
}

type storedUsage struct {
	team  uint
	point UsagePoint
}

type storedPhase struct {
	team uint
	step JoinStep
}

// NewMemoryBook returns an empty book. fallback is the Team used when the
// request carries none, which is a process that serves one Organization.
func NewMemoryBook(fallback TeamFallback) *MemoryBook {
	return &MemoryBook{fallback: fallback}
}

func (b *MemoryBook) team(ctx context.Context) uint {
	if b == nil {
		return 0
	}

	return historyTeam(ctx, b.fallback)
}

func (b *MemoryBook) RecordAction(ctx context.Context, e ActivityEvent) {
	if b == nil || strings.TrimSpace(e.Action) == "" {
		return
	}
	if err := b.RecordEntry(ctx, actionEntry(e)); err != nil {
		log.FromContext(ctx).Warn("history write failed, event dropped", "action", e.Action, "error", err)
	}
}

func (b *MemoryBook) ListActions(ctx context.Context, q ActivityQuery) (ActivityPage, error) {
	if b == nil {
		return ActivityPage{}, ErrActivityUnavailable
	}
	team := b.team(ctx)
	if team == 0 {
		return ActivityPage{}, ErrActivityUnavailable
	}
	limit := clampPage(q.Limit)
	b.mu.Lock()
	defer b.mu.Unlock()
	var newest string
	matched := make([]ActivityEvent, 0)
	since := strings.TrimSpace(q.Since)
	for _, row := range b.entries {
		if row.team != team || row.entry.Kind != HistoryAction {
			continue
		}
		event := entryAction(row.entry)
		if event.ID > newest {
			newest = event.ID
		}
		if q.DatabaseID != "" && event.DatabaseID != q.DatabaseID {
			continue
		}
		if since != "" && event.ID <= since {
			continue
		}
		matched = append(matched, event)
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].ID > matched[j].ID })
	truncated := len(matched) > limit
	if truncated {
		matched = matched[:limit]
	}

	return ActivityPage{Entries: matched, Cursor: newest, Truncated: truncated}, nil
}

func (b *MemoryBook) GetAction(ctx context.Context, id string) (ActivityEvent, error) {
	page, err := b.ListEntries(ctx, HistoryQuery{Kind: HistoryAction, Limit: 10000})
	if err != nil {
		return ActivityEvent{}, err
	}
	for _, entry := range page.Entries {
		if entry.ID == id {
			return entryAction(entry), nil
		}
	}

	return ActivityEvent{}, ErrActivityEventNotFound
}

func (b *MemoryBook) RecordEntry(ctx context.Context, e HistoryEntry) error {
	if b == nil {
		return nil
	}
	team := b.team(ctx)
	if team == 0 || strings.TrimSpace(e.Kind) == "" {
		return nil
	}
	e.Detail = Sanitize(clip(e.Detail))
	e.Error = Sanitize(clip(e.Error))
	e.Message = Sanitize(clip(e.Message))

	b.mu.Lock()
	defer b.mu.Unlock()
	if e.Kind == HistoryEvent {
		for _, row := range b.entries {
			if row.team == team && row.entry.Kind == HistoryEvent && row.entry.DatabaseID == e.DatabaseID && row.entry.Message == e.Message && row.entry.At.Equal(e.At) {
				return nil
			}
		}
	}
	if e.Kind == HistoryBackup || e.Kind == HistoryRestore {
		if updated := b.upsertLocked(team, e); updated {
			return nil
		}
	}
	b.next++
	e.ID = fmt.Sprintf("%020d", b.next)
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	b.entries = append(b.entries, storedEntry{team: team, entry: e})

	return nil
}

func (b *MemoryBook) upsertLocked(team uint, e HistoryEntry) bool {
	if e.ObjectID == "" {
		return false
	}
	for i := range b.entries {
		cur := &b.entries[i]
		if cur.team != team || cur.entry.Kind != e.Kind || cur.entry.ObjectID != e.ObjectID || cur.entry.DatabaseID != e.DatabaseID || cur.entry.Environment != e.Environment {
			continue
		}
		id := cur.entry.ID
		e.ID = id
		if e.At.IsZero() {
			e.At = cur.entry.At
		}
		cur.entry = e

		return true
	}

	return false
}

func (b *MemoryBook) ListEntries(ctx context.Context, q HistoryQuery) (HistoryPage, error) {
	if b == nil {
		return HistoryPage{}, ErrActivityUnavailable
	}
	team := b.team(ctx)
	if team == 0 {
		return HistoryPage{}, ErrActivityUnavailable
	}
	limit := clampPage(q.Limit)

	b.mu.Lock()
	defer b.mu.Unlock()
	matched := make([]HistoryEntry, 0)
	for _, row := range b.entries {
		if row.team != team {
			continue
		}
		e := row.entry
		if q.DatabaseID != "" && e.DatabaseID != q.DatabaseID {
			continue
		}
		if q.Environment != "" && e.Environment != q.Environment {
			continue
		}
		if q.Kind != "" && e.Kind != q.Kind {
			continue
		}
		if !q.Since.IsZero() && e.At.Before(q.Since) {
			continue
		}
		if !q.Until.IsZero() && e.At.After(q.Until) {
			continue
		}
		if q.Cursor != "" && e.ID >= q.Cursor {
			continue
		}
		matched = append(matched, e)
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].ID > matched[j].ID })
	truncated := len(matched) > limit
	if truncated {
		matched = matched[:limit]
	}
	cursor := ""
	if truncated && len(matched) > 0 {
		cursor = matched[len(matched)-1].ID
	}

	return HistoryPage{Entries: matched, Cursor: cursor, Truncated: truncated}, nil
}

func (b *MemoryBook) RecordUsage(ctx context.Context, p UsagePoint) (bool, error) {
	if b == nil {
		return false, nil
	}
	team := b.team(ctx)
	if team == 0 || p.DatabaseID == "" {
		return false, nil
	}
	if p.At.IsZero() {
		p.At = time.Now().UTC()
	}
	p.MetricsReason = Sanitize(p.MetricsReason)
	if p.Monitoring != nil {
		reading := *p.Monitoring
		reading.Pending = monitoringCode(reading.Pending)
		p.Monitoring = &reading
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	var prev time.Time
	for _, row := range b.usage {
		if row.team == team && row.point.DatabaseID == p.DatabaseID && row.point.At.After(prev) {
			prev = row.point.At
		}
	}
	if !KeepUsage(prev, p.At) {
		return false, nil
	}
	b.next++
	p.ID = fmt.Sprintf("%020d", b.next)
	b.usage = append(b.usage, storedUsage{team: team, point: p})

	return true, nil
}

func (b *MemoryBook) PruneUsage(ctx context.Context, before time.Time) (int64, error) {
	if b == nil {
		return 0, ErrActivityUnavailable
	}
	team := b.team(ctx)
	if team == 0 {
		return 0, ErrActivityUnavailable
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := b.usage[:0]
	var removed int64
	for _, row := range b.usage {
		if row.team == team && row.point.At.Before(before) {
			removed++

			continue
		}
		kept = append(kept, row)
	}
	b.usage = kept

	return removed, nil
}

func (b *MemoryBook) ListUsage(ctx context.Context, q UsageQuery) ([]UsagePoint, error) {
	if b == nil {
		return nil, ErrActivityUnavailable
	}
	team := b.team(ctx)
	if team == 0 {
		return nil, ErrActivityUnavailable
	}
	limit := clampPage(q.Limit)
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []UsagePoint
	for _, row := range b.usage {
		if row.team != team || row.point.DatabaseID != q.DatabaseID {
			continue
		}
		if !q.Since.IsZero() && row.point.At.Before(q.Since) {
			continue
		}
		if !q.Until.IsZero() && row.point.At.After(q.Until) {
			continue
		}
		out = append(out, row.point)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > limit {
		out = out[:limit]
	}

	return out, nil
}

func (b *MemoryBook) RecordPhase(ctx context.Context, step JoinStep) error {
	if b == nil {
		return nil
	}
	team := b.team(ctx)
	if team == 0 || step.Environment == "" || step.Phase == "" {
		return nil
	}
	step.Output = clipTail(ScrubTail(step.Output))
	if step.StartedAt.IsZero() {
		step.StartedAt = time.Now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.phases {
		cur := &b.phases[i]
		if cur.team == team && cur.step.Environment == step.Environment && cur.step.RunID == step.RunID && cur.step.Phase == step.Phase {
			if step.Output == "" {
				step.Output = cur.step.Output
			}
			if step.StartedAt.IsZero() {
				step.StartedAt = cur.step.StartedAt
			}
			cur.step = step

			return nil
		}
	}
	b.phases = append(b.phases, storedPhase{team: team, step: step})

	return nil
}

func (b *MemoryBook) ListJoins(ctx context.Context, environment string) ([]JoinRun, error) {
	if b == nil {
		return nil, ErrActivityUnavailable
	}
	team := b.team(ctx)
	if team == 0 {
		return nil, ErrActivityUnavailable
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	matched := make([]storedPhase, 0)
	for _, row := range b.phases {
		if row.team != team || row.step.Environment != environment {
			continue
		}
		matched = append(matched, row)
	}
	if len(matched) > MaxJoinPhases {
		matched = matched[len(matched)-MaxJoinPhases:]
	}
	order := []string{}
	grouped := map[string][]JoinStep{}
	for _, row := range matched {
		if _, ok := grouped[row.step.RunID]; !ok {
			order = append(order, row.step.RunID)
		}
		grouped[row.step.RunID] = append(grouped[row.step.RunID], row.step)
	}
	out := make([]JoinRun, 0, len(order))
	for _, id := range order {
		out = append(out, JoinRun{RunID: id, Phases: grouped[id]})
	}

	return out, nil
}

func (b *MemoryBook) LastPhases(ctx context.Context) map[string]string {
	out := map[string]string{}
	if b == nil {
		return out
	}
	team := b.team(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	var best = map[string]string{}
	for _, row := range b.entries {
		if row.team != team || row.entry.Kind != HistoryTransition || row.entry.DatabaseID == "" {
			continue
		}
		if row.entry.ID >= best[row.entry.DatabaseID] {
			best[row.entry.DatabaseID] = row.entry.ID
			out[row.entry.DatabaseID] = row.entry.ToPhase
		}
	}

	return out
}

func (b *MemoryBook) sameTransition(ctx context.Context, entry HistoryEntry) bool {
	team := b.team(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	var best, from, to string
	found := false
	for _, row := range b.entries {
		if row.team != team || row.entry.Kind != HistoryTransition || row.entry.DatabaseID != entry.DatabaseID {
			continue
		}
		if found && row.entry.ID < best {
			continue
		}
		best = row.entry.ID
		from = row.entry.FromPhase
		to = row.entry.ToPhase
		found = true
	}

	return found && from == entry.FromPhase && to == entry.ToPhase
}

func (b *MemoryBook) Observe(
	ctx context.Context,
	environment string,
	prev, next []engine.Instance,
	backups []engine.Backup,
	restores []engine.Restore,
	events []PodEvent,
	usage []UsagePoint,
) error {
	if b == nil {
		return nil
	}
	now := time.Now().UTC()
	for _, entry := range DiffTransitions(environment, prev, next, now) {
		if b.sameTransition(ctx, entry) {
			continue
		}
		_ = b.RecordEntry(ctx, entry)
	}
	byDB := map[string][]engine.Backup{}
	for _, backup := range backups {
		id := backup.Instance
		byDB[id] = append(byDB[id], backup)
	}
	for id, items := range byDB {
		for _, entry := range BackupEntries(environment, id, items, now) {
			_ = b.RecordEntry(ctx, entry)
		}
	}
	for _, entry := range RestoreEntries(environment, "", restores, now) {
		_ = b.RecordEntry(ctx, entry)
	}
	for _, event := range events {
		_ = b.RecordEntry(ctx, HistoryEntry{
			DatabaseID:  event.DatabaseID,
			Environment: environment,
			Kind:        HistoryEvent,
			At:          event.At,
			EventType:   event.Type,
			Reason:      event.Reason,
			Message:     Sanitize(event.Message),
			Count:       event.Count,
		})
	}
	for _, point := range usage {
		point.Environment = environment
		_, _ = b.RecordUsage(ctx, point)
	}

	return nil
}

func actionEntry(e ActivityEvent) HistoryEntry {
	return HistoryEntry{
		DatabaseID:   e.DatabaseID,
		Environment:  e.Environment,
		Kind:         HistoryAction,
		At:           e.At,
		Actor:        e.Actor,
		UserID:       e.UserID,
		TeamID:       e.TeamID,
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
	}
}

func entryAction(e HistoryEntry) ActivityEvent {
	return ActivityEvent{
		ID:           e.ID,
		At:           e.At,
		Actor:        e.Actor,
		UserID:       e.UserID,
		TeamID:       e.TeamID,
		Client:       e.Client,
		Agent:        e.Agent,
		RequestID:    e.RequestID,
		Action:       e.Action,
		DatabaseID:   e.DatabaseID,
		Environment:  e.Environment,
		Target:       e.Target,
		Outcome:      e.Outcome,
		Detail:       e.Detail,
		Error:        e.Error,
		ResourceKind: e.ResourceKind,
		ResourceName: e.ResourceName,
	}
}

func clip(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= MaxActivityText {
		return text
	}

	return text[:MaxActivityText]
}

func clipTail(text string) string {
	const max = 4000
	if len(text) <= max {
		return text
	}

	return text[len(text)-max:]
}
