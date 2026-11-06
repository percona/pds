package service_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

var identityNow = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

// fakeInvite is one password invite link, by the login it is for.
type fakeInvite struct {
	Username  string
	ExpiresAt time.Time
	ClaimedAt *time.Time
}

// fakeIdentityStore stands in for the PostgreSQL rows so the sign in rules can
// be driven without a server. It is deliberately dumb: every decision worth
// testing belongs to the service, and anything this fake decides would be
// tested twice.
type fakeIdentityStore struct {
	mu          sync.Mutex
	credentials map[string]store.PasswordLogin
	invites     map[string]fakeInvite
	sessions    map[string]store.Session
	key         []byte
	purged      int
	failErr     error
	placements  map[string]store.Placement
	teams       []store.Team
	// served is the Team of an Organization by name, for a test that wants one
	// the logins do not place anybody in.
	served  map[string]store.Team
	created []store.NewOrganization
}

func newFakeIdentityStore() *fakeIdentityStore {
	return &fakeIdentityStore{
		credentials: map[string]store.PasswordLogin{},
		invites:     map[string]fakeInvite{},
		sessions:    map[string]store.Session{},
		placements:  map[string]store.Placement{},
		served:      map[string]store.Team{},
		key:         []byte("0123456789abcdef0123456789abcdef"),
	}
}

// place sets where a login sits, replacing the default every new login gets.
func (f *fakeIdentityStore) place(login string, placement store.Placement) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.placements[login] = placement
}

// unplace removes a login's membership, as removing the User does.
func (f *fakeIdentityStore) unplace(login string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.placements, login)
}

func (f *fakeIdentityStore) Placement(_ context.Context, login string) (store.Placement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	placement, ok := f.placements[login]
	if !ok {
		return store.Placement{}, store.ErrLoginUnknown
	}

	return placement, nil
}

// The identity provider sign in is driven through the HTTP tests, which hold
// the rows it reads; here nobody has an identity at an issuer.
func (f *fakeIdentityStore) UserPlacement(context.Context, string, string) (store.Placement, error) {
	return store.Placement{}, store.ErrNoMembership
}

func (f *fakeIdentityStore) PlacementOf(_ context.Context, userID, organizationID uint) (store.Placement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, placement := range f.placements {
		if placement.UserID == userID && placement.OrganizationID == organizationID {
			return placement, nil
		}
	}

	return store.Placement{}, store.ErrNoMembership
}

func (f *fakeIdentityStore) Teams(_ context.Context, organizationID uint) ([]store.Team, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []store.Team
	for _, team := range f.teams {
		if team.OrganizationID == organizationID {
			out = append(out, team)
		}
	}

	return out, nil
}

func (f *fakeIdentityStore) Team(ctx context.Context, organizationID uint, name string) (store.Team, error) {
	teams, _ := f.Teams(ctx, organizationID)
	for _, team := range teams {
		if team.Name == name {
			return team, nil
		}
	}

	return store.Team{}, store.ErrTeamUnknown
}

func (f *fakeIdentityStore) EnsureLogin(_ context.Context, login string, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.credentials[login]; ok {
		return false, nil
	}
	f.credentials[login] = store.PasswordLogin{Username: login, CreatedAt: now, UpdatedAt: now}
	f.placements[login] = store.Placement{
		UserID: uint(len(f.placements) + 1), OrganizationID: uint(len(f.placements) + 1),
		Login: login, OrganizationName: login, Role: store.RoleAdmin, TeamID: uint(len(f.placements) + 1), TeamName: "default",
	}

	return true, nil
}

func (f *fakeIdentityStore) Logins(context.Context) ([]store.PasswordLogin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]store.PasswordLogin, 0, len(f.credentials))
	for _, row := range f.credentials {
		out = append(out, row)
	}

	return out, nil
}

func (f *fakeIdentityStore) PasswordLogin(_ context.Context, login string) (store.PasswordLogin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	row, ok := f.credentials[login]
	if !ok {
		return store.PasswordLogin{}, store.ErrLoginUnknown
	}

	return row, nil
}

func (f *fakeIdentityStore) SetPassword(_ context.Context, login, hash string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	row, ok := f.credentials[login]
	if !ok {
		return store.ErrLoginUnknown
	}
	row.PasswordHash = hash
	row.Failures = 0
	row.UpdatedAt = now
	f.credentials[login] = row
	f.dropSessionsLocked(login)

	return nil
}

