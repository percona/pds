package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// What inviting, resending and claiming can refuse. A link that cannot be used
// answers with the store's invite errors (ErrInviteUnknown, ErrInviteSpent,
// ErrInviteExpired), so the password invite and this one read alike.
var (
	// ErrInvitationUnknown means the Organization has no live invitation with
	// that id. Another Organization's reads the same.
	ErrInvitationUnknown = errors.New("the organization has no such pending invitation")
	// ErrInvitationPending means the email already has an invitation to this
	// Organization that can still be claimed.
	ErrInvitationPending = errors.New("the email already has a pending invitation")
	// ErrAlreadyMember means the person already belongs to the Organization,
	// which allows one membership each.
	ErrAlreadyMember = errors.New("the person already belongs to the organization")
	// ErrAnyEmailRole means an any-email invitation asked for more than the
	// User role.
	ErrAnyEmailRole = errors.New("an invitation to any email may only carry the user role")
	// ErrLoginTaken means a password login is already held by somebody.
	ErrLoginTaken = errors.New("that login is already taken")
)

// NewInvitation is an invitation to write. The token is made by the caller and
// only its hash arrives here.
type NewInvitation struct {
	OrganizationID uint
	Team           string
	Role           string
	// Email is already trimmed and lower case. It is empty with AnyEmail.
	Email     string
	AnyEmail  bool
	InvitedBy uint
	Hash      string
	ExpiresAt time.Time
}

// InvitationView is an invitation with the names a person reads.
type InvitationView struct {
	Invitation
	OrganizationName string
	TeamName         string
}

// Claim is the person claiming an invitation.
type Claim struct {
	Hash string
	// Issuer and Subject name the identity the provider vouched for. A
	// password claim has no issuer, and its subject is the login.
	Issuer  string
	Subject string
	Email   string
	Name    string
	// PasswordHash, when set, makes the claim a password one: the login in
	// Subject is created with it.
	PasswordHash string
}

// CreateInvitation writes an invitation. The Team is named, not numbered, and
// is looked up in the Organization, so an invitation cannot reach another one.
func (i *Identity) CreateInvitation(ctx context.Context, in NewInvitation, now time.Time) (InvitationView, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return InvitationView{}, err
	}

	var row Invitation
	err = tx.Transaction(func(tx *gorm.DB) error {
		if err := lockOrganization(tx, in.OrganizationID); err != nil {
			return err
		}

		team, err := teamByName(tx, in.OrganizationID, in.Team)
		if err != nil {
			return err
		}
		if err := checkInvitation(tx, in, now); err != nil {
			return err
		}

		row = Invitation{
			Hash: in.Hash, OrganizationID: in.OrganizationID, TeamID: team.ID, Role: in.Role,
			Email: in.Email, AnyEmail: in.AnyEmail, InvitedBy: in.InvitedBy, ExpiresAt: in.ExpiresAt.UTC(), CreatedAt: now.UTC(),
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("create invitation: %w", scrub(err))
		}

		return nil
	})
	if err != nil {
		return InvitationView{}, err
	}

	return i.invitationView(ctx, row.ID)
}

// checkInvitation refuses what must not be invited: a role that does not
// exist, an any-email invitation above User, an address that already belongs
// to a User of the Organization, and one that already has a live invitation.
func checkInvitation(tx *gorm.DB, in NewInvitation, now time.Time) error {
	if in.Role != RoleUser && in.Role != RoleAdmin {
		return ErrRoleUnknown
	}
	if in.AnyEmail {
		if in.Role != RoleUser {
			return ErrAnyEmailRole
		}

		return nil
	}

	var members int64
	err := tx.Table("hub_memberships m").
		Joins("JOIN hub_users u ON u.id = m.user_id").
		Where("m.organization_id = ? AND lower(u.email) = ?", in.OrganizationID, in.Email).
		Count(&members).Error
	if err != nil {
		return fmt.Errorf("check members: %w", scrub(err))
	}
	if members > 0 {
		return ErrAlreadyMember
	}

	var pending int64
	err = live(tx.Model(&Invitation{}), now).
		Where("organization_id = ? AND email = ?", in.OrganizationID, in.Email).Count(&pending).Error
	if err != nil {
		return fmt.Errorf("check invitations: %w", scrub(err))
	}
	if pending > 0 {
		return ErrInvitationPending
	}

	return nil
}

// live narrows a query to invitations that can still be claimed.
func live(tx *gorm.DB, now time.Time) *gorm.DB {
	return tx.Where("claimed_at IS NULL AND revoked_at IS NULL AND expires_at > ?", now.UTC())
}

