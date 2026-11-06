// Package service holds the adapter's business logic. This file owns customer
// login: who may sign in, what a session is worth, and how an invite is spent.
package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/store"
)

// Errors the API renders. The invite errors are the store's, passed through,
// because a customer reading "that link has already been used" needs the same
// distinction the row makes.
var (
	// ErrSignInRefused is the single answer to every bad sign in. A wrong
	// password, a login nobody created and a malformed name all get this, so
	// the form cannot be used to find out which usernames are real.
	ErrSignInRefused = errors.New("that username and password do not match")

	// ErrNotSignedIn means there is no live session behind the request.
	ErrNotSignedIn = errors.New("this request carries no signed in session")
	// ErrTeamUnknown means the Organization has no Team under that name.
	ErrTeamUnknown = errors.New("that team does not exist in this organization")

	// ErrPasswordRefused means the password the customer chose is not one the
	// product will store.
	ErrPasswordRefused = errors.New("that password cannot be used")
)

const (
	// defaultSessionLifetime is how long a session lasts no matter how much it
	// is used. A platform engineer keeps working tabs for weeks, and this is
	// the outer bound on a stolen cookie.
	defaultSessionLifetime = 30 * 24 * time.Hour

	// defaultSessionIdle is how long a session survives without being used.
	// An abandoned laptop stops working after a week even though the absolute
	// expiry is a month away.
	defaultSessionIdle = 7 * 24 * time.Hour

	// defaultInviteTTL is how long an invite link stays claimable. Long enough
	// for a customer to get to it on the next working day, short enough that a
	// link found later is already dead.
	defaultInviteTTL = 48 * time.Hour

	// throttleBase is the wait after one wrong password, doubling with each
	// consecutive failure.
	throttleBase = 250 * time.Millisecond

	// throttleMax caps that wait. Past this a longer wait stops slowing an
	// attacker down and starts being a way to make sign in hang.
	throttleMax = 2 * time.Second

	// absentPassword is hashed at start to give the unknown login path
	// something real to compare against. The value never reaches a row, so
	// nothing can be signed in with it.
	absentPassword = "no login holds this password"
)

// IdentityStore is the login state this service reads and writes. The store's
// Identity satisfies it, and tests pass a fake.
type IdentityStore interface {
	EnsureLogin(ctx context.Context, username string, now time.Time) (bool, error)
	Logins(ctx context.Context) ([]store.PasswordLogin, error)
	PasswordLogin(ctx context.Context, username string) (store.PasswordLogin, error)
	SetPassword(ctx context.Context, username, passwordHash string, now time.Time) error
	RecordFailure(ctx context.Context, username string, now time.Time) (int, error)
	ClearFailures(ctx context.Context, username string, now time.Time) error
	CreateInvite(ctx context.Context, username, hash string, expiresAt, now time.Time) error
	Invite(ctx context.Context, hash string, now time.Time) (string, error)
	ClaimInvite(ctx context.Context, hash, passwordHash string, now time.Time) (string, error)
	CreateSession(ctx context.Context, session store.Session) error
	Session(ctx context.Context, hash string, now time.Time, idle time.Duration) (store.Session, error)
	DeleteSession(ctx context.Context, hash string) error
	PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error)
	SessionKey(ctx context.Context, now time.Time) ([]byte, error)
	Placement(ctx context.Context, login string) (store.Placement, error)
	UserPlacement(ctx context.Context, issuer, subject string) (store.Placement, error)
	PlacementOf(ctx context.Context, userID, organizationID uint) (store.Placement, error)
	Teams(ctx context.Context, organizationID uint) ([]store.Team, error)
	Team(ctx context.Context, organizationID uint, name string) (store.Team, error)
	ServedTeam(ctx context.Context, organization string) (store.Team, error)
	CreateOrganization(ctx context.Context, in store.NewOrganization, now time.Time) (store.Placement, error)
	UserOrganizations(ctx context.Context, userID uint) ([]store.UserOrganization, error)
	SwitchOrganization(ctx context.Context, sessionHash string, userID, organizationID uint) error
}

