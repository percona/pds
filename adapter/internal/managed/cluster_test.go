package managed_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/percona/pds/adapter/internal/managed"
)

type fakeRemote struct {
	events  []string
	objects []*unstructured.Unstructured
	waited  []string
	failOn  string
}

func (f *fakeRemote) Apply(_ context.Context, manifests []byte) error {
	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifests), 4096)
	for {
		var raw map[string]any
		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return err
		}
		if len(raw) == 0 {
			continue
		}
		obj := &unstructured.Unstructured{Object: raw}
		f.objects = append(f.objects, obj)
		f.events = append(f.events, "apply "+obj.GetKind()+"/"+obj.GetName())
		if f.failOn != "" && obj.GetKind() == f.failOn {
			return errors.New("the Kubernetes API answered Forbidden")
		}
	}

	return nil
}

func (f *fakeRemote) WaitDeployments(_ context.Context, namespace string, names ...string) error {
	for _, name := range names {
		f.waited = append(f.waited, namespace+"/"+name)
		f.events = append(f.events, "wait "+name)
	}

	return nil
}

func (f *fakeRemote) find(kind, name string) *unstructured.Unstructured {
	for _, o := range f.objects {
		if o.GetKind() == kind && o.GetName() == name {
			return o
		}
	}

	return nil
}

func newEnvironmentAPI(remote *fakeRemote) (*managed.EnvironmentAPI, *[]string) {
	var versions []string
	cluster := managed.NewEnvironmentAPI(
		func(managed.KubeAccess) (managed.Remote, error) { return remote, nil },
		func(_ context.Context, version string) ([]byte, error) {
			versions = append(versions, version)
			if version != "2.5.1" {
				return nil, errors.New("no digest is pinned for this version")
			}

			return []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: flux-system\n"), nil
		},
	)

	return cluster, &versions
}

var access = managed.KubeAccess{Endpoint: "https://eks.example", CA: []byte("ca"), Token: "k8s-aws-v1.t"}

func TestInstallFluxAppliesThePinnedManifestThenWaitsForItsControllers(t *testing.T) {
	remote := &fakeRemote{}
	cluster, versions := newEnvironmentAPI(remote)

	require.NoError(t, cluster.InstallFlux(t.Context(), access, "2.5.1"))

	assert.Equal(t, []string{"2.5.1"}, *versions)
	assert.Equal(t, "apply Namespace/flux-system", remote.events[0])
	assert.Equal(t, []string{"flux-system/source-controller", "flux-system/kustomize-controller", "flux-system/helm-controller"}, remote.waited)
	firstWait := -1
	for i, e := range remote.events {
		if strings.HasPrefix(e, "wait") && firstWait < 0 {
			firstWait = i
		}
	}
	assert.Equal(t, "apply Namespace/everest-system", remote.events[firstWait+3], "the accounts follow once Flux is up")
}

func TestInstallFluxGivesTheReleaseItsOwnAdminAccount(t *testing.T) {
	remote := &fakeRemote{}
	cluster, _ := newEnvironmentAPI(remote)
	require.NoError(t, cluster.InstallFlux(t.Context(), access, "2.5.1"))

	binding := remote.find("ClusterRoleBinding", "pds-helm")
	require.NotNil(t, binding)
	ref, _, _ := unstructured.NestedString(binding.Object, "roleRef", "name")
	subjects, _, _ := unstructured.NestedSlice(binding.Object, "subjects")
	assert.Equal(t, "cluster-admin", ref)
	require.Len(t, subjects, 1)
	assert.Equal(t, "pds-helm", subjects[0].(map[string]any)["name"])
	assert.Equal(t, "flux-system", subjects[0].(map[string]any)["namespace"])

	assert.NotNil(t, remote.find("ServiceAccount", "hub-release"), "the release's Kustomization has an account of its own")
	assert.NotNil(t, remote.find("ServiceAccount", "hub-platform"))
	assert.NotNil(t, remote.find("Namespace", "everest-system"), "the Roles in it need the namespace")
}

func TestInstallFluxRefusesAVersionWithNoPinnedDigest(t *testing.T) {
	remote := &fakeRemote{}
	cluster, _ := newEnvironmentAPI(remote)

	err := cluster.InstallFlux(t.Context(), access, "9.9.9")

	require.Error(t, err)
	assert.Empty(t, remote.events, "nothing reaches the environment")
}

func joinFor() managed.Join {
	return managed.Join{
		Environment: "orders", AgentToken: "agt_secret-value", GitURL: "ssh://git@github.com/acme/pds-orders.git",
		GitIdentity: "PRIVATE-KEY", GitIdentityPub: "ssh-ed25519 AAAA", Namespace: "pds",
	}
}

