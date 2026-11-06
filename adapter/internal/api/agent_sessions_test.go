package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/features"
	"github.com/percona/pds/adapter/internal/oauth"
	"github.com/percona/pds/adapter/internal/service"
)

// agentFor signs an agent in as the person, in the Organization and Team, and
// returns its access token.
func (w *consentWorld) agentFor(t *testing.T, cookie *http.Cookie, organization, team uint) string {
	t.Helper()

	token := w.park(t, cookie, nil)
	back := w.redirectOf(t, w.consent(t, "approve", cookie, approval(token, organization, team, "read")))

	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(tokenForm(back.Query().Get("code")).Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.113.7:5000"
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	return jsonBody(t, rec)["access_token"].(string)
}

func (w *consentWorld) call(t *testing.T, method, target string, cookie *http.Cookie, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, target, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for _, m := range mutate {
		m(req)
	}
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req)

	return rec
}

// listed returns the sessions the person is shown.
func (w *consentWorld) listed(t *testing.T, cookie *http.Cookie) []map[string]any {
	t.Helper()

	rec := w.call(t, http.MethodGet, "/api/agent-sessions", cookie)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	out := []map[string]any{}
	for _, row := range jsonBody(t, rec)["sessions"].([]any) {
		out = append(out, row.(map[string]any))
	}

	return out
}

func memberNames(rows []map[string]any) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row["member"].(string))
	}

	return out
}

func (w *consentWorld) revoke(t *testing.T, cookie *http.Cookie, id any, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	return w.call(t, http.MethodDelete, "/api/agent-sessions/"+id.(string), cookie, mutate...)
}

func TestAMemberListsOnlyTheirOwnAgents(t *testing.T) {
	w := newConsentWorld(t, nil)
	root, ann, bob := w.as(t, "root"), w.as(t, "ann"), w.as(t, "bob")
	w.agentFor(t, root, 1, 1)
	w.agentFor(t, ann, 1, 1)
	w.agentFor(t, bob, 1, 2)

	rows := w.listed(t, ann)

	require.Len(t, rows, 1)
	assert.Equal(t, "Ann Lee", rows[0]["member"])
	assert.Equal(t, true, rows[0]["mine"])
	assert.Equal(t, "Test Agent", rows[0]["name"])
	assert.Equal(t, "agent.example", rows[0]["host"])
	assert.Equal(t, "cimd", rows[0]["kind"])
	assert.Equal(t, "default", rows[0]["team"])
	assert.Equal(t, "read", rows[0]["access"])
	assert.Equal(t, false, rows[0]["passwords"])
	assert.Equal(t, false, rows[0]["sqlWrites"])
	assert.NotEmpty(t, rows[0]["lastUsedAt"])
	assert.Equal(t, "no-store", w.call(t, http.MethodGet, "/api/agent-sessions", ann).Header().Get("Cache-Control"))
}

func TestAnAdminListsEveryAgentOfTheirOrganizationAndNoOtherOrganizations(t *testing.T) {
	w := newConsentWorld(t, nil)
	root, ann, bob, gus := w.as(t, "root"), w.as(t, "ann"), w.as(t, "bob"), w.as(t, "gus")
	w.agentFor(t, root, 1, 1)
	w.agentFor(t, ann, 1, 1)
	w.agentFor(t, bob, 1, 2)
	w.agentFor(t, gus, 2, 3)

	rows := w.listed(t, root)

	assert.ElementsMatch(t, []string{"Root Admin", "Ann Lee", "Bob Ray"}, memberNames(rows))
	for _, row := range rows {
		assert.Equal(t, row["member"] == "Root Admin", row["mine"], "mine is only the admin's own")
	}
	assert.ElementsMatch(t, []string{"Gus Dee"}, memberNames(w.listed(t, gus)))
}

func TestAMemberSignedInToAnotherOrganizationIsListedOnlyThere(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")
	w.agentFor(t, ann, 2, 3)

	assert.Empty(t, w.listed(t, ann), "ann's session in acme is a session in acme")
	assert.Len(t, w.listed(t, w.as(t, "gus")), 1, "the admin of globex sees the agent bound there")
}

