package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The refusals of CreateOrganization. Each is a limit rather than a failure,
// so callers tell the person instead of logging an error.
var (
	// ErrCreationCap means the person already made as many Organizations as
	// one person may.
	ErrCreationCap = errors.New("the person has made as many organizations as they may")
	// ErrOrganizationCeiling means this deployment holds as many Organizations
	// as it accepts.
	ErrOrganizationCeiling = errors.New("this deployment holds as many organizations as it accepts")
)

// organizationLockKey serializes Organization creation, so two signups cannot
// both pass a count and then both insert. Transaction scoped, and fixed for
// the same reason schemaLockKey is.
const organizationLockKey int64 = 7264193058471003

// NewOrganization is what creating an Organization needs: the person, the
// name, and the limits to enforce under the lock.
type NewOrganization struct {
	// UserID names a User already on file. Without it the identity below is
	// found by issuer and subject, or created.
	UserID  uint
	Issuer  string
	Subject string
	Email   string
	Name    string

	OrganizationName string
	// SessionHash, when set, names a session of the User that moves into the
	// new Organization in the same transaction.
	SessionHash string
	// PerPersonCap is how many Organizations one User may create, and
	// OrganizationCeiling how many the deployment holds. Zero for either
	// means no limit.
	PerPersonCap        int
	OrganizationCeiling int
}

// UserOrganization is one Organization a User belongs to.
type UserOrganization struct {
	ID   uint
	Name string
	Role string
}

// CreateOrganization makes an Organization, its default Team and its first
// admin in one transaction, finding or creating the User first. The new
// Organization becomes the User's last used. A refusal leaves nothing behind,
// not even the User.
func (i *Identity) CreateOrganization(ctx context.Context, in NewOrganization, now time.Time) (Placement, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return Placement{}, err
	}

	var user User
	var orgID uint
	err = tx.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", organizationLockKey).Error; err != nil {
			return fmt.Errorf("lock organization creation: %w", scrub(err))
		}

		var err error
		if user, err = findOrCreateUser(tx, in, now); err != nil {
			return err
		}

		if err := checkCreationLimits(tx, user.ID, in); err != nil {
			return err
		}

		org := Organization{Name: in.OrganizationName, CreatedAt: now, CreatedBy: user.ID}
		if err := tx.Create(&org).Error; err != nil {
			return fmt.Errorf("create organization: %w", scrub(err))
		}

		team := Team{OrganizationID: org.ID, Name: defaultTeamName, CreatedAt: now}
		if err := tx.Create(&team).Error; err != nil {
			return fmt.Errorf("create team: %w", scrub(err))
		}

		membership := Membership{
			UserID: user.ID, OrganizationID: org.ID, TeamID: team.ID, Role: RoleAdmin, CreatedAt: now,
		}
		if err := tx.Create(&membership).Error; err != nil {
			return fmt.Errorf("create membership: %w", scrub(err))
		}

		orgID = org.ID

		if in.SessionHash != "" {
			moved := tx.Model(&Session{}).
				Where("hash = ? AND user_id = ?", strings.TrimSpace(in.SessionHash), user.ID).
				Update("organization_id", org.ID)
			if moved.Error != nil {
				return fmt.Errorf("move session: %w", scrub(moved.Error))
			}
			if moved.RowsAffected == 0 {
				return ErrSessionUnknown
			}
		}

		if err := tx.Model(&User{}).Where("id = ?", user.ID).Update("last_organization_id", org.ID).Error; err != nil {
			return fmt.Errorf("record last used organization: %w", scrub(err))
		}

		return nil
	})
	if err != nil {
		return Placement{}, err
	}

	return i.PlacementOf(ctx, user.ID, orgID)
}

func findOrCreateUser(tx *gorm.DB, in NewOrganization, now time.Time) (User, error) {
	var user User
	if in.UserID != 0 {
		if err := tx.Take(&user, in.UserID).Error; err != nil {
			return User{}, fmt.Errorf("read user: %w", scrub(err))
		}

		return user, nil
	}

	user = User{
		Issuer: in.Issuer, Subject: in.Subject, Email: in.Email, Name: in.Name,
		CreatedAt: now, UpdatedAt: now,
	}
	// A second signup of the same identity finds the row the first made.
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&user).Error; err != nil {
		return User{}, fmt.Errorf("create user: %w", scrub(err))
	}
	if err := tx.Where("issuer = ? AND subject = ?", in.Issuer, in.Subject).Take(&user).Error; err != nil {
		return User{}, fmt.Errorf("read user: %w", scrub(err))
	}

	return user, nil
}

func checkCreationLimits(tx *gorm.DB, userID uint, in NewOrganization) error {
	if in.PerPersonCap > 0 {
		var made int64
		if err := tx.Model(&Organization{}).Where("created_by = ?", userID).Count(&made).Error; err != nil {
			return fmt.Errorf("count organizations made: %w", scrub(err))
		}
		if made >= int64(in.PerPersonCap) {
			return ErrCreationCap
		}
	}

	if in.OrganizationCeiling > 0 {
		var all int64
		if err := tx.Model(&Organization{}).Count(&all).Error; err != nil {
			return fmt.Errorf("count organizations: %w", scrub(err))
		}
		if all >= int64(in.OrganizationCeiling) {
			return ErrOrganizationCeiling
		}
	}

	return nil
}

// UserOrganizations lists the Organizations a User belongs to, oldest first.
func (i *Identity) UserOrganizations(ctx context.Context, userID uint) ([]UserOrganization, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return nil, err
	}

	var out []UserOrganization
	err = tx.Table("hub_memberships m").
		Select("o.id AS id, o.name AS name, m.role AS role").
		Joins("JOIN hub_organizations o ON o.id = m.organization_id").
		Where("m.user_id = ?", userID).
		Order("o.id").
		Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", scrub(err))
	}

	return out, nil
}

// SwitchOrganization moves a User's session into another Organization they
// belong to and records it as their last used. ErrNoMembership means they do
// not belong to it, which reads the same whether it exists or not.
func (i *Identity) SwitchOrganization(ctx context.Context, sessionHash string, userID, organizationID uint) error {
	tx, err := i.scope(ctx)
	if err != nil {
		return err
	}

	return tx.Transaction(func(tx *gorm.DB) error {
		var member int64
		err := tx.Model(&Membership{}).
			Where("user_id = ? AND organization_id = ?", userID, organizationID).Count(&member).Error
		if err != nil {
			return fmt.Errorf("read membership: %w", scrub(err))
		}
		if member == 0 {
			return ErrNoMembership
		}

		moved := tx.Model(&Session{}).
			Where("hash = ? AND user_id = ?", strings.TrimSpace(sessionHash), userID).
			Update("organization_id", organizationID)
		if moved.Error != nil {
			return fmt.Errorf("move session: %w", scrub(moved.Error))
		}
		if moved.RowsAffected == 0 {
			return ErrSessionUnknown
		}

		return tx.Model(&User{}).Where("id = ?", userID).Update("last_organization_id", organizationID).Error
	})
}
