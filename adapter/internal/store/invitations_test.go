package store_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

func invitationTo(org uint, email, hash string) store.NewInvitation {
	return store.NewInvitation{
		OrganizationID: org, Team: "default", Role: store.RoleUser, Email: email, Hash: hash,
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

func TestTwoClaimsOfOneInvitationSpendItOnce(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	_, err := db.Identity().CreateInvitation(t.Context(), store.NewInvitation{
		OrganizationID: admin.OrganizationID, Team: "default", Role: store.RoleUser, AnyEmail: true, Hash: "h1",
		ExpiresAt: time.Now().Add(time.Hour),
	}, time.Now())
	require.NoError(t, err)

	var wg sync.WaitGroup
	results := make([]error, 2)
	for n, subject := range []string{"auth0|bob", "auth0|rob"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[n] = db.Identity().ClaimInvitation(t.Context(),
				store.Claim{Hash: "h1", Issuer: signupIssuer, Subject: subject}, time.Now())
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, countNil(results), "one claim lands and the other finds the link spent: %v", results)
	assert.Equal(t, int64(2), count(t, db, &store.Membership{}, "organization_id = ?", admin.OrganizationID),
		"the admin and exactly one invitee")
}

func TestARefusedClaimLeavesNoUserBehind(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	_, err := db.Identity().CreateInvitation(t.Context(), invitationTo(admin.OrganizationID, "bob@example.com", "h1"), time.Now())
	require.NoError(t, err)
	member := addMember(t, db, admin.OrganizationID, "default", store.RoleUser, "auth0|bob")

	_, err = db.Identity().ClaimInvitation(t.Context(),
		store.Claim{Hash: "h1", Issuer: signupIssuer, Subject: "auth0|bob"}, time.Now())

	assert.ErrorIs(t, err, store.ErrAlreadyMember)
	pending, err := db.Identity().PendingInvitations(t.Context(), admin.OrganizationID, time.Now())
	require.NoError(t, err)
	assert.Len(t, pending, 1, "the invitation stays unspent")
	assert.Equal(t, int64(1), count(t, db, &store.User{}, "id = ?", member.ID))
}

func TestAPasswordClaimNeverAttachesToAnExistingLogin(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	_, err := db.Identity().CreateInvitation(t.Context(), invitationTo(admin.OrganizationID, "bob@example.com", "h1"), time.Now())
	require.NoError(t, err)
	require.NoError(t, db.Gorm().Create(&store.User{
		Issuer: "password", Subject: "taken", Username: "taken", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}).Error)

	_, err = db.Identity().ClaimInvitation(t.Context(),
		store.Claim{Hash: "h1", Subject: "taken", PasswordHash: "$2a$04$x"}, time.Now())

	assert.ErrorIs(t, err, store.ErrLoginTaken)
}

func TestInvitingRefusesAnAddressThatAlreadyBelongs(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	require.NoError(t, db.Gorm().Model(&store.User{}).Where("id = ?", admin.UserID).
		Update("email", "Ada@Example.com").Error)

	_, err := db.Identity().CreateInvitation(t.Context(), invitationTo(admin.OrganizationID, "ada@example.com", "h1"), time.Now())

	assert.ErrorIs(t, err, store.ErrAlreadyMember)
}

func TestTwoInvitationsForOneAddressAtOnceLeaveOnePending(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)

	var wg sync.WaitGroup
	results := make([]error, 2)
	for n, hash := range []string{"h1", "h2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[n] = db.Identity().CreateInvitation(t.Context(),
				invitationTo(admin.OrganizationID, "bob@example.com", hash), time.Now())
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, countNil(results), "%v", results)
}
