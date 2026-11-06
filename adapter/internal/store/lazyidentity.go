package store

import (
	"context"
	"time"
)

// LazyIdentity is the login state, resolved when the database connects.
//
// It has the same surface as Identity, so login can be wired at start in one
// pass rather than waiting for the connection. A sign in that arrives first
// reports that the database is not connected, which is the same thing
// readiness is already saying.
type LazyIdentity struct {
	starter *Starter
}

// NewLazyIdentity returns the login state, resolved on first use.
func NewLazyIdentity(starter *Starter) *LazyIdentity {
	return &LazyIdentity{starter: starter}
}

func (l *LazyIdentity) resolve() (*Identity, error) {
	if l == nil || l.starter == nil {
		return nil, ErrNotConnected
	}
	db := l.starter.Store()
	if db == nil {
		return nil, ErrNotConnected
	}

	return db.Identity(), nil
}

// EnsureLogin creates the login if it is not there.
func (l *LazyIdentity) EnsureLogin(ctx context.Context, username string, now time.Time) (bool, error) {
	identity, err := l.resolve()
	if err != nil {
		return false, err
	}

	return identity.EnsureLogin(ctx, username, now)
}

// Logins lists every password login.
func (l *LazyIdentity) Logins(ctx context.Context) ([]PasswordLogin, error) {
	identity, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return identity.Logins(ctx)
}

// PasswordLogin returns one login.
func (l *LazyIdentity) PasswordLogin(ctx context.Context, username string) (PasswordLogin, error) {
	identity, err := l.resolve()
	if err != nil {
		return PasswordLogin{}, err
	}

	return identity.PasswordLogin(ctx, username)
}

// SetPassword replaces one login's hash and ends its sessions.
func (l *LazyIdentity) SetPassword(ctx context.Context, username, passwordHash string, now time.Time) error {
	identity, err := l.resolve()
	if err != nil {
		return err
	}

	return identity.SetPassword(ctx, username, passwordHash, now)
}

// RecordFailure counts one wrong password.
func (l *LazyIdentity) RecordFailure(ctx context.Context, username string, now time.Time) (int, error) {
	identity, err := l.resolve()
	if err != nil {
		return 0, err
	}

	return identity.RecordFailure(ctx, username, now)
}

// ClearFailures resets the throttle.
func (l *LazyIdentity) ClearFailures(ctx context.Context, username string, now time.Time) error {
	identity, err := l.resolve()
	if err != nil {
		return err
	}

	return identity.ClearFailures(ctx, username, now)
}

// CreateInvite records one single-use link.
func (l *LazyIdentity) CreateInvite(ctx context.Context, username, hash string, expiresAt, now time.Time) error {
	identity, err := l.resolve()
	if err != nil {
		return err
	}

	return identity.CreateInvite(ctx, username, hash, expiresAt, now)
}

// Invite reports which login a claimable link belongs to.
func (l *LazyIdentity) Invite(ctx context.Context, hash string, now time.Time) (string, error) {
	identity, err := l.resolve()
	if err != nil {
		return "", err
	}

	return identity.Invite(ctx, hash, now)
}

// ClaimInvite spends the link and sets the password.
func (l *LazyIdentity) ClaimInvite(
	ctx context.Context,
	hash, passwordHash string,
	now time.Time,
) (string, error) {
	identity, err := l.resolve()
	if err != nil {
		return "", err
	}

	return identity.ClaimInvite(ctx, hash, passwordHash, now)
}

// CreateSession records one signed-in browser.
func (l *LazyIdentity) CreateSession(ctx context.Context, session Session) error {
	identity, err := l.resolve()
	if err != nil {
		return err
	}

	return identity.CreateSession(ctx, session)
}

// Session returns the live session under one id.
func (l *LazyIdentity) Session(
	ctx context.Context,
	hash string,
	now time.Time,
	idle time.Duration,
) (Session, error) {
	identity, err := l.resolve()
	if err != nil {
		return Session{}, err
	}

	return identity.Session(ctx, hash, now, idle)
}

