package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
)

// scopeNames are the Users of scopeWorld by id. User 99 was removed, so no
// row answers for it.
type scopeNames map[uint]string

func (n scopeNames) UserNames(_ context.Context, ids []uint) (map[uint]string, error) {
	out := map[uint]string{}
	for _, id := range ids {
		if name, ok := n[id]; ok {
			out[id] = name
		}
	}

	return out, nil
}

var scopeUserNames = scopeNames{1: "Rita Root", 2: "Ann Lee", 3: "Bob Ray", 4: "Gus Ng"}

// recordAs writes an action the way a request of that User would: the scope
// the session door resolved is on the context.
func (w *scopeWorld) recordAs(userID, orgID, teamID uint, role, target string) {
	ctx := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: "login",
		Scope: auth.Scope{
			UserID: userID, OrganizationID: orgID, Role: role,
			Team: auth.Team{ID: teamID},
		},
	})
	w.activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseCreate, Target: target})
}

// seedActivity leaves one event per actor: Ann and Rita in acme's default
// Team, Bob in payments, a removed User in default, and Gus in another
// Organization.
func (w *scopeWorld) seedActivity() {
	w.recordAs(2, 1, 1, auth.RoleUser, "ann-db")
	w.recordAs(1, 1, 1, auth.RoleAdmin, "root-db")
	w.recordAs(99, 1, 1, auth.RoleUser, "gone-db")
	w.recordAs(3, 1, 2, auth.RoleUser, "bob-db")
	w.recordAs(4, 2, 3, auth.RoleAdmin, "gus-db")
}

type activityRow struct{ actor, team string }

func activityRows(t *testing.T, rec *httptest.ResponseRecorder) map[string]activityRow {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	out := map[string]activityRow{}
	for _, item := range decodeBody(t, rec)["entries"].([]any) {
		row := item.(map[string]any)
		team, _ := row["team"].(string)
		out[row["target"].(string)] = activityRow{actor: row["actor"].(string), team: team}
	}

	return out
}

func TestActivityShowsTheUserWhoActed(t *testing.T) {
	world := newScopeWorld(t, "")
	world.seedActivity()

	rows := activityRows(t, world.call(t, world.as(t, "ann"), http.MethodGet, "/api/activity", ""))

	assert.Equal(t, "Ann Lee", rows["ann-db"].actor)
	assert.Equal(t, "Rita Root", rows["root-db"].actor)
}

func TestActivityRendersARemovedUser(t *testing.T) {
	world := newScopeWorld(t, "")
	world.seedActivity()

	rows := activityRows(t, world.call(t, world.as(t, "ann"), http.MethodGet, "/api/activity", ""))

	assert.Equal(t, "Removed user", rows["gone-db"].actor)
}

func TestAUserSeesOnlyTheirTeamsActivity(t *testing.T) {
	world := newScopeWorld(t, "")
	world.seedActivity()

	ann := activityRows(t, world.call(t, world.as(t, "ann"), http.MethodGet, "/api/activity", ""))
	bob := activityRows(t, world.call(t, world.as(t, "bob"), http.MethodGet, "/api/activity", ""))

	assert.ElementsMatch(t, []string{"ann-db", "root-db", "gone-db"}, keys(ann))
	assert.ElementsMatch(t, []string{"bob-db"}, keys(bob))
}

func TestAUserCannotReachAnotherTeamsActivity(t *testing.T) {
	world := newScopeWorld(t, "")
	world.seedActivity()
	ann := world.as(t, "ann")

	assert.Equal(t, http.StatusNotFound, world.call(t, ann, http.MethodGet, "/api/activity?team=payments", "").Code)
	assert.Equal(t, http.StatusForbidden, world.call(t, ann, http.MethodGet, "/api/activity?team=all", "").Code)
}

func TestAnAdminSeesEveryTeamsActivityNotAnotherOrganizations(t *testing.T) {
	world := newScopeWorld(t, "")
	world.seedActivity()
	root := world.as(t, "root")

	all := activityRows(t, world.call(t, root, http.MethodGet, "/api/activity?team=all", ""))

	assert.ElementsMatch(t, []string{"ann-db", "root-db", "gone-db", "bob-db"}, keys(all))
	assert.Equal(t, "payments", all["bob-db"].team, "a row across Teams says which Team it is in")
	assert.Equal(t, "default", all["ann-db"].team)

	named := activityRows(t, world.call(t, root, http.MethodGet, "/api/activity?team=payments", ""))
	assert.ElementsMatch(t, []string{"bob-db"}, keys(named))

	assert.Equal(t, http.StatusNotFound, world.call(t, root, http.MethodGet, "/api/activity?team=ledger", "").Code)
}

func TestAnotherTeamsActivityEntryAnswersNotFoundToAUser(t *testing.T) {
	world := newScopeWorld(t, "")
	world.seedActivity()
	bobEntries := decodeBody(t, world.call(t, world.as(t, "bob"), http.MethodGet, "/api/activity", ""))["entries"].([]any)
	id := bobEntries[0].(map[string]any)["id"].(string)

	rec := world.call(t, world.as(t, "ann"), http.MethodGet, "/api/activity/"+id, "")

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}

func TestAnAdminOpensAnotherTeamsActivityEntryByNamingTheTeam(t *testing.T) {
	world := newScopeWorld(t, "")
	world.seedActivity()
	bobEntries := decodeBody(t, world.call(t, world.as(t, "bob"), http.MethodGet, "/api/activity", ""))["entries"].([]any)
	id := bobEntries[0].(map[string]any)["id"].(string)

	rec := world.call(t, world.as(t, "root"), http.MethodGet, "/api/activity/"+id+"?team=payments", "")

	assert.NotEqual(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
	assert.NotEqual(t, http.StatusForbidden, rec.Code)
}
