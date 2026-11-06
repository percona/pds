package service_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/oauth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

func (r *oauthRig) screen(t *testing.T, who auth.Identity, overrides map[string]string) service.ConsentScreen {
	t.Helper()

	token, aerr := r.svc.StartAuthorization(t.Context(), who, r.request(overrides))
	require.Nil(t, aerr)
	screen, err := r.svc.ConsentScreen(t.Context(), who, token)
	require.NoError(t, err)

	return screen
}

func TestTheConsentScreenNamesWhoIsAsking(t *testing.T) {
	r := newOAuthRig(t)

	screen := r.screen(t, r.user, nil)

	assert.Equal(t, "Claude Code", screen.ClientName)
	assert.Equal(t, "claude.example", screen.ClientHost)
	assert.Equal(t, "cimd", screen.ClientKind)
	assert.Equal(t, "localhost", screen.RedirectHost)
	assert.True(t, screen.LoopbackRedirect)
}

func TestTheConsentScreenOffersTheTiersUpToTheLimitAndTheRequest(t *testing.T) {
	for name, tc := range map[string]struct {
		limit   string
		scope   string
		role    func(*oauthRig) auth.Identity
		highest auth.Access
	}{
		"limit below the request":  {"change", "", func(r *oauthRig) auth.Identity { return r.user }, auth.AccessChange},
		"request below the limit":  {"change_delete", "pds:read pds:change", func(r *oauthRig) auth.Identity { return r.user }, auth.AccessChange},
		"read only organization":   {"read", "", func(r *oauthRig) auth.Identity { return r.admin }, auth.AccessRead},
		"full limit and request":   {"change_delete", "", func(r *oauthRig) auth.Identity { return r.admin }, auth.AccessChangeDelete},
		"a request for read alone": {"change_delete", "pds:read", func(r *oauthRig) auth.Identity { return r.user }, auth.AccessRead},
	} {
		r := newOAuthRig(t)
		r.limits.set(store.AISettings{Access: tc.limit})

		screen := r.screen(t, tc.role(r), map[string]string{"scope": tc.scope})

		require.Len(t, screen.Organizations, 1, name)
		assert.Equal(t, tc.highest, screen.Organizations[0].Access, name)
	}
}

func TestTheConsentScreenOffersAUserTheirOwnTeamAndAnAdminEveryTeam(t *testing.T) {
	r := newOAuthRig(t)

	user := r.screen(t, r.user, nil).Organizations[0]
	assert.Equal(t, []auth.Team{{ID: 5, Name: "payments"}}, user.Teams)
	assert.Equal(t, uint(5), user.OwnTeamID)

	admin := r.screen(t, r.admin, nil).Organizations[0]
	assert.Equal(t, []auth.Team{{ID: 5, Name: "payments"}, {ID: 6, Name: "search"}}, admin.Teams)
	assert.Equal(t, uint(5), admin.OwnTeamID)
}

func TestTheConsentScreenListsEveryOrganizationThePersonBelongsTo(t *testing.T) {
	r := newOAuthRig(t)
	r.people.also = []store.Placement{
		{UserID: 7, Username: "Ada", OrganizationID: 4, OrganizationName: "Globex", Role: auth.RoleAdmin, TeamID: 9, TeamName: "core"},
	}
	r.people.teams[4] = []auth.Team{{ID: 9, Name: "core"}, {ID: 10, Name: "edge"}}
	r.limits.setFor(4, store.AISettings{Access: "read"})

	screen := r.screen(t, r.user, map[string]string{"scope": ""})

	require.Len(t, screen.Organizations, 2)
	acme, globex := screen.Organizations[0], screen.Organizations[1]
	assert.Equal(t, "Acme", acme.Name)
	assert.Equal(t, auth.AccessChangeDelete, acme.Access)
	assert.Equal(t, "Globex", globex.Name)
	assert.Equal(t, auth.AccessRead, globex.Access, "each Organization has its own limit")
	assert.Len(t, globex.Teams, 2, "the person is an admin there")
}

