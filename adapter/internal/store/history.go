package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// maxJoinPhases bounds one environment history read. Older rows stay stored.
const maxJoinPhases = 200

func eventIdentity(row HistoryRow) string {
	sum := fnv.New64a()
	_, _ = fmt.Fprintf(sum, "%s\x00%s\x00%d\x00%s\x00%s",
		row.DatabaseID, row.Environment, row.At.UTC().UnixNano(), row.Reason, row.Message)

	return fmt.Sprintf("event-%x", sum.Sum64())
}

// InsertHistory appends one row and returns it with its id. A backup or
// restore with the same object id is updated in place so the row survives
// after the Kubernetes object is gone.
func (h *HubState) InsertHistory(ctx context.Context, row HistoryRow) (HistoryRow, error) {
	if _, err := h.scope(ctx); err != nil {
		return HistoryRow{}, err
	}
	row.TeamID = h.team
	if row.At.IsZero() {
		row.At = time.Now().UTC()
	}
	if row.Kind == "event" {
		row.ObjectID = eventIdentity(row)
		err := h.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "team_id"},
				{Name: "database_id"},
				{Name: "environment"},
				{Name: "kind"},
				{Name: "object_id"},
			},
			TargetWhere: clause.Where{Exprs: []clause.Expression{
				clause.Expr{SQL: "object_id <> ''"},
			}},
			DoNothing: true,
		}).Create(&row).Error
		if err != nil {
			return HistoryRow{}, fmt.Errorf("write history: %w", scrub(err))
		}
		return row, nil
	}
	if row.ObjectID != "" && (row.Kind == "backup" || row.Kind == "restore") {
		err := h.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "team_id"},
				{Name: "database_id"},
				{Name: "environment"},
				{Name: "kind"},
				{Name: "object_id"},
			},
			TargetWhere: clause.Where{Exprs: []clause.Expression{
				clause.Expr{SQL: "object_id <> ''"},
			}},
			DoUpdates: clause.AssignmentColumns([]string{
				"at", "outcome", "message", "size_bytes", "storage",
				"started_at", "finished_at", "duration_ms",
			}),
		}).Create(&row).Error
		if err != nil {
			return HistoryRow{}, fmt.Errorf("write history: %w", scrub(err))
		}
		return row, nil
	}
	if err := h.db.WithContext(ctx).Create(&row).Error; err != nil {
		return HistoryRow{}, fmt.Errorf("write history: %w", scrub(err))
	}

	return row, nil
}

// HistoryFilter selects history rows. Cursor is an exclusive upper id.
type HistoryFilter struct {
	DatabaseID  string
	Environment string
	Kind        string
	Since       time.Time
	Until       time.Time
	Cursor      uint64
	MinID       uint64
	Limit       int
}

// ListHistory returns newest rows first.
func (h *HubState) ListHistory(ctx context.Context, f HistoryFilter) ([]HistoryRow, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return nil, err
	}
	q := tx.Model(&HistoryRow{})
	if f.DatabaseID != "" {
		q = q.Where("database_id = ?", f.DatabaseID)
	}
	if f.Environment != "" {
		q = q.Where("environment = ?", f.Environment)
	}
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if !f.Since.IsZero() {
		q = q.Where("at >= ?", f.Since.UTC())
	}
	if !f.Until.IsZero() {
		q = q.Where("at <= ?", f.Until.UTC())
	}
	if f.Cursor > 0 {
		q = q.Where("id < ?", f.Cursor)
	}
	if f.MinID > 0 {
		q = q.Where("id > ?", f.MinID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	var rows []HistoryRow
	if err := q.Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list history: %w", scrub(err))
	}

	return rows, nil
}

// ActionByID returns one action row. A missing id is gorm.ErrRecordNotFound.
func (h *HubState) ActionByID(ctx context.Context, id uint64) (HistoryRow, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return HistoryRow{}, err
	}
	var row HistoryRow
	err = tx.Where("id = ? AND kind = ?", id, "action").Take(&row).Error
	if err != nil {
		return HistoryRow{}, fmt.Errorf("read history: %w", scrub(err))
	}

	return row, nil
}
func (h *HubState) NewestActionID(ctx context.Context) (uint64, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return 0, err
	}
	var id uint64
	err = tx.Model(&HistoryRow{}).Where("kind = ?", "action").Select("COALESCE(MAX(id), 0)").Scan(&id).Error
	if err != nil {
		return 0, fmt.Errorf("read history cursor: %w", scrub(err))
	}

	return id, nil
}

// usageLockClass is the advisory lock class for one database's usage samples.
// It has to differ from the schema and import locks so those waits do not
// queue behind a heartbeat.
const usageLockClass int32 = 1735289602

// InsertUsageIfDue stores one sample when it is at least minGap after the
// newest sample for that database. The check and the insert share one
// transaction and an advisory lock, so two replicas cannot both decide the
// gap has elapsed.
func (h *HubState) InsertUsageIfDue(ctx context.Context, row UsageSample, minGap time.Duration) (UsageSample, bool, error) {
	if _, err := h.scope(ctx); err != nil {
		return UsageSample{}, false, err
	}
	row.TeamID = h.team
	if row.At.IsZero() {
		row.At = time.Now().UTC()
	}
	kept := false
	err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			"SELECT pg_advisory_xact_lock(?, ?)",
			usageLockClass, usageLockKey(h.team, row.DatabaseID),
		).Error; err != nil {
			return err
		}
		var at sql.NullTime
		err := tx.Model(&UsageSample{}).
			Where("team_id = ? AND database_id = ?", h.team, row.DatabaseID).
			Select("MAX(at)").Scan(&at).Error
		if err != nil {
			return err
		}
		if at.Valid && row.At.Before(at.Time.Add(minGap)) {
			return nil
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		kept = true

		return nil
	})
	if err != nil {
		return UsageSample{}, false, fmt.Errorf("write usage: %w", scrub(err))
	}

	return row, kept, nil
}

