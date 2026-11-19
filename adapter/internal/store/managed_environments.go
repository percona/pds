package store

import (
	"context"
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
	StackFailureSeen bool      `gorm:"not null;default:false"`
	CreatedAt        time.Time `gorm:"not null"`
	UpdatedAt        time.Time `gorm:"not null"`
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