func (f *fakeIdentityStore) RecordFailure(_ context.Context, login string, _ time.Time) (int, error) {
	if f.failErr != nil {
		return 0, f.failErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	row, ok := f.credentials[login]
	if !ok {
		return 0, store.ErrLoginUnknown
	}
	row.Failures++
	f.credentials[login] = row

	return row.Failures, nil
}

func (f *fakeIdentityStore) ClearFailures(_ context.Context, login string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	row, ok := f.credentials[login]
	if !ok {
		return store.ErrLoginUnknown
	}
	row.Failures = 0
	f.credentials[login] = row

	return nil
}

func (f *fakeIdentityStore) CreateInvite(_ context.Context, login, hash string, expires, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.invites[hash] = fakeInvite{Username: login, ExpiresAt: expires}

	return nil
}

func (f *fakeIdentityStore) Invite(_ context.Context, hash string, now time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	row, ok := f.invites[hash]
	if !ok {
		return "", store.ErrInviteUnknown
	}
	if row.ClaimedAt != nil {
		return "", store.ErrInviteSpent
	}
	if !now.Before(row.ExpiresAt) {
		return "", store.ErrInviteExpired
	}

	return row.Username, nil
}

func (f *fakeIdentityStore) ClaimInvite(
	_ context.Context,
	hash, passwordHash string,
	now time.Time,
) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	row, ok := f.invites[hash]
	if !ok {
		return "", store.ErrInviteUnknown
	}
	if row.ClaimedAt != nil {
		return "", store.ErrInviteSpent
	}
	if !now.Before(row.ExpiresAt) {
		return "", store.ErrInviteExpired
	}
	spent := now
	row.ClaimedAt = &spent
	f.invites[hash] = row

	credential := f.credentials[row.Username]
	credential.Username = row.Username
	credential.PasswordHash = passwordHash
	credential.Failures = 0
	f.credentials[row.Username] = credential
	f.dropSessionsLocked(row.Username)

	return row.Username, nil
}

func (f *fakeIdentityStore) CreateSession(_ context.Context, session store.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sessions[session.Hash] = session

	return nil
}

func (f *fakeIdentityStore) Session(
	_ context.Context,
	hash string,
	now time.Time,
	idle time.Duration,
) (store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	row, ok := f.sessions[hash]
	if !ok {
		return store.Session{}, store.ErrSessionUnknown
	}
	if !now.Before(row.ExpiresAt) || (idle > 0 && !now.Before(row.LastSeenAt.Add(idle))) {
		delete(f.sessions, hash)

		return store.Session{}, store.ErrSessionUnknown
	}

	return row, nil
}

func (f *fakeIdentityStore) DeleteSession(_ context.Context, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.sessions, hash)

	return nil
}

func (f *fakeIdentityStore) DeleteSessionsFor(_ context.Context, login string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.dropSessionsLocked(login)

	return nil
}

func (f *fakeIdentityStore) PurgeExpiredSessions(_ context.Context, _ time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.purged++

	return 0, nil
}

func (f *fakeIdentityStore) SessionKey(context.Context, time.Time) ([]byte, error) {
	return f.key, nil
}

func (f *fakeIdentityStore) dropSessionsLocked(login string) {
	for hash, row := range f.sessions {
		if row.UserID == f.placements[login].UserID {
			delete(f.sessions, hash)
		}
	}
}

func (f *fakeIdentityStore) liveSessions() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.sessions)
}

// newIdentityService returns the service with a fixed clock and a sleep that
// records instead of waiting, so the throttle can be asserted without the
// suite paying for it.
func newIdentityService(t *testing.T, fake *fakeIdentityStore) (*service.Identity, *[]time.Duration) {
	t.Helper()

	slept := &[]time.Duration{}

	return service.NewIdentity(fake,
		service.WithServedOrganization("acme"),
		service.WithIdentityClock(func() time.Time { return identityNow }),
		service.WithIdentitySleep(func(_ context.Context, d time.Duration) { *slept = append(*slept, d) }),
		service.WithPasswordCost(bcrypt.MinCost),
	), slept
}

func seedClaimedLogin(t *testing.T, fake *fakeIdentityStore, login, password string) {
	t.Helper()

	_, err := fake.EnsureLogin(t.Context(), login, identityNow)
	require.NoError(t, err)
	hash, err := auth.HashPassword(password, bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, fake.SetPassword(t.Context(), login, hash, identityNow))
}