// InvitationClaimer is how an identity joins an Organization it was invited
// to. *Invitations is one.
type InvitationClaimer interface {
	ClaimLink(ctx context.Context, token, issuer string, claims auth.OIDCClaims) (store.Placement, error)
	ClaimForEmail(ctx context.Context, issuer string, claims auth.OIDCClaims) (store.Placement, bool, error)
}

// SignedSession is what a handler needs to set a cookie: the signed value and
// when it stops being accepted.
type SignedSession struct {
	Value     string
	ExpiresAt time.Time
}

// LoginStatus is one password login as the command line lists it.
type LoginStatus struct {
	Username string
	Claimed  bool
	Created  time.Time
}

// Identity is customer login. One adapter serves one Organization, so a session in
// another reads as signed out rather than as somebody else's session.
type Identity struct {
	store IdentityStore
	// organization is the Organization a deployment that serves one is pinned
	// to. Empty means it serves every Organization.
	organization string
	now          func() time.Time
	sleep        func(context.Context, time.Duration)
	lifetime     time.Duration
	idle         time.Duration
	inviteTTL    time.Duration
	cost         int
	signup       Signup
	signups      *rateLimiter
	invites      InvitationClaimer

	// absentHash is compared against when the login is not served, so the
	// unknown path pays the same bcrypt cost as the known one. Without it the
	// speed of the answer would say which usernames are real. It is built
	// from the configured cost rather than pinned to a literal, so a test at
	// the bcrypt floor is not slowed down by a hash made at the real one.
	absentHash string

	// rowless counts refusals that have no credential row to count against, so
	// those paths throttle the way a known login does.
	rowless atomic.Int64
}

// IdentityOption configures NewIdentity.
type IdentityOption func(*Identity)

// WithServedOrganization names the Organization this deployment serves. A
// session in another one is refused. An empty name accepts any Organization,
// which is what the command line uses, where there is no request to scope.
func WithServedOrganization(name string) IdentityOption {
	return func(i *Identity) { i.organization = name }
}

// WithIdentityClock replaces the clock in tests.
func WithIdentityClock(now func() time.Time) IdentityOption {
	return func(i *Identity) { i.now = now }
}

// WithIdentitySleep replaces the throttle wait in tests, so the suite does not
// pay for the delay it is asserting.
func WithIdentitySleep(sleep func(context.Context, time.Duration)) IdentityOption {
	return func(i *Identity) { i.sleep = sleep }
}

// WithSessionLifetime sets how long a session lasts regardless of use.
func WithSessionLifetime(d time.Duration) IdentityOption {
	return func(i *Identity) {
		if d > 0 {
			i.lifetime = d
		}
	}
}

// WithSessionIdle sets how long a session survives unused.
func WithSessionIdle(d time.Duration) IdentityOption {
	return func(i *Identity) {
		if d > 0 {
			i.idle = d
		}
	}
}

// WithPasswordCost sets the bcrypt work factor. Tests drop it to the floor,
// because the real cost adds seconds per hash under the race detector and none
// of the sign in rules depend on it.
func WithPasswordCost(cost int) IdentityOption {
	return func(i *Identity) {
		if cost > 0 {
			i.cost = cost
		}
	}
}

// WithInviteTTL sets how long an invite link stays claimable.
func WithInviteTTL(d time.Duration) IdentityOption {
	return func(i *Identity) {
		if d > 0 {
			i.inviteTTL = d
		}
	}
}

