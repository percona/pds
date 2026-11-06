package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/resend"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/store"
)

const (
	// defaultInvitationTTL is how long an invitation to a named address stays
	// claimable.
	defaultInvitationTTL = 7 * 24 * time.Hour

	// anyEmailInvitationTTL is how long an any-email invitation lives. Nothing
	// ties it to a person, so whoever holds the link may claim it, and a short
	// life is what limits that.
	anyEmailInvitationTTL = 48 * time.Hour
)

// What claiming an invitation can be refused for beyond the store's invite
// errors. The refusals about the address are wrapped in an InviteRefusal.
var (
	// ErrInviteEmailMismatch means the verified address is not the one invited.
	ErrInviteEmailMismatch = errors.New("the verified email is not the one that was invited")
	// ErrInviteEmailUnverified means the identity provider did not vouch for an
	// address, or sent none.
	ErrInviteEmailUnverified = errors.New("the identity has no verified email")
	// ErrInviteEmail means an invitation names no address, or one that is not.
	ErrInviteEmail = errors.New("an invitation needs one email address, or any email")
	// ErrPasswordClaimClosed means this deployment has an identity provider, or
	// serves one Organization, so a password cannot claim an invitation.
	ErrPasswordClaimClosed = errors.New("this deployment does not claim invitations with a password")
)

// InviteRefusal is a claim refused over the address. Masked is the invited
// address with most of it hidden, which is all the person is told: enough to
// know which account to sign in with, not enough to learn an address.
type InviteRefusal struct {
	Reason error
	Masked string
}

// Error implements error.
func (r *InviteRefusal) Error() string { return r.Reason.Error() }

// Unwrap lets callers match the reason.
func (r *InviteRefusal) Unwrap() error { return r.Reason }

// InvitationStore is what invitations read and write. The store's Identity
// satisfies it, and tests pass a real one.
type InvitationStore interface {
	CreateInvitation(ctx context.Context, in store.NewInvitation, now time.Time) (store.InvitationView, error)
	PendingInvitations(ctx context.Context, organizationID uint, now time.Time) ([]store.InvitationView, error)
	RevokeInvitation(ctx context.Context, organizationID, id uint, now time.Time) (store.InvitationView, error)
	ResendInvitation(
		ctx context.Context, organizationID, id uint, hash string, now time.Time,
		lifetime func(anyEmail bool) time.Duration,
	) (store.InvitationView, error)
	InvitationByHash(ctx context.Context, hash string, now time.Time) (store.InvitationView, error)
	PendingInvitationForEmail(ctx context.Context, email string, now time.Time) (store.InvitationView, error)
	ClaimInvitation(ctx context.Context, claim store.Claim, now time.Time) (store.Placement, error)
}

// InvitationMailer sends one email. *resend.Client is one.
type InvitationMailer interface {
	Send(ctx context.Context, email resend.Email) error
}

// InvitationRecorder takes the activity an invitation leaves. *Activity is one.
type InvitationRecorder interface {
	Record(ctx context.Context, e ActivityEvent)
}

// InvitationOption configures Invitations.
type InvitationOption func(*Invitations)

