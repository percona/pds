package store_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

// addMember puts a User into an Organization's Team with a role.
func addMember(t *testing.T, db *store.Store, org uint, team, role, subject string) store.User {
	t.Helper()

	row, err := db.Identity().Team(t.Context(), org, team)
	require.NoError(t, err)
	now := time.Now().UTC()
	user := store.User{Issuer: signupIssuer, Subject: subject, Name: subject, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Gorm().Create(&user).Error)
	require.NoError(t, db.Gorm().Create(&store.Membership{
		UserID: user.ID, OrganizationID: org, TeamID: row.ID, Role: role, CreatedAt: now,
	}).Error)

	return user
}

func newOrganizationWithAdmin(t *testing.T) (*store.Store, store.Placement) {
	t.Helper()

	db := newSignupStore(t)
	admin, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|ada", "Acme"), time.Now())
	require.NoError(t, err)

	return db, admin
}

func TestCreateTeamAddsATeamOfItsOwn(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)

	team, err := db.Identity().CreateTeam(t.Context(), admin.OrganizationID, " payments ", time.Now())
	require.NoError(t, err)

	assert.Equal(t, "payments", team.Name)
	assert.NotZero(t, team.ID)
	assert.NotEqual(t, admin.TeamID, team.ID)
}

func TestCreateTeamRefusesADuplicateNameInTheOrganizationOnly(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	other, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|grace", "Globex"), time.Now())
	require.NoError(t, err)

	_, err = db.Identity().CreateTeam(t.Context(), admin.OrganizationID, "default", time.Now())
	assert.ErrorIs(t, err, store.ErrTeamExists)

	_, err = db.Identity().CreateTeam(t.Context(), other.OrganizationID, "payments", time.Now())
	assert.NoError(t, err)
	_, err = db.Identity().CreateTeam(t.Context(), admin.OrganizationID, "payments", time.Now())
	assert.NoError(t, err, "another Organization's Team does not take the name")
}

func TestRenameTeamKeepsTheIDAndRefusesATakenOrUnknownName(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	made, err := db.Identity().CreateTeam(t.Context(), admin.OrganizationID, "payments", time.Now())
	require.NoError(t, err)

	require.NoError(t, db.Identity().RenameTeam(t.Context(), admin.OrganizationID, "payments", "billing"))

	got, err := db.Identity().Team(t.Context(), admin.OrganizationID, "billing")
	require.NoError(t, err)
	assert.Equal(t, made.ID, got.ID, "the Team's rows stay where they were written")
	assert.ErrorIs(t, db.Identity().RenameTeam(t.Context(), admin.OrganizationID, "billing", "default"), store.ErrTeamExists)
	assert.ErrorIs(t, db.Identity().RenameTeam(t.Context(), admin.OrganizationID, "nope", "x"), store.ErrTeamUnknown)
	assert.ErrorIs(t, db.Identity().RenameTeam(t.Context(), admin.OrganizationID+99, "billing", "x"), store.ErrTeamUnknown)
}

func TestMembersListsTheOrganizationsUsersOnly(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	addMember(t, db, admin.OrganizationID, "default", store.RoleUser, "auth0|bob")
	_, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|grace", "Globex"), time.Now())
	require.NoError(t, err)

	members, err := db.Identity().Members(t.Context(), admin.OrganizationID)
	require.NoError(t, err)

	require.Len(t, members, 2)
	assert.Equal(t, store.RoleAdmin, members[0].Role)
	assert.Equal(t, "default", members[0].TeamName)
	assert.Equal(t, "auth0|bob", members[1].Name)
}

func TestUpdateMemberMovesTeamAndChangesRole(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	bob := addMember(t, db, admin.OrganizationID, "default", store.RoleUser, "auth0|bob")
	_, err := db.Identity().CreateTeam(t.Context(), admin.OrganizationID, "payments", time.Now())
	require.NoError(t, err)

	require.NoError(t, db.Identity().UpdateMember(t.Context(), admin.OrganizationID, bob.ID, "payments", store.RoleAdmin))

	placement, err := db.Identity().PlacementOf(t.Context(), bob.ID, admin.OrganizationID)
	require.NoError(t, err)
	assert.Equal(t, "payments", placement.TeamName)
	assert.Equal(t, store.RoleAdmin, placement.Role)

	require.NoError(t, db.Identity().UpdateMember(t.Context(), admin.OrganizationID, bob.ID, "", store.RoleUser))
	placement, err = db.Identity().PlacementOf(t.Context(), bob.ID, admin.OrganizationID)
	require.NoError(t, err)
	assert.Equal(t, "payments", placement.TeamName, "an empty team leaves the Team alone")
	assert.Equal(t, store.RoleUser, placement.Role)
}

