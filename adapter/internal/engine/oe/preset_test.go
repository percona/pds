package oe

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
)

func TestPresetName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "pds-mysql-dev", PresetName(MySQL.ID, "dev"))
	assert.Equal(t, "pds-postgresql-prod", PresetName(PostgreSQL.ID, "Prod"))
}

func TestParsePresetName(t *testing.T) {
	t.Parallel()

	engineID, profile, ok := ParsePresetName("pds-mysql-staging")
	assert.True(t, ok)
	assert.Equal(t, "mysql", engineID)
	assert.Equal(t, ProfileStaging, profile)

	_, _, ok = ParsePresetName("someone-elses-preset")
	assert.False(t, ok)
}

func TestCatalogPresetMatchesTheProfileTable(t *testing.T) {
	t.Parallel()

	profile, err := lookupProfile(ProfileDev)
	require.NoError(t, err)

	got := CatalogPreset(MySQL, profile)
	assert.Equal(t, "pds-mysql-dev", got.Metadata.Name)
	assert.Equal(t, PresetVersion, got.Metadata.Annotations[AnnotationPresetVersion])
	assert.Equal(t, everest.ProviderPXC, got.Spec.ProviderRef.Name)
	assert.Equal(t, int32(1), got.EngineComponent().ReplicaCount())
	assert.Equal(t, "1", got.EngineComponent().CPU())
	assert.Equal(t, "2Gi", got.EngineComponent().Memory())
	assert.Equal(t, "20Gi", got.EngineComponent().StorageSize())
	assert.Equal(t, everest.ComponentTypeHAProxy, got.ProxyComponent().Type)
}

func TestLookupFlavorServesMongoDB(t *testing.T) {
	t.Parallel()

	got, ok := LookupFlavor("mongodb")
	require.True(t, ok)
	assert.Equal(t, "percona-server-mongodb", got.Provider)
	assert.Equal(t, "replicaSet", got.Topology)
	assert.Empty(t, got.ProxyType)
	assert.Equal(t, "percona-backup-mongodb", got.BackupClass)
	assert.Equal(t, 27017, got.DefaultPort)
	assert.Equal(t, "mongodb", got.URIScheme)
}

func TestBackupClassForFollowsTheProvider(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "percona-backup-mongodb", BackupClassFor(everest.ProviderMongoDB))
	assert.Equal(t, "pg", BackupClassFor(everest.ProviderPostgreSQL))
	assert.Equal(t, "pxc", BackupClassFor(everest.ProviderPXC))
}

func TestMongoDBCatalogPresetHasNoProxy(t *testing.T) {
	t.Parallel()

	got, ok := LookupCatalogPreset("pds-mongodb-staging")
	require.True(t, ok)
	assert.Equal(t, int32(3), got.EngineComponent().ReplicaCount())
	assert.NotContains(t, got.Spec.Components, everest.ComponentProxy)
}

func TestHelmSnapshotMatchesCatalog(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join(findRepoRoot(t),
		"charts/pds/tests/template.instance-presets.snap.yaml"))
	require.NoError(t, err)
	snap := string(body)

	for _, name := range []string{
		"pds-mysql-dev", "pds-mysql-staging", "pds-mysql-prod",
		"pds-postgresql-dev", "pds-postgresql-staging", "pds-postgresql-prod",
		"pds-mongodb-dev", "pds-mongodb-staging", "pds-mongodb-prod",
	} {
		catalog, ok := LookupCatalogPreset(name)
		require.True(t, ok, name)

		idx := strings.Index(snap, "name: "+name)
		require.Greater(t, idx, -1, name)
		window := snap[idx:]
		if next := strings.Index(window[len("name: "+name):], "name: pds-"); next >= 0 {
			window = window[:len("name: "+name)+next]
		}

		eng := catalog.EngineComponent()
		assert.Contains(t, window, "cpu: \""+eng.CPU()+"\"", name)
		assert.Contains(t, window, "memory: "+eng.Memory(), name)
		assert.Contains(t, window, "size: "+eng.StorageSize(), name)
		assert.Contains(t, window, "replicas: "+strconv.Itoa(int(eng.ReplicaCount())), name)
		assert.Contains(t, window, "pds.percona.com/preset-version: \""+PresetVersion+"\"", name)
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "charts/pds/tests/template.instance-presets.snap.yaml")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("charts/pds/tests/template.instance-presets.snap.yaml not found")

	return ""
}
