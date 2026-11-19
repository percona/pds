package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// What the managed environment store can refuse.
var (
	// ErrManagedEnvironmentExists means the Team already has a managed
	// environment with that name.
	ErrManagedEnvironmentExists = errors.New("the team already has a managed environment with that name")
	// ErrManagedEnvironmentUnknown means the Team has no such managed
	// environment. Another Team's reads the same.
	ErrManagedEnvironmentUnknown = errors.New("the team has no such managed environment")
)

// ManagedEnvironment is what a provisioning run needs to know about an
// environment the control plane builds in a customer's AWS account. It holds no
// credential: the connection's role is assumed afresh for every step.
type ManagedEnvironment struct {
	// ID is the value of the percona.com/pds tag on everything the environment
	// owns in AWS, and part of its stack and cluster names.
	ID     string `gorm:"primaryKey;size:12"`
	TeamID uint   `gorm:"not null;uniqueIndex:uidx_hub_managed_environments_name,priority:1"`
	Name   string `gorm:"size:253;not null;uniqueIndex:uidx_hub_managed_environments_name,priority:2"`
	// ConnectionID is the cloud connection the environment was created through.
	ConnectionID string `gorm:"size:32;not null"`
	Region       string `gorm:"size:32;not null"`
	// StackFailureSeen records that a failed stack was reported to the person.
	// The next attempt then deletes it and starts the stack again.
	StackFailureSeen bool `gorm:"not null;default:false"`
	// ChartVersion is the pds chart version the environment's release in Git
	// names. It is empty until the release is first written.
	ChartVersion string `gorm:"size:64;not null;default:''"`
	// ReadyAt is when the environment first reported to the control plane. An
	// environment that never did is not Lost and is never upgraded.
	ReadyAt *time.Time
	// LostAt is when the cluster was first found missing while the environment
	// had stopped reporting. Rebuild or a returning heartbeat clears it.
	LostAt *time.Time
	// MaintenanceDay, MaintenanceHour and MaintenanceZone are the weekly window
	// in which the control plane may commit a chart upgrade.
	MaintenanceDay  int    `gorm:"not null;default:0"`
	MaintenanceHour int    `gorm:"not null;default:2"`
	MaintenanceZone string `gorm:"size:64;not null;default:'UTC'"`
	// DeferredVersion was put off until DeferredUntil. A version is deferred
	// once, so the field also says it has been.
	DeferredVersion string `gorm:"size:64;not null;default:''"`
	DeferredUntil   *time.Time
	// SkippedVersion is the newest chart version that failed and was rolled back.
	// The control plane tries nothing at or below it until a User retries.
	SkippedVersion string `gorm:"size:64;not null;default:''"`
	// Drift lists what was changed outside PDS as of DriftCheckedAt.
	Drift          []DriftItem `gorm:"type:jsonb;serializer:json"`
	DriftCheckedAt *time.Time
	CreatedAt      time.Time `gorm:"not null"`
	UpdatedAt      time.Time `gorm:"not null"`
}

// DriftItem is one thing in an environment's infrastructure that was changed
// outside PDS.
type DriftItem struct {
	Resource string `json:"resource"`
	Detail   string `json:"detail"`
}

// MaintenanceWindow is the weekly time the control plane may upgrade an
// environment. It lasts MaintenanceLength from the start of Hour on Day in Zone.
type MaintenanceWindow struct {
	Day  time.Weekday
	Hour int
	Zone string
}

// MaintenanceLength is how long a maintenance window stays open.
const MaintenanceLength = 2 * time.Hour

// Window returns the environment's maintenance window.
func (e ManagedEnvironment) Window() MaintenanceWindow {
	return MaintenanceWindow{Day: time.Weekday(e.MaintenanceDay), Hour: e.MaintenanceHour, Zone: e.MaintenanceZone}
}

// TableName pins the table name.
func (ManagedEnvironment) TableName() string { return "hub_managed_environments" }

// NewManagedEnvironment is a managed environment to record.
type NewManagedEnvironment struct {
	ID           string
	TeamID       uint
	Name         string
	ConnectionID string
	Region       string
}

// ManagedEnvironments is the store of managed environments.
type ManagedEnvironments struct {
	// open returns the database, or ErrNotConnected while the adapter's
	// connection has not landed.
	open func(context.Context) (*gorm.DB, error)
}

// ManagedEnvironments returns the managed environment state.
func (s *Store) ManagedEnvironments() *ManagedEnvironments {
	return &ManagedEnvironments{open: func(ctx context.Context) (*gorm.DB, error) { return s.db.WithContext(ctx), nil }}
}

// ManagedEnvironments returns the managed environment state, resolved on each
// call, so it can be wired before the database connects.
func (s *Starter) ManagedEnvironments() *ManagedEnvironments {
	return &ManagedEnvironments{open: func(ctx context.Context) (*gorm.DB, error) {
		db := s.Store()
		if db == nil {
			return nil, ErrNotConnected
		}

		return db.db.WithContext(ctx), nil
	}}
}

// Create records a managed environment.
func (m *ManagedEnvironments) Create(ctx context.Context, in NewManagedEnvironment, now time.Time) (ManagedEnvironment, error) {
	db, err := m.open(ctx)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	row := ManagedEnvironment{
		ID: in.ID, TeamID: in.TeamID, Name: in.Name, ConnectionID: in.ConnectionID, Region: in.Region,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := db.Create(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ManagedEnvironment{}, ErrManagedEnvironmentExists
		}

		return ManagedEnvironment{}, fmt.Errorf("create managed environment: %w", scrub(err))
	}

	return row, nil
}

