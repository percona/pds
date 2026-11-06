package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// Two Organizations share one control plane. acme has a default Team and a
// payments Team, globex has a default Team and a ledger Team.
//
//	acme    admin   root  (default)   ann (user, default)   bob (user, payments)
//	globex  admin   gus   (default)
//
// Each Team's rows live under their own key, so a Team's environments are the
// only ones its hub knows.
const scopePassword = "a long enough secret"

type scopeWorld struct {
	srv      *api.Server
	rows     *memoryIdentityStore
	identity *service.Identity
	hubs     *hub.Registry
	activity *service.Activity
}

var teamEnvironments = map[uint]string{
	1: "acme-env",
	2: "pay-env",
	3: "globex-env",
	4: "ledger-env",
}

// newScopeWorld serves every Organization when organization is empty, and the
// one of that name otherwise.
func newScopeWorld(t *testing.T, organization string, tweak ...func(*api.Deps, *service.Identity)) *scopeWorld {
	t.Helper()

	rows := newMemoryIdentityStore()
	identity := service.NewIdentity(rows,
		service.WithServedOrganization(organization),
		service.WithPasswordCost(bcrypt.MinCost),
		service.WithIdentitySleep(func(context.Context, time.Duration) {}),
	)

	rows.teams = []store.Team{
		{ID: 1, OrganizationID: 1, Name: "default"},
		{ID: 2, OrganizationID: 1, Name: "payments"},
		{ID: 3, OrganizationID: 2, Name: "default"},
		{ID: 4, OrganizationID: 2, Name: "ledger"},
	}
	logins := []struct {
		name string
		at   store.Placement
	}{
		{"root", store.Placement{UserID: 1, OrganizationID: 1, OrganizationName: "acme", Role: store.RoleAdmin, TeamID: 1, TeamName: "default"}},
		{"ann", store.Placement{UserID: 2, OrganizationID: 1, OrganizationName: "acme", Role: store.RoleUser, TeamID: 1, TeamName: "default"}},
		{"bob", store.Placement{UserID: 3, OrganizationID: 1, OrganizationName: "acme", Role: store.RoleUser, TeamID: 2, TeamName: "payments"}},
		{"gus", store.Placement{UserID: 4, OrganizationID: 2, OrganizationName: "globex", Role: store.RoleAdmin, TeamID: 3, TeamName: "default"}},
	}
	for _, login := range logins {
		_, err := identity.CreateLogin(t.Context(), login.name)
		require.NoError(t, err)
		require.NoError(t, identity.SetPassword(t.Context(), login.name, scopePassword))
		rows.place(login.name, login.at)
	}

	registry := hub.NewRegistry(func(_ context.Context, team uint) (*hub.Service, error) {
		inv := hub.NewInventory(time.Minute)
		inv.Register(teamEnvironments[team], testEverestNamespace)

		return hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
			Namespace:     testEverestNamespace,
			BackupStorage: testBackupStorage,
			Now:           func() time.Time { return time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC) },
			Tokens:        hub.NewTokenStore(nil, time.Hour),
		}), nil
	})

	activity := service.NewActivity(nil, service.ActivityOptions{})
	activity.UseBook(service.NewMemoryBook(nil))
	activity.UseActors(scopeUserNames)

	databases := service.NewDatabase(engine.NewRegistry(), service.WithRecorder(activity), service.WithHubFor(
		func(ctx context.Context) (*hub.Service, error) {
			team, _ := auth.TeamFromContext(ctx)

			return registry.Get(ctx, team)
		}))

	cfg := testConfig()
	deps := api.Deps{
		Config:          cfg,
		Logger:          log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:            api.NewSessionAuthenticator(identity, testHubOperatorToken),
		Identity:        identity,
		Databases:       databases,
		Activity:        activity,
		ActivityDetails: service.NewActivityDetails(activity, databases),
	}
	for _, apply := range tweak {
		apply(&deps, identity)
	}

	return &scopeWorld{srv: api.NewServer(deps), rows: rows, identity: identity, hubs: registry, activity: activity}
}

func (w *scopeWorld) as(t *testing.T, login string) *http.Cookie {
	t.Helper()

	return signIn(t, w.srv, login, scopePassword)
}

func (w *scopeWorld) call(t *testing.T, cookie *http.Cookie, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w.srv.ServeHTTP(rec, withCookie(req, cookie))

	return rec
}

