package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// ErrTeamUnknown means the Organization has no Team under that name.
var ErrTeamUnknown = errors.New("no team is registered under that name")

// ErrNoMembership means an identity is not a member of any Organization, or
// the member it named has left the one it was placed in.
var ErrNoMembership = errors.New("the identity belongs to no organization")

// ErrTeamHasEnvironments and ErrTeamHasUsers refuse deleting a Team that would
// leave work or people without one.
var (
	ErrTeamHasEnvironments = errors.New("the team still has environments")
	ErrTeamHasUsers        = errors.New("the team still has users")
)

// Placement is where a login sits: who it is, the Organization it belongs to,
// and the Team and role it holds there.
type Placement struct {
	UserID uint
	// Username is what the console shows for the User: the name the provider
	// sent, else the email, else the login kept from the password era.
	Username string
	// Login is the password login name, empty for a person an identity
	// provider vouches for.
	Login            string
	OrganizationID   uint
	OrganizationName string
	Role             string
	TeamID           uint
	TeamName         string
}

// Placement returns where a password login sits. A login in several
// Organizations is placed in the oldest, the same one on every request. It reads the membership on
// every call, so a login that was moved or removed is placed again, or refused,
// on its next request rather than when its session ends.
func (i *Identity) Placement(ctx context.Context, login string) (Placement, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return Placement{}, err
	}

	return firstPlacement(placements(tx).
		Where("u.issuer = ? AND u.subject = ?", passwordIssuer, strings.TrimSpace(login)).
		Order("m.organization_id"), ErrLoginUnknown)
}

// firstPlacement reads the first row of a placements query, or answers missing
// when there is none.
func firstPlacement(query *gorm.DB, missing error) (Placement, error) {
	var out Placement
	if err := query.Limit(1).Scan(&out).Error; err != nil {
		return Placement{}, fmt.Errorf("read placement: %w", scrub(err))
	}
	if out.UserID == 0 {
		return Placement{}, missing
	}

	return out, nil
}

// placements is the join every Placement reads: a User, one membership, and
// the Team and Organization it names.
func placements(tx *gorm.DB) *gorm.DB {
	return tx.Table("hub_users u").
		Select("u.id AS user_id, " +
			"COALESCE(NULLIF(u.name, ''), NULLIF(u.email, ''), u.username) AS username, " +
			"CASE WHEN u.issuer = 'password' THEN u.subject ELSE '' END AS login, " +
			"m.organization_id, o.name AS organization_name, m.role, " +
			"t.id AS team_id, t.name AS team_name").
		Joins("JOIN hub_memberships m ON m.user_id = u.id").
		Joins("JOIN hub_organizations o ON o.id = m.organization_id").
		Joins("JOIN hub_teams t ON t.id = m.team_id")
}

// UserPlacement places an identity an issuer vouches for. One in several
// Organizations is placed in the one last used, else the oldest. ErrNoMembership means the identity is
// unknown or belongs nowhere.
func (i *Identity) UserPlacement(ctx context.Context, issuer, subject string) (Placement, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return Placement{}, err
	}

	return firstPlacement(placements(tx).
		Where("u.issuer = ? AND u.subject = ?", issuer, subject).
		Order("CASE WHEN m.organization_id = u.last_organization_id THEN 0 ELSE 1 END, m.organization_id"),
		ErrNoMembership)
}

// PlacementOf places a User in one Organization. It reads the membership on
// every call, so a removed or moved User acts in the new Team, or is refused,
// on the next request.
func (i *Identity) PlacementOf(ctx context.Context, userID, organizationID uint) (Placement, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return Placement{}, err
	}

	return firstPlacement(placements(tx).
		Where("u.id = ? AND m.organization_id = ?", userID, organizationID), ErrNoMembership)
}

// Teams lists the Teams of one Organization, oldest first.
func (i *Identity) Teams(ctx context.Context, organizationID uint) ([]Team, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return nil, err
	}

	var teams []Team
	if err := tx.Where("organization_id = ?", organizationID).Order("id").Find(&teams).Error; err != nil {
		return nil, fmt.Errorf("list teams: %w", scrub(err))
	}

	return teams, nil
}

// Team returns the Team an Organization holds under a name. A Team of another
// Organization is never returned, however it is named.
func (i *Identity) Team(ctx context.Context, organizationID uint, name string) (Team, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return Team{}, err
	}

	var team Team
	err = tx.Where("organization_id = ? AND name = ?", organizationID, strings.TrimSpace(name)).Take(&team).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Team{}, ErrTeamUnknown
	}
	if err != nil {
		return Team{}, fmt.Errorf("read team: %w", scrub(err))
	}

	return team, nil
}

// ServedTeam returns the oldest Team of the Organization a single-Organization
// deployment serves, found by its name. It holds even before anybody has a
// login there, because the Organization is made at start.
func (i *Identity) ServedTeam(ctx context.Context, organization string) (Team, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return Team{}, err
	}

	var team Team
	err = tx.Table("hub_teams t").
		Select("t.*").
		Joins("JOIN hub_organizations o ON o.id = t.organization_id").
		Where("o.name = ?", strings.TrimSpace(organization)).
		Order("o.id, t.id").
		Limit(1).
		Scan(&team).Error
	if err != nil {
		return Team{}, fmt.Errorf("read team: %w", scrub(err))
	}
	if team.ID == 0 {
		return Team{}, ErrTeamUnknown
	}

	return team, nil
}

// TeamOwner names the Organization and the Team behind a Team id.
func (i *Identity) TeamOwner(ctx context.Context, teamID uint) (organization, team string, err error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return "", "", err
	}

	var out struct {
		Organization string
		Team         string
	}
	err = tx.Table("hub_teams t").
		Select("o.name AS organization, t.name AS team").
		Joins("JOIN hub_organizations o ON o.id = t.organization_id").
		Where("t.id = ?", teamID).
		Limit(1).
		Scan(&out).Error
	if err != nil {
		return "", "", fmt.Errorf("read team owner: %w", scrub(err))
	}
	if out.Team == "" {
		return "", "", ErrTeamUnknown
	}

	return out.Organization, out.Team, nil
}

// DeleteTeam removes an empty Team. A Team that still holds environments or
// users is refused, so nothing is left without a Team.
func (i *Identity) DeleteTeam(ctx context.Context, organizationID uint, name string) error {
	tx, err := i.scope(ctx)
	if err != nil {
		return err
	}

	return tx.Transaction(func(tx *gorm.DB) error {
		// A User moved into the Team while it is counted would be left in a
		// Team that no longer exists.
		if err := lockOrganization(tx, organizationID); err != nil {
			return err
		}

		team, err := teamByName(tx, organizationID, name)
		if err != nil {
			return err
		}

		var environments int64
		if err := tx.Model(&HubEnvironment{}).Where("team_id = ?", team.ID).Count(&environments).Error; err != nil {
			return fmt.Errorf("count environments: %w", scrub(err))
		}
		if environments > 0 {
			return ErrTeamHasEnvironments
		}

		var users int64
		if err := tx.Model(&Membership{}).Where("team_id = ?", team.ID).Count(&users).Error; err != nil {
			return fmt.Errorf("count users: %w", scrub(err))
		}
		if users > 0 {
			return ErrTeamHasUsers
		}

		if err := tx.Delete(&team).Error; err != nil {
			return fmt.Errorf("delete team: %w", scrub(err))
		}

		return nil
	})
}