// WithInvitationLinks sets the address links point at, with no trailing slash.
func WithInvitationLinks(publicURL string) InvitationOption {
	return func(i *Invitations) { i.publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/") }
}

// WithInvitationMailer emails invitations from an address. Without it the link
// is shown to the admin and nothing is sent.
func WithInvitationMailer(mailer InvitationMailer, from string) InvitationOption {
	return func(i *Invitations) { i.mailer, i.from = mailer, from }
}

// WithInvitationRecorder records invitation activity.
func WithInvitationRecorder(recorder InvitationRecorder) InvitationOption {
	return func(i *Invitations) { i.recorder = recorder }
}

// WithInvitationTTL sets how long an invitation to a named address lives.
func WithInvitationTTL(d time.Duration) InvitationOption {
	return func(i *Invitations) {
		if d > 0 {
			i.ttl = d
		}
	}
}

// WithInvitationPasswordCost sets the bcrypt cost, which tests lower.
func WithInvitationPasswordCost(cost int) InvitationOption {
	return func(i *Invitations) { i.cost = cost }
}

// WithInvitationPasswords lets a password claim an invitation. It is on only
// where there is no identity provider to verify an address and the deployment
// serves every Organization, since a login in another one could not sign in.
func WithInvitationPasswords(on bool) InvitationOption {
	return func(i *Invitations) { i.passwords = on }
}

// Invitations is how people join an Organization: an admin invites, the
// invitee claims. Every admin call names the Organization, which the router
// took from the session, so nothing here can reach another one.
type Invitations struct {
	store     InvitationStore
	now       func() time.Time
	ttl       time.Duration
	publicURL string
	mailer    InvitationMailer
	from      string
	recorder  InvitationRecorder
	cost      int
	passwords bool
}

// NewInvitations returns the invitation service.
func NewInvitations(st InvitationStore, opts ...InvitationOption) *Invitations {
	i := &Invitations{store: st, now: time.Now, ttl: defaultInvitationTTL, cost: auth.DefaultPasswordCost}
	for _, opt := range opts {
		opt(i)
	}

	return i
}

// Invite is what an admin asks for. AnyEmail leaves Email empty.
type Invite struct {
	Email    string
	AnyEmail bool
	Team     string
	Role     string
}

// InviteResult is an invitation and the link to it. The link exists only in
// this result: the store keeps its hash, so it cannot be shown again.
type InviteResult struct {
	Invitation store.InvitationView
	Link       string
	// Emailed says the link was sent to the invited address. An invitation
	// whose email failed still exists, and the admin still has the link.
	Emailed bool
}

// Invite creates an invitation, and emails it when a mailer is configured.
func (i *Invitations) Invite(ctx context.Context, organizationID uint, req Invite) (InviteResult, error) {
	in := store.NewInvitation{
		OrganizationID: organizationID, Team: strings.TrimSpace(req.Team), Role: strings.TrimSpace(req.Role),
		AnyEmail: req.AnyEmail,
	}
	if scope, ok := auth.ScopeFromContext(ctx); ok {
		in.InvitedBy = scope.UserID
	}

	switch {
	case req.AnyEmail:
		if strings.TrimSpace(req.Email) != "" {
			return InviteResult{}, ErrInviteEmail
		}
	default:
		address, err := mail.ParseAddress(strings.TrimSpace(req.Email))
		if err != nil || address.Name != "" {
			return InviteResult{}, ErrInviteEmail
		}
		in.Email = strings.ToLower(address.Address)
	}

	token, hash, err := auth.NewToken()
	if err != nil {
		return InviteResult{}, fmt.Errorf("make invitation token: %w", err)
	}
	now := i.now()
	in.Hash, in.ExpiresAt = hash, now.Add(i.lifetime(req.AnyEmail))

	view, err := i.store.CreateInvitation(ctx, in, now)
	if err != nil {
		return InviteResult{}, err
	}
	i.record(ctx, view, ActionInvitationCreate, OutcomeSucceeded, "")

	return i.deliver(ctx, view, token), nil
}

// Resend replaces an invitation with a new one, so the old link stops working,
// and emails the new link.
func (i *Invitations) Resend(ctx context.Context, organizationID, id uint) (InviteResult, error) {
	token, hash, err := auth.NewToken()
	if err != nil {
		return InviteResult{}, fmt.Errorf("make invitation token: %w", err)
	}

	view, err := i.store.ResendInvitation(ctx, organizationID, id, hash, i.now(), i.lifetime)
	if err != nil {
		return InviteResult{}, err
	}
	i.record(ctx, view, ActionInvitationResend, OutcomeSucceeded, "")

	return i.deliver(ctx, view, token), nil
}

// lifetime is how long an invitation lives: an any-email one is short lived.
func (i *Invitations) lifetime(anyEmail bool) time.Duration {
	if anyEmail {
		return anyEmailInvitationTTL
	}

	return i.ttl
}

// Revoke ends an invitation that was not claimed.
func (i *Invitations) Revoke(ctx context.Context, organizationID, id uint) error {
	view, err := i.store.RevokeInvitation(ctx, organizationID, id, i.now())
	if err != nil {
		return err
	}
	i.record(ctx, view, ActionInvitationRevoke, OutcomeSucceeded, "")

	return nil
}

// Pending lists what can still be claimed.
func (i *Invitations) Pending(ctx context.Context, organizationID uint) ([]store.InvitationView, error) {
	out, err := i.store.PendingInvitations(ctx, organizationID, i.now())
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}

	return out, nil
}