// NewIdentity returns the login service.
func NewIdentity(st IdentityStore, opts ...IdentityOption) *Identity {
	i := &Identity{
		store:     st,
		now:       time.Now,
		sleep:     sleepUntilDone,
		lifetime:  defaultSessionLifetime,
		idle:      defaultSessionIdle,
		inviteTTL: defaultInviteTTL,
		cost:      auth.DefaultPasswordCost,
	}
	for _, opt := range opts {
		opt(i)
	}

	// A failure here can only mean a cost bcrypt refuses. Leaving the hash
	// empty then costs the unknown path its comparison, which is a weaker
	// answer on a side channel rather than a reason to refuse to start.
	i.absentHash, _ = auth.HashPassword(absentPassword, i.cost)
	i.signups = newRateLimiter(signupLimit, signupWindow, signupMaxClients, func() time.Time { return i.now() })

	return i
}

// Login checks a username and password and returns a session cookie value.
//
// Every refusal answers ErrSignInRefused and waits, whatever the reason: the
// password was wrong, the login does not exist, or it has not claimed its
// invite yet. Telling those apart would turn the form into a way to
// enumerate customers.
func (i *Identity) Login(ctx context.Context, username, password string) (SignedSession, error) {
	now := i.now()

	if err := auth.ValidateUsername(username); err != nil {
		i.refuse(ctx, "", 0)

		return SignedSession{}, ErrSignInRefused
	}

	login, placement, err := i.servedLogin(ctx, username)
	if errors.Is(err, errLoginNotServed) {
		// Compare against a hash nobody holds so this path costs about what a
		// real comparison does, bcrypt dominating the extra queries.
		auth.PasswordMatches(i.absentHash, password)
		i.refuse(ctx, "", 0)

		return SignedSession{}, ErrSignInRefused
	}
	if err != nil {
		return SignedSession{}, err
	}

	if !auth.PasswordMatches(login.PasswordHash, password) {
		i.refuse(ctx, username, login.Failures)

		return SignedSession{}, ErrSignInRefused
	}

	if err := i.store.ClearFailures(ctx, username, now); err != nil {
		return SignedSession{}, fmt.Errorf("clear sign in failures: %w", err)
	}

	// Dead rows are cleared on the way in rather than by a timer nobody
	// watches. A failure here is not worth refusing a correct password over.
	_, _ = i.store.PurgeExpiredSessions(ctx, now)

	return i.issue(ctx, store.Session{
		Method:         store.SignInPassword,
		UserID:         placement.UserID,
		OrganizationID: placement.OrganizationID,
	}, now)
}

// errLoginNotServed means a login does not exist, has no membership, or sits in
// an Organization this deployment does not serve. The sign in answers all three
// the same way.
var errLoginNotServed = errors.New("no served login has that name")

// servedLogin reads a password login and where it sits. A login whose
// Organization this deployment does not serve reads as one that does not exist:
// Resolve would refuse its session on the very next request, and answering the
// same way keeps one deployment from being a password oracle for another's.
func (i *Identity) servedLogin(ctx context.Context, username string) (store.PasswordLogin, store.Placement, error) {
	login, err := i.store.PasswordLogin(ctx, username)
	if errors.Is(err, store.ErrLoginUnknown) {
		return store.PasswordLogin{}, store.Placement{}, errLoginNotServed
	}
	if err != nil {
		return store.PasswordLogin{}, store.Placement{}, fmt.Errorf("read login: %w", err)
	}

	placement, err := i.store.Placement(ctx, username)
	if err == nil {
		placement, err = i.placeInServed(ctx, placement)
	}
	if errors.Is(err, store.ErrLoginUnknown) || errors.Is(err, store.ErrNoMembership) {
		return store.PasswordLogin{}, store.Placement{}, errLoginNotServed
	}
	if err != nil {
		return store.PasswordLogin{}, store.Placement{}, fmt.Errorf("read membership: %w", err)
	}

	return login, placement, nil
}

