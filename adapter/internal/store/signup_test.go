package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

const signupIssuer = "https://login.example/"

func newSignupStore(t *testing.T) *store.Store {
	t.Helper()

	db := newInstall(t).open("*")
	require.NoError(t, db.Migrate(t.Context()))

	return db
}

func newcomer(subject, organization string) store.NewOrganization {
	return store.NewOrganization{
		Issuer: signupIssuer, Subject: subject, Email: subject + "@example.com", Name: "Ada",
		OrganizationName: organization, PerPersonCap: 3, OrganizationCeiling: 100,
	}
}

func TestCreateOrganizationBuildsTheOrganizationTeamAndAdminTogether(t *testing.T) {
	db := newSignupStore(t)

	placement, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|ada", "Ada's lab"), time.Now())
	require.NoError(t, err)

	assert.Equal(t, "Ada's lab", placement.OrganizationName)
	assert.Equal(t, "default", placement.TeamName)
	assert.Equal(t, store.RoleAdmin, placement.Role)
	assert.NotZero(t, placement.TeamID)
	assert.Equal(t, "Ada", placement.Username)

	again, err := db.Identity().UserPlacement(t.Context(), signupIssuer, "auth0|ada")
	require.NoError(t, err)
	assert.Equal(t, placement, again)
}

func TestCreateOrganizationGivesEachOrganizationItsOwnTeam(t *testing.T) {
	db := newSignupStore(t)

	first, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|ada", "One"), time.Now())
	require.NoError(t, err)
	second, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|grace", "Two"), time.Now())
	require.NoError(t, err)

	assert.NotEqual(t, first.TeamID, second.TeamID)
}

func TestCreateOrganizationRefusesAPersonPastTheirCap(t *testing.T) {
	db := newSignupStore(t)
	for range 3 {
		_, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|ada", "Lab"), time.Now())
		require.NoError(t, err)
	}

	_, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|ada", "One too many"), time.Now())

	assert.ErrorIs(t, err, store.ErrCreationCap)
	assert.Equal(t, int64(3), count(t, db, &store.Organization{}, "1 = 1"), "a refusal leaves nothing behind")
}

func TestCreateOrganizationCapCountsOnlyThePersonsOwn(t *testing.T) {
	db := newSignupStore(t)
	for range 3 {
		_, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|ada", "Lab"), time.Now())
		require.NoError(t, err)
	}

	_, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|grace", "Hers"), time.Now())

	assert.NoError(t, err)
}

func TestCreateOrganizationRefusesPastTheCeiling(t *testing.T) {
	db := newSignupStore(t)
	in := newcomer("auth0|ada", "First")
	in.OrganizationCeiling = 1
	_, err := db.Identity().CreateOrganization(t.Context(), in, time.Now())
	require.NoError(t, err)

	in = newcomer("auth0|grace", "Second")
	in.OrganizationCeiling = 1
	_, err = db.Identity().CreateOrganization(t.Context(), in, time.Now())

	assert.ErrorIs(t, err, store.ErrOrganizationCeiling)
	_, err = db.Identity().UserPlacement(t.Context(), signupIssuer, "auth0|grace")
	assert.ErrorIs(t, err, store.ErrNoMembership, "a refused signup leaves no User behind")
}

func TestCreateOrganizationForAKnownUserKeepsTheirIdentity(t *testing.T) {
	db := newSignupStore(t)
	first, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|ada", "One"), time.Now())
	require.NoError(t, err)

	second, err := db.Identity().CreateOrganization(t.Context(), store.NewOrganization{
		UserID: first.UserID, OrganizationName: "Two", PerPersonCap: 3, OrganizationCeiling: 100,
	}, time.Now())
	require.NoError(t, err)

	assert.Equal(t, first.UserID, second.UserID)
	assert.NotEqual(t, first.OrganizationID, second.OrganizationID)
	assert.Equal(t, int64(1), count(t, db, &store.User{}, "subject = ?", "auth0|ada"))
}