func TestTheListNeverCarriesATokenAHashOrTheResource(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")
	access := w.agentFor(t, ann, 1, 1)
	session := w.mem.OnlySession()

	rec := w.call(t, http.MethodGet, "/api/agent-sessions", ann)

	body := rec.Body.String()
	assert.NotContains(t, body, access)
	assert.NotContains(t, body, auth.HashToken(access))
	assert.NotContains(t, strings.ToLower(body), "token")
	assert.NotContains(t, strings.ToLower(body), "hash")
	assert.NotContains(t, body, oauthTestClient, "the client id is not shown, only its name and host")
	assert.NotContains(t, body, session.Resource)
}

func TestAnExpiredAgentIsNotListed(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")
	w.agentFor(t, ann, 1, 1)
	require.Len(t, w.listed(t, ann), 1)

	session := w.mem.OnlySession()
	session.ExpiresAt = time.Now().Add(-time.Minute)
	w.mem.PutSession(session)

	assert.Empty(t, w.listed(t, ann))
}

func TestAMemberRevokesTheirOwnAgentAndItsTokensStopAtOnce(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")
	w.agentFor(t, ann, 1, 1)
	id := w.listed(t, ann)[0]["id"]

	rec := w.revoke(t, ann, id)

	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Empty(t, w.listed(t, ann))
	assert.Zero(t, w.mem.SessionCount())
}

func TestAMemberCannotSeeOrRevokeAnotherMembersAgent(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann, bob := w.as(t, "ann"), w.as(t, "bob")
	w.agentFor(t, bob, 1, 2)
	id := w.listed(t, bob)[0]["id"]

	rec := w.revoke(t, ann, id)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, api.CodeNotFound, jsonBody(t, rec)["code"], "not 403: the id is outside what ann may see")
	assert.Equal(t, 1, w.mem.SessionCount())
	assert.Len(t, w.listed(t, bob), 1)
}

func TestAnAdminRevokesAMembersAgentWithinTheirOrganizationOnly(t *testing.T) {
	w := newConsentWorld(t, nil)
	root, ann, gus := w.as(t, "root"), w.as(t, "ann"), w.as(t, "gus")
	w.agentFor(t, ann, 1, 1)
	w.agentFor(t, gus, 2, 3)
	own := w.listed(t, root)[0]["id"]
	other := w.listed(t, gus)[0]["id"]

	foreign := w.revoke(t, root, other)
	assert.Equal(t, http.StatusNotFound, foreign.Code, "an Organization's admin has no reach into another")
	assert.Len(t, w.listed(t, gus), 1)

	ok := w.revoke(t, root, own)
	assert.Equal(t, http.StatusNoContent, ok.Code, ok.Body.String())
	assert.Empty(t, w.listed(t, ann))
	assert.Len(t, w.listed(t, gus), 1, "the other Organization's agent is untouched")
}

func TestRevokingTwiceChangesNothingTheSecondTime(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")
	w.agentFor(t, ann, 1, 1)
	w.agentFor(t, ann, 1, 1)
	rows := w.listed(t, ann)
	require.Len(t, rows, 2)
	id := rows[0]["id"]

	require.Equal(t, http.StatusNoContent, w.revoke(t, ann, id).Code)
	again := w.revoke(t, ann, id)

	assert.Equal(t, http.StatusNotFound, again.Code, "a session that is gone is not one of ann's")
	assert.Len(t, w.listed(t, ann), 1, "the other agent was not touched")
	assert.Len(t, w.events.all(), 1, "only the first revoke is recorded")
}

func TestARevokeIsRecordedInActivityWithoutSecrets(t *testing.T) {
	w := newConsentWorld(t, nil)
	root, ann := w.as(t, "root"), w.as(t, "ann")
	access := w.agentFor(t, ann, 1, 1)
	id := w.listed(t, root)[0]["id"]

	require.Equal(t, http.StatusNoContent, w.revoke(t, root, id).Code)

	events := w.events.all()
	require.Len(t, events, 1)
	assert.Equal(t, service.ActionAgentRevoke, events[0].Action)
	assert.Equal(t, service.OutcomeSucceeded, events[0].Outcome)
	assert.Contains(t, events[0].Target, "Test Agent")
	assert.Contains(t, events[0].Detail, "Ann Lee", "an admin's revoke says whose agent it was")
	assert.NotContains(t, events[0].Target+events[0].Detail, access)
	assert.NotContains(t, events[0].Target+events[0].Detail, id)
}