func TestUpdateMemberRefusesWhatCannotApply(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	bob := addMember(t, db, admin.OrganizationID, "default", store.RoleUser, "auth0|bob")
	stranger, err := db.Identity().CreateOrganization(t.Context(), newcomer("auth0|grace", "Globex"), time.Now())
	require.NoError(t, err)

	assert.ErrorIs(t, db.Identity().UpdateMember(t.Context(), admin.OrganizationID, bob.ID, "nope", ""), store.ErrTeamUnknown)
	assert.ErrorIs(t, db.Identity().UpdateMember(t.Context(), admin.OrganizationID, stranger.UserID, "", store.RoleUser), store.ErrNoMembership,
		"a User of another Organization is not found")
	assert.ErrorIs(t, db.Identity().UpdateMember(t.Context(), admin.OrganizationID, bob.ID, "", "owner"), store.ErrRoleUnknown)
}

func TestTheLastAdminCannotBeDemotedOrRemoved(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)

	err := db.Identity().UpdateMember(t.Context(), admin.OrganizationID, admin.UserID, "", store.RoleUser)
	assert.ErrorIs(t, err, store.ErrLastAdmin)
	err = db.Identity().RemoveMember(t.Context(), admin.OrganizationID, admin.UserID)
	assert.ErrorIs(t, err, store.ErrLastAdmin)

	bob := addMember(t, db, admin.OrganizationID, "default", store.RoleAdmin, "auth0|bob")
	require.NoError(t, db.Identity().UpdateMember(t.Context(), admin.OrganizationID, admin.UserID, "", store.RoleUser),
		"with a second admin the first may step down")
	err = db.Identity().RemoveMember(t.Context(), admin.OrganizationID, bob.ID)
	assert.ErrorIs(t, err, store.ErrLastAdmin, "and now bob is the last")
}

func TestRemoveMemberEndsAccessAndLeavesOtherOrganizations(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	bob := addMember(t, db, admin.OrganizationID, "default", store.RoleUser, "auth0|bob")

	require.NoError(t, db.Identity().RemoveMember(t.Context(), admin.OrganizationID, bob.ID))

	_, err := db.Identity().PlacementOf(t.Context(), bob.ID, admin.OrganizationID)
	assert.ErrorIs(t, err, store.ErrNoMembership)
	assert.ErrorIs(t, db.Identity().RemoveMember(t.Context(), admin.OrganizationID, bob.ID), store.ErrNoMembership)
}

func TestDeleteTeamIsRefusedWhileUsersRemain(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	made, err := db.Identity().CreateTeam(t.Context(), admin.OrganizationID, "payments", time.Now())
	require.NoError(t, err)
	bob := addMember(t, db, admin.OrganizationID, "payments", store.RoleUser, "auth0|bob")

	assert.ErrorIs(t, db.Identity().DeleteTeam(t.Context(), admin.OrganizationID, made.Name), store.ErrTeamHasUsers)

	require.NoError(t, db.Identity().RemoveMember(t.Context(), admin.OrganizationID, bob.ID))
	assert.NoError(t, db.Identity().DeleteTeam(t.Context(), admin.OrganizationID, made.Name))
}

func TestTwoAdminsStepDownAtOnceLeavesOne(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	bob := addMember(t, db, admin.OrganizationID, "default", store.RoleAdmin, "auth0|bob")

	var wg sync.WaitGroup
	results := make([]error, 2)
	for n, id := range []uint{admin.UserID, bob.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[n] = db.Identity().UpdateMember(t.Context(), admin.OrganizationID, id, "", store.RoleUser)
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, countNil(results), "one demotion lands and the other is refused: %v", results)
	assert.Equal(t, int64(1), count(t, db, &store.Membership{}, "organization_id = ? AND role = ?", admin.OrganizationID, store.RoleAdmin))
}

func countNil(errs []error) int {
	n := 0
	for _, err := range errs {
		if err == nil {
			n++
		}
	}

	return n
}
