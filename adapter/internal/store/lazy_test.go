package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

// lazyCalls is every method Lazy forwards, named so a failure says which one.
// They are near identical wrappers, which is exactly where a call that reaches
// the wrong table would go unnoticed.
func lazyCalls(l *store.Lazy) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"Environments":      func(ctx context.Context) error { _, err := l.Environments(ctx); return err },
		"SaveEnvironment":   func(ctx context.Context) error { return l.SaveEnvironment(ctx, "eks-prod", `{"seen":true}`) },
		"RemoveEnvironment": func(ctx context.Context) error { return l.RemoveEnvironment(ctx, "eks-prod") },
		"Tokens":            func(ctx context.Context) error { _, err := l.Tokens(ctx); return err },
		"SaveToken":         func(ctx context.Context) error { return l.SaveToken(ctx, "hash-a", `{"environment":"eks-prod"}`) },
		"RemoveTokens":      func(ctx context.Context) error { return l.RemoveTokens(ctx, []string{"hash-a"}) },
		"Work":              func(ctx context.Context) error { _, err := l.Work(ctx); return err },
		"SaveWork":          func(ctx context.Context) error { return l.SaveWork(ctx, "eks-prod", `[]`) },
		"SupportAccess":     func(ctx context.Context) error { _, err := l.SupportAccess(ctx); return err },
		"SaveSupportAccess": func(ctx context.Context) error { return l.SaveSupportAccess(ctx, "eks-prod", `{}`) },
		"CloneKeys":         func(ctx context.Context) error { _, err := l.CloneKeys(ctx); return err },
		"SaveCloneKey":      func(ctx context.Context) error { return l.SaveCloneKey(ctx, "eks-prod", "identity") },
		"RemoveCloneKey":    func(ctx context.Context) error { return l.RemoveCloneKey(ctx, "eks-prod") },
		"Connection":        func(ctx context.Context) error { _, err := l.Connection(ctx, "servicenow"); return err },
		"SaveConnection": func(ctx context.Context) error {
			return l.SaveConnection(ctx, "servicenow", map[string]string{"SN_API_KEY": "key"})
		},
		"RemoveConnection": func(ctx context.Context) error { return l.RemoveConnection(ctx, "servicenow") },
	}
}

func TestLazyReportsNotConnectedBeforeTheDatabaseIsUp(t *testing.T) {
	// Port 1 is reserved and nothing listens on it, so the attempt never lands
	// and every call below is the window a first install spends waiting.
	starter := store.Start(t.Context(), store.Options{
		URL:            "postgres://hub:hunter2@127.0.0.1:1/hub?sslmode=disable",
		ConnectTimeout: time.Second,
		RetryBackoff:   time.Hour,
	})
	t.Cleanup(func() { _ = starter.Close() })

	lazy := store.NewLazy(starter, 1, nil)
	assert.Nil(t, lazy.State())

	for name, call := range lazyCalls(lazy) {
		t.Run(name, func(t *testing.T) {
			require.Error(t, call(t.Context()), "a call before the connection lands must not look like success")
		})
	}
}

func TestLazyForwardsEveryCallOnceConnected(t *testing.T) {
	url := liveURL(t)
	cipher, err := store.NewCipher(testKey(60))
	require.NoError(t, err)

	starter := store.Start(t.Context(), store.Options{URL: url})
	t.Cleanup(func() { _ = starter.Close() })

	lazy := store.NewLazy(starter, nextTeam(t, openLive(t)), cipher)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	state, err := lazy.Wait(ctx)
	require.NoError(t, err)
	require.NotNil(t, state)

	for name, call := range lazyCalls(lazy) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, call(ctx))
		})
	}
}

