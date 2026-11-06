package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/mysql"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// loginName is the login every test here signs in as, and the login the
// test adapter is configured to serve.
const (
	loginName     = "acme"
	loginPassword = "a long enough secret"
)

// memoryInvite is one password invite link, by the login it is for.
type memoryInvite struct {
	Username  string
	ExpiresAt time.Time
	ClaimedAt *time.Time
}

// memoryIdentityStore is the login rows in a map. The rules under test here
// are the HTTP ones, so this only has to remember what it was told.
type memoryIdentityStore struct {
	mu          sync.Mutex
	credentials map[string]store.PasswordLogin
	invites     map[string]memoryInvite
	sessions    map[string]store.Session
	placements  map[string]store.Placement
	oidcUsers   map[string]store.Placement
	// memberships are placements beyond the one an identity signs in to.
	memberships []store.Placement
	teams       []store.Team
}

func newMemoryIdentityStore() *memoryIdentityStore {
	return &memoryIdentityStore{
		credentials: map[string]store.PasswordLogin{},
		invites:     map[string]memoryInvite{},
		sessions:    map[string]store.Session{},
		placements:  map[string]store.Placement{},
		oidcUsers:   map[string]store.Placement{},
	}
}

// place sets where a login sits, replacing the default every new login gets.
func (m *memoryIdentityStore) place(login string, placement store.Placement) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.placements[login] = placement
}

func (m *memoryIdentityStore) Placement(_ context.Context, login string) (store.Placement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	placement, ok := m.placements[login]
	if !ok {
		return store.Placement{}, store.ErrLoginUnknown
	}

	return placement, nil
}

// addOIDCUser records an identity an issuer vouches for, and where it sits.
func (m *memoryIdentityStore) addOIDCUser(issuer, subject string, placement store.Placement) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.oidcUsers[issuer+"\x00"+subject] = placement
}

// addMembership puts a User already on file into another Organization.
func (m *memoryIdentityStore) addMembership(placement store.Placement) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.memberships = append(m.memberships, placement)
}

// allPlacementsLocked is every placement an OIDC User holds.
func (m *memoryIdentityStore) allPlacementsLocked() []store.Placement {
	out := append([]store.Placement(nil), m.memberships...)
	for _, placement := range m.oidcUsers {
		out = append(out, placement)
	}

	return out
}

// loginPlacementsLocked is every placement a password login holds.
func (m *memoryIdentityStore) loginPlacementsLocked() []store.Placement {
	out := make([]store.Placement, 0, len(m.placements))
	for _, placement := range m.placements {
		out = append(out, placement)
	}

	return out
}

func (m *memoryIdentityStore) UserPlacement(_ context.Context, issuer, subject string) (store.Placement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	placement, ok := m.oidcUsers[issuer+"\x00"+subject]
	if !ok {
		return store.Placement{}, store.ErrNoMembership
	}

	return placement, nil
}

func (m *memoryIdentityStore) PlacementOf(_ context.Context, userID, organizationID uint) (store.Placement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, placement := range append(m.allPlacementsLocked(), m.loginPlacementsLocked()...) {
		if placement.UserID == userID && placement.OrganizationID == organizationID {
			return placement, nil
		}
	}

	return store.Placement{}, store.ErrNoMembership
}

func (m *memoryIdentityStore) Teams(_ context.Context, organizationID uint) ([]store.Team, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []store.Team
	for _, team := range m.teams {
		if team.OrganizationID == organizationID {
			out = append(out, team)
		}
	}

	return out, nil
}

func (m *memoryIdentityStore) Team(ctx context.Context, organizationID uint, name string) (store.Team, error) {
	teams, _ := m.Teams(ctx, organizationID)
	for _, team := range teams {
		if team.Name == name {
			return team, nil
		}
	}

	return store.Team{}, store.ErrTeamUnknown
}

func (m *memoryIdentityStore) EnsureLogin(_ context.Context, login string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.credentials[login]; ok {
		return false, nil
	}
	m.credentials[login] = store.PasswordLogin{Username: login, CreatedAt: now}
	n := uint(len(m.placements) + 1)
	m.placements[login] = store.Placement{
		UserID: n, Login: login, OrganizationName: login, OrganizationID: n, Role: store.RoleAdmin, TeamID: n, TeamName: "default",
	}

	return true, nil
}

func (m *memoryIdentityStore) Logins(context.Context) ([]store.PasswordLogin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]store.PasswordLogin, 0, len(m.credentials))
	for _, row := range m.credentials {
		out = append(out, row)
	}

	return out, nil
}

