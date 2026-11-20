package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientfake "k8s.io/client-go/kubernetes/fake"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/kube"
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

func (r *recordedRuns) Retry(_ context.Context, _ uint, environment string) error {
	r.started = append(r.started, environment+"/retry")

	return nil
}

func (r *recordedRuns) Latest(_ context.Context, _ uint, environment string) (store.ProvisioningRun, error) {
	return store.ProvisioningRun{ID: 5, Environment: environment}, nil
}

func (r *recordedRuns) Steps(context.Context, uint) ([]store.ProvisioningStep, error) {
	return []store.ProvisioningStep{{Name: "assume-role"}}, nil
}

func TestLateRunsRefuseUntilTheDatabaseHasConnected(t *testing.T) {
	late := &lateRuns{}

	_, err := late.Start(t.Context(), 1, "orders", managed.KindCreate)
	require.ErrorIs(t, err, store.ErrNotConnected)

	require.ErrorIs(t, late.Retry(t.Context(), 1, "orders"), store.ErrNotConnected)
	_, err = late.Latest(t.Context(), 1, "orders")
	require.ErrorIs(t, err, store.ErrNotConnected)
	_, err = late.Steps(t.Context(), 5)
	require.ErrorIs(t, err, store.ErrNotConnected)

	runs := &recordedRuns{}
	late.set(runs)
	run, err := late.Start(t.Context(), 1, "orders", managed.KindCreate)

	require.NoError(t, err)
	assert.Equal(t, "orders", run.Environment)
	assert.Equal(t, []string{"orders/create"}, runs.started)

	require.NoError(t, late.Retry(t.Context(), 1, "orders"))
	latest, err := late.Latest(t.Context(), 1, "orders")
	require.NoError(t, err)
	assert.Equal(t, uint(5), latest.ID)
	steps, err := late.Steps(t.Context(), 5)
	require.NoError(t, err)
	assert.Len(t, steps, 1)
}

type recordedLifecycle struct{ builtLifecycle }

func (recordedLifecycle) Describe(rec store.ManagedEnvironment) managed.Status {
	return managed.Status{ChartVersion: rec.ChartVersion}
}

func (recordedLifecycle) Defer(context.Context, auth.Scope, string, int) error { return nil }

func TestLateLifecycleHasNothingToSayAndRefusesUntilItIsBuilt(t *testing.T) {
	late := &lateLifecycle{}

	_, ok := late.Describe(store.ManagedEnvironment{})
	assert.False(t, ok)
	require.ErrorIs(t, late.Defer(t.Context(), auth.Scope{}, "orders", 3), managed.ErrUnavailable)
	_, err := late.UpgradeNow(t.Context(), auth.Scope{}, "orders")
	require.ErrorIs(t, err, managed.ErrUnavailable)
	_, err = late.UpdateInfrastructure(t.Context(), auth.Scope{}, "orders", managed.InfraUpdate{})
	require.ErrorIs(t, err, managed.ErrUnavailable)
	_, err = late.PreviewUpdate(t.Context(), auth.Scope{}, "orders", managed.InfraUpdate{})
	require.ErrorIs(t, err, managed.ErrUnavailable)
	_, err = late.Rebuild(t.Context(), auth.Scope{}, "orders")
	require.ErrorIs(t, err, managed.ErrUnavailable)
	require.ErrorIs(t, late.SetWindow(t.Context(), auth.Scope{}, "orders", store.MaintenanceWindow{}), managed.ErrUnavailable)

	late.set(recordedLifecycle{})
	status, ok := late.Describe(store.ManagedEnvironment{ChartVersion: "0.1.0"})

	assert.True(t, ok)
	assert.Equal(t, "0.1.0", status.ChartVersion)
	require.NoError(t, late.Defer(t.Context(), auth.Scope{}, "orders", 3))
}

func TestWorkerOwnerNamesTheReplica(t *testing.T) {
	a, b := workerOwner(), workerOwner()

	assert.NotEqual(t, a, b, "two replicas on one host must hold different leases")
	assert.NotEmpty(t, a)
}

func fluxReleaseClient(objects ...runtime.Object) *kube.Client {
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{helmReleases: "HelmReleaseList"}, objects...)

	return kube.New(clientfake.NewClientset()).WithDynamic(dynamic)
}

func TestTheAgentReadsTheHelmReleaseItIsToldToReport(t *testing.T) {
	release := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": "pds", "namespace": "flux-system", "generation": int64(1)},
		"status": map[string]any{
			"observedGeneration": int64(1), "lastAttemptedRevision": "0.2.0",
			"history":    []any{map[string]any{"chartVersion": "0.2.0", "status": "deployed"}},
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
		},
	}}

	got, err := fluxReleaseReader(fluxReleaseClient(release), "flux-system", "pds")(t.Context())

	require.NoError(t, err)
	assert.Equal(t, "0.2.0", got["status"].(map[string]any)["lastAttemptedRevision"])
}

func TestAnEnvironmentWithNoHelmReleaseHasNothingToReport(t *testing.T) {
	got, err := fluxReleaseReader(fluxReleaseClient(), "flux-system", "pds")(t.Context())

	require.NoError(t, err)
	assert.Nil(t, got)
}