func TestLazyReachesTheSameRowsAsTheStateItWraps(t *testing.T) {
	url := liveURL(t)
	cipher, err := store.NewCipher(testKey(61))
	require.NoError(t, err)
	team := nextTeam(t, openLive(t))

	starter := store.Start(t.Context(), store.Options{URL: url})
	t.Cleanup(func() { _ = starter.Close() })

	lazy := store.NewLazy(starter, team, cipher)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	state, err := lazy.Wait(ctx)
	require.NoError(t, err)

	require.NoError(t, lazy.SaveEnvironment(ctx, "eks-prod", `{"namespace":"pds"}`))
	require.NoError(t, lazy.SaveToken(ctx, "hash-a", `{"environment":"eks-prod"}`))
	require.NoError(t, lazy.SaveWork(ctx, "eks-prod", `[{"id":"w1"}]`))
	require.NoError(t, lazy.SaveCloneKey(ctx, "eks-prod", "identity"))
	require.NoError(t, lazy.SaveConnection(ctx, "servicenow", map[string]string{"SN_API_KEY": "key"}))

	// Each write has to have reached its own table, not a neighbour's.
	environments, err := state.Environments(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `{"namespace":"pds"}`, environments["eks-prod"])

	tokens, err := state.Tokens(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `{"environment":"eks-prod"}`, tokens["hash-a"])

	work, err := state.Work(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"id":"w1"}]`, work["eks-prod"])

	keys, err := state.CloneKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, "identity", keys["eks-prod"])

	conn, err := state.Connection(ctx, "servicenow")
	require.NoError(t, err)
	assert.Equal(t, "key", conn["SN_API_KEY"])

	require.NoError(t, lazy.RemoveEnvironment(ctx, "eks-prod"))
	require.NoError(t, lazy.RemoveTokens(ctx, []string{"hash-a"}))
	require.NoError(t, lazy.RemoveCloneKey(ctx, "eks-prod"))
	require.NoError(t, lazy.RemoveConnection(ctx, "servicenow"))

	environments, err = state.Environments(ctx)
	require.NoError(t, err)
	assert.Empty(t, environments)
	tokens, err = state.Tokens(ctx)
	require.NoError(t, err)
	assert.Empty(t, tokens)
	keys, err = state.CloneKeys(ctx)
	require.NoError(t, err)
	assert.Empty(t, keys)
	conn, err = state.Connection(ctx, "servicenow")
	require.NoError(t, err)
	assert.Empty(t, conn)
}

func TestLazyWaitGivesUpWithTheContext(t *testing.T) {
	starter := store.Start(t.Context(), store.Options{
		URL:            "postgres://hub:hunter2@127.0.0.1:1/hub?sslmode=disable",
		ConnectTimeout: time.Second,
		RetryBackoff:   time.Hour,
	})
	t.Cleanup(func() { _ = starter.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	_, err := store.NewLazy(starter, 1, nil).Wait(ctx)

	require.Error(t, err)
}

func TestLazyOnNothingIsNotAPanic(t *testing.T) {
	var lazy *store.Lazy

	assert.Nil(t, lazy.State())

	_, err := lazy.Environments(t.Context())
	require.Error(t, err)
}

func TestLazyForNamesAnotherTeam(t *testing.T) {
	base := store.NewLazy(nil, 1, nil)

	other := base.For(2)

	require.NotNil(t, other)
	assert.Equal(t, uint(2), other.Team())
	assert.Equal(t, uint(1), base.Team(), "the original keeps its own team")
}

func TestLazyForRefusesWhenNoTeamIsNamed(t *testing.T) {
	base := store.NewLazy(nil, 1, nil)

	// Team zero is a request that carried no team. It must not inherit the
	// pinned team's rows: HubState refuses a blank team, so this state reads and
	// writes nothing until a caller names one.
	assert.Zero(t, base.For(0).Team())
}

func TestLazyForOnANilLazyStaysNil(t *testing.T) {
	var base *store.Lazy

	assert.Nil(t, base.For(2))
}

func TestLazyNamesTheIDOfAManagedEnvironmentAndNoOtherKind(t *testing.T) {
	url := liveURL(t)
	team := nextTeam(t, openLive(t))
	starter := store.Start(t.Context(), store.Options{URL: url})
	t.Cleanup(func() { _ = starter.Close() })
	lazy := store.NewLazy(starter, team, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err := lazy.Wait(ctx)
	require.NoError(t, err)
	_, err = starter.ManagedEnvironments().Create(ctx, managedInput(team, "k3j9x2m4p1q7", "orders"), time.Now())
	require.NoError(t, err)

	id, err := lazy.ManagedEnvironmentID(ctx, "orders")
	require.NoError(t, err)
	assert.Equal(t, "k3j9x2m4p1q7", id)

	_, err = lazy.ManagedEnvironmentID(ctx, "connected-one")
	assert.ErrorIs(t, err, store.ErrManagedEnvironmentUnknown)
}
