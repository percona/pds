package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/store"
)

// directoryFixture is an Organization with an admin signed in, and a way to
// add Users to it and sign them in.
type directoryFixture struct {
	signupFixture
	admin *http.Cookie
	org   uint
}

func newDirectoryFixture(t *testing.T) directoryFixture {
	t.Helper()

	f := newSignupFixture(t, openSignup)
	admin := cookieNamed(f.signUpAs(t, "auth0|ada", "ada@example.com", "Ada", true), auth.SessionCookieName)
	require.NotNil(t, admin)
	placement, err := f.db.Identity().UserPlacement(t.Context(), f.issuer.URL, "auth0|ada")
	require.NoError(t, err)

	return directoryFixture{signupFixture: f, admin: admin, org: placement.OrganizationID}
}

// addUser puts a User in the Organization's default Team and returns their id.
func (f directoryFixture) addUser(t *testing.T, subject, role string) uint {
	t.Helper()

	team, err := f.db.Identity().Team(t.Context(), f.org, "default")
	require.NoError(t, err)
	now := time.Now().UTC()
	user := store.User{Issuer: f.issuer.URL, Subject: subject, Name: subject, Email: subject + "@example.com", CreatedAt: now, UpdatedAt: now}
	require.NoError(t, f.db.Gorm().Create(&user).Error)
	require.NoError(t, f.db.Gorm().Create(&store.Membership{
		UserID: user.ID, OrganizationID: f.org, TeamID: team.ID, Role: role, CreatedAt: now,
	}).Error)

	return user.ID
}

func (f directoryFixture) signInAs(t *testing.T, subject string) *http.Cookie {
	t.Helper()

	session := cookieNamed(f.signUpAs(t, subject, subject+"@example.com", subject, true), auth.SessionCookieName)
	require.NotNil(t, session)

	return session
}

func (f directoryFixture) call(t *testing.T, session *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := withCookie(httptest.NewRequest(method, path, strings.NewReader(body)), session)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)

	return rec
}

func TestAnAdminManagesTeams(t *testing.T) {
	f := newDirectoryFixture(t)

	rec := f.call(t, f.admin, http.MethodPost, "/api/teams", `{"name":"payments"}`)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	assert.Equal(t, http.StatusConflict, f.call(t, f.admin, http.MethodPost, "/api/teams", `{"name":"payments"}`).Code)
	assert.Equal(t, http.StatusBadRequest, f.call(t, f.admin, http.MethodPost, "/api/teams", `{"name":"  "}`).Code)

	list := f.call(t, f.admin, http.MethodGet, "/api/teams", "")
	require.Equal(t, http.StatusOK, list.Code)
	assert.Equal(t, []any{map[string]any{"name": "default"}, map[string]any{"name": "payments"}}, decodeBody(t, list)["teams"])

	assert.Equal(t, http.StatusNoContent, f.call(t, f.admin, http.MethodPatch, "/api/teams/payments", `{"name":"billing"}`).Code)
	assert.Equal(t, http.StatusNotFound, f.call(t, f.admin, http.MethodPatch, "/api/teams/payments", `{"name":"x"}`).Code)
	assert.Equal(t, http.StatusConflict, f.call(t, f.admin, http.MethodPatch, "/api/teams/billing", `{"name":"default"}`).Code)

	assert.Equal(t, http.StatusNoContent, f.call(t, f.admin, http.MethodDelete, "/api/teams/billing", "").Code)
	assert.Equal(t, http.StatusNotFound, f.call(t, f.admin, http.MethodDelete, "/api/teams/billing", "").Code)
}