func TestLoginIssuesASignedCookieForTheRightPassword(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")

	session, err := identity.Login(t.Context(), "acme", "a long enough secret")
	require.NoError(t, err)

	assert.NotEmpty(t, session.Value)
	assert.NotContains(t, session.Value, "a long enough secret")
	assert.Equal(t, identityNow.Add(30*24*time.Hour), session.ExpiresAt)
	assert.Equal(t, 1, fake.liveSessions(), "signing in wrote one session row")

	resolved, err := identity.Resolve(t.Context(), session.Value)
	require.NoError(t, err)
	assert.Equal(t, "acme", resolved.Subject)
}

func TestLoginRefusesAWrongPasswordAndAnUnknownLoginTheSameWay(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")

	tests := []struct {
		name     string
		login    string
		password string
	}{
		{"wrong password", "acme", "a different secret"},
		{"unknown login", "globex", "a long enough secret"},
		{"malformed login", "Not A Login", "a long enough secret"},
		{"empty password", "acme", ""},
	}

	var messages []string
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := identity.Login(t.Context(), tc.login, tc.password)

			require.ErrorIs(t, err, service.ErrSignInRefused)
			messages = append(messages, err.Error())
		})
	}

	// One message for every refusal. Telling a real login id from a made up
	// one would turn the login form into a customer list.
	for _, message := range messages[1:] {
		assert.Equal(t, messages[0], message)
	}
	assert.Zero(t, fake.liveSessions())
}

// TestLoginRefusesAnOrganizationThisDeploymentDoesNotServe is the bug this guard
// exists for. Without it a credential for another login signs in, gets a
// cookie, and is refused on the next request, which reads to the customer as a
// login page that will not stay away.
func TestLoginRefusesAnOrganizationThisDeploymentDoesNotServe(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	// A real, claimed credential, for a login this adapter does not serve.
	seedClaimedLogin(t, fake, "globex", "a long enough secret")

	_, err := identity.Login(t.Context(), "globex", "a long enough secret")

	require.ErrorIs(t, err, service.ErrSignInRefused)
	assert.Zero(t, fake.liveSessions(), "no session row is written for a login that cannot use it")
}

// TestLoginPlacesALoginInTheOrganizationThisDeploymentServes covers a login that
// belongs to several Organizations and is oldest in one this deployment does not
// serve: it signs in to the served one rather than being refused.
func TestLoginPlacesALoginInTheOrganizationThisDeploymentServes(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")
	// The deployment serves the Organization named acme, and the login's oldest
	// membership is in another one.
	fake.served["acme"] = store.Team{ID: 10, OrganizationID: 3, Name: "default"}
	fake.place("acme", store.Placement{
		UserID: 7, Login: "acme", OrganizationID: 1, Role: store.RoleAdmin, TeamID: 1, TeamName: "default",
	})
	fake.place("acme/served", store.Placement{
		UserID: 7, Login: "acme", OrganizationID: 3, Role: store.RoleUser, TeamID: 10, TeamName: "default",
	})

	cookie := signedIn(t, identity, "acme", "a long enough secret")

	resolved, err := identity.Resolve(t.Context(), cookie)
	require.NoError(t, err)
	assert.Equal(t, uint(3), resolved.Scope.OrganizationID)
}

// TestLoginRefusesTheRightPasswordForAnotherOrganizationTheSameWay closes the
// oracle: telling a correct password from a wrong one for a login this
// deployment does not serve would make one customer's adapter a way to guess
// another's password.
func TestLoginRefusesTheRightPasswordForAnotherOrganizationTheSameWay(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "globex", "a long enough secret")

	_, rightErr := identity.Login(t.Context(), "globex", "a long enough secret")
	_, wrongErr := identity.Login(t.Context(), "globex", "the wrong secret entirely")

	require.ErrorIs(t, rightErr, service.ErrSignInRefused)
	require.ErrorIs(t, wrongErr, service.ErrSignInRefused)
	assert.Equal(t, rightErr.Error(), wrongErr.Error(),
		"a correct password for another login answers exactly what a wrong one does")

	credential, err := fake.PasswordLogin(t.Context(), "globex")
	require.NoError(t, err)
	assert.Zero(t, credential.Failures,
		"the other login's throttle is not driven by traffic against this adapter")
}