// SignInOIDC opens a session for an identity its issuer has vouched for. The
// issuer proves who the person is; the membership on file decides what they
// may do. An identity with none starts an Organization of its own when signup
// is on, and is refused with store.ErrNoMembership otherwise. from is the
// caller's address, which signup rate limits. invite is the token of an
// invitation link the person followed, or empty: it adds them to the
// Organization it names, and is refused without opening a session when it
// cannot.
func (i *Identity) SignInOIDC(
	ctx context.Context,
	issuer string,
	claims auth.OIDCClaims,
	from, invite, organization string,
) (SignedSession, error) {
	now := i.now()

	placement, err := i.oidcPlacement(ctx, issuer, claims, from, invite, organization)
	if err != nil {
		return SignedSession{}, err
	}
	if err := i.serves(ctx, placement); err != nil {
		return SignedSession{}, err
	}

	_, _ = i.store.PurgeExpiredSessions(ctx, now)

	return i.issue(ctx, store.Session{
		Method:         store.SignInOIDC,
		UserID:         placement.UserID,
		OrganizationID: placement.OrganizationID,
	}, now)
}

// WithInvitationClaims lets a sign in claim an invitation: the one a link
// names, or one waiting for the verified email, which wins over signing up.
func WithInvitationClaims(c InvitationClaimer) IdentityOption {
	return func(i *Identity) { i.invites = c }
}

// oidcPlacement says where an identity acts. A link it followed decides; else
// the membership on file; else an invitation waiting for its verified email,
// which wins over making an Organization; else a new Organization.
func (i *Identity) oidcPlacement(
	ctx context.Context, issuer string, claims auth.OIDCClaims, from, invite, organization string,
) (store.Placement, error) {
	if invite != "" {
		if i.invites == nil {
			return store.Placement{}, store.ErrInviteUnknown
		}

		return i.invites.ClaimLink(ctx, invite, issuer, claims)
	}

	placement, err := i.store.UserPlacement(ctx, issuer, claims.Subject)
	if !errors.Is(err, store.ErrNoMembership) {
		return placement, err
	}

	if i.invites != nil {
		placement, claimed, err := i.invites.ClaimForEmail(ctx, issuer, claims)
		if claimed || err != nil {
			return placement, err
		}
	}

	return i.signUp(ctx, issuer, claims, from, organization)
}

// serves refuses a placement in an Organization this deployment does not
// serve. A deployment pinned to one Organization serves only that one.
func (i *Identity) serves(ctx context.Context, placement store.Placement) error {
	if i.organization == "" {
		return nil
	}

	team, err := i.store.ServedTeam(ctx, i.organization)
	if errors.Is(err, store.ErrTeamUnknown) {
		return store.ErrNoMembership
	}
	if err != nil {
		return fmt.Errorf("read the sole organization: %w", err)
	}
	if team.OrganizationID != placement.OrganizationID {
		return store.ErrNoMembership
	}

	return nil
}

// placeInServed moves a placement into the Organization a single-Organization
// deployment serves, when the login belongs to it but sits first in another.
// A deployment that serves every Organization keeps the placement it was given.
func (i *Identity) placeInServed(ctx context.Context, placement store.Placement) (store.Placement, error) {
	if i.organization == "" {
		return placement, nil
	}

	team, err := i.store.ServedTeam(ctx, i.organization)
	if errors.Is(err, store.ErrTeamUnknown) {
		return store.Placement{}, store.ErrNoMembership
	}
	if err != nil {
		return store.Placement{}, fmt.Errorf("read the sole organization: %w", err)
	}
	if team.OrganizationID == placement.OrganizationID {
		return placement, nil
	}

	return i.store.PlacementOf(ctx, placement.UserID, team.OrganizationID)
}

// SealOIDCFlow remembers a sign in across the trip to the issuer, as the value
// of a short lived cookie.
func (i *Identity) SealOIDCFlow(ctx context.Context, flow auth.OIDCFlow) (string, error) {
	now := i.now()
	key, err := i.store.SessionKey(ctx, now)
	if err != nil {
		return "", fmt.Errorf("read session key: %w", err)
	}

	return auth.SealOIDCFlow(key, flow, now), nil
}