func (m *memoryIdentityStore) PasswordLogin(_ context.Context, login string) (store.PasswordLogin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, ok := m.credentials[login]
	if !ok {
		return store.PasswordLogin{}, store.ErrLoginUnknown
	}

	return row, nil
}

func (m *memoryIdentityStore) SetPassword(_ context.Context, login, hash string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, ok := m.credentials[login]
	if !ok {
		return store.ErrLoginUnknown
	}
	row.PasswordHash = hash
	row.Failures = 0
	m.credentials[login] = row
	m.dropSessionsLocked(login)

	return nil
}

func (m *memoryIdentityStore) RecordFailure(_ context.Context, login string, _ time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, ok := m.credentials[login]
	if !ok {
		return 0, store.ErrLoginUnknown
	}
	row.Failures++
	m.credentials[login] = row

	return row.Failures, nil
}

func (m *memoryIdentityStore) ClearFailures(_ context.Context, login string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, ok := m.credentials[login]
	if !ok {
		return store.ErrLoginUnknown
	}
	row.Failures = 0
	m.credentials[login] = row

	return nil
}

func (m *memoryIdentityStore) CreateInvite(
	_ context.Context,
	login, hash string,
	expires, now time.Time,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.invites[hash] = memoryInvite{Username: login, ExpiresAt: expires}

	return nil
}

func (m *memoryIdentityStore) Invite(_ context.Context, hash string, now time.Time) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.claimableLocked(hash, now)
}

func (m *memoryIdentityStore) ClaimInvite(
	_ context.Context,
	hash, passwordHash string,
	now time.Time,
) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	login, err := m.claimableLocked(hash, now)
	if err != nil {
		return "", err
	}

	row := m.invites[hash]
	spent := now
	row.ClaimedAt = &spent
	m.invites[hash] = row

	credential := m.credentials[login]
	credential.Username = login
	credential.PasswordHash = passwordHash
	m.credentials[login] = credential
	m.dropSessionsLocked(login)

	return login, nil
}

func (m *memoryIdentityStore) claimableLocked(hash string, now time.Time) (string, error) {
	row, ok := m.invites[hash]
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

func (m *memoryIdentityStore) CreateSession(_ context.Context, session store.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sessions[session.Hash] = session

	return nil
}

func (m *memoryIdentityStore) Session(
	_ context.Context,
	hash string,
	now time.Time,
	idle time.Duration,
) (store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, ok := m.sessions[hash]
	if !ok {
		return store.Session{}, store.ErrSessionUnknown
	}
	if !now.Before(row.ExpiresAt) || (idle > 0 && !now.Before(row.LastSeenAt.Add(idle))) {
		delete(m.sessions, hash)

		return store.Session{}, store.ErrSessionUnknown
	}

	return row, nil
}

func (m *memoryIdentityStore) DeleteSession(_ context.Context, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.sessions, hash)

	return nil
}

func (m *memoryIdentityStore) DeleteSessionsFor(_ context.Context, login string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.dropSessionsLocked(login)

	return nil
}

