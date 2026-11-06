package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/store"
)

// ErrTeamName means a Team name is empty or too long.
var ErrTeamName = errors.New("a team needs a name of up to 128 characters")

// ErrTeamNameReserved means a Team was named all, which a request reads as
// every Team.
var ErrTeamNameReserved = errors.New("a team cannot be named all")

// ErrAIAccess means the most agents may do is not read, change or change and
// delete.
var ErrAIAccess = errors.New("the most agents may do is read, change, or change and delete")

const maxTeamName = 128

// DirectoryStore is what an Organization's admin changes: its Teams and the
// Users in them. The store's Identity satisfies it, and tests pass a real one.
type DirectoryStore interface {
	Teams(ctx context.Context, organizationID uint) ([]store.Team, error)
	CreateTeam(ctx context.Context, organizationID uint, name string, now time.Time) (store.Team, error)
	RenameOrganization(ctx context.Context, organizationID uint, name string) error
	OrganizationAI(ctx context.Context, organizationID uint) (store.AISettings, error)
	SetOrganizationAI(ctx context.Context, organizationID uint, settings store.AISettings) error
	RenameTeam(ctx context.Context, organizationID uint, name, to string) error
	DeleteTeam(ctx context.Context, organizationID uint, name string) error
	Members(ctx context.Context, organizationID uint) ([]store.Member, error)
	UpdateMember(ctx context.Context, organizationID, userID uint, team, role string) error
	RemoveMember(ctx context.Context, organizationID, userID uint) error
}

// Directory is the admin's view of one Organization: its Teams and its Users.
// Every call names the Organization, which the router took from the session,
// so nothing here can reach another one.
type Directory struct {
	store    DirectoryStore
	now      func() time.Time
	recorder ActivityRecorder
}

// NewDirectory returns the Organization directory. The recorder takes the
// activity an Organization change leaves, and may be nil.
func NewDirectory(st DirectoryStore, recorder ActivityRecorder) *Directory {
	return &Directory{store: st, now: time.Now, recorder: recorder}
}

// RenameOrganization renames the Organization. The name follows the rules of
// creation. The change is recorded for the User in ctx.
func (d *Directory) RenameOrganization(ctx context.Context, organizationID uint, name string) error {
	name, err := OrganizationName(name)
	if err != nil {
		return err
	}
	if err := d.store.RenameOrganization(ctx, organizationID, name); err != nil {
		return err
	}
	if d.recorder != nil {
		d.recorder.Record(ctx, ActivityEvent{
			Action: ActionOrganizationRename, Outcome: OutcomeSucceeded, Target: name,
		})
	}

	return nil
}

// AI is what the Organization lets agents and the Chat do.
func (d *Directory) AI(ctx context.Context, organizationID uint) (store.AISettings, error) {
	return d.store.OrganizationAI(ctx, organizationID)
}

// SetAI replaces what the Organization lets agents and the Chat do. The
// change is recorded for the User in ctx.
func (d *Directory) SetAI(ctx context.Context, organizationID uint, settings store.AISettings) error {
	if !auth.Access(settings.Access).Valid() {
		return ErrAIAccess
	}
	if err := d.store.SetOrganizationAI(ctx, organizationID, settings); err != nil {
		return err
	}
	if d.recorder != nil {
		d.recorder.Record(ctx, ActivityEvent{
			Action: ActionOrganizationAI, Outcome: OutcomeSucceeded, Target: fmt.Sprintf("%s, passwords %s, SQL writes %s", settings.Access, onOff(settings.Passwords), onOff(settings.SQLWrites)),
		})
	}

	return nil
}

// Teams lists an Organization's Teams, oldest first.
func (d *Directory) Teams(ctx context.Context, organizationID uint) ([]store.Team, error) {
	teams, err := d.store.Teams(ctx, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}

	return teams, nil
}

// CreateTeam adds a Team.
func (d *Directory) CreateTeam(ctx context.Context, organizationID uint, name string) (store.Team, error) {
	name, err := teamName(name)
	if err != nil {
		return store.Team{}, err
	}

	return d.store.CreateTeam(ctx, organizationID, name, d.now())
}

// RenameTeam renames a Team.
func (d *Directory) RenameTeam(ctx context.Context, organizationID uint, name, to string) error {
	to, err := teamName(to)
	if err != nil {
		return err
	}

	return d.store.RenameTeam(ctx, organizationID, name, to)
}

// DeleteTeam removes a Team that holds no Users and no environments.
func (d *Directory) DeleteTeam(ctx context.Context, organizationID uint, name string) error {
	return d.store.DeleteTeam(ctx, organizationID, name)
}

// Members lists an Organization's Users.
func (d *Directory) Members(ctx context.Context, organizationID uint) ([]store.Member, error) {
	members, err := d.store.Members(ctx, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}

	return members, nil
}

// MemberChange is what an admin changes about a User. A nil field is left
// alone; one that is set has to hold a value.
type MemberChange struct {
	Team *string
	Role *string
}

// ErrMemberChange means a change names a Team or role and gives it no value, or
// changes nothing at all.
var ErrMemberChange = errors.New("a change needs a team, a role, or both")

// UpdateMember moves a User to another Team, changes their role, or both.
func (d *Directory) UpdateMember(ctx context.Context, organizationID, userID uint, change MemberChange) error {
	var team, role string
	if change.Team != nil {
		if team = strings.TrimSpace(*change.Team); team == "" {
			return ErrMemberChange
		}
	}
	if change.Role != nil {
		if role = strings.TrimSpace(*change.Role); role == "" {
			return ErrMemberChange
		}
	}
	if team == "" && role == "" {
		return ErrMemberChange
	}

	return d.store.UpdateMember(ctx, organizationID, userID, team, role)
}

// RemoveMember takes a User out of the Organization.
func (d *Directory) RemoveMember(ctx context.Context, organizationID, userID uint) error {
	return d.store.RemoveMember(ctx, organizationID, userID)
}

func teamName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxTeamName {
		return "", ErrTeamName
	}

	if strings.EqualFold(name, "all") {
		return "", ErrTeamNameReserved
	}

	return name, nil
}

func onOff(on bool) string {
	if on {
		return "on"
	}

	return "off"
}
