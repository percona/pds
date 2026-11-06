package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const (
	// everyOrganization is Options.Organization when the deployment serves
	// every Organization rather than one named one.
	everyOrganization = "*"

	// defaultTeamName is the Team every migrated Organization starts with.
	defaultTeamName = "default"

	// passwordIssuer marks a User whose login is a password, rather than an
	// identity a sign-in provider vouches for.
	passwordIssuer = "password"
)

// ensureServedOrganization makes the Organization a single-Organization
// deployment serves, with its default Team, when none of that name exists. A
// deployment that serves every Organization, or names none, makes nothing.
func (s *Store) ensureServedOrganization(ctx context.Context) error {
	if s.organization == "" || s.organization == everyOrganization {
		return nil
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&Organization{}).Where("name = ?", s.organization).Count(&existing).Error; err != nil {
			return fmt.Errorf("look for organization %q: %w", s.organization, err)
		}
		if existing > 0 {
			return nil
		}

		_, _, err := newOrganization(tx, s.organization, time.Now().UTC())

		return err
	})
}

// addLoginTo creates the password User for a login and makes them an admin. On
// a deployment that serves one Organization the login joins it, in its oldest
// Team; otherwise the login brings an Organization of its own, named after it.
func (i *Identity) addLoginTo(tx *gorm.DB, username string, now time.Time) error {
	if i.organization == "" || i.organization == everyOrganization {
		org, team, err := newOrganization(tx, username, now)
		if err != nil {
			return err
		}
		_, err = addLogin(tx, org, team, username, loginSeed{}, now)

		return err
	}

	var team Team
	err := tx.Table("hub_teams t").
		Select("t.*").
		Joins("JOIN hub_organizations o ON o.id = t.organization_id").
		Where("o.name = ?", i.organization).
		Order("o.id, t.id").
		Limit(1).
		Scan(&team).Error
	if err != nil {
		return fmt.Errorf("read the served team: %w", err)
	}

	var org Organization
	switch {
	case team.ID != 0:
		if err := tx.Take(&org, team.OrganizationID).Error; err != nil {
			return fmt.Errorf("read the served organization: %w", err)
		}
	default:
		if org, team, err = newOrganization(tx, i.organization, now); err != nil {
			return err
		}
	}
	_, err = addLogin(tx, org, team, username, loginSeed{}, now)

	return err
}

// loginSeed is what a password User starts with. The zero value is a login
// nobody has claimed yet; the upgrade fills it from the login it replaces.
type loginSeed struct {
	hash      string
	failures  int
	createdAt time.Time
}

func newOrganization(tx *gorm.DB, name string, now time.Time) (Organization, Team, error) {
	org := Organization{Name: name, CreatedAt: now}
	if err := tx.Create(&org).Error; err != nil {
		return Organization{}, Team{}, fmt.Errorf("create organization: %w", err)
	}

	team := Team{OrganizationID: org.ID, Name: defaultTeamName, CreatedAt: now}
	if err := tx.Create(&team).Error; err != nil {
		return Organization{}, Team{}, fmt.Errorf("create team: %w", err)
	}

	return org, team, nil
}

// addLogin creates the password User for a login and makes them an admin of the
// Team's Organization.
func addLogin(tx *gorm.DB, org Organization, team Team, username string, seed loginSeed, now time.Time) (User, error) {
	created := seed.createdAt
	if created.IsZero() {
		created = now
	}
	user := User{
		Issuer: passwordIssuer, Subject: username, Username: username,
		PasswordHash: seed.hash, Failures: seed.failures,
		CreatedAt: created, UpdatedAt: now,
	}
	if err := tx.Create(&user).Error; err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	membership := Membership{
		UserID: user.ID, OrganizationID: org.ID, TeamID: team.ID, Role: RoleAdmin, CreatedAt: now,
	}
	if err := tx.Create(&membership).Error; err != nil {
		return User{}, fmt.Errorf("create membership: %w", err)
	}

	return user, nil
}
