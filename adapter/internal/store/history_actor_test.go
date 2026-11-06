package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

func TestHistoryRowKeepsTheUserAndTeamIDs(t *testing.T) {
	db, err := store.Open(t.Context(), store.Options{URL: liveURL(t)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Migrate(t.Context()))

	team := nextTeam(t, db)
	state := db.Hub(team, nil)
	user := uint(41)

	inserted, err := state.InsertHistory(t.Context(), store.HistoryRow{
		Kind: "action", Action: "database.create", UserID: &user,
	})
	require.NoError(t, err)

	got, err := state.ActionByID(t.Context(), inserted.ID)
	require.NoError(t, err)
	require.NotNil(t, got.UserID)
	assert.Equal(t, user, *got.UserID)
	assert.Equal(t, team, got.TeamID, "the row belongs to the team that wrote it")
	assert.Empty(t, got.Actor)
}

func TestUserNamesSkipsAUserWhoWasRemoved(t *testing.T) {
	db, err := store.Open(t.Context(), store.Options{URL: liveURL(t)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Migrate(t.Context()))

	suffix := time.Now().UnixNano()
	named := store.User{Issuer: "test", Subject: fmt.Sprintf("named-%d", suffix), Name: "Ann Lee", Username: "ann"}
	login := store.User{Issuer: "test", Subject: fmt.Sprintf("login-%d", suffix), Username: "bob"}
	gone := store.User{Issuer: "test", Subject: fmt.Sprintf("gone-%d", suffix), Name: "Cy"}
	for _, user := range []*store.User{&named, &login, &gone} {
		require.NoError(t, db.Gorm().Create(user).Error)
	}
	require.NoError(t, db.Gorm().Delete(&gone).Error)

	names, err := db.Hub(nextTeam(t, db), nil).UserNames(t.Context(), []uint{named.ID, login.ID, gone.ID})
	require.NoError(t, err)

	assert.Equal(t, map[uint]string{named.ID: "Ann Lee", login.ID: "bob"}, names)
}