func (m *memoryIdentityStore) PurgeExpiredSessions(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (m *memoryIdentityStore) SessionKey(context.Context, time.Time) ([]byte, error) {
	return []byte("0123456789abcdef0123456789abcdef"), nil
}

func (m *memoryIdentityStore) dropSessionsLocked(login string) {
	for hash, row := range m.sessions {
		if row.UserID == m.placements[login].UserID {
			delete(m.sessions, hash)
		}
	}
}

// newLoginServer builds the router with login required, the way a deployment
// serves it. The login is already claimed, which is the state a customer is
// in after their first sign in.
func newLoginServer(t *testing.T, tweak ...func(*api.Deps)) (*api.Server, *service.Identity, *memoryIdentityStore) {
	t.Helper()

	rows := newMemoryIdentityStore()
	identity := service.NewIdentity(rows,
		service.WithServedOrganization(loginName),
		service.WithPasswordCost(bcrypt.MinCost),
		service.WithIdentitySleep(func(context.Context, time.Duration) {}),
	)

	_, err := identity.CreateLogin(t.Context(), loginName)
	require.NoError(t, err)
	require.NoError(t, identity.SetPassword(t.Context(), loginName, loginPassword))

	registry := engine.NewRegistry()
	registry.Register(mysql.New(newFakeEverest(t).client(t),
		mysql.WithNamespace(testEverestNamespace),
		mysql.WithBackupStorage(testBackupStorage),
	))

	cfg := testConfig()
	cfg.Organization = loginName
	deps := api.Deps{
		Config:    cfg,
		Logger:    log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:      api.NewSessionAuthenticator(identity, ""),
		Identity:  identity,
		Databases: service.NewDatabase(registry),
	}
	for _, apply := range tweak {
		apply(&deps)
	}

	return api.NewServer(deps), identity, rows
}

// signIn posts the login form and returns the session cookie.
func signIn(t *testing.T, srv http.Handler, login, password string) *http.Cookie {
	t.Helper()

	rec := do(t, srv, http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"`+login+`","password":"`+password+`"}`))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			return cookie
		}
	}
	t.Fatal("the sign in response carried no session cookie")

	return nil
}

// identityIn is a caller acting in the Team.
func identityIn(team uint) auth.Identity {
	return auth.Identity{Scope: auth.Scope{Team: auth.Team{ID: team}}}
}

// fixedTeam is the Team a call with no request Team is recorded under.
func fixedTeam(team uint) service.TeamFallback {
	return func(context.Context) uint { return team }
}

func withCookie(req *http.Request, cookie *http.Cookie) *http.Request {
	if cookie != nil {
		req.AddCookie(cookie)
	}

	return req
}

func TestLoginSetsASessionCookieThatOpensTheAPI(t *testing.T) {
	srv, _, _ := newLoginServer(t)

	rec := do(t, srv, http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"`+loginName+`","password":"`+loginPassword+`"}`))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, loginName, decodeBody(t, rec)["username"])

	// The password must not come back out, in the body or in the cookie.
	assert.NotContains(t, rec.Body.String(), loginPassword)
	assert.NotContains(t, rec.Header().Get("Set-Cookie"), loginPassword)

	var session *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			session = cookie
		}
	}
	require.NotNil(t, session)
	assert.True(t, session.HttpOnly, "a script must not be able to read the session")
	assert.Equal(t, http.SameSiteLaxMode, session.SameSite,
		"lax is what keeps a cross site post from carrying the session")
	assert.Equal(t, "/", session.Path)
	assert.NotEmpty(t, session.Value)

	guarded := httptest.NewRecorder()
	srv.ServeHTTP(guarded, withCookie(httptest.NewRequest(http.MethodGet, "/api/features", nil), session))
	assert.Equal(t, http.StatusOK, guarded.Code)
}

func TestTheAPIIsClosedWithoutASession(t *testing.T) {
	srv, _, _ := newLoginServer(t)

	tests := []struct {
		name   string
		method string
		target string
	}{
		{"features", http.MethodGet, "/api/features"},
		{"databases", http.MethodGet, "/api/databases"},
		{"environments", http.MethodGet, "/api/environments"},
		{"connections", http.MethodGet, "/api/connections"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
		})
	}
}

