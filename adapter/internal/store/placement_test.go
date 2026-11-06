package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

func TestPlacementOfAMigratedLoginIsAnAdminInTheDefaultTeam(t *testing.T) {
	install := newInstall(t)
	install.seed("acme", "$2a$hash", 0)

	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))

	placement, err := db.Identity().Placement(t.Context(), "acme")
	require.NoError(t, err)

	assert.Equal(t, store.RoleAdmin, placement.Role)
	assert.Equal(t, "default", placement.TeamName)
	assert.NotZero(t, placement.UserID)
	assert.NotZero(t, placement.OrganizationID)
	assert.NotZero(t, placement.TeamID)
}

func TestPlacementOfAnUnknownLoginIsRefused(t *testing.T) {
	install := newInstall(t)
	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))

	_, err := db.Identity().Placement(t.Context(), "nobody")

	assert.ErrorIs(t, err, store.ErrLoginUnknown)
}

func TestEnsureLoginGivesTheNewLoginAnOrganizationAndATeam(t *testing.T) {
	install := newInstall(t)
	db := install.open("*")
	require.NoError(t, db.Migrate(t.Context()))

	created, err := db.Identity().EnsureLogin(t.Context(), "globex", time.Now())
	require.NoError(t, err)
	require.True(t, created)

	placement, err := db.Identity().Placement(t.Context(), "globex")
	require.NoError(t, err)
	assert.Equal(t, store.RoleAdmin, placement.Role)
	assert.Equal(t, "globex", placement.OrganizationName)

	created, err = db.Identity().EnsureLogin(t.Context(), "globex", time.Now())
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, int64(1), count(t, db, &store.Organization{}, "name = ?", "globex"), "a second call adds nothing")
}

func TestTeamsOfAnOrganizationAreFoundByName(t *testing.T) {
	install := newInstall(t)
	install.seed("acme", "$2a$hash", 0)
	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))

	placement, err := db.Identity().Placement(t.Context(), "acme")
	require.NoError(t, err)
	require.NoError(t, db.Gorm().Create(&store.Team{
		OrganizationID: placement.OrganizationID, Name: "payments", CreatedAt: time.Now(),
	}).Error)

	teams, err := db.Identity().Teams(t.Context(), placement.OrganizationID)
	require.NoError(t, err)
	require.Len(t, teams, 2)

	team, err := db.Identity().Team(t.Context(), placement.OrganizationID, "payments")
	require.NoError(t, err)
	assert.Equal(t, "payments", team.Name)

	_, err = db.Identity().Team(t.Context(), placement.OrganizationID+1, "payments")
	assert.ErrorIs(t, err, store.ErrTeamUnknown, "another Organization's team is not found")
}

func TestTheSoleOrganizationIsFoundByItsNameBeforeAnyoneHasALogin(t *testing.T) {
	install := newInstall(t)
	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))

	team, err := db.Identity().ServedTeam(t.Context(), "acme")
	require.NoError(t, err)
	assert.Equal(t, "default", team.Name)

	_, err = db.Identity().ServedTeam(t.Context(), "globex")
	assert.ErrorIs(t, err, store.ErrTeamUnknown)
}

func TestTeamOwnerNamesTheOrganizationAndTeamOfATeam(t *testing.T) {
	install := newInstall(t)
	install.seed("acme", "$2a$hash", 0)
	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))

	served, err := db.Identity().ServedTeam(t.Context(), "acme")
	require.NoError(t, err)
	organization, team, err := db.Identity().TeamOwner(t.Context(), served.ID)
	require.NoError(t, err)

	assert.Equal(t, "acme", organization)
	assert.Equal(t, "default", team)
}

func TestTeamOwnerOfAnUnknownTeamIsRefused(t *testing.T) {
	install := newInstall(t)
	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))

	_, _, err := db.Identity().TeamOwner(t.Context(), 999999)

	assert.ErrorIs(t, err, store.ErrTeamUnknown)
}

func TestDeleteTeamWithEnvironmentsIsRefused(t *testing.T) {
	install := newInstall(t)
	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))
	team, err := db.Identity().ServedTeam(t.Context(), "acme")
	require.NoError(t, err)
	require.NoError(t, db.Hub(team.ID, nil).SaveEnvironment(t.Context(), "prod", `{"seen":true}`))

	err = db.Identity().DeleteTeam(t.Context(), team.OrganizationID, team.Name)

	require.ErrorIs(t, err, store.ErrTeamHasEnvironments)
	_, err = db.Identity().ServedTeam(t.Context(), "acme")
	assert.NoError(t, err, "the team is still there")
}

func TestDeleteTeamWithUsersIsRefused(t *testing.T) {
	install := newInstall(t)
	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))
	team, err := db.Identity().ServedTeam(t.Context(), "acme")
	require.NoError(t, err)
	now := time.Now().UTC()
	user := store.User{Issuer: "https://login.example/", Subject: "u1", CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Gorm().Create(&user).Error)
	require.NoError(t, db.Gorm().Create(&store.Membership{
		UserID: user.ID, OrganizationID: team.OrganizationID, TeamID: team.ID, Role: store.RoleUser, CreatedAt: now,
	}).Error)

	err = db.Identity().DeleteTeam(t.Context(), team.OrganizationID, team.Name)

	assert.ErrorIs(t, err, store.ErrTeamHasUsers)
}