// PendingInvitations lists the invitations of an Organization that can still
// be claimed, newest first.
func (i *Identity) PendingInvitations(ctx context.Context, organizationID uint, now time.Time) ([]InvitationView, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return nil, err
	}

	var out []InvitationView
	err = live(invitationViews(tx), now).
		Where("i.organization_id = ?", organizationID).Order("i.id DESC").Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", scrub(err))
	}

	return out, nil
}

func invitationViews(tx *gorm.DB) *gorm.DB {
	return tx.Table("hub_invitations i").
		Select("i.*, o.name AS organization_name, t.name AS team_name").
		Joins("JOIN hub_organizations o ON o.id = i.organization_id").
		Joins("JOIN hub_teams t ON t.id = i.team_id")
}

func (i *Identity) invitationView(ctx context.Context, id uint) (InvitationView, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return InvitationView{}, err
	}

	return readInvitation(invitationViews(tx).Where("i.id = ?", id))
}

func readInvitation(query *gorm.DB) (InvitationView, error) {
	var view InvitationView
	if err := query.Limit(1).Scan(&view).Error; err != nil {
		return InvitationView{}, fmt.Errorf("read invitation: %w", scrub(err))
	}
	if view.ID == 0 {
		return InvitationView{}, ErrInviteUnknown
	}

	return view, nil
}

// InvitationByHash reads the invitation a link names and says whether it can
// be claimed: a revoked one reads as never issued, a claimed one as spent.
func (i *Identity) InvitationByHash(ctx context.Context, hash string, now time.Time) (InvitationView, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return InvitationView{}, err
	}

	return claimableInvitation(tx, strings.TrimSpace(hash), now)
}

func claimableInvitation(tx *gorm.DB, hash string, now time.Time) (InvitationView, error) {
	view, err := readInvitation(invitationViews(tx).Where("i.hash = ?", hash))
	if err != nil {
		return InvitationView{}, err
	}

	switch {
	case view.RevokedAt != nil:
		return InvitationView{}, ErrInviteUnknown
	case view.ClaimedAt != nil:
		return InvitationView{}, ErrInviteSpent
	case !now.Before(view.ExpiresAt):
		return InvitationView{}, ErrInviteExpired
	}

	return view, nil
}

// PendingInvitationForEmail finds the oldest invitation that can still be
// claimed by an address, an any-email one never matching.
func (i *Identity) PendingInvitationForEmail(ctx context.Context, email string, now time.Time) (InvitationView, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return InvitationView{}, err
	}

	return readInvitation(live(invitationViews(tx), now).
		Where("i.email = ? AND i.any_email = false", strings.ToLower(strings.TrimSpace(email))).
		Order("i.id"))
}

// RevokeInvitation ends an invitation that has not been claimed.
func (i *Identity) RevokeInvitation(ctx context.Context, organizationID, id uint, now time.Time) (InvitationView, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return InvitationView{}, err
	}

	err = tx.Transaction(func(tx *gorm.DB) error {
		if err := lockOrganization(tx, organizationID); err != nil {
			return err
		}
		_, err := revokeLive(tx, organizationID, id, now)

		return err
	})
	if err != nil {
		return InvitationView{}, err
	}

	return i.invitationView(ctx, id)
}

// revokeLive ends one live invitation of the Organization and returns it.
func revokeLive(tx *gorm.DB, organizationID, id uint, now time.Time) (Invitation, error) {
	var row Invitation
	err := live(tx, now).Where("id = ? AND organization_id = ?", id, organizationID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Invitation{}, ErrInvitationUnknown
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("read invitation: %w", scrub(err))
	}
	if err := tx.Model(&row).Update("revoked_at", now.UTC()).Error; err != nil {
		return Invitation{}, fmt.Errorf("revoke invitation: %w", scrub(err))
	}

	return row, nil
}

