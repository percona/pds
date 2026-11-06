package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/features"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
	"github.com/percona/pds/adapter/internal/testutil"
)

// orgLimits gives each Organization its own AI limit.
type orgLimits map[uint]store.AISettings

func (l orgLimits) AI(_ context.Context, organization uint) (store.AISettings, error) {
	return l[organization], nil
}

// consentWorld is two Organizations that share one control plane:
//
//	acme    admin root (default)   ann (user, default)   bob (user, payments)
//	globex  admin gus   (default)  and ann, an admin there with a read only limit
type consentWorld struct {
	srv    *api.Server
	svc    *service.OAuth
	docs   *documentFetcher
	mem    *testutil.MemoryOAuthStore
	events *recordedEvents
}

// recordedEvents is the activity the service left.
type recordedEvents struct {
	mu   sync.Mutex
	rows []service.ActivityEvent
}

func (r *recordedEvents) Record(_ context.Context, e service.ActivityEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, e)
}

func (r *recordedEvents) all() []service.ActivityEvent {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]service.ActivityEvent(nil), r.rows...)
}

// worldMembers names the people of newConsentWorld by Organization.
type worldMembers map[uint][]store.Member

func (m worldMembers) Members(_ context.Context, organization uint) ([]store.Member, error) {
	return m[organization], nil
}

func newConsentWorld(t *testing.T, flags map[string]bool) *consentWorld {
	t.Helper()

	rows := newMemoryIdentityStore()
	identity := service.NewIdentity(rows,
		service.WithPasswordCost(bcrypt.MinCost),
		service.WithIdentitySleep(func(context.Context, time.Duration) {}),
	)
	rows.teams = []store.Team{
		{ID: 1, OrganizationID: 1, Name: "default"},
		{ID: 2, OrganizationID: 1, Name: "payments"},
		{ID: 3, OrganizationID: 2, Name: "default"},
		{ID: 4, OrganizationID: 2, Name: "ledger"},
	}
	for name, at := range map[string]store.Placement{
		"root": {UserID: 1, OrganizationID: 1, OrganizationName: "acme", Role: store.RoleAdmin, TeamID: 1, TeamName: "default"},
		"ann":  {UserID: 2, OrganizationID: 1, OrganizationName: "acme", Role: store.RoleUser, TeamID: 1, TeamName: "default"},
		"bob":  {UserID: 3, OrganizationID: 1, OrganizationName: "acme", Role: store.RoleUser, TeamID: 2, TeamName: "payments"},
		"gus":  {UserID: 4, OrganizationID: 2, OrganizationName: "globex", Role: store.RoleAdmin, TeamID: 3, TeamName: "default"},
	} {
		_, err := identity.CreateLogin(t.Context(), name)
		require.NoError(t, err)
		require.NoError(t, identity.SetPassword(t.Context(), name, scopePassword))
		rows.place(name, at)
	}
	rows.addMembership(store.Placement{
		UserID: 2, OrganizationID: 2, OrganizationName: "globex", Role: store.RoleAdmin, TeamID: 3, TeamName: "default",
	})

	w := &consentWorld{docs: &documentFetcher{}, mem: testutil.NewMemoryOAuthStore(), events: &recordedEvents{}}
	set, err := features.New(flags)
	require.NoError(t, err)
	limits := orgLimits{
		1: {Access: "change_delete", Passwords: true, SQLWrites: true},
		2: {Access: "read"},
	}
	members := worldMembers{
		1: {{UserID: 1, Name: "Root Admin", Role: store.RoleAdmin}, {UserID: 2, Name: "Ann Lee", Role: store.RoleUser}, {UserID: 3, Name: "Bob Ray", Role: store.RoleUser}},
		2: {{UserID: 4, Name: "Gus Dee", Role: store.RoleAdmin}, {UserID: 2, Name: "Ann Lee", Role: store.RoleAdmin}},
	}
	w.svc = service.NewOAuth(w.mem, identity, limits, w.docs, oauthTestIssuer,
		service.WithOAuthMembers(members), service.WithOAuthRecorder(w.events))
	w.srv = api.NewServer(api.Deps{
		Config:   testConfig(),
		Logger:   log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:     api.NewSessionAuthenticator(identity, testHubOperatorToken),
		Identity: identity, Features: set, OAuth: w.svc,
	})

	return w
}

