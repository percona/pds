package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// States of a cloud connection.
const (
	CloudPending     = "pending"
	CloudReady       = "ready"
	CloudUnreachable = "unreachable"
	CloudOutOfDate   = "out_of_date"
)

// Statuses of a step the connect script reports.
const (
	CloudStepStarted   = "started"
	CloudStepSucceeded = "succeeded"
	CloudStepFailed    = "failed"
	CloudStepHeartbeat = "heartbeat"
)

// What the cloud connection store can refuse.
var (
	// ErrCloudTokenInvalid means the token is unknown, expired or revoked. The
	// three read alike so the route cannot be used to probe for tokens.
	ErrCloudTokenInvalid = errors.New("the connect token is not valid")
	// ErrCloudTokenAccount means the token already enrolled another account.
	ErrCloudTokenAccount = errors.New("the connect token belongs to another account")
	// ErrCloudConnectionUnknown means the Team has no such connection. Another
	// Team's reads the same.
	ErrCloudConnectionUnknown = errors.New("the team has no such cloud connection")
	// ErrCloudConnectionReady means the connection has been Ready, so it is
	// removed by deleting its stack, not by cancelling.
	ErrCloudConnectionReady = errors.New("the cloud connection has been ready")
)

// CloudConnections is the store of cloud connections, tokens and events.
type CloudConnections struct {
	// open returns the database, or ErrNotConnected while the adapter's
	// connection has not landed.
	open func(context.Context) (*gorm.DB, error)
}

// CloudConnections returns the cloud connection state.
func (s *Store) CloudConnections() *CloudConnections {
	return &CloudConnections{open: func(ctx context.Context) (*gorm.DB, error) { return s.db.WithContext(ctx), nil }}
}

// CloudConnections returns the cloud connection state, resolved on each call,
// so it can be wired before the database connects.
func (s *Starter) CloudConnections() *CloudConnections {
	return &CloudConnections{open: func(ctx context.Context) (*gorm.DB, error) {
		db := s.Store()
		if db == nil {
			return nil, ErrNotConnected
		}

		return db.db.WithContext(ctx), nil
	}}
}

// NewCloudToken is a token to write. Only its hash arrives here.
type NewCloudToken struct {
	TeamID         uint
	Hash           string
	AllowedRegions []string
	ExpiresAt      time.Time
}

// CloudEnrollment is the script naming its account. The New fields are the
// values to use when the account has no connection yet.
type CloudEnrollment struct {
	TokenHash   string
	AccountID   string
	Alias       string
	StackRegion string
	// AllowedRegions, when set, replaces the token's regions. The caller has
	// already checked it narrows them.
	AllowedRegions []string

	NewID         string
	NewExternalID string
	NewStackName  string
}

// CloudCheck is what a connection check found.
type CloudCheck struct {
	State           string
	Reason          string
	TemplateVersion int
}

// CreateToken stores a token.
func (c *CloudConnections) CreateToken(ctx context.Context, in NewCloudToken) error {
	db, err := c.open(ctx)
	if err != nil {
		return err
	}
	row := CloudToken{
		Hash: in.Hash, TeamID: in.TeamID, AllowedRegions: in.AllowedRegions,
		ExpiresAt: in.ExpiresAt, CreatedAt: time.Now().UTC(),
	}
	if err = db.Create(&row).Error; err != nil {
		return fmt.Errorf("store cloud token: %w", scrub(err))
	}

	return nil
}

// RevokeToken ends one token. An unknown token is not an error.
func (c *CloudConnections) RevokeToken(ctx context.Context, teamID uint, hash string, now time.Time) error {
	db, err := c.open(ctx)
	if err != nil {
		return err
	}
	err = db.Model(&CloudToken{}).
		Where("team_id = ? AND hash = ? AND revoked_at IS NULL", teamID, hash).
		Update("revoked_at", now.UTC()).Error
	if err != nil {
		return fmt.Errorf("revoke cloud token: %w", scrub(err))
	}

	return nil
}

// RevokeTokenByID ends one of the Team's tokens by its public id, the first 16
// characters of its hash, and returns it. A token that is unknown, another
// Team's or already revoked reads as ErrCloudTokenInvalid.
func (c *CloudConnections) RevokeTokenByID(ctx context.Context, teamID uint, id string, now time.Time) (CloudToken, error) {
	if len(id) != 16 {
		return CloudToken{}, ErrCloudTokenInvalid
	}
	db, err := c.open(ctx)
	if err != nil {
		return CloudToken{}, err
	}
	var token CloudToken
	err = db.Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("team_id = ? AND substr(hash, 1, 16) = ? AND revoked_at IS NULL", teamID, id).
			First(&token).Error
		if err != nil {
			return err
		}

		return tx.Model(&token).Update("revoked_at", now.UTC()).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CloudToken{}, ErrCloudTokenInvalid
	}
	if err != nil {
		return CloudToken{}, fmt.Errorf("revoke cloud token: %w", scrub(err))
	}

	return token, nil
}