// ResendInvitation replaces an invitation with a new one for the same person,
// Team and role, so the old link stops working. A row that has expired can be
// resent too, which is the usual reason to.
func (i *Identity) ResendInvitation(
	ctx context.Context, organizationID, id uint, hash string, now time.Time,
	lifetime func(anyEmail bool) time.Duration,
) (InvitationView, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return InvitationView{}, err
	}

	var fresh Invitation
	err = tx.Transaction(func(tx *gorm.DB) error {
		if err := lockOrganization(tx, organizationID); err != nil {
			return err
		}

		var old Invitation
		err := tx.Where("id = ? AND organization_id = ? AND claimed_at IS NULL AND revoked_at IS NULL", id, organizationID).
			Take(&old).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrInvitationUnknown
		}
		if err != nil {
			return fmt.Errorf("read invitation: %w", scrub(err))
		}
		if err := tx.Model(&old).Update("revoked_at", now.UTC()).Error; err != nil {
			return fmt.Errorf("revoke invitation: %w", scrub(err))
		}

		fresh = Invitation{
			Hash: hash, OrganizationID: old.OrganizationID, TeamID: old.TeamID, Role: old.Role,
			Email: old.Email, AnyEmail: old.AnyEmail, InvitedBy: old.InvitedBy,
			ExpiresAt: now.Add(lifetime(old.AnyEmail)).UTC(), CreatedAt: now.UTC(),
		}
		if err := tx.Create(&fresh).Error; err != nil {
			return fmt.Errorf("create invitation: %w", scrub(err))
		}

		return nil
	})
	if err != nil {
		return InvitationView{}, err
	}

	return i.invitationView(ctx, fresh.ID)
}

// ClaimInvitation spends an invitation and adds the person to its Organization
// in its Team with its role, in one transaction, so a refusal leaves the
// invitation unspent and the person no member. Who may claim it is decided by
// the caller; this refuses a link that is gone, spent or out of time, and a
// person who already belongs to the Organization.
func (i *Identity) ClaimInvitation(ctx context.Context, claim Claim, now time.Time) (Placement, error) {
	tx, err := i.scope(ctx)
	if err != nil {
		return Placement{}, err
	}

	var userID uint
	var orgID uint
	err = tx.Transaction(func(tx *gorm.DB) error {
		peek, err := claimableInvitation(tx, strings.TrimSpace(claim.Hash), now)
		if err != nil {
			return err
		}
		if err := lockOrganization(tx, peek.OrganizationID); err != nil {
			return err
		}
		// A second claim waiting on the lock sees the first one's result.
		inv, err := claimableInvitation(tx, strings.TrimSpace(claim.Hash), now)
		if err != nil {
			return err
		}

		user, err := claimant(tx, claim, now)
		if err != nil {
			return err
		}
		var held int64
		if err := tx.Model(&Membership{}).
			Where("user_id = ? AND organization_id = ?", user.ID, inv.OrganizationID).Count(&held).Error; err != nil {
			return fmt.Errorf("read membership: %w", scrub(err))
		}
		if held > 0 {
			return ErrAlreadyMember
		}

		membership := Membership{
			UserID: user.ID, OrganizationID: inv.OrganizationID, TeamID: inv.TeamID, Role: inv.Role, CreatedAt: now.UTC(),
		}
		if err := tx.Create(&membership).Error; err != nil {
			return fmt.Errorf("create membership: %w", scrub(err))
		}
		err = tx.Model(&Invitation{}).Where("id = ?", inv.ID).
			Updates(map[string]any{"claimed_at": now.UTC(), "claimed_by": user.ID}).Error
		if err != nil {
			return fmt.Errorf("spend invitation: %w", scrub(err))
		}
		err = tx.Model(&User{}).Where("id = ?", user.ID).Update("last_organization_id", inv.OrganizationID).Error
		if err != nil {
			return fmt.Errorf("record last used organization: %w", scrub(err))
		}

		userID, orgID = user.ID, inv.OrganizationID

		return nil
	})
	if err != nil {
		return Placement{}, err
	}

	return i.PlacementOf(ctx, userID, orgID)
}

// claimant finds the User a claim is for, or makes them. A password claim
// makes a new login and refuses one anybody holds, so it can never attach to
// somebody else's account.
func claimant(tx *gorm.DB, claim Claim, now time.Time) (User, error) {
	if claim.PasswordHash == "" {
		return findOrCreateUser(tx, NewOrganization{
			Issuer: claim.Issuer, Subject: claim.Subject, Email: claim.Email, Name: claim.Name,
		}, now)
	}

	// A login is global, so two claims in different Organizations take turns.
	if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", organizationLockKey).Error; err != nil {
		return User{}, fmt.Errorf("lock logins: %w", scrub(err))
	}
	var held int64
	if err := tx.Model(&User{}).Where("issuer = ? AND subject = ?", passwordIssuer, claim.Subject).Count(&held).Error; err != nil {
		return User{}, fmt.Errorf("check login: %w", scrub(err))
	}
	if held > 0 {
		return User{}, ErrLoginTaken
	}

	user := User{
		Issuer: passwordIssuer, Subject: claim.Subject, Username: claim.Subject, Email: claim.Email,
		PasswordHash: claim.PasswordHash, CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := tx.Create(&user).Error; err != nil {
		return User{}, fmt.Errorf("create user: %w", scrub(err))
	}
	return user, nil
}