func TestWriteJoinPutsTheAgentTokenInTheSecretTheReleaseNames(t *testing.T) {
	remote := &fakeRemote{}
	cluster, _ := newEnvironmentAPI(remote)

	require.NoError(t, cluster.WriteJoin(t.Context(), access, joinFor()))

	secret := remote.find("Secret", managed.AgentTokenSecret)
	require.NotNil(t, secret)
	assert.Equal(t, managed.ReleaseNamespace, secret.GetNamespace())
	token, _, _ := unstructured.NestedString(secret.Object, "stringData", "HUB_AGENT_TOKEN")
	assert.Equal(t, "agt_secret-value", token)
}

func TestWriteJoinPointsFluxAtTheEnvironmentsRepositoryWithItsReadOnlyKey(t *testing.T) {
	remote := &fakeRemote{}
	cluster, _ := newEnvironmentAPI(remote)

	require.NoError(t, cluster.WriteJoin(t.Context(), access, joinFor()))

	repo := remote.find("GitRepository", "hub")
	require.NotNil(t, repo)
	url, _, _ := unstructured.NestedString(repo.Object, "spec", "url")
	secretRef, _, _ := unstructured.NestedString(repo.Object, "spec", "secretRef", "name")
	assert.Equal(t, "ssh://git@github.com/acme/pds-orders.git", url)
	assert.Equal(t, "hub-github", secretRef)
	keys := remote.find("Secret", "hub-github")
	require.NotNil(t, keys)
	identity, _, _ := unstructured.NestedString(keys.Object, "stringData", "identity")
	assert.Equal(t, "PRIVATE-KEY\n", identity)
	known, _, _ := unstructured.NestedString(keys.Object, "stringData", "known_hosts")
	assert.Contains(t, known, "github.com ssh-ed25519")
}

func TestWriteJoinWithoutAnIdentityReadsTheRepositoryAnonymously(t *testing.T) {
	remote := &fakeRemote{}
	cluster, _ := newEnvironmentAPI(remote)
	join := joinFor()
	join.GitIdentity, join.GitIdentityPub = "", ""

	require.NoError(t, cluster.WriteJoin(t.Context(), access, join))

	assert.Nil(t, remote.find("Secret", "hub-github"))
	_, found, _ := unstructured.NestedMap(remote.find("GitRepository", "hub").Object, "spec", "secretRef")
	assert.False(t, found)
}

func TestWriteJoinSyncsInstancesPlatformAndTheReleaseFromTheirPaths(t *testing.T) {
	remote := &fakeRemote{}
	cluster, _ := newEnvironmentAPI(remote)
	require.NoError(t, cluster.WriteJoin(t.Context(), access, joinFor()))

	want := map[string]struct{ path, target, account string }{
		"instances": {"clusters/orders/instances", "pds", "hub-instances"},
		"platform":  {"clusters/orders/platform/pmm", managed.ReleaseNamespace, "hub-platform"},
		"pds":       {"clusters/orders/platform/pds", "", "hub-release"},
	}
	for name, w := range want {
		k := remote.find("Kustomization", name)
		require.NotNil(t, k, name)
		path, _, _ := unstructured.NestedString(k.Object, "spec", "path")
		target, _, _ := unstructured.NestedString(k.Object, "spec", "targetNamespace")
		account, _, _ := unstructured.NestedString(k.Object, "spec", "serviceAccountName")
		assert.Equal(t, w.path, path, name)
		assert.Equal(t, w.target, target, name)
		assert.Equal(t, w.account, account, name)
		prune, _, _ := unstructured.NestedBool(k.Object, "spec", "prune")
		assert.True(t, prune, name)
	}
}

func TestWriteJoinWritesTheSecretBeforeTheKustomizationThatInstallsTheAgent(t *testing.T) {
	remote := &fakeRemote{}
	cluster, _ := newEnvironmentAPI(remote)
	require.NoError(t, cluster.WriteJoin(t.Context(), access, joinFor()))

	secret, release := -1, -1
	for i, e := range remote.events {
		switch e {
		case "apply Secret/" + managed.AgentTokenSecret:
			secret = i
		case "apply Kustomization/pds":
			release = i
		}
	}
	assert.Less(t, secret, release)
	assert.GreaterOrEqual(t, secret, 0)
}

func TestWriteJoinErrorsNeverCarryTheToken(t *testing.T) {
	remote := &fakeRemote{failOn: "Secret"}
	cluster, _ := newEnvironmentAPI(remote)

	err := cluster.WriteJoin(t.Context(), access, joinFor())

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "agt_secret-value")
}

func TestWriteJoinFailsWhenTheEnvironmentCannotBeReached(t *testing.T) {
	cluster := managed.NewEnvironmentAPI(func(managed.KubeAccess) (managed.Remote, error) { return nil, errors.New("dial") }, nil)

	assert.Error(t, cluster.WriteJoin(t.Context(), access, joinFor()))
	assert.Error(t, cluster.InstallFlux(t.Context(), access, "2.5.1"))
}