// DeleteSession ends one session.
func (l *LazyIdentity) DeleteSession(ctx context.Context, hash string) error {
	identity, err := l.resolve()
	if err != nil {
		return err
	}

	return identity.DeleteSession(ctx, hash)
}

// PurgeExpiredSessions drops the rows that are past their expiry.
func (l *LazyIdentity) PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	identity, err := l.resolve()
	if err != nil {
		return 0, err
	}

	return identity.PurgeExpiredSessions(ctx, now)
}

// SessionKey returns the key session cookies are signed with.
func (l *LazyIdentity) SessionKey(ctx context.Context, now time.Time) ([]byte, error) {
	identity, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return identity.SessionKey(ctx, now)
}

// Placement returns where a login sits.
func (l *LazyIdentity) Placement(ctx context.Context, login string) (Placement, error) {
	identity, err := l.resolve()
	if err != nil {
		return Placement{}, err
	}

	return identity.Placement(ctx, login)
}

// UserPlacement places an identity an issuer vouches for.
func (l *LazyIdentity) UserPlacement(ctx context.Context, issuer, subject string) (Placement, error) {
	identity, err := l.resolve()
	if err != nil {
		return Placement{}, err
	}

	return identity.UserPlacement(ctx, issuer, subject)
}

// PlacementOf places a User in one Organization.
func (l *LazyIdentity) PlacementOf(ctx context.Context, userID, organizationID uint) (Placement, error) {
	identity, err := l.resolve()
	if err != nil {
		return Placement{}, err
	}

	return identity.PlacementOf(ctx, userID, organizationID)
}

// Teams lists the Teams of one Organization.
func (l *LazyIdentity) Teams(ctx context.Context, organizationID uint) ([]Team, error) {
	identity, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return identity.Teams(ctx, organizationID)
}

// Team returns the Team an Organization holds under a name.
func (l *LazyIdentity) Team(ctx context.Context, organizationID uint, name string) (Team, error) {
	identity, err := l.resolve()
	if err != nil {
		return Team{}, err
	}

	return identity.Team(ctx, organizationID, name)
}

// ServedTeam returns the oldest Team of the Organization this deployment serves.
func (l *LazyIdentity) ServedTeam(ctx context.Context, organization string) (Team, error) {
	identity, err := l.resolve()
	if err != nil {
		return Team{}, err
	}

	return identity.ServedTeam(ctx, organization)
}

// CreateOrganization makes an Organization with its default Team and admin.
func (l *LazyIdentity) CreateOrganization(ctx context.Context, in NewOrganization, now time.Time) (Placement, error) {
	identity, err := l.resolve()
	if err != nil {
		return Placement{}, err
	}

	return identity.CreateOrganization(ctx, in, now)
}

// UserOrganizations lists the Organizations a User belongs to.
func (l *LazyIdentity) UserOrganizations(ctx context.Context, userID uint) ([]UserOrganization, error) {
	identity, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return identity.UserOrganizations(ctx, userID)
}

// SwitchOrganization moves a session into another Organization of its User.
func (l *LazyIdentity) SwitchOrganization(ctx context.Context, sessionHash string, userID, organizationID uint) error {
	identity, err := l.resolve()
	if err != nil {
		return err
	}

	return identity.SwitchOrganization(ctx, sessionHash, userID, organizationID)
}

// CreateTeam adds a Team to an Organization.
func (l *LazyIdentity) CreateTeam(ctx context.Context, organizationID uint, name string, now time.Time) (Team, error) {
	identity, err := l.resolve()
	if err != nil {
		return Team{}, err
	}

	return identity.CreateTeam(ctx, organizationID, name, now)
}

// RenameTeam renames a Team.
func (l *LazyIdentity) RenameTeam(ctx context.Context, organizationID uint, name, to string) error {
	identity, err := l.resolve()
	if err != nil {
		return err
	}

	return identity.RenameTeam(ctx, organizationID, name, to)
}

// RenameOrganization renames an Organization.
func (l *LazyIdentity) RenameOrganization(ctx context.Context, organizationID uint, name string) error {
	identity, err := l.resolve()
	if err != nil {
		return err
	}

	return identity.RenameOrganization(ctx, organizationID, name)
}