// OpenOIDCFlow returns the sign in a cookie value remembers. It answers false
// for a value this deployment did not seal or that has gone stale.
func (i *Identity) OpenOIDCFlow(ctx context.Context, value string) (auth.OIDCFlow, bool, error) {
	now := i.now()
	key, err := i.store.SessionKey(ctx, now)
	if err != nil {
		return auth.OIDCFlow{}, false, fmt.Errorf("read session key: %w", err)
	}

	flow, ok := auth.OpenOIDCFlow(key, value, now)

	return flow, ok, nil
}

// Resolve turns a cookie value into the identity behind it.
func (i *Identity) Resolve(ctx context.Context, cookie string) (auth.Identity, error) {
	if cookie == "" {
		return auth.Identity{}, ErrNotSignedIn
	}

	now := i.now()
	key, err := i.store.SessionKey(ctx, now)
	if err != nil {
		return auth.Identity{}, fmt.Errorf("read session key: %w", err)
	}

	id, ok := auth.VerifySessionID(key, cookie)
	if !ok {
		return auth.Identity{}, ErrNotSignedIn
	}

	session, err := i.store.Session(ctx, auth.HashToken(id), now, i.idle)
	if err != nil {
		if errors.Is(err, store.ErrSessionUnknown) {
			return auth.Identity{}, ErrNotSignedIn
		}

		return auth.Identity{}, fmt.Errorf("read session: %w", err)
	}

	return i.resolveSession(ctx, session)
}

// resolveSession places a session through its User and Organization. The
// membership is read on every request, so a User who was removed or moved acts
// as nobody, or in the new Team, from the next one.
//
// The subject is the User's id when an identity provider opened the session,
// so the activity log can name them. A password login keeps being named by its
// username, which is what the log has always shown for it.
func (i *Identity) resolveSession(ctx context.Context, session store.Session) (auth.Identity, error) {
	placement, err := i.store.PlacementOf(ctx, session.UserID, session.OrganizationID)
	if err == nil {
		err = i.serves(ctx, placement)
	}
	if errors.Is(err, store.ErrNoMembership) {
		return auth.Identity{}, ErrNotSignedIn
	}
	if err != nil {
		return auth.Identity{}, fmt.Errorf("read membership: %w", err)
	}

	method, subject := store.SignInOIDC, fmt.Sprintf("user:%d", placement.UserID)
	if session.Method != store.SignInOIDC {
		method, subject = store.SignInPassword, placement.Login
	}

	return auth.Identity{
		Subject: subject,
		Scope:   scopeOf(placement),
		Method:  method,
	}, nil
}

// Membership is where a User sits in an Organization, read now. An Organization
// this deployment does not serve, and one the User does not belong to, are both
// store.ErrNoMembership. The agent authorization server asks it on every use of
// a token, as a session does on every request.
func (i *Identity) Membership(ctx context.Context, userID, organizationID uint) (store.Placement, error) {
	placement, err := i.store.PlacementOf(ctx, userID, organizationID)
	if err != nil {
		return store.Placement{}, err
	}
	if err := i.serves(ctx, placement); err != nil {
		return store.Placement{}, err
	}

	return placement, nil
}

// Operator is the identity of a caller holding the hub operator token. It is
// an admin of the Organization this deployment serves, borrowed from the
// login the deployment is pinned to, and it is no User. A deployment that
// serves every Organization has none to name, so the identity carries no
// Organization and the routes that need one refuse it.
func (i *Identity) Operator(ctx context.Context) (auth.Identity, error) {
	if i.organization == "" {
		return auth.Identity{}, nil
	}

	team, err := i.store.ServedTeam(ctx, i.organization)
	if err != nil {
		return auth.Identity{}, fmt.Errorf("read the sole organization: %w", err)
	}

	return auth.Identity{
		Scope: auth.Scope{
			OrganizationID: team.OrganizationID,
			Role:           auth.RoleAdmin,
			Team:           auth.Team{ID: team.ID, Name: team.Name},
		},
	}, nil
}

