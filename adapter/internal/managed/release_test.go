package managed_test

import (
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/managed"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func releaseInput() managed.ReleaseInput {
	return managed.ReleaseInput{
		ChartVersion:      "0.1.0",
		ChartURL:          "oci://ghcr.io/percona/charts",
		HubURL:            "https://hub.example",
		EnvironmentName:   "orders",
		EnvironmentID:     "0123456789ab",
		ImageRepository:   "ghcr.io/percona/pds-adapter",
		ImageTag:          "0.1.0",
		Bucket:            "pds-111122223333-0123456789ab",
		Region:            "eu-west-1",
		BackupUser:        "pds-0123456789ab-backup",
		BackupUserPath:    "/pds/aaaaaaaaaaaa/",
		BackupBoundaryARN: "arn:aws:iam::111122223333:policy/pds/aaaaaaaaaaaa/pds-aaaaaaaaaaaa-backup-boundary",
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

func TestRenderReleaseTagsVolumesWithTheEnvironmentID(t *testing.T) {
	got, err := managed.RenderRelease(releaseInput())
	require.NoError(t, err)

	var release struct {
		Spec struct {
			Values struct {
				TaggedStorageClass struct {
					Enabled bool              `json:"enabled"`
					Tags    map[string]string `json:"tags"`
				} `json:"taggedStorageClass"`
			} `json:"values"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(got, &release))
	class := release.Spec.Values.TaggedStorageClass
	assert.True(t, class.Enabled)
	assert.Equal(t, map[string]string{"percona.com/pds": "0123456789ab"}, class.Tags)
}

func TestRenderReleaseWithoutAnIDLeavesTheTaggedStorageClassOut(t *testing.T) {
	in := releaseInput()
	in.EnvironmentID, in.BackupUser = "", ""

	got, err := managed.RenderRelease(in)
	require.NoError(t, err)

	assert.NotContains(t, string(got), "taggedStorageClass", "a kind environment has no EBS driver")
}

func TestRenderReleaseRefusesWhatItCannotInstall(t *testing.T) {
	for name, mutate := range map[string]func(*managed.ReleaseInput){
		"no chart version": func(in *managed.ReleaseInput) { in.ChartVersion = "" },
		"no hub url":       func(in *managed.ReleaseInput) { in.HubURL = "" },
		"no environment":   func(in *managed.ReleaseInput) { in.EnvironmentName = "" },
		"no image":         func(in *managed.ReleaseInput) { in.ImageRepository = "" },
		"no bucket":        func(in *managed.ReleaseInput) { in.Bucket = "" },
		"no region":        func(in *managed.ReleaseInput) { in.Region = "" },
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

func TestAFailedInstallIsLeftInPlaceSoTheAgentKeepsReporting(t *testing.T) {
	got, err := managed.RenderRelease(releaseInput())
	require.NoError(t, err)
	var release struct {
		Spec struct {
			Timeout string `json:"timeout"`
			Install struct {
				Remediation struct {
					Retries              int   `json:"retries"`
					RemediateLastFailure *bool `json:"remediateLastFailure"`
				} `json:"remediation"`
			} `json:"install"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(got, &release))

	remediation := release.Spec.Install.Remediation
	assert.Zero(t, remediation.Retries, "every install retry is preceded by an uninstall")
	require.NotNil(t, remediation.RemediateLastFailure)
	assert.False(t, *remediation.RemediateLastFailure, "remediating an install uninstalls the agent with it")
	assert.Equal(t, "15m", release.Spec.Timeout, "a new environment's first install outlasts Helm's 5 minute default")
}

func TestRenderReleaseNamesTheStorageClassMonitoringClaims(t *testing.T) {
	got, err := managed.RenderRelease(releaseInput())
	require.NoError(t, err)
	var release struct {
		Spec struct {
			Values struct {
				TaggedStorageClass struct {
					Name string `json:"name"`
				} `json:"taggedStorageClass"`
			} `json:"values"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(got, &release))

	assert.Equal(t, hub.TaggedStorageClass, release.Spec.Values.TaggedStorageClass.Name)
}

func TestAFailedUpgradeRollsBackAfterOneAttemptAndStops(t *testing.T) {
	got, err := managed.RenderRelease(releaseInput())
	require.NoError(t, err)
	var release struct {
		Spec struct {
			Upgrade struct {
				Remediation struct {
					Retries              int    `json:"retries"`
					RemediateLastFailure bool   `json:"remediateLastFailure"`
					Strategy             string `json:"strategy"`
				} `json:"remediation"`
			} `json:"upgrade"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(got, &release))

	remediation := release.Spec.Upgrade.Remediation
	assert.Zero(t, remediation.Retries, "retries counts attempts after the first, so one attempt is zero retries")
	assert.True(t, remediation.RemediateLastFailure, "without it the single attempt that fails is not rolled back")
	assert.Equal(t, "rollback", remediation.Strategy)
}

func TestRenderReleaseNamesTheBackupUserTheAgentMakesAndNoKey(t *testing.T) {
	got, err := managed.RenderRelease(releaseInput())
	require.NoError(t, err)

	var release struct {
		Spec struct {
			Values struct {
				Adapter struct {
					BackupKey map[string]string `json:"backupKey"`
				} `json:"adapter"`
			} `json:"values"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(got, &release))
	assert.Equal(t, map[string]string{
		"user": "pds-0123456789ab-backup", "path": "/pds/aaaaaaaaaaaa/", "tag": "0123456789ab",
		"boundaryArn": "arn:aws:iam::111122223333:policy/pds/aaaaaaaaaaaa/pds-aaaaaaaaaaaa-backup-boundary",
	}, release.Spec.Values.Adapter.BackupKey)
	assert.NotContains(t, string(got), "AWS_SECRET_ACCESS_KEY", "no key reaches Git")
}

func TestRenderReleaseRefusesABackupUserWithNoPathToLiveUnder(t *testing.T) {
	in := releaseInput()
	in.BackupUserPath = ""

	_, err := managed.RenderRelease(in)

	require.Error(t, err)
}

func TestReleaseFilesInstallTheChartFromTheGivenRegistry(t *testing.T) {
	in := releaseInput()
	in.ChartURL = "oci://ghcr.io/example/charts"

	files, err := managed.ReleaseFiles("orders", in)
	require.NoError(t, err)

	var source struct {
		Spec struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(files["clusters/orders/platform/pds/helmrepository.yaml"], &source))
	assert.Equal(t, "oci", source.Spec.Type)
	assert.Equal(t, "oci://ghcr.io/example/charts", source.Spec.URL)
}

func TestReleaseFilesRefuseAMissingChartRegistry(t *testing.T) {
	in := releaseInput()
	in.ChartURL = ""

	_, err := managed.ReleaseFiles("orders", in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chart registry")
}