// TestLoginRefusesALoginThatHasNotClaimedItsInvite is the window between
// Percona creating a login and the customer setting a password. The row
// exists and holds no hash, which must not be an open door.
func TestLoginRefusesALoginThatHasNotClaimedItsInvite(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	_, err := fake.EnsureLogin(t.Context(), "acme", identityNow)
	require.NoError(t, err)

	_, err = identity.Login(t.Context(), "acme", "")

	require.ErrorIs(t, err, service.ErrSignInRefused)
}

func TestLoginThrottleGrowsWithConsecutiveFailuresAndResetsOnSuccess(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, slept := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")

	for range 4 {
		_, err := identity.Login(t.Context(), "acme", "wrong password here")
		require.ErrorIs(t, err, service.ErrSignInRefused)
	}

	require.Len(t, *slept, 4)
	for i := 1; i < len(*slept); i++ {
		assert.GreaterOrEqual(t, (*slept)[i], (*slept)[i-1],
			"each failure waits at least as long as the one before")
	}
	assert.Greater(t, (*slept)[3], (*slept)[0], "the throttle grows")

	_, err := identity.Login(t.Context(), "acme", "a long enough secret")
	require.NoError(t, err)

	credential, err := fake.PasswordLogin(t.Context(), "acme")
	require.NoError(t, err)
	assert.Zero(t, credential.Failures, "a success clears the throttle")
}

// TestLoginThrottleIsCapped keeps a slow answer from becoming an outage: an
// attacker must not be able to make every sign in hang by failing a thousand
// times first.
func TestLoginThrottleIsCapped(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, slept := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")

	for range 20 {
		_, err := identity.Login(t.Context(), "acme", "wrong password here")
		require.ErrorIs(t, err, service.ErrSignInRefused)
	}

	for _, waited := range *slept {
		assert.LessOrEqual(t, waited, 2*time.Second)
	}
}

// TestLoginDelaysAnUnknownLoginToo closes the timing side channel. Without a
// wait on the unknown path, the shape of the response would say which login
// ids are real.
func TestLoginDelaysAnUnknownLoginToo(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, slept := newIdentityService(t, fake)

	_, err := identity.Login(t.Context(), "globex", "a long enough secret")

	require.ErrorIs(t, err, service.ErrSignInRefused)
	require.Len(t, *slept, 1)
	assert.Positive(t, (*slept)[0])
}

// TestTheThrottleRampsForUnknownLoginsToo is the other half of that channel.
// A wait that only grew for logins with a row would say, after a few
// attempts, which ids are real: the ramp itself was the signal.
func TestTheThrottleRampsForUnknownLoginsToo(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, slept := newIdentityService(t, fake)

	// Each attempt names a different id that does not exist, which is what an
	// attacker sweeping for real logins does.
	for _, id := range []string{"globex", "initech", "umbrella", "tyrell", "soylent"} {
		_, err := identity.Login(t.Context(), id, "a long enough secret")
		require.ErrorIs(t, err, service.ErrSignInRefused)
	}

	require.Len(t, *slept, 5)
	assert.Greater(t, (*slept)[4], (*slept)[0],
		"a fresh id each time does not reset the throttle")
	for _, waited := range *slept {
		assert.LessOrEqual(t, waited, 2*time.Second)
	}
}

// TestTheThrottleGivesUpWhenTheCallerDoes keeps a disconnected client from
// pinning a goroutine for the length of the wait.
func TestTheThrottleGivesUpWhenTheCallerDoes(t *testing.T) {
	fake := newFakeIdentityStore()
	seedClaimedLogin(t, fake, "acme", "a long enough secret")
	identity := service.NewIdentity(fake,
		service.WithServedOrganization("acme"),
		service.WithPasswordCost(bcrypt.MinCost),
	)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	start := time.Now()
	_, err := identity.Login(ctx, "acme", "the wrong secret entirely")

	require.ErrorIs(t, err, service.ErrSignInRefused)
	assert.Less(t, time.Since(start), time.Second,
		"the wait ends with the request rather than running it out")
}

func TestLoginPurgesSessionsThatAreAlreadyDead(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")

	_, err := identity.Login(t.Context(), "acme", "a long enough secret")
	require.NoError(t, err)

	assert.Equal(t, 1, fake.purged, "the table is tidied by use, not by a timer")
}

func TestLogoutEndsTheSessionAtOnce(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")

	session, err := identity.Login(t.Context(), "acme", "a long enough secret")
	require.NoError(t, err)

	require.NoError(t, identity.Logout(t.Context(), session.Value))

	_, err = identity.Resolve(t.Context(), session.Value)
	require.ErrorIs(t, err, service.ErrNotSignedIn)
	assert.Zero(t, fake.liveSessions())
}