// Teams lists the Teams of an Organization.
func (i *Identity) Teams(ctx context.Context, organizationID uint) ([]auth.Team, error) {
	rows, err := i.store.Teams(ctx, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}

	out := make([]auth.Team, 0, len(rows))
	for _, row := range rows {
		out = append(out, auth.Team{ID: row.ID, Name: row.Name})
	}

	return out, nil
}

// Team returns the Team an Organization holds under a name, or ErrTeamUnknown.
// Another Organization's Team is never found.
func (i *Identity) Team(ctx context.Context, organizationID uint, name string) (auth.Team, error) {
	row, err := i.store.Team(ctx, organizationID, name)
	if err != nil {
		if errors.Is(err, store.ErrTeamUnknown) {
			return auth.Team{}, ErrTeamUnknown
		}

		return auth.Team{}, fmt.Errorf("read team: %w", err)
	}

	return auth.Team{ID: row.ID, Name: row.Name}, nil
}

func scopeOf(placement store.Placement) auth.Scope {
	return auth.Scope{
		UserID:           placement.UserID,
		UserName:         placement.Username,
		OrganizationID:   placement.OrganizationID,
		OrganizationName: placement.OrganizationName,
		Role:             placement.Role,
		Team:             auth.Team{ID: placement.TeamID, Name: placement.TeamName},
	}
}

// Logout ends the session a cookie names. A cookie that names nothing is not a
// failure: a second click, or a click after the session already expired, still
// leaves the customer signed out.
func (i *Identity) Logout(ctx context.Context, cookie string) error {
	if cookie == "" {
		return nil
	}

	key, err := i.store.SessionKey(ctx, i.now())
	if err != nil {
		return fmt.Errorf("read session key: %w", err)
	}

	id, ok := auth.VerifySessionID(key, cookie)
	if !ok {
		return nil
	}

	if err := i.store.DeleteSession(ctx, auth.HashToken(id)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	return nil
}

// InviteLogin reports which login a claimable link belongs to, so the claim
// page can name it without spending the link.
func (i *Identity) InviteLogin(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", store.ErrInviteUnknown
	}

	return i.store.Invite(ctx, auth.HashToken(token), i.now())
}

// Claim spends an invite link and sets the login's password, returning the
// login the link belonged to. Every session held under the old password ends.
//
// The password is validated before the link is touched, so a customer who
// picks something too short still has their invite.
func (i *Identity) Claim(ctx context.Context, token, password string) (string, error) {
	if token == "" {
		return "", store.ErrInviteUnknown
	}

	hash, err := auth.HashPassword(password, i.cost)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrPasswordRefused, err)
	}

	return i.store.ClaimInvite(ctx, auth.HashToken(token), hash, i.now())
}

// CreateLogin registers a login, with an Organization of its own, that can be
// invited. Nothing on the HTTP surface calls this: Percona onboards a customer
// from the command line, so there is no request anywhere in the product that
// creates one.
func (i *Identity) CreateLogin(ctx context.Context, username string) (bool, error) {
	if err := auth.ValidateUsername(username); err != nil {
		return false, err
	}

	return i.store.EnsureLogin(ctx, username, i.now())
}

// MintInvite issues a single-use link for an existing login and returns the
// token and its expiry. It is also the password reset path: a fresh link for a
// login that has already signed in replaces the password and ends its
// sessions when it is claimed.
func (i *Identity) MintInvite(ctx context.Context, username string) (string, time.Time, error) {
	if err := auth.ValidateUsername(username); err != nil {
		return "", time.Time{}, err
	}

	// A typo must not quietly mint a link for a login nobody created.
	if _, err := i.store.PasswordLogin(ctx, username); err != nil {
		return "", time.Time{}, err
	}

	token, hash, err := auth.NewToken()
	if err != nil {
		return "", time.Time{}, err
	}

	now := i.now()
	expires := now.Add(i.inviteTTL)
	if err := i.store.CreateInvite(ctx, username, hash, expires, now); err != nil {
		return "", time.Time{}, err
	}

	return token, expires, nil
}

