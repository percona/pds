package store_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestARepoNameBelongsToTheFirstOwnerThatClaimsIt(t *testing.T) {
	db := openLive(t)
	ctx := t.Context()
	repo := "claim-" + nextLogin(t)

	won, fresh, err := db.ClaimRepo(ctx, repo, "acme/prod")
	require.NoError(t, err)
	assert.True(t, won)
	assert.True(t, fresh)

	won, fresh, err = db.ClaimRepo(ctx, repo, "acme/prod")
	require.NoError(t, err)
	assert.True(t, won, "the owner claiming again keeps it, so a retried join reuses its name")
	assert.False(t, fresh, "and it is told the name was already its own")

	won, _, err = db.ClaimRepo(ctx, repo, "globex/prod")
	require.NoError(t, err)
	assert.False(t, won, "another owner is refused")
}

func TestOnlyOneOfManyRacingOwnersWinsARepoName(t *testing.T) {
	db := openLive(t)
	repo := "race-" + nextLogin(t)

	var wins atomic.Int32
	var wg sync.WaitGroup
	for _, owner := range []string{"a/x", "b/x", "c/x", "d/x", "e/x", "f/x"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, _, err := db.ClaimRepo(t.Context(), repo, owner)
			assert.NoError(t, err)
			if won {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), wins.Load())
}

func TestReleasingARepoNameLetsAnotherOwnerClaimIt(t *testing.T) {
	db := openLive(t)
	ctx := t.Context()
	repo := "release-" + nextLogin(t)
	_, _, err := db.ClaimRepo(ctx, repo, "acme/prod")
	require.NoError(t, err)

	require.NoError(t, db.ReleaseRepo(ctx, repo, "globex/prod"), "a non owner releases nothing")
	won, _, err := db.ClaimRepo(ctx, repo, "globex/prod")
	require.NoError(t, err)
	assert.False(t, won)

	require.NoError(t, db.ReleaseRepo(ctx, repo, "acme/prod"))
	won, _, err = db.ClaimRepo(ctx, repo, "globex/prod")
	require.NoError(t, err)
	assert.True(t, won)
}