// TestLogoutWithoutASessionIsNotAFailure keeps the button idempotent: a second
// click, or a click after the session already expired, still signs you out.
func TestLogoutWithoutASessionIsNotAFailure(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)

	require.NoError(t, identity.Logout(t.Context(), ""))
	require.NoError(t, identity.Logout(t.Context(), "not-a-cookie"))
}

func TestResolveRefusesACookieThisDeploymentDidNotSign(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")

	session, err := identity.Login(t.Context(), "acme", "a long enough secret")
	require.NoError(t, err)
	id := strings.SplitN(session.Value, ".", 2)[0]

	tests := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"unsigned id", id},
		{"wrong signature", id + ".AAAA"},
		{"junk", "not-a-cookie"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := identity.Resolve(t.Context(), tc.value)

			require.ErrorIs(t, err, service.ErrNotSignedIn)
		})
	}
}

// TestResolveRefusesASessionInAnotherOrganization is the Organization gate. One
// adapter serves one Organization, so a session row in any other cannot be this
// deployment's and must read as signed out.
func TestResolveRefusesASessionInAnotherOrganization(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "globex", "a long enough secret")

	token, hash, err := auth.NewToken()
	require.NoError(t, err)
	require.NoError(t, fake.CreateSession(t.Context(), store.Session{
		Hash:           hash,
		UserID:         fake.placements["globex"].UserID,
		OrganizationID: fake.placements["globex"].OrganizationID,
		CreatedAt:      identityNow,
		LastSeenAt:     identityNow,
		ExpiresAt:      identityNow.Add(time.Hour),
	}))

	_, err = identity.Resolve(t.Context(), auth.SignSessionID(fake.key, token))

	require.ErrorIs(t, err, service.ErrNotSignedIn)
}

func TestClaimSetsThePasswordAndLetsTheLoginSignIn(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	_, err := fake.EnsureLogin(t.Context(), "acme", identityNow)
	require.NoError(t, err)

	token, expires, err := identity.MintInvite(t.Context(), "acme")
	require.NoError(t, err)
	assert.Equal(t, identityNow.Add(48*time.Hour), expires)

	who, err := identity.InviteLogin(t.Context(), token)
	require.NoError(t, err)
	assert.Equal(t, "acme", who)

	claimed, err := identity.Claim(t.Context(), token, "a long enough secret")
	require.NoError(t, err)
	assert.Equal(t, "acme", claimed)

	_, err = identity.Login(t.Context(), "acme", "a long enough secret")
	require.NoError(t, err)
}

func TestClaimRefusesASpentOrExpiredLink(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	_, err := fake.EnsureLogin(t.Context(), "acme", identityNow)
	require.NoError(t, err)

	token, _, err := identity.MintInvite(t.Context(), "acme")
	require.NoError(t, err)
	_, err = identity.Claim(t.Context(), token, "a long enough secret")
	require.NoError(t, err)

	_, err = identity.Claim(t.Context(), token, "another long secret")
	require.ErrorIs(t, err, store.ErrInviteSpent)

	_, err = identity.InviteLogin(t.Context(), token)
	require.ErrorIs(t, err, store.ErrInviteSpent)

	_, err = identity.Claim(t.Context(), "never-minted", "a long enough secret")
	require.ErrorIs(t, err, store.ErrInviteUnknown)
}

func TestClaimRefusesAPasswordTheProductWillNotStore(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	_, err := fake.EnsureLogin(t.Context(), "acme", identityNow)
	require.NoError(t, err)
	token, _, err := identity.MintInvite(t.Context(), "acme")
	require.NoError(t, err)

	_, err = identity.Claim(t.Context(), token, "short")

	require.ErrorIs(t, err, service.ErrPasswordRefused)

	// The link is still claimable, so a rejected password does not cost the
	// customer their invite.
	who, err := identity.InviteLogin(t.Context(), token)
	require.NoError(t, err)
	assert.Equal(t, "acme", who)
}

