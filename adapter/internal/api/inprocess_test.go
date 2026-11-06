package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
)

func inProcessCaller(userID, team uint, teamName string) auth.Caller {
	return auth.Caller{
		Identity: auth.Identity{
			Subject: "user",
			Scope: auth.Scope{
				UserID: userID, OrganizationID: 1, OrganizationName: "acme", Role: auth.RoleUser,
				Team: auth.Team{ID: team, Name: teamName},
			},
			Client: auth.Client{Kind: auth.ClientMCP},
		},
		Access: auth.AccessRead,
	}
}

func getAs(t *testing.T, srv http.Handler, caller auth.Caller, target string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = req.WithContext(auth.WithCaller(req.Context(), caller))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	return rec
}

func TestAnInProcessCallerPassesTheSessionDoorInItsOwnTeam(t *testing.T) {
	world := newScopeWorld(t, "")

	ann := getAs(t, world.srv, inProcessCaller(2, 1, "default"), "/api/environments")
	bob := getAs(t, world.srv, inProcessCaller(3, 2, "payments"), "/api/environments")

	assert.Equal(t, []string{"acme-env"}, environmentNames(t, ann))
	assert.Equal(t, []string{"pay-env"}, environmentNames(t, bob))
}

func TestAnInProcessCallerStillMeetsTheRoleGuards(t *testing.T) {
	world := newScopeWorld(t, "")

	rec := getAs(t, world.srv, inProcessCaller(2, 1, "default"), "/api/teams")

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

func TestAnInProcessCallerWithNoOrganizationIsRefused(t *testing.T) {
	world := newScopeWorld(t, "")
	caller := inProcessCaller(2, 1, "default")
	caller.Identity.Scope.OrganizationID = 0

	rec := getAs(t, world.srv, caller, "/api/environments")

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
}

func TestAnInProcessCallerWithAnAgentNameItCannotRecordIsRefused(t *testing.T) {
	world := newScopeWorld(t, "")
	caller := inProcessCaller(2, 1, "default")

	for _, agent := range []string{"<b>Cursor</b>", strings.Repeat("a", 65)} {
		caller.Agent = agent

		rec := getAs(t, world.srv, caller, "/api/environments")

		assert.Equal(t, http.StatusUnauthorized, rec.Code, agent)
	}

	caller.Agent = "Claude Code"
	assert.Equal(t, http.StatusOK, getAs(t, world.srv, caller, "/api/environments").Code)
}

func TestAnInProcessCallerKeepsTheAgentItsIdentityNamesWhenTheCallerNamesNone(t *testing.T) {
	world := newScopeWorld(t, "")
	caller := inProcessCaller(2, 1, "default")
	caller.Identity.Client.Agent = "<b>Cursor</b>"

	rec := getAs(t, world.srv, caller, "/api/environments")

	assert.Equal(t, http.StatusUnauthorized, rec.Code, "the identity's own agent is held to the same rule")
}

func TestARequestFromOutsideCannotCarryAnInProcessCaller(t *testing.T) {
	world := newScopeWorld(t, "")
	outside := httptest.NewServer(world.srv)
	t.Cleanup(outside.Close)

	caller, err := json.Marshal(inProcessCaller(2, 1, "default"))
	require.NoError(t, err)

	attempts := []struct {
		name   string
		target string
		header http.Header
		cookie *http.Cookie
	}{
		{name: "no credential", target: "/api/environments"},
		{name: "a caller header", target: "/api/environments", header: http.Header{"X-Pds-Caller": {string(caller)}}},
		{name: "a user header", target: "/api/environments", header: http.Header{"X-User": {"ann"}, "X-Organization": {"acme"}}},
		{name: "a caller query parameter", target: "/api/environments?caller=" + string(caller) + "&team=default"},
		{name: "a caller cookie", target: "/api/environments", cookie: &http.Cookie{Name: "caller", Value: string(caller)}},
		{name: "a session cookie nobody minted", target: "/api/environments", cookie: &http.Cookie{Name: auth.SessionCookieName, Value: "caller"}},
	}
	for _, attempt := range attempts {
		t.Run(attempt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, outside.URL+attempt.target, nil)
			require.NoError(t, err)
			for name, values := range attempt.header {
				req.Header[name] = values
			}
			if attempt.cookie != nil {
				req.AddCookie(attempt.cookie)
			}

			res, err := outside.Client().Do(req)
			require.NoError(t, err)
			t.Cleanup(func() { _ = res.Body.Close() })

			assert.Equal(t, http.StatusUnauthorized, res.StatusCode)
		})
	}
}
