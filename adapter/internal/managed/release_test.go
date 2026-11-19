package managed_test

import (
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/percona/pds/adapter/internal/managed"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func releaseInput() managed.ReleaseInput {
	return managed.ReleaseInput{
		ChartVersion:    "0.1.0",
		HubURL:          "https://hub.example",
		EnvironmentName: "orders",
		ImageRepository: "ghcr.io/percona/pds-adapter",
		ImageTag:        "0.1.0",
	}
}

func TestRenderReleaseMatchesTheGolden(t *testing.T) {
	got, err := managed.RenderRelease(releaseInput())
	require.NoError(t, err)

	const path = "testdata/helmrelease.golden.yaml"
	if *update {
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
}

func TestRenderReleaseKeepsTheAgentTokenOutOfGit(t *testing.T) {
	got, err := managed.RenderRelease(releaseInput())
	require.NoError(t, err)

	var release struct {
		Spec struct {
			Values struct {
				Adapter struct {
					Hub map[string]any `json:"hub"`
				} `json:"adapter"`
			} `json:"values"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(got, &release))
	hub := release.Spec.Values.Adapter.Hub
	assert.Equal(t, managed.AgentTokenSecret, hub["agentTokenExistingSecret"])
	assert.NotContains(t, hub, "agentToken")
	assert.Equal(t, "https://hub.example", hub["url"])
	assert.Equal(t, "orders", hub["environmentName"])
}

func TestRenderReleaseRefusesWhatItCannotInstall(t *testing.T) {
	for name, mutate := range map[string]func(*managed.ReleaseInput){
		"no chart version": func(in *managed.ReleaseInput) { in.ChartVersion = "" },
		"no hub url":       func(in *managed.ReleaseInput) { in.HubURL = "" },
		"no environment":   func(in *managed.ReleaseInput) { in.EnvironmentName = "" },
		"no image":         func(in *managed.ReleaseInput) { in.ImageRepository = "" },
	} {
		in := releaseInput()
		mutate(&in)
		_, err := managed.RenderRelease(in)
		assert.Error(t, err, name)
	}
}

func TestSpokeValuesCopyMatchesTheChart(t *testing.T) {
	chart, err := os.ReadFile("../../../charts/pds/values-spoke.yaml")
	require.NoError(t, err)
	assert.Equal(t, string(chart), managed.SpokeValues, "copy charts/pds/values-spoke.yaml to internal/managed/values-spoke.yaml")
}
