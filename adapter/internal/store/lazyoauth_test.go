package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

func TestLazyOAuthReportsNotConnectedBeforeTheDatabaseLands(t *testing.T) {
	lazy := store.NewLazyOAuth(nil)
	ctx := t.Context()
	now := time.Now()

	require.ErrorIs(t, lazy.CreateClient(ctx, store.OAuthClient{}), store.ErrNotConnected)
	_, err := lazy.Client(ctx, "x")
	require.ErrorIs(t, err, store.ErrNotConnected)
	_, err = lazy.UnusedClients(ctx)
	require.ErrorIs(t, err, store.ErrNotConnected)
	require.ErrorIs(t, lazy.MarkClientUsed(ctx, "x"), store.ErrNotConnected)
	require.ErrorIs(t, lazy.PurgeClients(ctx, now), store.ErrNotConnected)
	require.ErrorIs(t, lazy.CreateRequest(ctx, store.OAuthRequest{}), store.ErrNotConnected)
	_, err = lazy.Request(ctx, "x", now)
	require.ErrorIs(t, err, store.ErrNotConnected)
	_, err = lazy.TakeRequest(ctx, "x", now)
	require.ErrorIs(t, err, store.ErrNotConnected)
	require.ErrorIs(t, lazy.CreateCode(ctx, store.OAuthCode{}), store.ErrNotConnected)
	_, err = lazy.UseCode(ctx, "x", now)
	require.ErrorIs(t, err, store.ErrNotConnected)
	require.ErrorIs(t, lazy.CreateAgentSession(ctx, store.AgentSession{}, nil, ""), store.ErrNotConnected)
	_, _, err = lazy.TokenSession(ctx, "x")
	require.ErrorIs(t, err, store.ErrNotConnected)
	require.ErrorIs(t, lazy.RotateRefresh(ctx, "x", now, nil), store.ErrNotConnected)
	require.ErrorIs(t, lazy.TouchAgentSession(ctx, "x", now), store.ErrNotConnected)
	require.ErrorIs(t, lazy.DeleteAgentSession(ctx, "x"), store.ErrNotConnected)
	require.ErrorIs(t, lazy.PurgeOAuth(ctx, now), store.ErrNotConnected)
}