func TestATeamCannotBeNamedAll(t *testing.T) {
	f := newDirectoryFixture(t)

	require.Equal(t, http.StatusCreated, f.call(t, f.admin, http.MethodPost, "/api/teams", `{"name":"payments"}`).Code)

	for _, name := range []string{"all", "ALL", " All "} {
		create := f.call(t, f.admin, http.MethodPost, "/api/teams", `{"name":"`+name+`"}`)
		assert.Equal(t, http.StatusBadRequest, create.Code, "create %q", name)
		assert.Contains(t, create.Body.String(), "name all is reserved")

		rename := f.call(t, f.admin, http.MethodPatch, "/api/teams/payments", `{"name":"`+name+`"}`)
		assert.Equal(t, http.StatusBadRequest, rename.Code, "rename to %q", name)
	}

	assert.Equal(t, http.StatusCreated, f.call(t, f.admin, http.MethodPost, "/api/teams", `{"name":"allies"}`).Code)
	assert.Equal(t, http.StatusNoContent, f.call(t, f.admin, http.MethodPatch, "/api/teams/payments", `{"name":"overall"}`).Code)
}

func TestAnAdminRenamesTheOrganization(t *testing.T) {
	f := newDirectoryFixture(t)
	placement, err := f.db.Identity().UserPlacement(t.Context(), f.issuer.URL, "auth0|ada")
	require.NoError(t, err)

	rec := f.call(t, f.admin, http.MethodPatch, "/api/organization", `{"name":"  Acme Corp  "}`)

	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
	session := f.sessionBody(t, f.admin)
	assert.Equal(t, "Acme Corp", session["organization"], "the session shows the trimmed name")
	organizations, ok := session["organizations"].([]any)
	require.True(t, ok)
	require.Len(t, organizations, 1)
	assert.Equal(t, "Acme Corp", organizations[0].(map[string]any)["name"], "and so does the switcher")
	assert.Equal(t, []string{"organization.rename:succeeded"}, f.events.actions())
	assert.Equal(t, []uint{placement.UserID}, f.events.actors, "recorded with the acting User")
}

func TestAnOrganizationNameFollowsTheCreationRules(t *testing.T) {
	f := newDirectoryFixture(t)

	for _, name := range []string{"", "   ", strings.Repeat("a", 201)} {
		rec := f.call(t, f.admin, http.MethodPatch, "/api/organization", fmt.Sprintf(`{"name":%q}`, name))

		assert.Equal(t, http.StatusBadRequest, rec.Code, "name %q", name)
	}
	assert.Equal(t, http.StatusNoContent, f.call(t, f.admin, http.MethodPatch, "/api/organization",
		fmt.Sprintf(`{"name":%q}`, strings.Repeat("é", 200))).Code, "200 characters fit")
	assert.Equal(t, []string{"organization.rename:succeeded"}, f.events.actions(), "only the accepted rename is recorded")
}

func TestAUserCannotRenameTheOrganization(t *testing.T) {
	f := newDirectoryFixture(t)
	f.addUser(t, "auth0|bob", store.RoleUser)
	session := f.signInAs(t, "auth0|bob")
	before := f.sessionBody(t, f.admin)["organization"]

	rec := f.call(t, session, http.MethodPatch, "/api/organization", `{"name":"Mine now"}`)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, before, f.sessionBody(t, f.admin)["organization"])
}

func TestATeamWithUsersCannotBeDeleted(t *testing.T) {
	f := newDirectoryFixture(t)

	rec := f.call(t, f.admin, http.MethodDelete, "/api/teams/default", "")

	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestAUserIsRefusedEveryDirectoryRoute(t *testing.T) {
	f := newDirectoryFixture(t)
	bob := f.addUser(t, "auth0|bob", store.RoleUser)
	session := f.signInAs(t, "auth0|bob")

	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/teams", ""},
		{http.MethodPost, "/api/teams", `{"name":"x"}`},
		{http.MethodPatch, "/api/teams/default", `{"name":"x"}`},
		{http.MethodDelete, "/api/teams/default", ""},
		{http.MethodGet, "/api/users", ""},
		{http.MethodPatch, fmt.Sprintf("/api/users/%d", bob), `{"role":"admin"}`},
		{http.MethodDelete, fmt.Sprintf("/api/users/%d", bob), ""},
	}
	for _, route := range routes {
		rec := f.call(t, session, route.method, route.path, route.body)

		assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s", route.method, route.path)
	}
	assert.Equal(t, http.StatusOK, f.call(t, f.admin, http.MethodGet, "/api/users", "").Code, "and nothing changed")
}