func TestLoginRefusesTheWrongPasswordWithoutACookie(t *testing.T) {
	srv, _, _ := newLoginServer(t)

	rec := do(t, srv, http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"`+loginName+`","password":"the wrong secret"}`))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, rec.Result().Cookies())
}

func TestLoginValidatesItsBody(t *testing.T) {
	srv, _, _ := newLoginServer(t)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"no login", `{"password":"a long enough secret"}`, http.StatusBadRequest},
		{"no password", `{"username":"acme"}`, http.StatusBadRequest},
		{"not json", `nonsense`, http.StatusBadRequest},
		{"wrong types", `{"username":1,"password":true}`, http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, srv, http.MethodPost, "/api/auth/login", strings.NewReader(tc.body))

			assert.Equal(t, tc.want, rec.Code, "body: %s", rec.Body.String())
		})
	}
}

func TestALoginThatDeclaresAClientSignsInAsAPersonAndRecordsNone(t *testing.T) {
	srv, _, rows := newLoginServer(t)

	rec := do(t, srv, http.MethodPost, "/api/auth/login", strings.NewReader(
		`{"username":"`+loginName+`","password":"`+loginPassword+`","client":"mcp","clientVersion":"1.2.3","agent":"Claude Code"}`))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Len(t, rows.sessions, 1)

	var session *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			session = cookie
		}
	}
	require.NotNil(t, session)
	got := httptest.NewRecorder()
	srv.ServeHTTP(got, withCookie(httptest.NewRequest(http.MethodGet, "/api/auth/session", nil), session))
	require.Equal(t, http.StatusOK, got.Code)

	body := decodeBody(t, got)
	assert.NotContains(t, body, "client")
	assert.NotContains(t, body, "clientVersion")
}

func TestSessionReportsTheControlPlaneVersion(t *testing.T) {
	srv, _, _ := newLoginServer(t, func(d *api.Deps) { d.Version = "1.4.0-rc.1" })
	session := signIn(t, srv, loginName, loginPassword)

	got := httptest.NewRecorder()
	srv.ServeHTTP(got, withCookie(httptest.NewRequest(http.MethodGet, "/api/auth/session", nil), session))
	require.Equal(t, http.StatusOK, got.Code)

	assert.Equal(t, "1.4.0-rc.1", decodeBody(t, got)["controlPlaneVersion"])
}

func TestTheVersionIsOnlyForASignedInCaller(t *testing.T) {
	srv, _, _ := newLoginServer(t, func(d *api.Deps) { d.Version = "1.4.0" })

	rec := do(t, srv, http.MethodGet, "/api/auth/session", nil)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.NotContains(t, rec.Body.String(), "1.4.0")
	assert.NotContains(t, do(t, srv, http.MethodGet, "/healthz", nil).Body.String(), "1.4.0")
}

func TestLogoutClosesTheAPIAgain(t *testing.T) {
	srv, _, _ := newLoginServer(t)
	session := signIn(t, srv, loginName, loginPassword)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil), session))

	require.Equal(t, http.StatusNoContent, rec.Code)
	cleared := rec.Result().Cookies()
	require.Len(t, cleared, 1)
	assert.Equal(t, auth.SessionCookieName, cleared[0].Name)
	assert.Empty(t, cleared[0].Value, "the browser is told to drop the cookie")
	assert.Negative(t, cleared[0].MaxAge)

	after := httptest.NewRecorder()
	srv.ServeHTTP(after, withCookie(httptest.NewRequest(http.MethodGet, "/api/features", nil), session))
	assert.Equal(t, http.StatusUnauthorized, after.Code,
		"the cookie stops working the moment the row goes")
}

func TestLogoutWithoutASessionStillClearsTheCookie(t *testing.T) {
	srv, _, _ := newLoginServer(t)

	rec := do(t, srv, http.MethodPost, "/api/auth/logout", nil)

	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestSessionRouteNamesTheSignedInLogin(t *testing.T) {
	srv, _, _ := newLoginServer(t)
	session := signIn(t, srv, loginName, loginPassword)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/api/auth/session", nil), session))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, loginName, decodeBody(t, rec)["username"])
}

func TestSessionRouteIsUnauthorizedWithoutACookie(t *testing.T) {
	srv, _, _ := newLoginServer(t)

	rec := do(t, srv, http.MethodGet, "/api/auth/session", nil)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestInvitePreviewNamesTheLoginWithoutSpendingTheLink(t *testing.T) {
	srv, identity, _ := newLoginServer(t)
	token, _, err := identity.MintInvite(t.Context(), loginName)
	require.NoError(t, err)

	rec := do(t, srv, http.MethodGet, "/api/auth/invite?token="+token, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, loginName, decodeBody(t, rec)["username"])

	again := do(t, srv, http.MethodGet, "/api/auth/invite?token="+token, nil)
	assert.Equal(t, http.StatusOK, again.Code, "reading the page twice is allowed")
}

func TestClaimSetsThePasswordAndThenRefusesAReplay(t *testing.T) {
	srv, identity, _ := newLoginServer(t)
	token, _, err := identity.MintInvite(t.Context(), loginName)
	require.NoError(t, err)

	rec := do(t, srv, http.MethodPost, "/api/auth/invite",
		strings.NewReader(`{"token":"`+token+`","password":"a replacement secret"}`))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, loginName, decodeBody(t, rec)["username"])

	replay := do(t, srv, http.MethodPost, "/api/auth/invite",
		strings.NewReader(`{"token":"`+token+`","password":"a third secret here"}`))
	assert.Equal(t, http.StatusConflict, replay.Code, "a spent link is refused")

	// The password the claim set is the one that now signs in.
	signIn(t, srv, loginName, "a replacement secret")
}

// TestClaimEndsTheBrowserSession is the leftover-cookie case: the customer
// opened an invite in a browser that was already signed in as somebody else.
// Claiming sets the new login's password and must not leave that other
// session in the cookie, or the sign in page sends them straight back in as
// the old login.
func TestClaimEndsTheBrowserSession(t *testing.T) {
	srv, identity, _ := newLoginServer(t)
	_, err := identity.CreateLogin(t.Context(), "globex")
	require.NoError(t, err)
	token, _, err := identity.MintInvite(t.Context(), "globex")
	require.NoError(t, err)

	session := signIn(t, srv, loginName, loginPassword)

	rec := httptest.NewRecorder()
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/auth/invite",
		strings.NewReader(`{"token":"`+token+`","password":"a long enough secret"}`)), session)
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "globex", decodeBody(t, rec)["username"])

	var cleared *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			cleared = cookie
		}
	}
	require.NotNil(t, cleared, "claim has to tell the browser to drop the leftover session")
	assert.Empty(t, cleared.Value)
	assert.Negative(t, cleared.MaxAge)

	after := httptest.NewRecorder()
	srv.ServeHTTP(after, withCookie(httptest.NewRequest(http.MethodGet, "/api/auth/session", nil), session))
	assert.Equal(t, http.StatusUnauthorized, after.Code,
		"the leftover session must not still name the old login")
}

func TestInviteRoutesRefuseALinkThatIsNotClaimable(t *testing.T) {
	srv, identity, rows := newLoginServer(t)

	spent, _, err := identity.MintInvite(t.Context(), loginName)
	require.NoError(t, err)
	_, err = identity.Claim(t.Context(), spent, "a replacement secret")
	require.NoError(t, err)

	expired, hash, err := auth.NewToken()
	require.NoError(t, err)
	require.NoError(t, rows.CreateInvite(t.Context(), loginName, hash,
		time.Now().Add(-time.Hour), time.Now().Add(-2*time.Hour)))

	tests := []struct {
		name  string
		token string
		want  int
	}{
		{"never minted", "nobody-minted-this", http.StatusNotFound},
		{"already claimed", spent, http.StatusConflict},
		{"expired", expired, http.StatusConflict},
		{"empty", "", http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			preview := do(t, srv, http.MethodGet, "/api/auth/invite?token="+tc.token, nil)
			assert.Equal(t, tc.want, preview.Code, "preview body: %s", preview.Body.String())

			claim := do(t, srv, http.MethodPost, "/api/auth/invite",
				strings.NewReader(`{"token":"`+tc.token+`","password":"a long enough secret"}`))
			assert.Equal(t, tc.want, claim.Code, "claim body: %s", claim.Body.String())
		})
	}
}

func TestClaimRefusesAPasswordThatIsTooShort(t *testing.T) {
	srv, identity, _ := newLoginServer(t)
	token, _, err := identity.MintInvite(t.Context(), loginName)
	require.NoError(t, err)

	rec := do(t, srv, http.MethodPost, "/api/auth/invite",
		strings.NewReader(`{"token":"`+token+`","password":"short"}`))

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// The invite survives a refused password, so the customer keeps their link.
	preview := do(t, srv, http.MethodGet, "/api/auth/invite?token="+token, nil)
	assert.Equal(t, http.StatusOK, preview.Code)
}

// TestNothingInTheProductCreatesALogin is the acceptance criterion as a test.
// Percona onboards a login from the command line, so no request may.
func TestNothingInTheProductCreatesALogin(t *testing.T) {
	srv, _, _ := newLoginServer(t)
	session := signIn(t, srv, loginName, loginPassword)

	tests := []struct {
		method string
		target string
	}{
		{http.MethodPost, "/api/logins"},
		{http.MethodPut, "/api/logins/globex"},
		{http.MethodPost, "/api/auth/register"},
		{http.MethodPost, "/api/auth/signup"},
		{http.MethodPost, "/api/auth/logins"},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, withCookie(httptest.NewRequest(tc.method, tc.target, nil), session))

			assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
		})
	}
}

// TestLoginRefusesAnOrganizationThisDeploymentDoesNotServe is the same gate at the
// door rather than on the next request. Without it the browser is handed a
// cookie that fails on its first use, which reads as a login page that will
// not stay away.
func TestLoginRefusesAnOrganizationThisDeploymentDoesNotServe(t *testing.T) {
	srv, identity, _ := newLoginServer(t)

	// A real login with a real password, in an Organization this adapter does not serve.
	_, err := identity.CreateLogin(t.Context(), "globex")
	require.NoError(t, err)
	require.NoError(t, identity.SetPassword(t.Context(), "globex", loginPassword))

	rec := do(t, srv, http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"globex","password":"`+loginPassword+`"}`))

	assert.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
	assert.Empty(t, rec.Result().Cookies(), "no cookie is handed out that cannot be used")
}

