package hub_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/hub"
)

func TestMemoryStoreWriteGetDelete(t *testing.T) {
	store := hub.NewMemoryStore()
	ctx := context.Background()
	path := "clusters/spoke-a/instances/orders-dev.yaml"

	require.NoError(t, store.Write(ctx, map[string][]byte{path: []byte("kind: Instance\n")}, "add instance"))

	got, err := store.Get(ctx, path)
	require.NoError(t, err)
	assert.Equal(t, "kind: Instance\n", string(got))

	require.NoError(t, store.Delete(ctx, []string{path}, "remove instance"))
	_, err = store.Get(ctx, path)
	require.ErrorIs(t, err, hub.ErrNotFound)
}