func TestAnAdminListsMovesAndChangesUsers(t *testing.T) {
	f := newDirectoryFixture(t)
	bob := f.addUser(t, "auth0|bob", store.RoleUser)
	require.Equal(t, http.StatusCreated, f.call(t, f.admin, http.MethodPost, "/api/teams", `{"name":"payments"}`).Code)

	list := f.call(t, f.admin, http.MethodGet, "/api/users", "")
	require.Equal(t, http.StatusOK, list.Code)
	users, ok := decodeBody(t, list)["users"].([]any)
	require.True(t, ok)
	assert.Len(t, users, 2)

	url := fmt.Sprintf("/api/users/%d", bob)
	assert.Equal(t, http.StatusNoContent, f.call(t, f.admin, http.MethodPatch, url, `{"team":"payments","role":"admin"}`).Code)
	assert.Equal(t, http.StatusNotFound, f.call(t, f.admin, http.MethodPatch, url, `{"team":"nope"}`).Code)
	assert.Equal(t, http.StatusBadRequest, f.call(t, f.admin, http.MethodPatch, url, `{"role":"owner"}`).Code)
	assert.Equal(t, http.StatusBadRequest, f.call(t, f.admin, http.MethodPatch, url, `{"role":""}`).Code)
	assert.Equal(t, http.StatusBadRequest, f.call(t, f.admin, http.MethodPatch, url, `{"team":"  "}`).Code)
	assert.Equal(t, http.StatusBadRequest, f.call(t, f.admin, http.MethodPatch, url, `{}`).Code)
	assert.Equal(t, http.StatusNotFound, f.call(t, f.admin, http.MethodPatch, "/api/users/abc", `{"role":"user"}`).Code)

	session := f.signInAs(t, "auth0|bob")
	body := f.sessionBody(t, session)
	assert.Equal(t, "payments", body["team"])
	assert.Equal(t, "admin", body["role"])
}

func TestTheLastAdminCannotLeaveOrStepDown(t *testing.T) {
	f := newDirectoryFixture(t)
	placement, err := f.db.Identity().UserPlacement(t.Context(), f.issuer.URL, "auth0|ada")
	require.NoError(t, err)
	url := fmt.Sprintf("/api/users/%d", placement.UserID)

	assert.Equal(t, http.StatusConflict, f.call(t, f.admin, http.MethodPatch, url, `{"role":"user"}`).Code)
	assert.Equal(t, http.StatusConflict, f.call(t, f.admin, http.MethodDelete, url, "").Code)
	assert.Equal(t, "admin", f.sessionBody(t, f.admin)["role"])
}

func TestRemovingAUserThroughTheDirectoryEndsTheirSessionNextRequest(t *testing.T) {
	f := newDirectoryFixture(t)
	bob := f.addUser(t, "auth0|bob", store.RoleUser)
	session := f.signInAs(t, "auth0|bob")
	require.Equal(t, http.StatusOK, f.call(t, session, http.MethodGet, "/api/features", "").Code)

	rec := f.call(t, f.admin, http.MethodDelete, fmt.Sprintf("/api/users/%d", bob), "")

	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, http.StatusUnauthorized, f.call(t, session, http.MethodGet, "/api/features", "").Code)
}

