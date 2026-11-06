package hub_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/hub"
)

// DirStore is what the hub actually runs on, while every service test wires
// MemoryStore. The two differ exactly where it matters: MemoryStore keys are
// opaque strings, DirStore joins them onto a root on disk.

func TestDirStoreWriteGetListDelete(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := hub.NewDirStore(root)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, store.Write(ctx, map[string][]byte{
		"clusters/spoke-a/instances/orders.yaml": []byte("orders\n"),
		"clusters/spoke-a/instances/carts.yaml":  []byte("carts\n"),
	}, "create"))

	body, err := store.Get(ctx, "clusters/spoke-a/instances/orders.yaml")
	require.NoError(t, err)
	assert.Equal(t, "orders\n", string(body))

	paths, err := store.List(ctx, "clusters/spoke-a/instances/")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"clusters/spoke-a/instances/carts.yaml",
		"clusters/spoke-a/instances/orders.yaml",
	}, paths)

	require.NoError(t, store.Delete(ctx, []string{"clusters/spoke-a/instances/carts.yaml"}, "delete"))
	_, err = store.Get(ctx, "clusters/spoke-a/instances/carts.yaml")
	require.ErrorIs(t, err, hub.ErrNotFound)
}

func TestDirStoreMissingPathsAreNotFailures(t *testing.T) {
	t.Parallel()

	store, err := hub.NewDirStore(t.TempDir())
	require.NoError(t, err)
	ctx := context.Background()

	_, err = store.Get(ctx, "clusters/spoke-a/instances/absent.yaml")
	require.ErrorIs(t, err, hub.ErrNotFound)

	// A spoke that has never had a database has no directory yet, and the
	// hub lists that prefix on every read.
	paths, err := store.List(ctx, "clusters/spoke-a/instances/")
	require.NoError(t, err)
	assert.Empty(t, paths)

	// Delete is how a create rolls itself back, so a path that is already
	// gone must not fail the caller.
	require.NoError(t, store.Delete(ctx, []string{"clusters/spoke-a/instances/absent.yaml"}, "delete"))
}

// TestDirStoreRefusesPathsOutsideRoot is the last line behind name validation:
// nothing a request can name may resolve outside the desired-state tree.
func TestDirStoreRefusesPathsOutsideRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := hub.NewDirStore(root)
	require.NoError(t, err)
	ctx := context.Background()

	escapes := map[string]string{
		"parent":        "../escaped.yaml",
		"deep parent":   "clusters/spoke-a/instances/../../../../escaped.yaml",
		"absolute-ish":  "../../etc/escaped.yaml",
		"bare traverse": "..",
	}

	for name, path := range escapes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, store.Write(ctx, map[string][]byte{path: []byte("x")}, "write"), hub.ErrOutsideRoot)

			_, getErr := store.Get(ctx, path)
			require.ErrorIs(t, getErr, hub.ErrOutsideRoot)

			require.ErrorIs(t, store.Delete(ctx, []string{path}, "delete"), hub.ErrOutsideRoot)

			_, listErr := store.List(ctx, path)
			require.ErrorIs(t, listErr, hub.ErrOutsideRoot)
		})
	}

	// Nothing landed next to the root either.
	siblings, err := os.ReadDir(filepath.Dir(root))
	require.NoError(t, err)
	for _, entry := range siblings {
		assert.NotEqual(t, "escaped.yaml", entry.Name())
	}
}

// A symlink already under the store root can otherwise redirect writes to
// a directory the lexical join still treats as inside the tree.
func TestDirStoreRefusesSymlinkEscape(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "clusters"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "clusters", "spoke-a")))

	store, err := hub.NewDirStore(root)
	require.NoError(t, err)
	ctx := context.Background()
	path := "clusters/spoke-a/cluster.yaml"

	require.ErrorIs(t, store.Write(ctx, map[string][]byte{path: []byte("x")}, "write"), hub.ErrOutsideRoot)

	_, getErr := store.Get(ctx, path)
	require.ErrorIs(t, getErr, hub.ErrOutsideRoot)

	require.ErrorIs(t, store.Delete(ctx, []string{path}, "delete"), hub.ErrOutsideRoot)

	_, listErr := store.List(ctx, "clusters/spoke-a/")
	require.ErrorIs(t, listErr, hub.ErrOutsideRoot)

	_, statErr := os.Stat(filepath.Join(outside, "cluster.yaml"))
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))
}

func TestGitDirKeepsTwoTeamsFromSharingAnEnvironmentTree(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx := context.Background()
	path := "clusters/prod/instances/kustomization.yaml"
	acme, err := hub.NewDirStore(hub.GitDir(root, 1, true))
	require.NoError(t, err)
	globex, err := hub.NewDirStore(hub.GitDir(root, 2, true))
	require.NoError(t, err)

	require.NoError(t, acme.Write(ctx, map[string][]byte{path: []byte("acme\n")}, "seed"))
	require.NoError(t, globex.Write(ctx, map[string][]byte{path: []byte("globex\n")}, "seed"))

	body, err := acme.Get(ctx, path)
	require.NoError(t, err)
	assert.Equal(t, "acme\n", string(body))
	body, err = globex.Get(ctx, path)
	require.NoError(t, err)
	assert.Equal(t, "globex\n", string(body))

	assert.FileExists(t, filepath.Join(root, "1", path))
	assert.FileExists(t, filepath.Join(root, "2", path))
}