func usageLockKey(team uint, databaseID string) int32 {
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(strconv.FormatUint(uint64(team), 10)))
	_, _ = sum.Write([]byte{0})
	_, _ = sum.Write([]byte(databaseID))

	return int32(sum.Sum32()) //nolint:gosec // the wrap is the intent: any fixed mapping onto int32 works.
}

// ListUsage returns newest samples first.
func (h *HubState) ListUsage(ctx context.Context, databaseID string, since, until time.Time, limit int) ([]UsageSample, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return nil, err
	}
	q := tx.Model(&UsageSample{}).Where("database_id = ?", databaseID)
	if !since.IsZero() {
		q = q.Where("at >= ?", since.UTC())
	}
	if !until.IsZero() {
		q = q.Where("at <= ?", until.UTC())
	}
	if limit <= 0 {
		limit = 100
	}
	var rows []UsageSample
	if err := q.Order("at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list usage: %w", scrub(err))
	}

	return rows, nil
}

// PruneUsage deletes this team's samples taken before the cut and reports
// how many went.
func (h *HubState) PruneUsage(ctx context.Context, before time.Time) (int64, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return 0, err
	}
	res := tx.Where("at < ?", before.UTC()).Delete(&UsageSample{})
	if res.Error != nil {
		return 0, fmt.Errorf("prune usage: %w", scrub(res.Error))
	}

	return res.RowsAffected, nil
}

// PruneUsage deletes every team's samples taken before the cut, in one
// statement, and reports how many went. It is the sweep that covers a team
// whose environments have stopped reporting and so trigger no per-team prune.
func (s *Store) PruneUsage(ctx context.Context, before time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, ErrNotConnected
	}
	res := s.db.WithContext(ctx).Where("at < ?", before.UTC()).Delete(&UsageSample{})
	if res.Error != nil {
		return 0, fmt.Errorf("prune usage: %w", scrub(res.Error))
	}

	return res.RowsAffected, nil
}

// UpsertJoinPhase inserts or replaces one phase of one run.
func (h *HubState) UpsertJoinPhase(ctx context.Context, row JoinPhase) error {
	if _, err := h.scope(ctx); err != nil {
		return err
	}
	row.TeamID = h.team
	if row.StartedAt.IsZero() {
		row.StartedAt = time.Now().UTC()
	}
	err := h.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "team_id"},
			{Name: "environment"},
			{Name: "run_id"},
			{Name: "phase"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"outcome":     gorm.Expr("EXCLUDED.outcome"),
			"finished_at": gorm.Expr("EXCLUDED.finished_at"),
			"duration_ms": gorm.Expr("EXCLUDED.duration_ms"),
			"output":      gorm.Expr("CASE WHEN EXCLUDED.output = '' THEN hub_join_phases.output ELSE EXCLUDED.output END"),
			"started_at":  gorm.Expr("CASE WHEN hub_join_phases.started_at IS NULL THEN EXCLUDED.started_at ELSE hub_join_phases.started_at END"),
		}),
	}).Create(&row).Error
	if err != nil {
		return fmt.Errorf("write join phase: %w", scrub(err))
	}

	return nil
}

// ListJoinPhases returns phases for one environment, oldest first.
func (h *HubState) ListJoinPhases(ctx context.Context, environment string) ([]JoinPhase, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return nil, err
	}
	var rows []JoinPhase
	if err := tx.Where("environment = ?", environment).Order("id DESC").Limit(maxJoinPhases).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list join phases: %w", scrub(err))
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}

	return rows, nil
}

// LastTransitionPhase is the newest to_phase per database.
func (h *HubState) LastTransitionPhase(ctx context.Context) (map[string]string, error) {
	rows, err := h.ListHistory(ctx, HistoryFilter{Kind: "transition", Limit: 5000})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	// Newest first, so the first sighting of a database is the latest phase.
	for _, row := range rows {
		if _, ok := out[row.DatabaseID]; ok || row.DatabaseID == "" {
			continue
		}
		out[row.DatabaseID] = row.ToPhase
	}

	return out, nil
}

// UserNames returns the display name of each User that still exists, keyed by
// id. Users are global, so this is not scoped to the team. An id with no
// User row is left out: that User was removed.
func (h *HubState) UserNames(ctx context.Context, ids []uint) (map[uint]string, error) {
	if h == nil || h.db == nil {
		return nil, errors.New("no database is configured for this hub state")
	}

	var users []User
	if err := h.db.WithContext(ctx).Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, fmt.Errorf("read users: %w", scrub(err))
	}

	out := make(map[uint]string, len(users))
	for _, user := range users {
		out[user.ID] = displayName(user)
	}

	return out, nil
}

// displayName is the name a person is shown under: the name they gave, then
// the login they signed in with, then their email, then the provider subject.
func displayName(user User) string {
	for _, name := range []string{user.Name, user.Username, user.Email, user.Subject} {
		if name != "" {
			return name
		}
	}

	return ""
}