func TestAnAgentRevokedInTheConsoleGetsA401OnItsNextMCPCall(t *testing.T) {
	f := newOAuthFixture(t, nil)
	access, _ := f.agentTokens(t, oauth.Grants{Access: auth.AccessRead})
	require.Equal(t, http.StatusOK, f.rpc(t, access, listTools).Code)

	list := f.get(t, "/api/agent-sessions", f.cookie)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	id := jsonBody(t, list)["sessions"].([]any)[0].(map[string]any)["id"].(string)

	req := httptest.NewRequest(http.MethodDelete, "/api/agent-sessions/"+id, nil)
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	revoked := f.rpc(t, access, listTools)
	assert.Equal(t, http.StatusUnauthorized, revoked.Code)
	assert.Contains(t, revoked.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
}

func TestTheAgentRoutesAnswerTheConsoleSessionAndNothingElse(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")
	access := w.agentFor(t, ann, 1, 1)
	id := w.listed(t, ann)[0]["id"].(string)

	bearer := func(token string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
	}

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"list with no session":              w.call(t, http.MethodGet, "/api/agent-sessions", nil),
		"list with the agent's own token":   w.call(t, http.MethodGet, "/api/agent-sessions", nil, bearer(access)),
		"list with the operator token":      w.call(t, http.MethodGet, "/api/agent-sessions", nil, bearer(testHubOperatorToken)),
		"revoke with no session":            w.call(t, http.MethodDelete, "/api/agent-sessions/"+id, nil),
		"revoke with the agent's own token": w.call(t, http.MethodDelete, "/api/agent-sessions/"+id, nil, bearer(access)),
		"revoke with the operator token":    w.call(t, http.MethodDelete, "/api/agent-sessions/"+id, nil, bearer(testHubOperatorToken)),
	} {
		assert.Equal(t, http.StatusUnauthorized, rec.Code, name)
	}
	assert.Equal(t, 1, w.mem.SessionCount(), "none of them revoked anything")
}

func TestARevokeFromAnotherSiteIsRefused(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")
	w.agentFor(t, ann, 1, 1)
	id := w.listed(t, ann)[0]["id"].(string)

	for _, site := range []string{"cross-site", "same-site", "none"} {
		rec := w.revoke(t, ann, id, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", site) })

		assert.Equal(t, http.StatusForbidden, rec.Code, site)
	}
	assert.Equal(t, 1, w.mem.SessionCount())

	ok := w.revoke(t, ann, id, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") })
	assert.Equal(t, http.StatusNoContent, ok.Code)
}

func TestAnIdThatIsNotASessionIsNotFound(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")

	for _, id := range []string{"nope", "0", url.PathEscape("../x"), strings.Repeat("a", 200)} {
		rec := w.call(t, http.MethodDelete, "/api/agent-sessions/"+id, ann)

		assert.Contains(t, []int{http.StatusNotFound, http.StatusMovedPermanently}, rec.Code, id)
	}
}

func TestTheAgentRoutesAnswer404WhileTheMCPFlagIsOff(t *testing.T) {
	w := newConsentWorld(t, map[string]bool{features.MCP: false})
	ann := w.as(t, "ann")

	assert.Equal(t, http.StatusNotFound, w.call(t, http.MethodGet, "/api/agent-sessions", ann).Code)
	assert.Equal(t, http.StatusNotFound, w.call(t, http.MethodDelete, "/api/agent-sessions/abc", ann).Code)
	assert.Equal(t, http.StatusNotFound, w.call(t, http.MethodGet, "/api/agent-sessions", nil).Code, "nothing to find, signed in or not")
}
