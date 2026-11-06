package hub_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

// An environment that has just joined gets the PMM definition Flux applies, so PMM
// can be changed later by editing Git rather than reinstalling the platform.
func TestAJoinedEnvironmentGetsItsPMMDefinition(t *testing.T) {
	ctx := context.Background()
	store := hub.NewMemoryStore()
	svc := hubOverStore(t, store)

	_, err := svc.IssueJoin(ctx, "spoke-new")
	require.NoError(t, err)

	for _, name := range []string{"kustomization.yaml", "helmrepository.yaml", "helmrelease.yaml"} {
		_, err := store.Get(ctx, "clusters/spoke-new/platform/pmm/"+name)
		require.NoError(t, err, name)
	}
	body, err := store.Get(ctx, "clusters/spoke-new/platform/pmm/helmrelease.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), "version: 1.9.1")
}

func TestPMMSettingsReadBackWhatWasWritten(t *testing.T) {
	ctx := context.Background()
	svc := hubOverStore(t, hub.NewMemoryStore())
	_, err := svc.IssueJoin(ctx, "spoke-new")
	require.NoError(t, err)

	got, err := svc.PMMSettings(ctx, "spoke-new")
	require.NoError(t, err)
	assert.Equal(t, hub.DefaultPMMSettings(), got)

	want := hub.PMMSettings{
		Access:        hub.PMMAccessPublic,
		SourceRanges:  []string{"203.0.113.0/24", "198.51.100.7/32"},
		CPUMillicores: 1500,
		MemoryMi:      3072,
		StorageGi:     20,
	}
	require.NoError(t, svc.SetPMMSettings(ctx, "spoke-new", want))

	got, err = svc.PMMSettings(ctx, "spoke-new")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestPMMAccessReadsBackForEachChoice(t *testing.T) {
	ctx := context.Background()
	svc := hubOverStore(t, hub.NewMemoryStore())
	_, err := svc.IssueJoin(ctx, "spoke-new")
	require.NoError(t, err)

	for name, tt := range map[string]struct {
		in         hub.PMMSettings
		wantAccess string
		wantRanges []string
	}{
		"cluster only": {hub.DefaultPMMSettings(), hub.PMMAccessCluster, nil},
		// A private one with no ranges stores the private ranges it is limited
		// to, so what the panel shows is what the Service enforces.
		"private default ranges": {
			func() hub.PMMSettings { s := hub.DefaultPMMSettings(); s.Access = hub.PMMAccessPrivate; return s }(),
			hub.PMMAccessPrivate, []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
		},
		"private narrowed": {
			func() hub.PMMSettings {
				s := hub.DefaultPMMSettings()
				s.Access = hub.PMMAccessPrivate
				s.SourceRanges = []string{"10.4.0.0/16"}
				return s
			}(),
			hub.PMMAccessPrivate, []string{"10.4.0.0/16"},
		},
		"public": {
			func() hub.PMMSettings {
				s := hub.DefaultPMMSettings()
				s.Access = hub.PMMAccessPublic
				s.SourceRanges = []string{"0.0.0.0/0"}
				return s
			}(),
			hub.PMMAccessPublic, []string{"0.0.0.0/0"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, svc.SetPMMSettings(ctx, "spoke-new", tt.in))

			got, err := svc.PMMSettings(ctx, "spoke-new")
			require.NoError(t, err)
			assert.Equal(t, tt.wantAccess, got.Access)
			assert.Equal(t, tt.wantRanges, got.SourceRanges)
		})
	}
}

func TestSetPMMSettingsRefusesAPublicServiceWithNoAllowList(t *testing.T) {
	ctx := context.Background()
	svc := hubOverStore(t, hub.NewMemoryStore())
	_, err := svc.IssueJoin(ctx, "spoke-new")
	require.NoError(t, err)

	bad := hub.DefaultPMMSettings()
	bad.Access = hub.PMMAccessPublic
	err = svc.SetPMMSettings(ctx, "spoke-new", bad)

	require.ErrorIs(t, err, hub.ErrInvalidPMMSettings)
	got, err := svc.PMMSettings(ctx, "spoke-new")
	require.NoError(t, err)
	assert.Equal(t, hub.PMMAccessCluster, got.Access, "the refused write left the service closed")
}

func TestSetPMMSettingsRejectsInvalidSettings(t *testing.T) {
	ctx := context.Background()
	svc := hubOverStore(t, hub.NewMemoryStore())
	_, err := svc.IssueJoin(ctx, "spoke-new")
	require.NoError(t, err)

	bad := hub.DefaultPMMSettings()
	bad.StorageGi = 1
	err = svc.SetPMMSettings(ctx, "spoke-new", bad)
	require.ErrorIs(t, err, hub.ErrInvalidPMMSettings)

	// The rejected write left the stored settings alone.
	got, err := svc.PMMSettings(ctx, "spoke-new")
	require.NoError(t, err)
	assert.Equal(t, hub.DefaultPMMSettings(), got)
}

func TestPMMSettingsForAnUnknownEnvironment(t *testing.T) {
	ctx := context.Background()
	svc := hubOverStore(t, hub.NewMemoryStore())

	_, err := svc.PMMSettings(ctx, "nope")
	require.ErrorIs(t, err, engine.ErrNotFound)
	err = svc.SetPMMSettings(ctx, "nope", hub.DefaultPMMSettings())
	require.ErrorIs(t, err, engine.ErrNotFound)
}

// An environment whose tree predates hub-managed PMM has no files to read. It gets
// the defaults, and saving writes the whole tree.
func TestPMMSettingsWithoutAStoredTree(t *testing.T) {
	ctx := context.Background()
	store := hub.NewMemoryStore()
	svc := hubOverStore(t, store)

	got, err := svc.PMMSettings(ctx, "spoke-a")
	require.NoError(t, err)
	assert.Equal(t, hub.DefaultPMMSettings(), got)

	require.NoError(t, svc.SetPMMSettings(ctx, "spoke-a", hub.DefaultPMMSettings()))
	_, err = store.Get(ctx, "clusters/spoke-a/platform/pmm/kustomization.yaml")
	require.NoError(t, err)
}