// TokenByID reads one of the Team's tokens by its public id, whether or not it
// still works. A token that is unknown or another Team's reads as
// ErrCloudTokenInvalid.
func (c *CloudConnections) TokenByID(ctx context.Context, teamID uint, id string) (CloudToken, error) {
	if len(id) != 16 {
		return CloudToken{}, ErrCloudTokenInvalid
	}
	db, err := c.open(ctx)
	if err != nil {
		return CloudToken{}, err
	}
	var token CloudToken
	err = db.Where("team_id = ? AND substr(hash, 1, 16) = ?", teamID, id).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CloudToken{}, ErrCloudTokenInvalid
	}
	if err != nil {
		return CloudToken{}, fmt.Errorf("read cloud token: %w", scrub(err))
	}

	return token, nil
}

// TokenTeam returns the Team a live token acts for.
func (c *CloudConnections) TokenTeam(ctx context.Context, hash string, now time.Time) (CloudToken, error) {
	db, err := c.open(ctx)
	if err != nil {
		return CloudToken{}, err
	}
	var token CloudToken
	err = db.
		Where("hash = ? AND revoked_at IS NULL AND expires_at > ?", hash, now.UTC()).
		First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CloudToken{}, ErrCloudTokenInvalid
	}
	if err != nil {
		return CloudToken{}, fmt.Errorf("read cloud token: %w", scrub(err))
	}

	return token, nil
}

// Enroll resolves a live token and returns the Team's connection for the
// account, making it on the first call. The token is locked for the call, so
// two enrollments with one token are taken in turn, and the unique index on
// (Team, account) settles two tokens racing to the same account.
func (c *CloudConnections) Enroll(ctx context.Context, in CloudEnrollment, now time.Time) (CloudConnection, error) {
	db, err := c.open(ctx)
	if err != nil {
		return CloudConnection{}, err
	}
	var out CloudConnection
	err = db.Transaction(func(tx *gorm.DB) error {
		var token CloudToken
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("hash = ? AND revoked_at IS NULL AND expires_at > ?", in.TokenHash, now.UTC()).
			First(&token).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCloudTokenInvalid
		}
		if err != nil {
			return err
		}

		regions := token.AllowedRegions
		if len(in.AllowedRegions) > 0 {
			regions = in.AllowedRegions
		}
		fresh := CloudConnection{
			ID: in.NewID, TeamID: token.TeamID, AccountID: in.AccountID, Alias: in.Alias,
			ExternalID: in.NewExternalID, StackName: in.NewStackName, StackRegion: in.StackRegion,
			AllowedRegions: regions, State: CloudPending,
			CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
		}
		err = tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "team_id"}, {Name: "account_id"}},
			DoNothing: true,
		}).Create(&fresh).Error
		if err != nil {
			return err
		}
		if err := tx.Where("team_id = ? AND account_id = ?", token.TeamID, in.AccountID).First(&out).Error; err != nil {
			return err
		}
		if token.ConnectionID != "" && token.ConnectionID != out.ID {
			return ErrCloudTokenAccount
		}

		out.Alias, out.StackRegion, out.AllowedRegions, out.UpdatedAt = in.Alias, in.StackRegion, regions, now.UTC()
		if err := tx.Model(&out).Select("alias", "stack_region", "allowed_regions", "updated_at").Updates(&out).Error; err != nil {
			return err
		}

		return tx.Model(&CloudToken{}).Where("hash = ?", in.TokenHash).Update("connection_id", out.ID).Error
	})
	if errors.Is(err, ErrCloudTokenInvalid) || errors.Is(err, ErrCloudTokenAccount) {
		return CloudConnection{}, err
	}
	if err != nil {
		return CloudConnection{}, fmt.Errorf("enroll cloud connection: %w", scrub(err))
	}

	return out, nil
}

// Get returns one of the Team's connections.
func (c *CloudConnections) Get(ctx context.Context, teamID uint, id string) (CloudConnection, error) {
	db, err := c.open(ctx)
	if err != nil {
		return CloudConnection{}, err
	}
	var row CloudConnection
	err = db.Where("team_id = ? AND id = ?", teamID, id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CloudConnection{}, ErrCloudConnectionUnknown
	}
	if err != nil {
		return CloudConnection{}, fmt.Errorf("read cloud connection: %w", scrub(err))
	}

	return row, nil
}

// List returns the Team's connections, oldest first.
func (c *CloudConnections) List(ctx context.Context, teamID uint) ([]CloudConnection, error) {
	db, err := c.open(ctx)
	if err != nil {
		return nil, err
	}
	var rows []CloudConnection
	if err = db.Where("team_id = ?", teamID).Order("created_at, id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list cloud connections: %w", scrub(err))
	}

	return rows, nil
}