func (w *consentWorld) as(t *testing.T, login string) *http.Cookie {
	t.Helper()

	return signIn(t, w.srv, login, scopePassword)
}

// park runs the authorize request as the person and returns the token that
// names the parked request.
func (w *consentWorld) park(t *testing.T, cookie *http.Cookie, overrides map[string]string) string {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+authorizeQuery(overrides).Encode(), nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())

	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	token := location.Query().Get("request")
	require.NotEmpty(t, token)

	return token
}

// consent posts a JSON body to one of the consent routes.
func (w *consentWorld) consent(t *testing.T, action string, cookie *http.Cookie, body any, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/agent-consent/"+action, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
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

type decision map[string]any

func approval(token string, organization, team uint, access string) decision {
	return decision{"request": token, "organizationId": organization, "teamId": team, "access": access}
}

func (w *consentWorld) redirectOf(t *testing.T, rec *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	u, err := url.Parse(jsonBody(t, rec)["redirectUrl"].(string))
	require.NoError(t, err)

	return u
}

func TestTheConsentScreenIsDescribedToThePersonWhoStartedTheRequest(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")
	token := w.park(t, ann, nil)

	rec := w.consent(t, "lookup", ann, decision{"request": token})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := jsonBody(t, rec)
	client := body["client"].(map[string]any)
	assert.Equal(t, "Test Agent", client["name"])
	assert.Equal(t, "agent.example", client["host"])
	assert.Equal(t, "cimd", client["kind"])
	assert.Equal(t, "127.0.0.1", client["redirectHost"])
	assert.Equal(t, true, client["loopback"])

	orgs := body["organizations"].([]any)
	require.Len(t, orgs, 2)
	acme, globex := orgs[0].(map[string]any), orgs[1].(map[string]any)
	assert.Equal(t, "acme", acme["name"])
	assert.Equal(t, "change", acme["access"], "the request asked for change, below the limit")
	assert.Len(t, acme["teams"], 1, "a User is offered their own Team")
	assert.EqualValues(t, 1, acme["ownTeamId"])
	assert.Equal(t, "read", globex["access"], "globex limits agents to read")
	assert.Len(t, globex["teams"], 2, "ann is an admin of globex")

	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.NotContains(t, rec.Body.String(), token, "the request token is never echoed")
}

func TestApprovingIssuesACodeBoundToTheChosenOrganizationTeamAndTier(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")
	token := w.park(t, ann, nil)

	rec := w.consent(t, "approve", ann, approval(token, 1, 1, "read"))

	back := w.redirectOf(t, rec)
	assert.Equal(t, "127.0.0.1:43211", back.Host, "the person goes to the address the client proved is theirs")
	assert.Equal(t, "abc123", back.Query().Get("state"))
	assert.Equal(t, oauthTestIssuer, back.Query().Get("iss"))
	code := back.Query().Get("code")
	require.NotEmpty(t, code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	form := tokenForm(code)
	exchange := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.113.7:5000"
	w.srv.ServeHTTP(exchange, req)
	require.Equal(t, http.StatusOK, exchange.Code, exchange.Body.String())
	assert.Equal(t, "pds:read", jsonBody(t, exchange)["scope"], "the token carries the tier that was chosen, not the one asked for")
}

func TestAnAdminMayPickAnyTeamOfTheOrganizationAndAUserOnlyTheirOwn(t *testing.T) {
	w := newConsentWorld(t, nil)
	root, ann := w.as(t, "root"), w.as(t, "ann")

	admin := w.consent(t, "approve", root, approval(w.park(t, root, nil), 1, 2, "read"))
	require.Equal(t, http.StatusOK, admin.Code, admin.Body.String())

	user := w.consent(t, "approve", ann, approval(w.park(t, ann, nil), 1, 2, "read"))
	assert.Equal(t, http.StatusNotFound, user.Code, "a User cannot send an agent into another Team")
	assert.Equal(t, api.CodeNotFound, jsonBody(t, user)["code"])
}

func TestADecisionForAnOrganizationOrTeamThePersonDoesNotBelongToIsRefusedAndTheRequestKeepsWaiting(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")
	token := w.park(t, ann, nil)

	for name, tc := range map[string]struct {
		organization, team uint
	}{
		"an Organization the person is not in": {organization: 9, team: 1},
		"a Team of another Organization":       {organization: 1, team: 3},
		"a Team that does not exist":           {organization: 1, team: 99},
		"no Organization":                      {organization: 0, team: 1},
		"no Team":                              {organization: 1, team: 0},
	} {
		rec := w.consent(t, "approve", ann, approval(token, tc.organization, tc.team, "read"))

		assert.Equal(t, http.StatusNotFound, rec.Code, name)
		assert.Empty(t, jsonBody(t, rec)["redirectUrl"], name)
	}

	ok := w.consent(t, "approve", ann, approval(token, 1, 1, "read"))
	assert.Equal(t, http.StatusOK, ok.Code, "refused choices left the request waiting")
}

func TestGrantsAboveTheLimitTheRequestOrTheClientAreRefusedAndTheRequestKeepsWaiting(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")
	token := w.park(t, ann, nil)

	for name, body := range map[string]decision{
		"above the Organization limit":    approval(token, 2, 3, "change"),
		"above what the client asked for": approval(token, 1, 1, "change_delete"),
		"a tier that does not exist":      approval(token, 1, 1, "root"),
		"no tier":                         approval(token, 1, 1, ""),
		"passwords for a client that is not the local one": {
			"request": token, "organizationId": 1, "teamId": 1, "access": "read", "passwords": true,
		},
		"sql writes for a client that is not the local one": {
			"request": token, "organizationId": 1, "teamId": 1, "access": "read", "sqlWrites": true,
		},
	} {
		rec := w.consent(t, "approve", ann, body)

		assert.GreaterOrEqual(t, rec.Code, 400, name)
		assert.Less(t, rec.Code, 500, name)
		assert.Empty(t, jsonBody(t, rec)["redirectUrl"], name)
	}

	ok := w.consent(t, "approve", ann, approval(token, 1, 1, "read"))
	assert.Equal(t, http.StatusOK, ok.Code, "refused choices left the request waiting")
}

func TestARequestTokenOfAnotherPersonIsRefused(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann, bob := w.as(t, "ann"), w.as(t, "bob")
	token := w.park(t, ann, nil)

	for _, action := range []string{"lookup", "approve", "deny"} {
		rec := w.consent(t, action, bob, approval(token, 1, 2, "read"))

		assert.Equal(t, http.StatusNotFound, rec.Code, action)
		assert.Empty(t, jsonBody(t, rec)["redirectUrl"], action)
	}

	ok := w.consent(t, "approve", ann, approval(token, 1, 1, "read"))
	assert.Equal(t, http.StatusOK, ok.Code, "the owner can still answer")
}

func TestAnAnsweredRequestCannotBeAnsweredAgain(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")

	approved := w.park(t, ann, nil)
	require.Equal(t, http.StatusOK, w.consent(t, "approve", ann, approval(approved, 1, 1, "read")).Code)
	for _, action := range []string{"approve", "deny", "lookup"} {
		rec := w.consent(t, action, ann, approval(approved, 1, 1, "read"))
		assert.Equal(t, http.StatusNotFound, rec.Code, "after approve: "+action)
	}

	denied := w.park(t, ann, nil)
	require.Equal(t, http.StatusOK, w.consent(t, "deny", ann, decision{"request": denied}).Code)
	for _, action := range []string{"approve", "deny"} {
		rec := w.consent(t, action, ann, approval(denied, 1, 1, "read"))
		assert.Equal(t, http.StatusNotFound, rec.Code, "after deny: "+action)
	}
}

func TestDenyingSendsAccessDeniedToTheClientsRedirect(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")

	rec := w.consent(t, "deny", ann, decision{"request": w.park(t, ann, nil)})

	back := w.redirectOf(t, rec)
	assert.Equal(t, "127.0.0.1:43211", back.Host)
	assert.Equal(t, "/callback", back.Path)
	assert.Equal(t, "access_denied", back.Query().Get("error"))
	assert.Equal(t, "abc123", back.Query().Get("state"))
	assert.Equal(t, oauthTestIssuer, back.Query().Get("iss"))
	assert.Empty(t, back.Query().Get("code"))
}

func TestTheRedirectIsNeverTakenFromTheBody(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")
	body := approval(w.park(t, ann, nil), 1, 1, "read")
	body["redirectUri"], body["redirectUrl"] = "https://evil.example/steal", "https://evil.example/steal"

	back := w.redirectOf(t, w.consent(t, "approve", ann, body))

	assert.Equal(t, "127.0.0.1:43211", back.Host)
}

func TestTheConsentRoutesNeedAConsoleSession(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")
	token := w.park(t, ann, nil)

	for _, action := range []string{"lookup", "approve", "deny"} {
		anonymous := w.consent(t, action, nil, approval(token, 1, 1, "read"))
		assert.Equal(t, http.StatusUnauthorized, anonymous.Code, action)

		operator := w.consent(t, action, nil, approval(token, 1, 1, "read"), func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+testHubOperatorToken)
		})
		assert.Equal(t, http.StatusUnauthorized, operator.Code, "the operator token is no person: "+action)
	}

	ok := w.consent(t, "approve", ann, approval(token, 1, 1, "read"))
	assert.Equal(t, http.StatusOK, ok.Code, "nothing above spent the request")
}