func TestUserPlacementPrefersTheLastUsedOrganization(t *testing.T) {
	db := newSignupStore(t)
	first, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|ada", "One"), time.Now())
	require.NoError(t, err)

	second, err := db.Identity().CreateOrganization(t.Context(), store.NewOrganization{
		UserID: first.UserID, OrganizationName: "Two", PerPersonCap: 3, OrganizationCeiling: 100,
	}, time.Now())
	require.NoError(t, err)

	got, err := db.Identity().UserPlacement(t.Context(), signupIssuer, "auth0|ada")
	require.NoError(t, err)
	assert.Equal(t, second.OrganizationID, got.OrganizationID, "creating an Organization makes it the last used")

	now := time.Now()
	require.NoError(t, db.Identity().CreateSession(t.Context(), store.Session{
		Hash: "h", UserID: first.UserID, OrganizationID: second.OrganizationID, Method: store.SignInOIDC,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}))
	require.NoError(t, db.Identity().SwitchOrganization(t.Context(), "h", first.UserID, first.OrganizationID))
	got, err = db.Identity().UserPlacement(t.Context(), signupIssuer, "auth0|ada")
	require.NoError(t, err)
	assert.Equal(t, first.OrganizationID, got.OrganizationID)
}

func TestSwitchOrganizationMovesTheSessionAndRefusesAStrangersOrganization(t *testing.T) {
	db := newSignupStore(t)
	ada, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|ada", "One"), time.Now())
	require.NoError(t, err)
	grace, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|grace", "Hers"), time.Now())
	require.NoError(t, err)
	two, err := db.Identity().CreateOrganization(t.Context(), store.NewOrganization{
		UserID: ada.UserID, OrganizationName: "Two", PerPersonCap: 3, OrganizationCeiling: 100,
	}, time.Now())
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, db.Identity().CreateSession(t.Context(), store.Session{
		Hash: "h", UserID: ada.UserID, OrganizationID: two.OrganizationID, Method: store.SignInOIDC,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}))

	err = db.Identity().SwitchOrganization(t.Context(), "h", ada.UserID, grace.OrganizationID)
	assert.ErrorIs(t, err, store.ErrNoMembership, "another person's Organization is not one to switch to")

	require.NoError(t, db.Identity().SwitchOrganization(t.Context(), "h", ada.UserID, ada.OrganizationID))

	session, err := db.Identity().Session(t.Context(), "h", now, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, ada.OrganizationID, session.OrganizationID)
}

func TestUserOrganizationsListsEveryMembershipOldestFirst(t *testing.T) {
	db := newSignupStore(t)
	one, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|ada", "One"), time.Now())
	require.NoError(t, err)
	two, err := db.Identity().CreateOrganization(t.Context(), store.NewOrganization{
		UserID: one.UserID, OrganizationName: "Two", PerPersonCap: 3, OrganizationCeiling: 100,
	}, time.Now())
	require.NoError(t, err)

	got, err := db.Identity().UserOrganizations(t.Context(), one.UserID)
	require.NoError(t, err)

	assert.Equal(t, []store.UserOrganization{
		{ID: one.OrganizationID, Name: "One", Role: store.RoleAdmin},
		{ID: two.OrganizationID, Name: "Two", Role: store.RoleAdmin},
	}, got)
}

func TestCreateOrganizationMovesTheSessionInTheSameTransaction(t *testing.T) {
	db := newSignupStore(t)
	first, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|ada", "One"), time.Now())
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, db.Identity().CreateSession(t.Context(), store.Session{
		Hash: "h", UserID: first.UserID, OrganizationID: first.OrganizationID, Method: store.SignInOIDC,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}))

	second, err := db.Identity().CreateOrganization(t.Context(), store.NewOrganization{
		UserID: first.UserID, SessionHash: "h", OrganizationName: "Two", PerPersonCap: 3,
	}, now)
	require.NoError(t, err)

	session, err := db.Identity().Session(t.Context(), "h", now, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, second.OrganizationID, session.OrganizationID)

	_, err = db.Identity().CreateOrganization(t.Context(), store.NewOrganization{
		UserID: first.UserID, SessionHash: "gone", OrganizationName: "Three", PerPersonCap: 3,
	}, now)
	assert.ErrorIs(t, err, store.ErrSessionUnknown)
	assert.Equal(t, int64(2), count(t, db, &store.Organization{}, "1 = 1"), "a session that is not there rolls the Organization back")
}
