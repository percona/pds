package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/store"
)

func TestManagedSettingsComeFromTheDeploymentsConfiguration(t *testing.T) {
	cfg := config.Config{
		ManagedOutgoingCIDRs: []string{"203.0.113.7/32"}, ManagedChartVersion: "0.1.0",
		HubAgentImage: "ghcr.io/percona/pds-adapter:0.1.0", HubPublicURL: "https://hub.example",
	}

	deps, creator := managedSettings(cfg)

	assert.Equal(t, []string{"203.0.113.7/32"}, deps.OutgoingCIDRs)
	assert.Equal(t, "0.1.0", deps.ChartVersion)
	assert.Equal(t, managed.FluxVersion, deps.FluxVersion)
	assert.Equal(t, "https://hub.example", deps.HubURL)
	assert.Equal(t, "ghcr.io/percona/pds-adapter", deps.ImageRepository)
	assert.Equal(t, "0.1.0", deps.ImageTag)
	assert.Equal(t, managed.CreatorSettings{OutgoingCIDRs: []string{"203.0.113.7/32"}, ChartVersion: "0.1.0", ImageRepository: "ghcr.io/percona/pds-adapter"}, creator)
}

func TestSplitImageSeparatesTheTagFromARegistryWithAPort(t *testing.T) {
	for image, want := range map[string][2]string{
		"ghcr.io/percona/pds-adapter:0.1.0":   {"ghcr.io/percona/pds-adapter", "0.1.0"},
		"registry.local:5000/pds-adapter:dev": {"registry.local:5000/pds-adapter", "dev"},
		"registry.local:5000/pds-adapter":     {"registry.local:5000/pds-adapter", ""},
		"ghcr.io/percona/pds-adapter":         {"ghcr.io/percona/pds-adapter", ""},
		" ghcr.io/percona/pds-adapter:0.1.0 ": {"ghcr.io/percona/pds-adapter", "0.1.0"},
		"":                                    {"", ""},
	} {
		repo, tag := splitImage(image)
		assert.Equal(t, want, [2]string{repo, tag}, image)
	}
}

func TestManagedGitURLIsTheGitHubSSHRemoteOrTheConfiguredAddress(t *testing.T) {
	github := config.Config{HubGitHubOrg: "acme", HubGitHubToken: "t"}
	assert.Equal(t, "ssh://git@github.com/acme/acme-orders.git", managedGitURL(github)("acme-orders"))

	plain := config.Config{HubGitURL: "http://git.local/hub.git"}
	assert.Equal(t, "http://git.local/hub.git", managedGitURL(plain)("orders"))

	assert.Empty(t, managedGitURL(config.Config{})("orders"))
}

type recordedRuns struct{ started []string }

func (r *recordedRuns) Start(_ context.Context, _ uint, environment, kind string) (store.ProvisioningRun, error) {
	r.started = append(r.started, environment+"/"+kind)

	return store.ProvisioningRun{Environment: environment, Kind: kind}, nil
}

func TestLateRunsRefuseUntilTheDatabaseHasConnected(t *testing.T) {
	late := &lateRuns{}

	_, err := late.Start(t.Context(), 1, "orders", managed.KindCreate)
	require.ErrorIs(t, err, store.ErrNotConnected)

	runs := &recordedRuns{}
	late.set(runs)
	run, err := late.Start(t.Context(), 1, "orders", managed.KindCreate)

	require.NoError(t, err)
	assert.Equal(t, "orders", run.Environment)
	assert.Equal(t, []string{"orders/create"}, runs.started)
}

func TestWorkerOwnerNamesTheReplica(t *testing.T) {
	a, b := workerOwner(), workerOwner()

	assert.NotEqual(t, a, b, "two replicas on one host must hold different leases")
	assert.NotEmpty(t, a)
}