func TestDeleteTeamRemovesAnEmptyTeam(t *testing.T) {
	install := newInstall(t)
	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))
	team, err := db.Identity().ServedTeam(t.Context(), "acme")
	require.NoError(t, err)

	require.NoError(t, db.Identity().DeleteTeam(t.Context(), team.OrganizationID, team.Name))

	_, err = db.Identity().ServedTeam(t.Context(), "acme")
	assert.ErrorIs(t, err, store.ErrTeamUnknown)
}

func TestDeleteTeamOfAnotherOrganizationIsNotFound(t *testing.T) {
	install := newInstall(t)
	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))
	team, err := db.Identity().ServedTeam(t.Context(), "acme")
	require.NoError(t, err)

	err = db.Identity().DeleteTeam(t.Context(), team.OrganizationID+1, team.Name)

	assert.ErrorIs(t, err, store.ErrTeamUnknown)
}

// seedIssuerUser puts a User the issuer vouches for into the migrated
// Organization, in its default Team.
func seedIssuerUser(t *testing.T, db *store.Store, issuer, subject, name, email string) (store.User, store.Team) {
	t.Helper()

	team, err := db.Identity().ServedTeam(t.Context(), "acme")
	require.NoError(t, err)
	now := time.Now().UTC()
	user := store.User{Issuer: issuer, Subject: subject, Name: name, Email: email, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Gorm().Create(&user).Error)
	require.NoError(t, db.Gorm().Create(&store.Membership{
		UserID: user.ID, OrganizationID: team.OrganizationID, TeamID: team.ID, Role: store.RoleUser, CreatedAt: now,
	}).Error)

	return user, team
}

func TestUserPlacementFindsAnIdentityByIssuerAndSubject(t *testing.T) {
	install := newInstall(t)
	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))
	user, team := seedIssuerUser(t, db, "https://login.example/", "auth0|ada", "Ada", "ada@example.com")

	placement, err := db.Identity().UserPlacement(t.Context(), "https://login.example/", "auth0|ada")
	require.NoError(t, err)

	assert.Equal(t, user.ID, placement.UserID)
	assert.Equal(t, "Ada", placement.Username)
	assert.Equal(t, team.OrganizationID, placement.OrganizationID)
	assert.NotEmpty(t, placement.OrganizationName)
	assert.Equal(t, store.RoleUser, placement.Role)
}

func TestUserPlacementRefusesAnIdentityNobodyKnows(t *testing.T) {
	install := newInstall(t)
	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))
	seedIssuerUser(t, db, "https://login.example/", "auth0|ada", "Ada", "ada@example.com")

	tests := []struct{ name, issuer, subject string }{
		{"unknown subject", "https://login.example/", "auth0|stranger"},
		{"same subject at another issuer", "https://other.example/", "auth0|ada"},
		{"the password login's subject at the provider", "https://login.example/", "acme"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Identity().UserPlacement(t.Context(), tc.issuer, tc.subject)

			assert.ErrorIs(t, err, store.ErrNoMembership)
		})
	}
}

func TestPlacementOfFollowsTheMembershipAndEndsWhenItDoes(t *testing.T) {
	install := newInstall(t)
	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))
	user, team := seedIssuerUser(t, db, "https://login.example/", "auth0|ada", "", "ada@example.com")

	placement, err := db.Identity().PlacementOf(t.Context(), user.ID, team.OrganizationID)
	require.NoError(t, err)
	assert.Equal(t, "ada@example.com", placement.Username, "the email stands in for a missing name")

	require.NoError(t, db.Gorm().Delete(&store.Membership{}, "user_id = ?", user.ID).Error)

	_, err = db.Identity().PlacementOf(t.Context(), user.ID, team.OrganizationID)
	assert.ErrorIs(t, err, store.ErrNoMembership)
}

func TestAnIdentityProviderSessionNeedsAUser(t *testing.T) {
	install := newInstall(t)
	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))
	user, team := seedIssuerUser(t, db, "https://login.example/", "auth0|ada", "Ada", "")
	now := time.Now()

	require.NoError(t, db.Identity().CreateSession(t.Context(), store.Session{
		Hash: "h", UserID: user.ID, OrganizationID: team.OrganizationID, Method: store.SignInOIDC,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}))

	got, err := db.Identity().Session(t.Context(), "h", now, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, user.ID, got.UserID)
	assert.Equal(t, store.SignInOIDC, got.Method)

	err = db.Identity().CreateSession(t.Context(), store.Session{Hash: "x", CreatedAt: now, ExpiresAt: now})
	assert.Error(t, err, "a session for nobody is refused")
}