func TestAnAdminCannotReachAnotherOrganizationsUsersOrTeams(t *testing.T) {
	f := newDirectoryFixture(t)
	other := cookieNamed(f.signUpAs(t, "auth0|grace", "grace@example.com", "Grace", true), auth.SessionCookieName)
	placement, err := f.db.Identity().UserPlacement(t.Context(), f.issuer.URL, "auth0|grace")
	require.NoError(t, err)
	require.NotNil(t, other)

	url := fmt.Sprintf("/api/users/%d", placement.UserID)
	assert.Equal(t, http.StatusNotFound, f.call(t, f.admin, http.MethodPatch, url, `{"role":"user"}`).Code)
	assert.Equal(t, http.StatusNotFound, f.call(t, f.admin, http.MethodDelete, url, "").Code)
	assert.Equal(t, http.StatusOK, f.call(t, other, http.MethodGet, "/api/users", "").Code)
	users, ok := decodeBody(t, f.call(t, f.admin, http.MethodGet, "/api/users", ""))["users"].([]any)
	require.True(t, ok)
	assert.Len(t, users, 1, "only its own Organization's Users are listed")
}

func TestAnAdminCannotRenameOrDeleteAnotherOrganizationsTeam(t *testing.T) {
	f := newDirectoryFixture(t)
	other := cookieNamed(f.signUpAs(t, "auth0|grace", "grace@example.com", "Grace", true), auth.SessionCookieName)
	require.NotNil(t, other)
	require.Equal(t, http.StatusCreated, f.call(t, other, http.MethodPost, "/api/teams", `{"name":"theirs"}`).Code)

	assert.Equal(t, http.StatusNotFound, f.call(t, f.admin, http.MethodPatch, "/api/teams/theirs", `{"name":"mine"}`).Code)
	assert.Equal(t, http.StatusNotFound, f.call(t, f.admin, http.MethodDelete, "/api/teams/theirs", "").Code)
}

func TestEveryMemberReadsWhatAgentsMayDoAndOnlyAnAdminChangesIt(t *testing.T) {
	f := newDirectoryFixture(t)
	f.addUser(t, "auth0|bob", store.RoleUser)
	bob := f.signInAs(t, "auth0|bob")

	start := f.call(t, bob, http.MethodGet, "/api/organization/ai", "")
	require.Equal(t, http.StatusOK, start.Code, "body: %s", start.Body.String())
	assert.Equal(t, map[string]any{"access": "read", "passwords": false, "sqlWrites": false}, decodeBody(t, start),
		"a new Organization starts at read with both switches off")

	refused := f.call(t, bob, http.MethodPut, "/api/organization/ai", `{"access":"change","passwords":true,"sqlWrites":true}`)
	assert.Equal(t, http.StatusForbidden, refused.Code)
	assert.Equal(t, "read", decodeBody(t, f.call(t, bob, http.MethodGet, "/api/organization/ai", ""))["access"])

	set := f.call(t, f.admin, http.MethodPut, "/api/organization/ai", `{"access":"change","passwords":true,"sqlWrites":false}`)
	require.Equal(t, http.StatusOK, set.Code, "body: %s", set.Body.String())
	want := map[string]any{"access": "change", "passwords": true, "sqlWrites": false}
	assert.Equal(t, want, decodeBody(t, set))
	assert.Equal(t, want, decodeBody(t, f.call(t, bob, http.MethodGet, "/api/organization/ai", "")), "the member sees the change")
	assert.Equal(t, []string{"organization.ai:succeeded"}, f.events.actions())
	assert.Equal(t, "change, passwords on, SQL writes off", f.events.targets()[0], "the switches are recorded too")
}

func TestAnUnknownAccessTierIsRefusedWithASentence(t *testing.T) {
	f := newDirectoryFixture(t)

	for _, body := range []string{
		`{"access":"admin"}`,
		`{"access":"off"}`,
		`{"access":""}`,
		`{"passwords":true}`,
	} {
		rec := f.call(t, f.admin, http.MethodPut, "/api/organization/ai", body)

		assert.Equal(t, http.StatusBadRequest, rec.Code, "body %s", body)
		assert.Equal(t, "Choose read, change, or change and delete.", decodeBody(t, rec)["message"], "body %s", body)
	}
	assert.Equal(t, "read", decodeBody(t, f.call(t, f.admin, http.MethodGet, "/api/organization/ai", ""))["access"])
	assert.Empty(t, f.events.actions())
}
