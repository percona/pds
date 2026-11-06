package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// What changing an Organization's Teams and Users can refuse.
var (
	// ErrTeamExists means the Organization already has a Team by that name.
	ErrTeamExists = errors.New("the organization already has a team by that name")
	// ErrOrganizationUnknown means there is no Organization with that id.
	ErrOrganizationUnknown = errors.New("the organization does not exist")
	// ErrLastAdmin means the change would leave the Organization with no admin.
	ErrLastAdmin = errors.New("the organization would be left without an admin")
	// ErrRoleUnknown means a role is neither User nor admin.
	ErrRoleUnknown = errors.New("that role does not exist")
)

// memberLockClass is the first half of the advisory lock that serializes
// changes to one Organization's Teams and members, the Organization's id being
// the second. Without it two admins demoting each other at once would each see
// the other still an admin. Transaction scoped, and fixed for the same reason
// schemaLockKey is.
const memberLockClass int32 = 7264194

// Member is one User of an Organization, in the Team they hold there.
type Member struct {
	UserID uint
	// Name is what the console shows: the provider's name, else the email,
	// else the login kept from the password era.
	Name     string
	Email    string
	TeamID   uint
	TeamName string
	Role     string
}

func lockOrganization(tx *gorm.DB, organizationID uint) error {
	if err := tx.Exec("SELECT pg_advisory_xact_lock(?, ?)", memberLockClass, int32(organizationID)).Error; err != nil { //nolint:gosec // ids are far below int32
		return fmt.Errorf("lock organization: %w", scrub(err))
	}

	return nil
}

// CreateTeam adds a Team to an Organization.
func (i *Identity) CreateTeam(ctx context.Context, organizationID uint, name string, now time.Time) (Team, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return Team{}, err
	}

	team := Team{OrganizationID: organizationID, Name: strings.TrimSpace(name), CreatedAt: now}
	err = tx.Transaction(func(tx *gorm.DB) error {
		if err := lockOrganization(tx, organizationID); err != nil {
			return err
		}
		if err := teamNameFree(tx, organizationID, team.Name); err != nil {
			return err
		}
		if err := tx.Create(&team).Error; err != nil {
			return fmt.Errorf("create team: %w", scrub(err))
		}

		return nil
	})
	if err != nil {
		return Team{}, err
	}

	return team, nil
}

func teamNameFree(tx *gorm.DB, organizationID uint, name string) error {
	var taken int64
	if err := tx.Model(&Team{}).Where("organization_id = ? AND name = ?", organizationID, name).Count(&taken).Error; err != nil {
		return fmt.Errorf("check team name: %w", scrub(err))
	}
	if taken > 0 {
		return ErrTeamExists
	}

	return nil
}

// RenameTeam renames a Team. Its key does not change, because that is where
// the Team's rows were written.
func (i *Identity) RenameTeam(ctx context.Context, organizationID uint, name, to string) error {
	tx, err := i.scope(ctx)
	if err != nil {
		return err
	}

	return tx.Transaction(func(tx *gorm.DB) error {
		if err := lockOrganization(tx, organizationID); err != nil {
			return err
		}

		team, err := teamByName(tx, organizationID, name)
		if err != nil {
			return err
		}

		to = strings.TrimSpace(to)
		if to == team.Name {
			return nil
		}
		if err := teamNameFree(tx, organizationID, to); err != nil {
			return err
		}
		if err := tx.Model(&team).Update("name", to).Error; err != nil {
			return fmt.Errorf("rename team: %w", scrub(err))
		}

		return nil
	})
}

// RenameOrganization changes an Organization's name. Teams, Users and rows
// are keyed by id, so nothing else moves.
func (i *Identity) RenameOrganization(ctx context.Context, organizationID uint, name string) error {
	tx, err := i.scope(ctx)
	if err != nil {
		return err
	}

	return tx.Transaction(func(tx *gorm.DB) error {
		if err := lockOrganization(tx, organizationID); err != nil {
			return err
		}

		result := tx.Model(&Organization{}).Where("id = ?", organizationID).Update("name", strings.TrimSpace(name))
		if result.Error != nil {
			return fmt.Errorf("rename organization: %w", scrub(result.Error))
		}
		if result.RowsAffected == 0 {
			return ErrOrganizationUnknown
		}

		return nil
	})
}

// AISettings is what an Organization lets agents and the Chat do.
type AISettings struct {
	// Access is the most they may do: read, change or change_delete.
	Access string
	// Passwords lets local agents fetch database passwords.
	Passwords bool
	// SQLWrites lets local agents change data with SQL.
	SQLWrites bool
}

// OrganizationAI reads what an Organization lets agents and the Chat do.
func (i *Identity) OrganizationAI(ctx context.Context, organizationID uint) (AISettings, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return AISettings{}, err
	}

	var org Organization
	err = tx.Select("ai_access", "ai_passwords", "ai_sql_writes").Where("id = ?", organizationID).Limit(1).Find(&org).Error
	if err != nil {
		return AISettings{}, fmt.Errorf("read organization settings: %w", scrub(err))
	}
	if org.AIAccess == "" {
		return AISettings{}, ErrOrganizationUnknown
	}

	return AISettings{Access: org.AIAccess, Passwords: org.AIPasswords, SQLWrites: org.AISQLWrites}, nil
}

