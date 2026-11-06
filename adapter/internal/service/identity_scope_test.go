package service_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

func signedIn(t *testing.T, identity *service.Identity, login, password string) string {
	t.Helper()

	session, err := identity.Login(t.Context(), login, password)
	require.NoError(t, err)

	return session.Value
}

func TestResolveCarriesTheUsersScope(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")
	// The deployment serves the Organization named acme, whose first Team is
	// default, and the login sits in its payments Team.
	fake.served["acme"] = store.Team{ID: 10, OrganizationID: 3, Name: "default"}
	fake.place("acme", store.Placement{
		UserID: 7, Login: "acme", OrganizationID: 3, Role: store.RoleUser, TeamID: 11, TeamName: "payments",
	})

	resolved, err := identity.Resolve(t.Context(), signedIn(t, identity, "acme", "a long enough secret"))
	require.NoError(t, err)

	assert.Equal(t, auth.Scope{
		UserID: 7, OrganizationID: 3, Role: auth.RoleUser,
		Team: auth.Team{ID: 11, Name: "payments"},
	}, resolved.Scope)
	assert.Equal(t, "acme", resolved.Subject, "the actor stays the login")
}

func TestResolveRefusesALoginThatNoLongerHasAMembership(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")
	cookie := signedIn(t, identity, "acme", "a long enough secret")

	fake.unplace("acme")

	_, err := identity.Resolve(t.Context(), cookie)
	assert.ErrorIs(t, err, service.ErrNotSignedIn, "removal takes effect on the next request")
}

func TestOperatorIsAnAdminOfTheSoleOrganization(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")
	fake.place("acme", store.Placement{
		UserID: 7, OrganizationID: 3, OrganizationName: "acme", Role: store.RoleUser, TeamID: 11, TeamName: "default",
	})

	operator, err := identity.Operator(t.Context())
	require.NoError(t, err)

	assert.Equal(t, auth.RoleAdmin, operator.Scope.Role, "the token outranks the login it borrows the Organization from")
	assert.Equal(t, uint(3), operator.Scope.OrganizationID)
	assert.Zero(t, operator.Scope.UserID, "the operator is not a User")
	assert.Equal(t, uint(11), operator.Scope.Team.ID)
}

func TestOperatorHasNoOrganizationWhenManyAreServed(t *testing.T) {
	fake := newFakeIdentityStore()
	identity := service.NewIdentity(fake) // no Organization pinned: every one is served

	operator, err := identity.Operator(t.Context())
	require.NoError(t, err)

	assert.Zero(t, operator.Scope.OrganizationID)
	assert.Zero(t, operator.Scope.Team.ID)
}

func TestTeamLookupStaysInsideTheOrganization(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	fake.teams = []store.Team{
		{ID: 1, OrganizationID: 3, Name: "default"},
		{ID: 2, OrganizationID: 3, Name: "payments"},
		{ID: 3, OrganizationID: 4, Name: "payments"},
	}

	team, err := identity.Team(t.Context(), 3, "payments")
	require.NoError(t, err)
	assert.Equal(t, auth.Team{ID: 2, Name: "payments"}, team)

	_, err = identity.Team(t.Context(), 3, "ledger")
	assert.ErrorIs(t, err, service.ErrTeamUnknown)

	teams, err := identity.Teams(t.Context(), 3)
	require.NoError(t, err)
	assert.Len(t, teams, 2)
}

func TestMembershipPlacesAUserOnlyInAnOrganizationThisDeploymentServes(t *testing.T) {
	fake := newFakeIdentityStore()
	identity, _ := newIdentityService(t, fake)
	seedClaimedLogin(t, fake, "acme", "a long enough secret")
	fake.served["acme"] = store.Team{ID: 10, OrganizationID: 3, Name: "default"}
	fake.place("acme", store.Placement{
		UserID: 7, Login: "acme", OrganizationID: 3, OrganizationName: "acme", Role: store.RoleUser, TeamID: 11, TeamName: "payments",
	})
	fake.place("elsewhere", store.Placement{
		UserID: 8, Login: "elsewhere", OrganizationID: 4, OrganizationName: "other", Role: store.RoleUser, TeamID: 12, TeamName: "default",
	})

	placement, err := identity.Membership(t.Context(), 7, 3)
	require.NoError(t, err)
	assert.Equal(t, uint(11), placement.TeamID)

	_, err = identity.Membership(t.Context(), 8, 4)
	require.ErrorIs(t, err, store.ErrNoMembership, "an Organization this deployment does not serve")

	_, err = identity.Membership(t.Context(), 7, 4)
	require.ErrorIs(t, err, store.ErrNoMembership, "a membership the person does not have")
}