// Preview is what a link says without spending it.
type Preview struct {
	Organization string
	Team         string
	Role         string
	// Email is the invited address, masked. It is empty for any email.
	Email    string
	AnyEmail bool
}

// Preview reads an invitation link so the claim page can say what it is for.
// A token that names no invitation is store.ErrInviteUnknown.
func (i *Invitations) Preview(ctx context.Context, token string) (Preview, error) {
	view, err := i.byToken(ctx, token)
	if err != nil {
		return Preview{}, err
	}

	return Preview{
		Organization: view.OrganizationName, Team: view.TeamName, Role: view.Role,
		Email: maskedEmail(view), AnyEmail: view.AnyEmail,
	}, nil
}

func (i *Invitations) byToken(ctx context.Context, token string) (store.InvitationView, error) {
	if token = strings.TrimSpace(token); token == "" {
		return store.InvitationView{}, store.ErrInviteUnknown
	}

	return i.store.InvitationByHash(ctx, auth.HashToken(token), i.now())
}

// ClaimLink adds the identity an issuer vouched for to the Organization a link
// names. The verified address has to be the one invited. A refusal leaves the
// invitation unspent, so the right account can still claim it.
func (i *Invitations) ClaimLink(
	ctx context.Context, token, issuer string, claims auth.OIDCClaims,
) (store.Placement, error) {
	view, err := i.byToken(ctx, token)
	if err != nil {
		return store.Placement{}, err
	}

	return i.claim(ctx, view, issuer, claims)
}

// ClaimForEmail claims the oldest invitation waiting for a verified address,
// and reports whether there was one. It is what lets an invitation win over
// creating an Organization on a first sign in.
func (i *Invitations) ClaimForEmail(
	ctx context.Context, issuer string, claims auth.OIDCClaims,
) (store.Placement, bool, error) {
	email := strings.TrimSpace(claims.Email)
	if !claims.EmailVerified || email == "" {
		return store.Placement{}, false, nil
	}

	view, err := i.store.PendingInvitationForEmail(ctx, email, i.now())
	if errors.Is(err, store.ErrInviteUnknown) {
		return store.Placement{}, false, nil
	}
	if err != nil {
		return store.Placement{}, false, fmt.Errorf("find invitation: %w", err)
	}

	placement, err := i.claim(ctx, view, issuer, claims)

	return placement, err == nil, err
}

func (i *Invitations) claim(
	ctx context.Context, view store.InvitationView, issuer string, claims auth.OIDCClaims,
) (store.Placement, error) {
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	var reason error
	switch {
	case !claims.EmailVerified || email == "":
		reason = ErrInviteEmailUnverified
	case !view.AnyEmail && email != view.Email:
		reason = ErrInviteEmailMismatch
	}
	if reason != nil {
		i.record(ctx, view, ActionInvitationRefuse, OutcomeFailed, reason.Error())

		return store.Placement{}, &InviteRefusal{Reason: reason, Masked: maskedEmail(view)}
	}

	placement, err := i.store.ClaimInvitation(ctx, store.Claim{
		Hash: view.Hash, Issuer: issuer, Subject: claims.Subject, Email: email, Name: strings.TrimSpace(claims.Name),
	}, i.now())
	if err != nil {
		if errors.Is(err, store.ErrAlreadyMember) {
			i.record(ctx, view, ActionInvitationRefuse, OutcomeFailed, "the person already belongs to the organization")
		}

		return store.Placement{}, err
	}
	i.recordClaimed(ctx, view, placement)

	return placement, nil
}