// Get returns one of the Team's managed environments by name.
func (m *ManagedEnvironments) Get(ctx context.Context, teamID uint, name string) (ManagedEnvironment, error) {
	db, err := m.open(ctx)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	var row ManagedEnvironment
	err = db.Where("team_id = ? AND name = ?", teamID, name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ManagedEnvironment{}, ErrManagedEnvironmentUnknown
	}
	if err != nil {
		return ManagedEnvironment{}, fmt.Errorf("read managed environment: %w", scrub(err))
	}

	return row, nil
}

// SetStackFailureSeen records whether a failed stack was reported.
func (m *ManagedEnvironments) SetStackFailureSeen(ctx context.Context, teamID uint, name string, seen bool) error {
	db, err := m.open(ctx)
	if err != nil {
		return err
	}
	res := db.Model(&ManagedEnvironment{}).Where("team_id = ? AND name = ?", teamID, name).
		Updates(map[string]any{"stack_failure_seen": seen, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return fmt.Errorf("update managed environment: %w", scrub(res.Error))
	}
	if res.RowsAffected == 0 {
		return ErrManagedEnvironmentUnknown
	}

	return nil
}

// Remove forgets a managed environment that was never started.
func (m *ManagedEnvironments) Remove(ctx context.Context, teamID uint, name string) error {
	db, err := m.open(ctx)
	if err != nil {
		return err
	}
	if err := db.Where("team_id = ? AND name = ?", teamID, name).Delete(&ManagedEnvironment{}).Error; err != nil {
		return fmt.Errorf("remove managed environment: %w", scrub(err))
	}

	return nil
}

// All lists every Team's managed environments, for the jobs that look after
// them all.
func (m *ManagedEnvironments) All(ctx context.Context) ([]ManagedEnvironment, error) {
	db, err := m.open(ctx)
	if err != nil {
		return nil, err
	}
	var rows []ManagedEnvironment
	if err := db.Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list managed environments: %w", scrub(err))
	}

	return rows, nil
}

// update changes columns of one of the Team's managed environments.
func (m *ManagedEnvironments) update(ctx context.Context, teamID uint, name string, set map[string]any) error {
	db, err := m.open(ctx)
	if err != nil {
		return err
	}
	set["updated_at"] = time.Now().UTC()
	res := db.Model(&ManagedEnvironment{}).Where("team_id = ? AND name = ?", teamID, name).Updates(set)
	if res.Error != nil {
		return fmt.Errorf("update managed environment: %w", scrub(res.Error))
	}
	if res.RowsAffected == 0 {
		return ErrManagedEnvironmentUnknown
	}

	return nil
}

// SetChartVersion records the chart version the environment's release names.
func (m *ManagedEnvironments) SetChartVersion(ctx context.Context, teamID uint, name, version string) error {
	return m.update(ctx, teamID, name, map[string]any{"chart_version": version})
}

// MarkReady records that the environment has reported, keeping the first time,
// and clears Lost.
func (m *ManagedEnvironments) MarkReady(ctx context.Context, teamID uint, name string, now time.Time) error {
	db, err := m.open(ctx)
	if err != nil {
		return err
	}
	res := db.Model(&ManagedEnvironment{}).Where("team_id = ? AND name = ?", teamID, name).Updates(map[string]any{
		"ready_at": gorm.Expr("COALESCE(ready_at, ?)", now.UTC()), "lost_at": nil, "updated_at": now.UTC(),
	})
	if res.Error != nil {
		return fmt.Errorf("update managed environment: %w", scrub(res.Error))
	}
	if res.RowsAffected == 0 {
		return ErrManagedEnvironmentUnknown
	}

	return nil
}

// SetLost marks the environment Lost, keeping the time it was first found so, or
// clears it.
func (m *ManagedEnvironments) SetLost(ctx context.Context, teamID uint, name string, lost bool, now time.Time) error {
	if !lost {
		return m.update(ctx, teamID, name, map[string]any{"lost_at": nil})
	}

	return m.update(ctx, teamID, name, map[string]any{"lost_at": gorm.Expr("COALESCE(lost_at, ?)", now.UTC())})
}

// SetMaintenanceWindow records the weekly window the environment may be upgraded in.
func (m *ManagedEnvironments) SetMaintenanceWindow(ctx context.Context, teamID uint, name string, w MaintenanceWindow) error {
	return m.update(ctx, teamID, name, map[string]any{
		"maintenance_day": int(w.Day), "maintenance_hour": w.Hour, "maintenance_zone": w.Zone,
	})
}

// SetDeferral records that a chart version was put off until the time.
func (m *ManagedEnvironments) SetDeferral(ctx context.Context, teamID uint, name, version string, until time.Time) error {
	return m.update(ctx, teamID, name, map[string]any{"deferred_version": version, "deferred_until": until.UTC()})
}

// SetSkippedVersion records the newest chart version that failed and was rolled
// back, or clears it with an empty version.
func (m *ManagedEnvironments) SetSkippedVersion(ctx context.Context, teamID uint, name, version string) error {
	return m.update(ctx, teamID, name, map[string]any{"skipped_version": version})
}

// SetDrift records what the latest check found changed outside PDS.
func (m *ManagedEnvironments) SetDrift(ctx context.Context, teamID uint, name string, items []DriftItem, now time.Time) error {
	raw, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("encode drift: %w", err)
	}

	return m.update(ctx, teamID, name, map[string]any{"drift": raw, "drift_checked_at": now.UTC()})
}