func environmentNames(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var names []string
	items, _ := decodeBody(t, rec)["environments"].([]any)
	for _, item := range items {
		names = append(names, item.(map[string]any)["name"].(string))
	}

	return names
}

func TestAUserOnAnAdminRouteGetsForbiddenNotNotFound(t *testing.T) {
	world := newScopeWorld(t, "")
	ann := world.as(t, "ann")

	for _, route := range []struct{ method, target string }{
		{http.MethodPut, "/api/connections/servicenow"},
		{http.MethodPut, "/api/connections/assistant"},
		{http.MethodDelete, "/api/connections/servicenow"},
	} {
		rec := world.call(t, ann, route.method, route.target, `{}`)

		assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s", route.method, route.target)
		assert.Equal(t, api.CodeForbidden, decodeBody(t, rec)["code"])
	}
}

func TestAnAdminPassesTheAdminGuard(t *testing.T) {
	world := newScopeWorld(t, "")

	rec := world.call(t, world.as(t, "root"), http.MethodPut, "/api/connections/servicenow", `{}`)

	assert.NotEqual(t, http.StatusForbidden, rec.Code)
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
}

func TestAUserKeepsEveryCapabilityInsideTheirTeam(t *testing.T) {
	world := newScopeWorld(t, "")
	ann := world.as(t, "ann")

	rec := world.call(t, ann, http.MethodPost, "/api/connect/tokens", `{"name":"ann-env"}`)

	assert.Equal(t, http.StatusCreated, rec.Code, "a User mints a join token for their Team: %s", rec.Body.String())
}

func TestListsAreScopedToTheCallersOwnTeamByDefault(t *testing.T) {
	world := newScopeWorld(t, "")

	assert.Equal(t, []string{"acme-env"}, environmentNames(t, world.call(t, world.as(t, "ann"), http.MethodGet, "/api/environments", "")))
	assert.Equal(t, []string{"pay-env"}, environmentNames(t, world.call(t, world.as(t, "bob"), http.MethodGet, "/api/environments", "")))
	assert.Equal(t, []string{"acme-env"}, environmentNames(t, world.call(t, world.as(t, "root"), http.MethodGet, "/api/environments", "")),
		"an admin's list starts at their own Team too")
}

func TestAnAdminNamesATeamOrAllTeams(t *testing.T) {
	world := newScopeWorld(t, "")
	root := world.as(t, "root")

	assert.Equal(t, []string{"pay-env"},
		environmentNames(t, world.call(t, root, http.MethodGet, "/api/environments?team=payments", "")))

	rec := world.call(t, root, http.MethodGet, "/api/environments?team=all", "")
	assert.ElementsMatch(t, []string{"acme-env", "pay-env"}, environmentNames(t, rec))
	teams := map[string]string{}
	for _, item := range decodeBody(t, rec)["environments"].([]any) {
		row := item.(map[string]any)
		teams[row["name"].(string)] = row["team"].(string)
	}
	assert.Equal(t, map[string]string{"acme-env": "default", "pay-env": "payments"}, teams,
		"a list across Teams says which Team each row is in")
}

func TestAnotherTeamsOrOrganizationsListAnswersNotFound(t *testing.T) {
	world := newScopeWorld(t, "")

	tests := []struct {
		name, login, target string
		want                int
	}{
		{"a User names another Team", "ann", "/api/environments?team=payments", http.StatusNotFound},
		{"a User names their own Team", "ann", "/api/environments?team=default", http.StatusOK},
		{"a User asks for every Team", "ann", "/api/environments?team=all", http.StatusForbidden},
		{"an admin names another Organization's Team", "root", "/api/environments?team=ledger", http.StatusNotFound},
		{"an admin names a Team that does not exist", "root", "/api/environments?team=nope", http.StatusNotFound},
		{"every Team on a route that is not a list", "root", "/api/connect?team=all", http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := world.call(t, world.as(t, tc.login), http.MethodGet, tc.target, "")

			assert.Equal(t, tc.want, rec.Code, "body: %s", rec.Body.String())
		})
	}
}

func TestAnObjectResolvesItsTeamFromTheObjectForAnAdmin(t *testing.T) {
	world := newScopeWorld(t, "")
	root := world.as(t, "root")

	rec := world.call(t, root, http.MethodDelete, "/api/environments/pay-env", "")
	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())

	assert.Empty(t, environmentNames(t, world.call(t, root, http.MethodGet, "/api/environments?team=payments", "")),
		"the admin acted in the payments Team without naming it")
}

