package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/store"
)

func TestTokenTeamReturnsTheMintingTeam(t *testing.T) {
	db := openLive(t)
	ctx := t.Context()

	firstTeam := nextTeam(t, db)
	secondTeam := nextTeam(t, db)
	first := db.Hub(firstTeam, nil)
	second := db.Hub(secondTeam, nil)

	hashA := hub.HashToken("jnt_team_a")
	hashB := hub.HashToken("jnt_team_b")
	require.NotEqual(t, hashA, hashB)

	require.NoError(t, first.SaveToken(ctx, hashA, `{"environment":"prod"}`))
	require.NoError(t, second.SaveToken(ctx, hashB, `{"environment":"prod"}`))

	gotA, err := db.TokenTeam(ctx, hashA)
	require.NoError(t, err)
	assert.Equal(t, firstTeam, gotA)

	gotB, err := db.TokenTeam(ctx, hashB)
	require.NoError(t, err)
	assert.Equal(t, secondTeam, gotB)
	assert.NotEqual(t, gotA, gotB)
}

func TestTokenTeamUnknownHashIsNotFound(t *testing.T) {
	db := openLive(t)

	team, err := db.TokenTeam(t.Context(), hub.HashToken("jnt_missing"))
	assert.Zero(t, team)
	require.ErrorIs(t, err, store.ErrTokenUnknown)
}

func TestTokenTeamCollisionDoesNotPickATeam(t *testing.T) {
	db := openLive(t)
	ctx := t.Context()
	hash := hub.HashToken("jnt_collision")
	first := nextTeam(t, db)
	second := nextTeam(t, db)

	require.NoError(t, db.Hub(first, nil).SaveToken(ctx, hash, `{"environment":"a"}`))
	require.NoError(t, db.Hub(second, nil).SaveToken(ctx, hash, `{"environment":"b"}`))

	team, err := db.TokenTeam(ctx, hash)
	assert.Zero(t, team)
	require.Error(t, err)
	require.ErrorIs(t, err, store.ErrTokenCollision)
	require.NotErrorIs(t, err, store.ErrTokenUnknown)
}

func TestStarterTokenTeamReportsNotConnected(t *testing.T) {
	starter := store.Start(t.Context(), store.Options{
		URL:            "postgres://hub:hunter2@127.0.0.1:1/hub?sslmode=disable",
		ConnectTimeout: time.Second,
		RetryBackoff:   time.Hour,
	})
	t.Cleanup(func() { _ = starter.Close() })

	team, err := starter.TokenTeam(t.Context(), hub.HashToken("jnt_pending"))
	assert.Zero(t, team)
	require.Error(t, err)
}

func TestStarterTokenTeamReturnsTheMintingTeam(t *testing.T) {
	url := liveURL(t)
	starter := store.Start(t.Context(), store.Options{URL: url})
	t.Cleanup(func() { _ = starter.Close() })

	lazy := store.NewLazy(starter, nextTeam(t, openLive(t)), nil)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	state, err := lazy.Wait(ctx)
	require.NoError(t, err)

	hash := hub.HashToken("jnt_starter")
	require.NoError(t, state.SaveToken(ctx, hash, `{"environment":"prod"}`))

	got, err := starter.TokenTeam(ctx, hash)
	require.NoError(t, err)
	assert.Equal(t, lazy.Team(), got)
}

func TestAnAgentTokenAndItsEnvironmentOutliveTheUserWhoMintedThem(t *testing.T) {
	install := newInstall(t)
	install.seed("acme", "$2a$hash", 0)
	db := install.open("acme")
	require.NoError(t, db.Migrate(t.Context()))
	ctx := t.Context()
	team, err := db.Identity().ServedTeam(ctx, "acme")
	require.NoError(t, err)
	placement, err := db.Identity().Placement(ctx, "acme")
	require.NoError(t, err)

	require.NoError(t, db.Gorm().Where("user_id = ?", placement.UserID).Delete(&store.Membership{}).Error)
	require.NoError(t, db.Gorm().Delete(&store.User{}, placement.UserID).Error)

	got, err := db.TokenTeam(ctx, "hash-acme")
	require.NoError(t, err)
	assert.Equal(t, team.ID, got, "the token still names the Team")
	environments, err := db.Hub(team.ID, nil).Environments(ctx)
	require.NoError(t, err)
	assert.Contains(t, environments, "eks-prod", "the environment is still connected")
}