func TestTheBoxesAreOfferedOnlyToTheLocalClientAndOnlyWhereTheOrganizationAllowsThem(t *testing.T) {
	r := newOAuthRig(t)
	other := r.screen(t, r.user, map[string]string{"scope": ""}).Organizations[0]
	assert.False(t, other.Passwords, "a client that is not the local one is never offered passwords")
	assert.False(t, other.SQLWrites)

	r.useLocalClient()
	local := r.screen(t, r.user, map[string]string{"scope": ""})
	assert.Equal(t, "local", local.ClientKind)
	assert.True(t, local.Organizations[0].Passwords)
	assert.True(t, local.Organizations[0].SQLWrites)

	r.limits.set(store.AISettings{Access: "change_delete", Passwords: false, SQLWrites: true})
	narrowed := r.screen(t, r.user, map[string]string{"scope": ""}).Organizations[0]
	assert.False(t, narrowed.Passwords, "the Organization switch is off")
	assert.True(t, narrowed.SQLWrites)

	r.limits.set(store.AISettings{Access: "change_delete", Passwords: true, SQLWrites: true})
	asked := r.screen(t, r.user, map[string]string{"scope": "pds:read pds:credentials"}).Organizations[0]
	assert.True(t, asked.Passwords)
	assert.False(t, asked.SQLWrites, "the client did not ask for it")
}

func TestAClientThatIsNotTheLocalOneCannotBeGrantedThePasswordOrSQLBoxes(t *testing.T) {
	r := newOAuthRig(t)

	for name, grants := range map[string]oauth.Grants{
		"passwords": {Access: auth.AccessRead, Passwords: true},
		"sql":       {Access: auth.AccessRead, SQLWrites: true},
	} {
		token, aerr := r.svc.StartAuthorization(t.Context(), r.user, r.request(map[string]string{"scope": ""}))
		require.Nil(t, aerr, name)

		_, err := r.svc.Approve(t.Context(), r.user, token, r.consentFor(r.user, grants))

		require.ErrorIs(t, err, service.ErrConsentGrants, name)
	}
}

func TestTheLocalClientIsKnownWithoutFetchingAnything(t *testing.T) {
	r := newOAuthRig(t)
	r.useLocalClient()

	token, aerr := r.svc.StartAuthorization(t.Context(), r.user, r.request(map[string]string{"scope": ""}))
	require.Nil(t, aerr)

	assert.Empty(t, r.docs.called, "the control plane fetches no document for a client it ships with")
	redirect, err := r.svc.Approve(t.Context(), r.user, token,
		r.consentFor(r.user, oauth.Grants{Access: auth.AccessRead, Passwords: true, SQLWrites: true}))
	require.NoError(t, err)
	u, err := url.Parse(redirect)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:51234", u.Host)
}

func TestTheLocalClientCannotBeSentToAnotherRedirect(t *testing.T) {
	r := newOAuthRig(t)
	r.useLocalClient()

	for _, redirect := range []string{"https://evil.example/callback", "http://127.0.0.1:1/other", "http://localhost:1/callback"} {
		_, aerr := r.svc.StartAuthorization(t.Context(), r.user, r.request(map[string]string{"redirect_uri": redirect}))
		require.NotNil(t, aerr, redirect)
		assert.Empty(t, aerr.RedirectURI, "an address that is not the client's gets an error page")
	}
}

func TestTheConsentScreenIsForThePersonWhoStartedTheRequest(t *testing.T) {
	r := newOAuthRig(t)
	token, aerr := r.svc.StartAuthorization(t.Context(), r.user, r.request(nil))
	require.Nil(t, aerr)

	_, err := r.svc.ConsentScreen(t.Context(), r.admin, token)

	require.ErrorIs(t, err, service.ErrAuthorizationGone)
}