func TestAnotherTeamsObjectAnswersNotFoundToAUser(t *testing.T) {
	world := newScopeWorld(t, "")

	rec := world.call(t, world.as(t, "ann"), http.MethodDelete, "/api/environments/pay-env", "")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, api.CodeNotFound, decodeBody(t, rec)["code"])
	assert.Equal(t, []string{"pay-env"},
		environmentNames(t, world.call(t, world.as(t, "bob"), http.MethodGet, "/api/environments", "")),
		"nothing was disconnected")
}

func TestAnotherOrganizationsObjectAnswersNotFoundToItsAdmin(t *testing.T) {
	world := newScopeWorld(t, "")

	rec := world.call(t, world.as(t, "gus"), http.MethodDelete, "/api/environments/pay-env", "")

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestACreateNamesItsTeamExplicitly(t *testing.T) {
	world := newScopeWorld(t, "")
	root := world.as(t, "root")

	rec := world.call(t, root, http.MethodPost, "/api/connect/tokens?team=payments", `{"name":"pay-new"}`)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	rec = world.call(t, world.as(t, "ann"), http.MethodPost, "/api/connect/tokens?team=payments", `{"name":"pay-new"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, "a User cannot create in another Team")
}

func TestTheOperatorTokenIsAnAdminOfTheSoleOrganization(t *testing.T) {
	world := newScopeWorld(t, "acme")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/connections/servicenow", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+testHubOperatorToken)
	world.srv.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
	assert.NotEqual(t, http.StatusForbidden, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/environments?team=all", nil)
	req.Header.Set("Authorization", "Bearer "+testHubOperatorToken)
	world.srv.ServeHTTP(rec, req)
	assert.ElementsMatch(t, []string{"acme-env", "pay-env"}, environmentNames(t, rec), "the admin sees every Team of the Organization")
}

func TestTheOperatorTokenIsRefusedEverywhereWhenManyAreServed(t *testing.T) {
	world := newScopeWorld(t, "")

	for _, route := range []struct{ method, target string }{
		{http.MethodPut, "/api/connections/servicenow"},
		{http.MethodGet, "/api/environments"},
		{http.MethodGet, "/api/databases?team=all"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(route.method, route.target, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+testHubOperatorToken)
		world.srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s: %s", route.method, route.target, rec.Body.String())
	}
}

func TestARemovedUserLosesAccessOnTheNextRequest(t *testing.T) {
	world := newScopeWorld(t, "")
	ann := world.as(t, "ann")
	require.Equal(t, http.StatusOK, world.call(t, ann, http.MethodGet, "/api/environments", "").Code)

	delete(world.rows.placements, "ann")

	assert.Equal(t, http.StatusUnauthorized, world.call(t, ann, http.MethodGet, "/api/environments", "").Code)
}

func TestTwoTeamsMayConnectEnvironmentsWithTheSameName(t *testing.T) {
	world := newScopeWorld(t, "")
	root := world.as(t, "root")

	require.Equal(t, http.StatusCreated,
		world.call(t, root, http.MethodPost, "/api/connect/tokens", `{"name":"shared"}`).Code)
	require.Equal(t, http.StatusCreated,
		world.call(t, root, http.MethodPost, "/api/connect/tokens?team=payments", `{"name":"shared"}`).Code)

	assert.ElementsMatch(t, []string{"acme-env", "shared"},
		environmentNames(t, world.call(t, root, http.MethodGet, "/api/environments", "")))
	assert.ElementsMatch(t, []string{"pay-env", "shared"},
		environmentNames(t, world.call(t, root, http.MethodGet, "/api/environments?team=payments", "")))
}

func TestMintingAgainInATeamReissuesTheSameEnvironment(t *testing.T) {
	world := newScopeWorld(t, "")
	ann := world.as(t, "ann")

	require.Equal(t, http.StatusCreated, world.call(t, ann, http.MethodPost, "/api/connect/tokens", `{"name":"ann-env"}`).Code)
	require.Equal(t, http.StatusCreated, world.call(t, ann, http.MethodPost, "/api/connect/tokens", `{"name":"ann-env"}`).Code,
		"running the command again is how a connection is repaired")

	assert.ElementsMatch(t, []string{"acme-env", "ann-env"},
		environmentNames(t, world.call(t, ann, http.MethodGet, "/api/environments", "")))
}