// SetOrganizationAI replaces what an Organization lets agents and the Chat do.
func (i *Identity) SetOrganizationAI(ctx context.Context, organizationID uint, settings AISettings) error {
	tx, err := i.scope(ctx)
	if err != nil {
		return err
	}

	// A map, because Updates with a struct skips a false switch.
	result := tx.Model(&Organization{}).Where("id = ?", organizationID).Updates(map[string]any{
		"ai_access":     settings.Access,
		"ai_passwords":  settings.Passwords,
		"ai_sql_writes": settings.SQLWrites,
	})
	if result.Error != nil {
		return fmt.Errorf("save organization settings: %w", scrub(result.Error))
	}
	if result.RowsAffected == 0 {
		return ErrOrganizationUnknown
	}

	return nil
}

// Members lists the Users of an Organization, oldest membership first.
func (i *Identity) Members(ctx context.Context, organizationID uint) ([]Member, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return nil, err
	}

	var out []Member
	err = placements(tx).
		Select("u.id AS user_id, "+
			"COALESCE(NULLIF(u.name, ''), NULLIF(u.email, ''), u.username) AS name, "+
			"u.email AS email, m.role AS role, t.id AS team_id, t.name AS team_name").
		Where("m.organization_id = ?", organizationID).
		Order("m.created_at, u.id").
		Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list members: %w", scrub(err))
	}

	return out, nil
}

// UpdateMember moves a User to another Team of the Organization, changes their
// role, or both. An empty team or role leaves that part alone. The last admin
// cannot be demoted.
func (i *Identity) UpdateMember(ctx context.Context, organizationID, userID uint, team, role string) error {
	if role != "" && role != RoleUser && role != RoleAdmin {
		return ErrRoleUnknown
	}

	tx, err := i.scope(ctx)
	if err != nil {
		return err
	}

	return tx.Transaction(func(tx *gorm.DB) error {
		if err := lockOrganization(tx, organizationID); err != nil {
			return err
		}

		member, err := membership(tx, organizationID, userID)
		if err != nil {
			return err
		}

		changes := map[string]any{}
		if team = strings.TrimSpace(team); team != "" {
			row, err := teamByName(tx, organizationID, team)
			if err != nil {
				return err
			}
			changes["team_id"] = row.ID
		}
		if role != "" && role != member.Role {
			if member.Role == RoleAdmin {
				if err := requireAnotherAdmin(tx, organizationID); err != nil {
					return err
				}
			}
			changes["role"] = role
		}
		if len(changes) == 0 {
			return nil
		}

		err = tx.Model(&Membership{}).
			Where("user_id = ? AND organization_id = ?", userID, organizationID).Updates(changes).Error
		if err != nil {
			return fmt.Errorf("update member: %w", scrub(err))
		}

		return nil
	})
}

// RemoveMember takes a User out of an Organization. They lose access on their
// next request, because a session is placed through the membership every time.
// The last admin cannot be removed.
func (i *Identity) RemoveMember(ctx context.Context, organizationID, userID uint) error {
	tx, err := i.scope(ctx)
	if err != nil {
		return err
	}

	return tx.Transaction(func(tx *gorm.DB) error {
		if err := lockOrganization(tx, organizationID); err != nil {
			return err
		}

		member, err := membership(tx, organizationID, userID)
		if err != nil {
			return err
		}
		if member.Role == RoleAdmin {
			if err := requireAnotherAdmin(tx, organizationID); err != nil {
				return err
			}
		}

		err = tx.Where("user_id = ? AND organization_id = ?", userID, organizationID).Delete(&Membership{}).Error
		if err != nil {
			return fmt.Errorf("remove member: %w", scrub(err))
		}

		return nil
	})
}

// teamByName finds a Team of one Organization, never another's.
func teamByName(tx *gorm.DB, organizationID uint, name string) (Team, error) {
	var team Team
	err := tx.Where("organization_id = ? AND name = ?", organizationID, strings.TrimSpace(name)).Take(&team).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Team{}, ErrTeamUnknown
	}
	if err != nil {
		return Team{}, fmt.Errorf("read team: %w", scrub(err))
	}

	return team, nil
}

func membership(tx *gorm.DB, organizationID, userID uint) (Membership, error) {
	var member Membership
	err := tx.Where("user_id = ? AND organization_id = ?", userID, organizationID).Take(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Membership{}, ErrNoMembership
	}
	if err != nil {
		return Membership{}, fmt.Errorf("read membership: %w", scrub(err))
	}

	return member, nil
}

// requireAnotherAdmin refuses when the admin being changed is the only one.
func requireAnotherAdmin(tx *gorm.DB, organizationID uint) error {
	var admins int64
	err := tx.Model(&Membership{}).Where("organization_id = ? AND role = ?", organizationID, RoleAdmin).Count(&admins).Error
	if err != nil {
		return fmt.Errorf("count admins: %w", scrub(err))
	}
	if admins <= 1 {
		return ErrLastAdmin
	}

	return nil
}