// All returns every Team's connections, for the hourly check.
func (c *CloudConnections) All(ctx context.Context) ([]CloudConnection, error) {
	db, err := c.open(ctx)
	if err != nil {
		return nil, err
	}
	var rows []CloudConnection
	if err = db.Order("created_at, id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list cloud connections: %w", scrub(err))
	}

	return rows, nil
}

// SaveCheck records a check. The first time a connection is Ready it stamps
// ReadyAt and revokes the tokens that enrolled it.
func (c *CloudConnections) SaveCheck(ctx context.Context, id string, in CloudCheck, now time.Time) (CloudConnection, error) {
	db, err := c.open(ctx)
	if err != nil {
		return CloudConnection{}, err
	}
	var row CloudConnection
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		stamp := now.UTC()
		row.State, row.Reason, row.CheckedAt, row.UpdatedAt = in.State, in.Reason, &stamp, stamp
		if in.TemplateVersion > 0 {
			row.TemplateVersion = in.TemplateVersion
		}
		firstReady := in.State == CloudReady && row.ReadyAt == nil
		if firstReady {
			row.ReadyAt = &stamp
		}
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		if !firstReady {
			return nil
		}

		return tx.Model(&CloudToken{}).Where("connection_id = ? AND revoked_at IS NULL", id).Update("revoked_at", stamp).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CloudConnection{}, ErrCloudConnectionUnknown
	}
	if err != nil {
		return CloudConnection{}, fmt.Errorf("save cloud connection check: %w", scrub(err))
	}

	return row, nil
}

// Cancel drops a connection that has never been Ready, with its tokens and
// events.
func (c *CloudConnections) Cancel(ctx context.Context, teamID uint, id string, now time.Time) error {
	db, err := c.open(ctx)
	if err != nil {
		return err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		var row CloudConnection
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("team_id = ? AND id = ?", teamID, id).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCloudConnectionUnknown
		}
		if err != nil {
			return err
		}
		if row.ReadyAt != nil {
			return ErrCloudConnectionReady
		}
		err = tx.Model(&CloudToken{}).Where("connection_id = ? AND revoked_at IS NULL", id).Update("revoked_at", now.UTC()).Error
		if err != nil {
			return err
		}
		if err := tx.Where("connection_id = ?", id).Delete(&CloudEvent{}).Error; err != nil {
			return err
		}

		return tx.Delete(&row).Error
	})
	if errors.Is(err, ErrCloudConnectionUnknown) || errors.Is(err, ErrCloudConnectionReady) {
		return err
	}
	if err != nil {
		return fmt.Errorf("cancel cloud connection: %w", scrub(err))
	}

	return nil
}

// Remove forgets one of the Team's connections, ready or not, with its tokens
// and events. It touches nothing in the customer's account.
func (c *CloudConnections) Remove(ctx context.Context, teamID uint, id string) error {
	db, err := c.open(ctx)
	if err != nil {
		return err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		var row CloudConnection
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("team_id = ? AND id = ?", teamID, id).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCloudConnectionUnknown
		}
		if err != nil {
			return err
		}
		if err := tx.Where("connection_id = ?", id).Delete(&CloudToken{}).Error; err != nil {
			return err
		}
		if err := tx.Where("connection_id = ?", id).Delete(&CloudEvent{}).Error; err != nil {
			return err
		}

		return tx.Delete(&row).Error
	})
	if errors.Is(err, ErrCloudConnectionUnknown) {
		return err
	}
	if err != nil {
		return fmt.Errorf("remove cloud connection: %w", scrub(err))
	}

	return nil
}

// RecordEvent keeps a step the script reported. A heartbeat is not kept, it
// only moves SignalAt.
func (c *CloudConnections) RecordEvent(ctx context.Context, id string, in CloudEvent, now time.Time) error {
	db, err := c.open(ctx)
	if err != nil {
		return err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if in.Status != CloudStepHeartbeat {
			row := CloudEvent{ConnectionID: id, Step: in.Step, Status: in.Status, Detail: in.Detail, CreatedAt: now.UTC()}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}

		return tx.Model(&CloudConnection{}).Where("id = ?", id).Update("signal_at", now.UTC()).Error
	})
	if err != nil {
		return fmt.Errorf("record cloud connection event: %w", scrub(err))
	}

	return nil
}

// Events returns the steps reported for one of the Team's connections, oldest first.
func (c *CloudConnections) Events(ctx context.Context, teamID uint, id string) ([]CloudEvent, error) {
	db, err := c.open(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := c.Get(ctx, teamID, id); err != nil {
		return nil, err
	}
	var rows []CloudEvent
	if err = db.Where("connection_id = ?", id).Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list cloud connection events: %w", scrub(err))
	}

	return rows, nil
}