// TestAnIdledOutSessionReadsAsSignedOut covers the window the store tests
// drive with a clock, at the level a browser actually meets it.
func TestAnIdledOutSessionReadsAsSignedOut(t *testing.T) {
	rows := newMemoryIdentityStore()
	now := time.Now()
	identity := service.NewIdentity(rows,
		service.WithServedOrganization(loginName),
		service.WithPasswordCost(bcrypt.MinCost),
		service.WithIdentitySleep(func(context.Context, time.Duration) {}),
		service.WithSessionIdle(time.Hour),
		service.WithIdentityClock(func() time.Time { return now }),
	)
	_, err := identity.CreateLogin(t.Context(), loginName)
	require.NoError(t, err)
	require.NoError(t, identity.SetPassword(t.Context(), loginName, loginPassword))

	cfg := testConfig()
	cfg.Organization = loginName
	srv := api.NewServer(api.Deps{
		Config:   cfg,
		Logger:   log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:     api.NewSessionAuthenticator(identity, ""),
		Identity: identity,
	})

	session := signIn(t, srv, loginName, loginPassword)

	live := httptest.NewRecorder()
	srv.ServeHTTP(live, withCookie(httptest.NewRequest(http.MethodGet, "/api/features", nil), session))
	require.Equal(t, http.StatusOK, live.Code)

	// Two hours on, with an idle window of one.
	now = now.Add(2 * time.Hour)

	idled := httptest.NewRecorder()
	srv.ServeHTTP(idled, withCookie(httptest.NewRequest(http.MethodGet, "/api/features", nil), session))
	assert.Equal(t, http.StatusUnauthorized, idled.Code, "an unused session stops being accepted")
}

