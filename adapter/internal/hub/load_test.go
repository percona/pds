package hub_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/hub"
)

func TestLoadInventoryRegistersEnvironmentYAML(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "clusters", "spoke-a")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cluster.yaml"), []byte("name: spoke-a\nnamespace: pds\n"), 0o644))

	inv := hub.NewInventory(time.Minute)
	require.NoError(t, hub.LoadInventory(root, inv))
	got, ok := inv.Get("spoke-a", time.Now())
	require.True(t, ok)
	assert.Equal(t, "spoke-a", got.Name)
	assert.Equal(t, hub.StatusUnreachable, got.Status)
}