// SetPassword replaces a login's password without an invite and ends every
// session it had. It exists for the chart seeded development login, which
// needs a password the test suite already knows.
func (i *Identity) SetPassword(ctx context.Context, username, password string) error {
	if err := auth.ValidateUsername(username); err != nil {
		return err
	}

	hash, err := auth.HashPassword(password, i.cost)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrPasswordRefused, err)
	}

	return i.store.SetPassword(ctx, username, hash, i.now())
}

// Logins lists who can sign in with a password and whether each has claimed an
// invite.
func (i *Identity) Logins(ctx context.Context) ([]LoginStatus, error) {
	rows, err := i.store.Logins(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]LoginStatus, 0, len(rows))
	for _, row := range rows {
		out = append(out, LoginStatus{
			Username: row.Username,
			Claimed:  row.PasswordHash != "",
			Created:  row.CreatedAt,
		})
	}

	return out, nil
}

// issue writes a session row and returns the signed cookie value.
func (i *Identity) issue(
	ctx context.Context,
	row store.Session,
	now time.Time,
) (SignedSession, error) {
	id, hash, err := auth.NewToken()
	if err != nil {
		return SignedSession{}, err
	}

	key, err := i.store.SessionKey(ctx, now)
	if err != nil {
		return SignedSession{}, fmt.Errorf("read session key: %w", err)
	}

	expires := now.Add(i.lifetime)
	row.Hash = hash
	row.CreatedAt = now
	row.LastSeenAt = now
	row.ExpiresAt = expires
	if err := i.store.CreateSession(ctx, row); err != nil {
		return SignedSession{}, fmt.Errorf("create session: %w", err)
	}

	return SignedSession{Value: auth.SignSessionID(key, id), ExpiresAt: expires}, nil
}

// refuse counts the failure and then waits.
//
// A login with a row is counted in that row, so replicas agree on how hard to
// throttle it. A refusal with no row to count, which is an unknown login, a
// malformed name, or a login in an Organization this deployment does not serve,
// is counted in this process instead.
//
// Counting the rowless paths is what keeps the wait from saying which ids are
// real. Without it a ramping answer meant the login existed and a fast one
// meant it did not, which is the channel the dummy hash comparison exists to
// close. The counter is not per id on purpose: an attacker choosing a fresh id
// each attempt would otherwise reset it, and keeping one entry per attempted
// id is unbounded state they control.
//
// A failure to record the failure is not worth turning a wrong password into
// an error the customer cannot act on, so the wait still happens.
func (i *Identity) refuse(ctx context.Context, username string, seen int) {
	failures := 0
	if username != "" {
		failures = seen + 1
		if counted, err := i.store.RecordFailure(ctx, username, i.now()); err == nil {
			failures = counted
		}
	} else {
		failures = int(i.rowless.Add(1))
	}

	i.wait(ctx, throttleDelay(failures))
}

// wait sleeps for the throttle, giving up if the caller has gone away. A
// client that disconnected is not owed the delay, and holding the goroutine
// for it only helps whoever is making the requests.
func (i *Identity) wait(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	i.sleep(ctx, d)
}

// sleepUntilDone is the production throttle wait. It returns early when the
// request is cancelled, which is what a client hanging up looks like.
func sleepUntilDone(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// throttleDelay is how long to wait after a failed sign in. It doubles with
// each consecutive failure up to a cap, which is what makes online guessing
// slow without letting an attacker make sign in hang for everybody.
func throttleDelay(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}

	delay := throttleBase
	for range failures - 1 {
		delay *= 2
		if delay >= throttleMax {
			return throttleMax
		}
	}

	return delay
}