// TestASessionInAnotherOrganizationIsSignedOut is the Organization gate. One
// adapter serves one Organization, so a row belonging to anybody else cannot
// open this API.
func TestASessionInAnotherOrganizationIsSignedOut(t *testing.T) {
	srv, _, rows := newLoginServer(t)
	_, err := rows.EnsureLogin(t.Context(), "globex", time.Now())
	require.NoError(t, err)
	globex, err := rows.Placement(t.Context(), "globex")
	require.NoError(t, err)

	token, hash, err := auth.NewToken()
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, rows.CreateSession(t.Context(), store.Session{
		Hash:           hash,
		UserID:         globex.UserID,
		OrganizationID: globex.OrganizationID,
		CreatedAt:      now,
		LastSeenAt:     now,
		ExpiresAt:      now.Add(time.Hour),
	}))
	key, err := rows.SessionKey(t.Context(), now)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/features", nil)
	req.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: auth.SignSessionID(key, token),
	})
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestAnIDBelongingToAnotherTeamIsNotFound is the acceptance criterion about
// 404 rather than 403. Every read is scoped to the signed in team, so an id
// outside that scope is simply not there. A 403 would confirm it exists.
func TestAnIDBelongingToAnotherTeamIsNotFound(t *testing.T) {
	srv, _ := newLoginHubServer(t)
	session := signIn(t, srv, loginName, loginPassword)
	beat := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat",
		strings.NewReader(`{"environment":"spoke-a","namespace":"pds"}`))
	beat.Header.Set("Authorization", "Bearer agent")
	beatRec := httptest.NewRecorder()
	srv.ServeHTTP(beatRec, beat)
	require.Equal(t, http.StatusOK, beatRec.Code, beatRec.Body.String())

	tests := []string{
		"/api/databases/pds.globex-orders",
		"/api/databases/spoke-a/pds.globex-orders",
		"/api/databases/spoke-a/pds.globex-orders/backups",
	}

	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, target, nil), session))

			assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
			assert.NotEqual(t, http.StatusForbidden, rec.Code)
		})
	}
}

// TestAgentsCannotEnrolWithABrowserSession keeps the two credentials apart. A
// spoke joins with a join token, and a stolen browser cookie must not be able
// to stand one up.
func TestAgentsCannotEnrolWithABrowserSession(t *testing.T) {
	srv, _, _ := newLoginServer(t)
	session := signIn(t, srv, loginName, loginPassword)

	rec := httptest.NewRecorder()
	req := withCookie(httptest.NewRequest(http.MethodPost,
		"/api/internal/environments/spoke-a/heartbeat", strings.NewReader(`{}`)), session)
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"a session cookie is not an agent credential")
}