// TestClaimEndsEveryLiveSession is the reset path: claiming a fresh link for a
// login already signed in elsewhere signs that browser out.
func TestClaimEndsEveryLiveSession(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")

	session, err := identity.Login(t.Context(), "acme", "a long enough secret")
	require.NoError(t, err)

	token, _, err := identity.MintInvite(t.Context(), "acme")
	require.NoError(t, err)
	_, err = identity.Claim(t.Context(), token, "a replacement secret")
	require.NoError(t, err)

	_, err = identity.Resolve(t.Context(), session.Value)
	require.ErrorIs(t, err, service.ErrNotSignedIn)
}

func TestMintInviteRefusesALoginNobodyCreated(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)

	_, _, err := identity.MintInvite(t.Context(), "globex")

	require.ErrorIs(t, err, store.ErrLoginUnknown)
}

func TestCreateLoginValidatesTheIDBeforeWritingARow(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)

	_, err := identity.CreateLogin(t.Context(), "Not A Login")

	require.Error(t, err)
	rows, listErr := fake.Logins(t.Context())
	require.NoError(t, listErr)
	assert.Empty(t, rows, "a refused id leaves no row behind")
}

func TestCreateLoginIsIdempotent(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)

	created, err := identity.CreateLogin(t.Context(), "acme")
	require.NoError(t, err)
	assert.True(t, created)

	created, err = identity.CreateLogin(t.Context(), "acme")
	require.NoError(t, err)
	assert.False(t, created)
}

func TestSetPasswordEndsSessionsAndValidatesThePassword(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")
	session, err := identity.Login(t.Context(), "acme", "a long enough secret")
	require.NoError(t, err)

	require.ErrorIs(t, identity.SetPassword(t.Context(), "acme", "short"), service.ErrPasswordRefused)
	require.NoError(t, identity.SetPassword(t.Context(), "acme", "a replacement secret"))

	_, err = identity.Resolve(t.Context(), session.Value)
	require.ErrorIs(t, err, service.ErrNotSignedIn)

	_, err = identity.Login(t.Context(), "acme", "a replacement secret")
	require.NoError(t, err)
}

func TestLoginsReportsWhetherEachOneHasClaimed(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")
	_, err := fake.EnsureLogin(t.Context(), "globex", identityNow)
	require.NoError(t, err)

	rows, err := identity.Logins(t.Context())
	require.NoError(t, err)

	claimed := map[string]bool{}
	for _, row := range rows {
		claimed[row.Username] = row.Claimed
	}
	assert.True(t, claimed["acme"])
	assert.False(t, claimed["globex"])
}

// TestLoginSurvivesAFailureToRecordTheFailure keeps a database hiccup on the
// counting path from turning a wrong password into a 500. The answer is still
// refused.
func TestLoginSurvivesAFailureToRecordTheFailure(t *testing.T) {
	fake := newFakeIdentityStore()
	fake.failErr = errors.New("the counter is unreachable")
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")

	_, err := identity.Login(t.Context(), "acme", "wrong password here")

	require.ErrorIs(t, err, service.ErrSignInRefused)
}

// newMultiOrganizationIdentity is the service as a deployment that serves every
// login builds it: no pinned name, so any login with a row can sign in.
func newMultiOrganizationIdentity(t *testing.T, fake *fakeIdentityStore) *service.Identity {
	t.Helper()

	return service.NewIdentity(fake,
		service.WithServedOrganization(""),
		service.WithIdentityClock(func() time.Time { return identityNow }),
		service.WithIdentitySleep(func(_ context.Context, _ time.Duration) {}),
		service.WithPasswordCost(bcrypt.MinCost),
	)
}