func TestADecisionFromAnotherSiteOrAsAFormIsRefusedAndTheRequestKeepsWaiting(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")
	token := w.park(t, ann, nil)

	for name, mutate := range map[string]func(*http.Request){
		"fetch metadata says cross site": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"fetch metadata says same site":  func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
		"a form encoded body":            func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") },
		"a plain text body":              func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		"no content type":                func(r *http.Request) { r.Header.Del("Content-Type") },
	} {
		for _, action := range []string{"approve", "deny"} {
			rec := w.consent(t, action, ann, approval(token, 1, 1, "read"), mutate)

			assert.GreaterOrEqual(t, rec.Code, 400, name+" "+action)
			assert.Empty(t, jsonBody(t, rec)["redirectUrl"], name+" "+action)
		}
	}

	same := w.consent(t, "approve", ann, approval(token, 1, 1, "read"), func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	})
	assert.Equal(t, http.StatusOK, same.Code, "the console's own request goes through")
}

func TestTheConsentRoutesRefuseGETAndOversizedBodies(t *testing.T) {
	w := newConsentWorld(t, nil)
	ann := w.as(t, "ann")

	req := httptest.NewRequest(http.MethodGet, "/api/agent-consent/approve?request=x", nil)
	req.AddCookie(ann)
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, "an approval is never a link")

	big := w.consent(t, "approve", ann, decision{"request": strings.Repeat("a", 1<<20)})
	assert.GreaterOrEqual(t, big.Code, 400)
}

func TestTheConsentRoutesAre404WhileTheMCPSurfaceIsOff(t *testing.T) {
	w := newConsentWorld(t, map[string]bool{features.MCP: false})
	ann := w.as(t, "ann")

	for _, action := range []string{"lookup", "approve", "deny"} {
		rec := w.consent(t, action, ann, decision{"request": "x"})

		assert.Equal(t, http.StatusNotFound, rec.Code, action)
	}
}