// TestASessionCannotEnrolASpoke is the same rule on the route that actually
// stands an environment up. Enrolling takes a join token, and a browser cookie is
// not one.
func TestASessionCannotEnrolASpoke(t *testing.T) {
	srv, _ := newLoginHubServer(t)
	session := signIn(t, srv, loginName, loginPassword)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, withCookie(
		httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil), session))

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"enrolling takes a join token, and a browser cookie is not one")
}

// newLoginHubServer builds a hub that requires login, which is the shape a
// customer facing deployment actually runs in.
func newLoginHubServer(t *testing.T) (*api.Server, *service.Identity) {
	t.Helper()

	rows := newMemoryIdentityStore()
	identity := service.NewIdentity(rows,
		service.WithServedOrganization(loginName),
		service.WithPasswordCost(bcrypt.MinCost),
		service.WithIdentitySleep(func(context.Context, time.Duration) {}),
	)
	_, err := identity.CreateLogin(t.Context(), loginName)
	require.NoError(t, err)
	require.NoError(t, identity.SetPassword(t.Context(), loginName, loginPassword))

	cfg := config.Config{
		Addr:                 ":8080",
		Env:                  config.EnvDev,
		EverestNamespace:     testEverestNamespace,
		Organization:         loginName,
		HubGitDir:            t.TempDir(),
		HubAgentToken:        "agent",
		HubOperatorToken:     testHubOperatorToken,
		HubAgentEnvironments: []string{"spoke-a"},
		HubPublicURL:         "https://hub.example",
		HubGitURL:            "https://github.com/example/pds.git",
	}
	require.True(t, cfg.ServesHubAPI(), "these tests only say anything on a hub")

	hubStore := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", testEverestNamespace)
	hubSvc := hub.NewService(hubStore, inv, hub.Options{
		Namespace:     testEverestNamespace,
		BackupStorage: testBackupStorage,
		Now:           func() time.Time { return time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC) },
		Tokens:        hub.NewTokenStore(nil, cfg.JoinTokenTTL),
	})

	return api.NewServer(api.Deps{
		Config:    cfg,
		Logger:    log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:      api.NewSessionAuthenticator(identity, testHubOperatorToken),
		Identity:  identity,
		Databases: service.NewDatabase(engine.NewRegistry(), service.WithHub(hubSvc)),
	}), identity
}

// TestASignedInLoginMayWriteWithoutTheOperatorToken is the other half of the
// same split: the operator token exists for callers with no session, so a
// signed in customer must not need one as well.
func TestASignedInLoginMayWriteWithoutTheOperatorToken(t *testing.T) {
	srv, _ := newLoginHubServer(t)
	session := signIn(t, srv, loginName, loginPassword)

	rec := httptest.NewRecorder()
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/connect/tokens",
		strings.NewReader(`{"environment":"spoke-b"}`)), session)
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusUnauthorized, rec.Code,
		"body: %s", rec.Body.String())
}