// OrganizationAI reads what an Organization lets agents and the Chat do.
func (l *LazyIdentity) OrganizationAI(ctx context.Context, organizationID uint) (AISettings, error) {
	identity, err := l.resolve()
	if err != nil {
		return AISettings{}, err
	}

	return identity.OrganizationAI(ctx, organizationID)
}

// SetOrganizationAI replaces what an Organization lets agents and the Chat do.
func (l *LazyIdentity) SetOrganizationAI(ctx context.Context, organizationID uint, settings AISettings) error {
	identity, err := l.resolve()
	if err != nil {
		return err
	}

	return identity.SetOrganizationAI(ctx, organizationID, settings)
}

// Members lists the Users of an Organization.
func (l *LazyIdentity) Members(ctx context.Context, organizationID uint) ([]Member, error) {
	identity, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return identity.Members(ctx, organizationID)
}

// UpdateMember moves a User to another Team, changes their role, or both.
func (l *LazyIdentity) UpdateMember(ctx context.Context, organizationID, userID uint, team, role string) error {
	identity, err := l.resolve()
	if err != nil {
		return err
	}

	return identity.UpdateMember(ctx, organizationID, userID, team, role)
}

// RemoveMember takes a User out of an Organization.
func (l *LazyIdentity) RemoveMember(ctx context.Context, organizationID, userID uint) error {
	identity, err := l.resolve()
	if err != nil {
		return err
	}

	return identity.RemoveMember(ctx, organizationID, userID)
}

// DeleteTeam removes an empty Team.
func (l *LazyIdentity) DeleteTeam(ctx context.Context, organizationID uint, name string) error {
	identity, err := l.resolve()
	if err != nil {
		return err
	}

	return identity.DeleteTeam(ctx, organizationID, name)
}

// CreateInvitation writes an invitation.
func (l *LazyIdentity) CreateInvitation(ctx context.Context, in NewInvitation, now time.Time) (InvitationView, error) {
	identity, err := l.resolve()
	if err != nil {
		return InvitationView{}, err
	}

	return identity.CreateInvitation(ctx, in, now)
}

// PendingInvitations lists the invitations that can still be claimed.
func (l *LazyIdentity) PendingInvitations(ctx context.Context, organizationID uint, now time.Time) ([]InvitationView, error) {
	identity, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return identity.PendingInvitations(ctx, organizationID, now)
}

// RevokeInvitation ends an invitation that was not claimed.
func (l *LazyIdentity) RevokeInvitation(ctx context.Context, organizationID, id uint, now time.Time) (InvitationView, error) {
	identity, err := l.resolve()
	if err != nil {
		return InvitationView{}, err
	}

	return identity.RevokeInvitation(ctx, organizationID, id, now)
}

// ResendInvitation replaces an invitation with a new one.
func (l *LazyIdentity) ResendInvitation(
	ctx context.Context, organizationID, id uint, hash string, now time.Time,
	lifetime func(anyEmail bool) time.Duration,
) (InvitationView, error) {
	identity, err := l.resolve()
	if err != nil {
		return InvitationView{}, err
	}

	return identity.ResendInvitation(ctx, organizationID, id, hash, now, lifetime)
}

// InvitationByHash reads the invitation a link names.
func (l *LazyIdentity) InvitationByHash(ctx context.Context, hash string, now time.Time) (InvitationView, error) {
	identity, err := l.resolve()
	if err != nil {
		return InvitationView{}, err
	}

	return identity.InvitationByHash(ctx, hash, now)
}

// PendingInvitationForEmail finds the invitation waiting for an address.
func (l *LazyIdentity) PendingInvitationForEmail(ctx context.Context, email string, now time.Time) (InvitationView, error) {
	identity, err := l.resolve()
	if err != nil {
		return InvitationView{}, err
	}

	return identity.PendingInvitationForEmail(ctx, email, now)
}

// ClaimInvitation spends an invitation and adds the person to its Organization.
func (l *LazyIdentity) ClaimInvitation(ctx context.Context, claim Claim, now time.Time) (Placement, error) {
	identity, err := l.resolve()
	if err != nil {
		return Placement{}, err
	}

	return identity.ClaimInvitation(ctx, claim, now)
}