func TestTwoLoginsHoldSessionsAtOnce(t *testing.T) {
	fake := newFakeIdentityStore()
	identity := newMultiOrganizationIdentity(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")
	seedClaimedLogin(t, fake, "globex", "another long secret")

	acme, err := identity.Login(t.Context(), "acme", "a long enough secret")
	require.NoError(t, err)
	globex, err := identity.Login(t.Context(), "globex", "another long secret")
	require.NoError(t, err)

	// Both cookies stay live at the same time. One login signing in must not
	// end the other's session.
	acmeID, err := identity.Resolve(t.Context(), acme.Value)
	require.NoError(t, err)
	assert.Equal(t, "acme", acmeID.Subject)

	globexID, err := identity.Resolve(t.Context(), globex.Value)
	require.NoError(t, err)
	assert.Equal(t, "globex", globexID.Subject)
}

func TestTwoLoginsEachClaimTheirOwnInviteAndSignIn(t *testing.T) {
	fake := newFakeIdentityStore()
	identity := newMultiOrganizationIdentity(t, fake)

	for _, login := range []string{"acme", "globex"} {
		_, err := fake.EnsureLogin(t.Context(), login, identityNow)
		require.NoError(t, err)
	}

	acmeToken, _, err := identity.MintInvite(t.Context(), "acme")
	require.NoError(t, err)
	globexToken, _, err := identity.MintInvite(t.Context(), "globex")
	require.NoError(t, err)

	// Each link names its own login, and claiming one sets only that login's
	// password. Neither link is a way into the other login.
	claimed, err := identity.Claim(t.Context(), acmeToken, "a long enough secret")
	require.NoError(t, err)
	assert.Equal(t, "acme", claimed)

	claimed, err = identity.Claim(t.Context(), globexToken, "another long secret")
	require.NoError(t, err)
	assert.Equal(t, "globex", claimed)

	acme, err := identity.Login(t.Context(), "acme", "a long enough secret")
	require.NoError(t, err)
	globex, err := identity.Login(t.Context(), "globex", "another long secret")
	require.NoError(t, err)

	acmeID, err := identity.Resolve(t.Context(), acme.Value)
	require.NoError(t, err)
	assert.Equal(t, "acme", acmeID.Subject)

	globexID, err := identity.Resolve(t.Context(), globex.Value)
	require.NoError(t, err)
	assert.Equal(t, "globex", globexID.Subject)

	// The password one login set is not the other's.
	_, err = identity.Login(t.Context(), "globex", "a long enough secret")
	require.Error(t, err)
}

func TestOneSessionKeySignsBothLoginsAndStillKeepsThemApart(t *testing.T) {
	fake := newFakeIdentityStore()
	identity := newMultiOrganizationIdentity(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")
	seedClaimedLogin(t, fake, "globex", "another long secret")

	acme, err := identity.Login(t.Context(), "acme", "a long enough secret")
	require.NoError(t, err)
	globex, err := identity.Login(t.Context(), "globex", "another long secret")
	require.NoError(t, err)

	// One key signs the whole deployment, so a signature check alone cannot say
	// which login a cookie belongs to. The login on the session row is what
	// answers that, and each cookie resolves as its own login and no other.
	acmeID, err := identity.Resolve(t.Context(), acme.Value)
	require.NoError(t, err)
	assert.Equal(t, "acme", acmeID.Subject)

	globexID, err := identity.Resolve(t.Context(), globex.Value)
	require.NoError(t, err)
	assert.Equal(t, "globex", globexID.Subject)

	// Sharing the key does not make a cookie malleable. Changing the signed id
	// breaks the signature, and there is no session row behind the new id
	// either, so the tampered cookie reads as signed out rather than as the
	// other login.
	_, err = identity.Resolve(t.Context(), tamperedCookie(t, acme.Value))
	require.ErrorIs(t, err, service.ErrNotSignedIn)
}

// tamperedCookie flips the first character of the signed id, which is what an
// attacker holding the shared key would try in order to reach another login.
func tamperedCookie(t *testing.T, cookie string) string {
	t.Helper()
	require.NotEmpty(t, cookie)

	swapped := byte('a')
	if cookie[0] == 'a' {
		swapped = 'b'
	}

	return string(swapped) + cookie[1:]
}

func (f *fakeIdentityStore) ServedTeam(_ context.Context, organization string) (store.Team, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if team, ok := f.served[organization]; ok {
		return team, nil
	}
	for _, placement := range f.placements {
		if placement.OrganizationName == organization {
			return store.Team{ID: placement.TeamID, OrganizationID: placement.OrganizationID, Name: placement.TeamName}, nil
		}
	}

	return store.Team{}, store.ErrTeamUnknown
}

// CreateOrganization records what it was asked for and places the person in
// the Organization it names. The limits belong to the store and are tested
// there.
func (f *fakeIdentityStore) CreateOrganization(
	_ context.Context, in store.NewOrganization, _ time.Time,
) (store.Placement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.created = append(f.created, in)

	return store.Placement{
		UserID: 7, OrganizationID: 9, OrganizationName: in.OrganizationName,
		Role: store.RoleAdmin, TeamID: 11, TeamName: "default",
	}, nil
}

func (f *fakeIdentityStore) UserOrganizations(context.Context, uint) ([]store.UserOrganization, error) {
	return nil, nil
}

func (f *fakeIdentityStore) SwitchOrganization(context.Context, string, uint, uint) error {
	return nil
}