// ClaimPassword adds a new password login to the Organization a link names,
// returning the login. The link's address is not verified by anything here, so
// a deployment with an identity provider refuses it: the claim goes through
// the provider, which does verify.
func (i *Invitations) ClaimPassword(ctx context.Context, token, login, password string) (string, error) {
	view, err := i.byToken(ctx, token)
	if err != nil {
		return "", err
	}
	if !i.passwords {
		return "", ErrPasswordClaimClosed
	}

	login = strings.TrimSpace(login)
	if err := auth.ValidateUsername(login); err != nil {
		return "", fmt.Errorf("%w: %s", ErrLoginInvalid, err)
	}
	hash, err := auth.HashPassword(password, i.cost)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrPasswordRefused, err)
	}

	placement, err := i.store.ClaimInvitation(ctx, store.Claim{
		Hash: view.Hash, Subject: login, Email: view.Email, PasswordHash: hash,
	}, i.now())
	if err != nil {
		return "", err
	}
	i.recordClaimed(ctx, view, placement)

	return login, nil
}

// ErrLoginInvalid means the login a password claim chose is not one the
// product accepts.
var ErrLoginInvalid = errors.New("that login cannot be used")

// deliver builds the link and emails it. A failed email is logged and reported
// as not sent: the invitation exists either way, and the admin has the link.
func (i *Invitations) deliver(ctx context.Context, view store.InvitationView, token string) InviteResult {
	link := i.publicURL + "/claim?token=" + url.QueryEscape(token)
	out := InviteResult{Invitation: view, Link: link}
	if i.mailer == nil || view.AnyEmail || view.Email == "" {
		return out
	}

	err := i.mailer.Send(ctx, resend.Email{
		From:    i.from,
		To:      []string{view.Email},
		Subject: "You are invited to " + view.OrganizationName + " on Percona Database Service",
		Text:    invitationText(view, link),
	})
	if err != nil {
		log.FromContext(ctx).Warn("invitation email was not sent", slog.Any("err", err))

		return out
	}
	out.Emailed = true

	return out
}

func invitationText(view store.InvitationView, link string) string {
	role := "a user"
	if view.Role == store.RoleAdmin {
		role = "an admin"
	}

	return fmt.Sprintf("You have been invited to join %s on Percona Database Service as %s in the %s team.\n\n"+
		"Open this link to accept. It can be used once and expires on %s:\n\n%s\n\n"+
		"Sign in with the email address this message was sent to.\n",
		view.OrganizationName, role, view.TeamName, view.ExpiresAt.UTC().Format("2 January 2006"), link)
}

// maskedEmail hides all but the first letter of the address's name.
func maskedEmail(view store.InvitationView) string {
	if view.AnyEmail || view.Email == "" {
		return ""
	}
	name, domain, ok := strings.Cut(view.Email, "@")
	if !ok || name == "" {
		return "***"
	}

	return string([]rune(name)[:1]) + "***@" + domain
}

// record writes an invitation's activity under the Team it is for, so that
// Team's Users and the admins see it. text is the error of a refusal.
func (i *Invitations) record(ctx context.Context, view store.InvitationView, action, outcome, text string) {
	if i.recorder == nil {
		return
	}

	target := view.Email
	if view.AnyEmail {
		target = "any email"
	}
	i.recorder.Record(auth.WithActingTeam(ctx, auth.Team{ID: view.TeamID, Name: view.TeamName}),
		ActivityEvent{
			Action: action, Outcome: outcome, Target: target, Error: text,
			Detail: fmt.Sprintf("team %s, role %s", view.TeamName, view.Role),
		})
}

// recordClaimed records a claim as the person who made it, who has no session
// yet, so the scope is built from where they were placed.
func (i *Invitations) recordClaimed(ctx context.Context, view store.InvitationView, placement store.Placement) {
	if i.recorder == nil {
		return
	}

	target := view.Email
	if view.AnyEmail {
		target = "any email"
	}
	ctx = auth.WithIdentity(ctx, auth.Identity{
		Subject: placement.Username,
		Scope: auth.Scope{
			UserID: placement.UserID, UserName: placement.Username,
			OrganizationID: placement.OrganizationID, OrganizationName: placement.OrganizationName,
			Role: placement.Role, Team: auth.Team{ID: placement.TeamID, Name: placement.TeamName},
		},
	})
	i.recorder.Record(ctx, ActivityEvent{
		Action: ActionInvitationClaim, Outcome: OutcomeSucceeded, Target: target,
		Detail: fmt.Sprintf("team %s, role %s", view.TeamName, view.Role),
	})
}