// TestTheOperatorTokenIsStillAWayInWithoutASession keeps scripts and curl
// working. The session middleware runs first, so a token bearing caller has to
// be accepted there: refusing it at the door and accepting it deeper down
// would mean it was never accepted at all.
func TestTheOperatorTokenIsStillAWayInWithoutASession(t *testing.T) {
	srv, _ := newLoginHubServer(t)

	tests := []struct {
		name       string
		authHeader string
		want       int
	}{
		{"no credential at all", "", http.StatusUnauthorized},
		{"a token that is not this hub's", "Bearer not-the-token", http.StatusUnauthorized},
		{"not even a bearer scheme", "Basic " + testHubOperatorToken, http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/connect/tokens",
				strings.NewReader(`{"environment":"spoke-b"}`))
			req.Header.Set("Content-Type", "application/json")
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			srv.ServeHTTP(rec, req)

			assert.Equal(t, tc.want, rec.Code, "body: %s", rec.Body.String())
		})
	}

	// The real token gets through, which is the half that would otherwise have
	// been silently broken.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/connect/tokens",
		strings.NewReader(`{"environment":"spoke-b"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testHubOperatorToken)
	srv.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
}

// TestAReadIsAlsoClosedToACallerWithNoCredential states the shape of the
// change: before login, reads were open to anything that could reach the
// adapter. They are not any more.
func TestAReadIsAlsoClosedToACallerWithNoCredential(t *testing.T) {
	srv, _ := newLoginHubServer(t)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/environments", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	withToken := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/environments", nil)
	req.Header.Set("Authorization", "Bearer "+testHubOperatorToken)
	srv.ServeHTTP(withToken, req)
	assert.Equal(t, http.StatusOK, withToken.Code, "body: %s", withToken.Body.String())
}

// TestLoginIsReachableWithoutASession states the obvious in a test, because it
// is the one route that must never end up behind the guard it opens.
func TestLoginIsReachableWithoutASession(t *testing.T) {
	srv, _, _ := newLoginServer(t)

	tests := []struct {
		method string
		target string
	}{
		{http.MethodPost, "/api/auth/login"},
		{http.MethodPost, "/api/auth/logout"},
		{http.MethodGet, "/api/auth/invite"},
		{http.MethodPost, "/api/auth/invite"},
	}

	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			rec := do(t, srv, tc.method, tc.target, strings.NewReader(`{}`))

			assert.NotEqual(t, http.StatusUnauthorized, rec.Code,
				"this route answers for itself rather than asking for a session")
		})
	}
}

// TestAnUnwiredAdapterRefusesEverything is the fail closed default. A zero
// Deps used to mean the pass-through stub, so a wiring mistake served every
// route to anybody; now it answers 401 to its own UI, which somebody notices.
func TestAnUnwiredAdapterRefusesEverything(t *testing.T) {
	srv := api.NewServer(api.Deps{
		Config: testConfig(),
		Logger: log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
	})

	for _, target := range []string{"/api/features", "/api/databases", "/api/environments"} {
		t.Run(target, func(t *testing.T) {
			rec := do(t, srv, http.MethodGet, target, nil)

			assert.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
		})
	}

	// The probes stay open, because Kubernetes has no credential to offer and
	// an unwired adapter still has to be able to report that it is unwired.
	for _, target := range []string{"/healthz", "/readyz"} {
		t.Run(target, func(t *testing.T) {
			rec := do(t, srv, http.MethodGet, target, nil)

			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

// TestProbesStayOpen keeps Kubernetes able to tell a starting pod from a dead
// one. A probe carries no cookie and never will.
func TestProbesStayOpen(t *testing.T) {
	srv, _, _ := newLoginServer(t)

	for _, target := range []string{"/healthz", "/readyz"} {
		t.Run(target, func(t *testing.T) {
			rec := do(t, srv, http.MethodGet, target, nil)

			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func (m *memoryIdentityStore) ServedTeam(_ context.Context, organization string) (store.Team, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, placement := range append(m.allPlacementsLocked(), m.loginPlacementsLocked()...) {
		if placement.OrganizationName == organization {
			return store.Team{ID: placement.TeamID, OrganizationID: placement.OrganizationID, Name: placement.TeamName}, nil
		}
	}

	return store.Team{}, store.ErrTeamUnknown
}

// The Organization model's creation and switching run against a real database
// in signup_test.go. The pinned deployments this store serves have neither.
func (m *memoryIdentityStore) CreateOrganization(
	context.Context, store.NewOrganization, time.Time,
) (store.Placement, error) {
	return store.Placement{}, store.ErrOrganizationCeiling
}

func (m *memoryIdentityStore) UserOrganizations(_ context.Context, userID uint) ([]store.UserOrganization, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []store.UserOrganization
	for _, placement := range append(m.allPlacementsLocked(), m.loginPlacementsLocked()...) {
		if placement.UserID == userID {
			out = append(out, store.UserOrganization{
				ID: placement.OrganizationID, Name: placement.OrganizationName, Role: placement.Role,
			})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })

	return out, nil
}

func (m *memoryIdentityStore) SwitchOrganization(context.Context, string, uint, uint) error {
	return store.ErrNoMembership
}

func TestLoginAnswersWithTheUsernameOnly(t *testing.T) {
	srv, _, _ := newLoginServer(t)

	rec := do(t, srv, http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"`+loginName+`","password":"`+loginPassword+`"}`))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, loginName, body["username"])
	assert.NotContains(t, body, "login")
}

func TestSessionNamesTheUsernameOnly(t *testing.T) {
	srv, _, _ := newLoginServer(t)
	cookie := signIn(t, srv, loginName, loginPassword)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/api/auth/session", nil), cookie))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, loginName, body["username"])
	assert.NotContains(t, body, "login")
}
