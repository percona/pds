package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/managed"
)

type recorder struct{ applied []string }

func (r *recorder) Apply(_ context.Context, manifests []byte) error {
	r.applied = append(r.applied, string(manifests))

	return nil
}

func (*recorder) WaitDeployments(context.Context, string, ...string) error { return nil }

func testInput(dir string) input {
	return input{
		access:  managed.KubeAccess{Endpoint: "https://127.0.0.1:1", Token: "t"},
		join:    managed.Join{Environment: "spoke-a", AgentToken: "agent-secret", GitURL: "http://git/pds.git", Namespace: "pds"},
		gitDir:  dir,
		release: managed.ReleaseInput{ChartVersion: "0.1.0", ChartURL: "oci://ghcr.io/percona/charts", HubURL: "http://hub", EnvironmentName: "spoke-a", ImageRepository: "img", ImageTag: "dev", Bucket: "smoke", Region: "us-east-1"},
		flux:    managed.FluxVersion,
	}
}

func TestBootstrapInstallsFluxWritesTheReleaseThenTheJoin(t *testing.T) {
	dir := t.TempDir()
	remote := &recorder{}
	api := managed.NewEnvironmentAPI(
		func(managed.KubeAccess) (managed.Remote, error) { return remote, nil },
		func(context.Context, string) ([]byte, error) {
			return []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: flux-system\n"), nil
		},
	)

	require.NoError(t, bootstrap(t.Context(), api, testInput(dir)))

	release, err := os.ReadFile(filepath.Join(dir, "clusters", "spoke-a", "platform", "pds", "helmrelease.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(release), "kind: HelmRelease")
	require.Len(t, remote.applied, 3, "Flux, its accounts, then the join")
	assert.Contains(t, remote.applied[2], "kind: GitRepository")
	assert.Contains(t, remote.applied[2], "pds-hub-agent-token")
}

func TestReadEnrollmentTakesTheJoinFromTheHubsAnswer(t *testing.T) {
	body := `{"environment":"spoke-a","agentToken":"agent-secret","gitUrl":"http://git/pds.git","namespace":"pds"}`

	join, err := readEnrollment(strings.NewReader(body))

	require.NoError(t, err)
	assert.Equal(t, managed.Join{Environment: "spoke-a", AgentToken: "agent-secret", GitURL: "http://git/pds.git", Namespace: "pds"}, join)
}

func TestReadEnrollmentRefusesAnAnswerWithoutAgentToken(t *testing.T) {
	_, err := readEnrollment(strings.NewReader(`{"environment":"spoke-a","gitUrl":"http://git/pds.git"}`))

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "agent-secret")
}
