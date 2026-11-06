package hub

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRegistryService() *Service {
	return NewService(NewMemoryStore(), NewInventory(time.Minute), Options{
		Tokens: NewTokenStore(nil, 0),
	})
}

func TestRegistryGetRejectsABlankTeam(t *testing.T) {
	t.Parallel()

	called := false
	reg := NewRegistry(func(context.Context, uint) (*Service, error) {
		called = true

		return newRegistryService(), nil
	})

	got, err := reg.Get(t.Context(), 0)
	require.Error(t, err)
	assert.Nil(t, got)
	assert.False(t, called, "a blank team must not share or invent a service")
}

func TestRegistryGetRetriesAFailedBuild(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	reg := NewRegistry(func(context.Context, uint) (*Service, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("the hub could not start")
		}

		return newRegistryService(), nil
	})

	got, err := reg.Get(t.Context(), 1)
	require.Error(t, err)
	assert.Nil(t, got)

	got, err = reg.Get(t.Context(), 1)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int32(2), calls.Load())
}

func TestRegistryGetReturnsOneServicePerTeam(t *testing.T) {
	t.Parallel()

	var builds atomic.Int32
	reg := NewRegistry(func(_ context.Context, _ uint) (*Service, error) {
		builds.Add(1)
		// Overlap concurrent Gets so the registry has to wait rather than
		// build twice for the same team.
		time.Sleep(20 * time.Millisecond)

		return newRegistryService(), nil
	})

	const n = 32
	acme := make([]*Service, n)
	globex := make([]*Service, n)
	var wg sync.WaitGroup
	errCh := make(chan error, 2*n)
	for i := range n {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			svc, err := reg.Get(t.Context(), 1)
			if err != nil {
				errCh <- err

				return
			}
			acme[i] = svc
		}(i)
		go func(i int) {
			defer wg.Done()
			svc, err := reg.Get(t.Context(), 2)
			if err != nil {
				errCh <- err

				return
			}
			globex[i] = svc
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	require.NotNil(t, acme[0])
	require.NotNil(t, globex[0])
	assert.NotSame(t, acme[0], globex[0])
	for i := range n {
		assert.Same(t, acme[0], acme[i])
		assert.Same(t, globex[0], globex[i])
	}
	assert.Equal(t, int32(2), builds.Load())
}

func TestRegistryIsolatesTeamState(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(func(context.Context, uint) (*Service, error) {
		return newRegistryService(), nil
	})
	ctx := t.Context()
	acme, err := reg.Get(ctx, 1)
	require.NoError(t, err)
	globex, err := reg.Get(ctx, 2)
	require.NoError(t, err)
	require.NotSame(t, acme, globex)

	token, err := acme.IssueJoin(ctx, "prod")
	require.NoError(t, err)
	require.NotEmpty(t, token)

	assert.Len(t, acme.Environments(), 1)
	assert.Equal(t, "prod", acme.Environments()[0].Name)
	assert.Empty(t, globex.Environments(), "team B must not see team A's environment")

	name, ok := acme.JoinEnvironment(ctx, token)
	assert.True(t, ok)
	assert.Equal(t, "prod", name)
	_, ok = globex.JoinEnvironment(ctx, token)
	assert.False(t, ok, "team B must not redeem team A's join token")

	require.NoError(t, acme.work.Push("prod", Work{Kind: WorkDelete, Database: "orders"}))
	assert.Equal(t, 1, acme.work.Pending("prod"))
	assert.Equal(t, 0, globex.work.Pending("prod"), "team B must not see team A's queued work")
}

func TestRegistryHoldsIndependentInventories(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(func(context.Context, uint) (*Service, error) {
		return newRegistryService(), nil
	})
	ctx := t.Context()
	acme, err := reg.Get(ctx, 1)
	require.NoError(t, err)
	globex, err := reg.Get(ctx, 2)
	require.NoError(t, err)

	_, err = acme.IssueJoin(ctx, "prod")
	require.NoError(t, err)

	assert.Len(t, acme.Environments(), 1)
	assert.Empty(t, globex.Environments())
}
