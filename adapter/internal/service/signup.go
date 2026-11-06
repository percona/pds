package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/store"
)

// What a signup can be refused for, besides the store's two limits
// (store.ErrCreationCap and store.ErrOrganizationCeiling).
var (
	// ErrSignUpUnverified means the identity provider did not vouch for an
	// email address, so nobody can be told apart from anybody else.
	ErrSignUpUnverified = errors.New("the identity has no verified email")
	// ErrSignUpRateLimited means too many Organizations were made from one
	// address in the last hour.
	ErrSignUpRateLimited = errors.New("too many organizations were made from this address")
	// ErrOrganizationName means the name is empty or too long.
	ErrOrganizationName = errors.New("an organization needs a name of up to 200 characters")
	// ErrOrganizationsClosed means this deployment does not let people make
	// or switch Organizations: signup is off, or it serves one Organization.
	ErrOrganizationsClosed = errors.New("this deployment does not create or switch organizations")
	// ErrNotOIDCSession means the session was not opened through the identity
	// provider. Only those people create and switch Organizations.
	ErrNotOIDCSession = errors.New("only a person signed in through the identity provider may do this")
)

const (
	// personCap is how many Organizations one person may create. It is the
	// product rule, so it is not configurable.
	personCap = 3

	// maxOrganizationName is the width of the column.
	maxOrganizationName = 200

	// A script making Organizations is the abuse this stops. Five an hour is
	// more than a person needs and few enough to matter.
	signupLimit      = 5
	signupWindow     = time.Hour
	signupMaxClients = 10000
)

// Signup says whether an unknown identity may make an Organization on first
// sign in, and how many this deployment holds. The zero value is off, which is
// what a self-hosted install wants.
type Signup struct {
	Enabled bool
	// OrganizationCeiling is how many Organizations the deployment accepts.
	// Zero means no ceiling.
	OrganizationCeiling int
}

// WithSignup sets the signup policy.
func WithSignup(policy Signup) IdentityOption {
	return func(i *Identity) { i.signup = policy }
}

// signUp makes the Organization an unknown identity starts in. The name the
// person chose wins over the one made from their identity. Anything that
// stops it for a reason the person cannot fix by waiting answers
// store.ErrNoMembership, the same as an identity with no membership, so a
// deployment with signup off reads as one with no such identity.
func (i *Identity) signUp(
	ctx context.Context,
	issuer string,
	claims auth.OIDCClaims,
	from, chosen string,
) (store.Placement, error) {
	if !i.organizationsOpen() {
		return store.Placement{}, store.ErrNoMembership
	}

	email := strings.TrimSpace(claims.Email)
	if !claims.EmailVerified || email == "" {
		return store.Placement{}, ErrSignUpUnverified
	}
	if !i.signups.allow(from) {
		return store.Placement{}, ErrSignUpRateLimited
	}

	name := startingName(claims.Name, email)
	if strings.TrimSpace(chosen) != "" {
		var err error
		if name, err = OrganizationName(chosen); err != nil {
			return store.Placement{}, err
		}
	}

	in := i.newOrganization(name)
	in.Issuer, in.Subject, in.Email, in.Name = issuer, claims.Subject, email, strings.TrimSpace(claims.Name)

	return i.store.CreateOrganization(ctx, in, i.now())
}

// organizationsOpen reports whether this deployment lets Organizations be made
// or switched. One pinned to a single Organization serves one Organization, and a
// new one would be refused on its first request.
func (i *Identity) organizationsOpen() bool {
	return i.signup.Enabled && i.organization == ""
}

func (i *Identity) newOrganization(name string) store.NewOrganization {
	return store.NewOrganization{
		OrganizationName:    name,
		PerPersonCap:        personCap,
		OrganizationCeiling: i.signup.OrganizationCeiling,
	}
}

// startingName is what a new Organization is called until its admin renames
// it: the person's name, else the part of their email before the at sign.
func startingName(name, email string) string {
	if name = strings.TrimSpace(name); name != "" {
		return truncate(name)
	}
	if address, err := mail.ParseAddress(email); err == nil {
		if local, _, ok := strings.Cut(address.Address, "@"); ok && local != "" {
			return truncate(local)
		}
	}

	return "My organization"
}

func truncate(s string) string {
	if utf8.RuneCountInString(s) <= maxOrganizationName {
		return s
	}

	return string([]rune(s)[:maxOrganizationName])
}

// OrganizationName trims a name a person typed and checks it fits the column.
func OrganizationName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxOrganizationName {
		return "", ErrOrganizationName
	}

	return name, nil
}

// CreateOrganization makes another Organization for the person behind a
// session and moves the session into it. The same limits apply as at signup.
func (i *Identity) CreateOrganization(ctx context.Context, cookie, name, from string) error {
	identity, hash, err := i.oidcSession(ctx, cookie)
	if err != nil {
		return err
	}

	name, err = OrganizationName(name)
	if err != nil {
		return err
	}
	if !i.organizationsOpen() {
		return ErrOrganizationsClosed
	}
	if !i.signups.allow(from) {
		return ErrSignUpRateLimited
	}

	// The session moves in the same transaction that makes the Organization,
	// so a failure cannot leave one made, and counted against the person's
	// cap, with the session still elsewhere.
	in := i.newOrganization(name)
	in.UserID, in.SessionHash = identity.Scope.UserID, hash
	_, err = i.store.CreateOrganization(ctx, in, i.now())

	return err
}

// SwitchOrganization moves the session into another Organization its person
// belongs to. One they do not belong to is store.ErrNoMembership.
func (i *Identity) SwitchOrganization(ctx context.Context, cookie string, organizationID uint) error {
	identity, hash, err := i.oidcSession(ctx, cookie)
	if err != nil {
		return err
	}
	// A pinned deployment serves one Organization, and a session moved out of
	// it would read as signed out from then on.
	if i.organization != "" {
		return ErrOrganizationsClosed
	}

	return i.store.SwitchOrganization(ctx, hash, identity.Scope.UserID, organizationID)
}

// Organizations lists what a User belongs to, for the switcher.
func (i *Identity) Organizations(ctx context.Context, userID uint) ([]store.UserOrganization, error) {
	out, err := i.store.UserOrganizations(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}

	return out, nil
}

// oidcSession resolves a cookie to an identity provider session and the hash
// of its row. A password session is ErrNotOIDCSession.
func (i *Identity) oidcSession(ctx context.Context, cookie string) (auth.Identity, string, error) {
	identity, err := i.Resolve(ctx, cookie)
	if err != nil {
		return auth.Identity{}, "", err
	}
	if identity.Method != store.SignInOIDC {
		return auth.Identity{}, "", ErrNotOIDCSession
	}

	key, err := i.store.SessionKey(ctx, i.now())
	if err != nil {
		return auth.Identity{}, "", fmt.Errorf("read session key: %w", err)
	}
	id, ok := auth.VerifySessionID(key, cookie)
	if !ok {
		return auth.Identity{}, "", ErrNotSignedIn
	}

	return identity, auth.HashToken(id), nil
}
