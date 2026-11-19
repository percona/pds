package kube

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

type patched struct {
	resource  string
	namespace string
	name      string
	manager   string
	force     bool
	patchType types.PatchType
	body      map[string]any
}

type remoteLab struct {
	mu      sync.Mutex
	applied []patched
	crds    map[string]bool
	remote  *Remote
	resets  int
	known   func() meta.RESTMapper
}

func mapperWith(kinds ...schema.GroupVersionKind) meta.RESTMapper {
	mapper := meta.NewDefaultRESTMapper(nil)
	for _, gvk := range kinds {
		scope := meta.RESTScopeNamespace
		if gvk.Kind == "Namespace" || gvk.Kind == "CustomResourceDefinition" || gvk.Kind == "ClusterRoleBinding" {
			scope = meta.RESTScopeRoot
		}
		mapper.Add(gvk, scope)
	}

	return mapper
}

var allKinds = []schema.GroupVersionKind{
	{Version: "v1", Kind: "Namespace"},
	{Version: "v1", Kind: "ServiceAccount"},
	{Version: "v1", Kind: "Secret"},
	{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRoleBinding"},
	{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "GitRepository"},
}

func newRemoteLab(t *testing.T, kinds ...schema.GroupVersionKind) *remoteLab {
	t.Helper()

	l := &remoteLab{crds: map[string]bool{}}
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{})
	dyn.PrependReactor("patch", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patch := action.(clienttesting.PatchAction)
		var body map[string]any
		require.NoError(t, json.Unmarshal(patch.GetPatch(), &body))
		l.mu.Lock()
		defer l.mu.Unlock()
		l.applied = append(l.applied, patched{
			resource: action.GetResource().Resource, namespace: patch.GetNamespace(), name: patch.GetName(),
			patchType: patch.GetPatchType(), body: body,
		})
		if action.GetResource().Resource == "customresourcedefinitions" {
			l.crds[patch.GetName()] = true
		}

		return true, &unstructured.Unstructured{Object: body}, nil
	})
	dyn.PrependReactor("get", "customresourcedefinitions", func(action clienttesting.Action) (bool, runtime.Object, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		name := action.(clienttesting.GetAction).GetName()
		if !l.crds[name] {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "customresourcedefinitions"}, name)
		}

		return true, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
			"metadata": map[string]any{"name": name},
			"status":   map[string]any{"conditions": []any{map[string]any{"type": "Established", "status": "True"}}},
		}}, nil
	})

	if len(kinds) == 0 {
		kinds = allKinds
	}
	l.known = func() meta.RESTMapper { return mapperWith(kinds...) }
	l.remote = &Remote{
		dyn: dyn, clientset: k8sfake.NewSimpleClientset(),
		mapper: func() meta.RESTMapper { return l.known() }, reset: func() { l.resets++ },
		poll: time.Millisecond, patience: 200 * time.Millisecond,
	}

	return l
}

func (l *remoteLab) names() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, p := range l.applied {
		out = append(out, p.resource+"/"+p.name)
	}

	return out
}

const flux = `apiVersion: v1
kind: Namespace
metadata:
  name: flux-system
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: source-controller
  namespace: flux-system
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: gitrepositories.source.toolkit.fluxcd.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: cluster-reconciler
`

func TestApplyServerSideAppliesEachObjectToItsNamespace(t *testing.T) {
	l := newRemoteLab(t)

	require.NoError(t, l.remote.Apply(t.Context(), []byte(flux)))

	assert.Len(t, l.applied, 4)
	for _, p := range l.applied {
		assert.Equal(t, types.ApplyPatchType, p.patchType)
	}
	var account patched
	for _, p := range l.applied {
		if p.resource == "serviceaccounts" {
			account = p
		}
	}
	assert.Equal(t, "flux-system", account.namespace)
	assert.Equal(t, "source-controller", account.name)
}

func TestApplyPutsNamespacesAndDefinitionsBeforeWhatUsesThem(t *testing.T) {
	l := newRemoteLab(t)

	require.NoError(t, l.remote.Apply(t.Context(), []byte(flux)))

	names := l.names()
	assert.Equal(t, "namespaces/flux-system", names[0])
	assert.Equal(t, "customresourcedefinitions/gitrepositories.source.toolkit.fluxcd.io", names[1])
}

func TestApplyWaitsForAKindThatTheAPIDoesNotServeYet(t *testing.T) {
	l := newRemoteLab(t)
	calls := 0
	l.known = func() meta.RESTMapper {
		calls++
		if calls < 3 {
			return mapperWith(allKinds[:5]...)
		}

		return mapperWith(allKinds...)
	}
	repo := `apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: hub
  namespace: flux-system
`

	require.NoError(t, l.remote.Apply(t.Context(), []byte(repo)))

	assert.Equal(t, []string{"gitrepositories/hub"}, l.names())
	assert.Positive(t, l.resets, "the discovery cache is dropped so a new kind is found")
}

func TestApplyGivesUpOnAKindThatNeverAppears(t *testing.T) {
	l := newRemoteLab(t, allKinds[:5]...)
	repo := "apiVersion: source.toolkit.fluxcd.io/v1\nkind: GitRepository\nmetadata:\n  name: hub\n  namespace: flux-system\n"

	err := l.remote.Apply(t.Context(), []byte(repo))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "GitRepository")
}

func TestApplyNeverPutsASecretValueInAnError(t *testing.T) {
	l := newRemoteLab(t, allKinds[:2]...)
	secret := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n  namespace: n\nstringData:\n  token: hunter2\n"

	err := l.remote.Apply(t.Context(), []byte(secret))

	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), "hunter2"))
}

func TestWaitDeploymentsReturnsWhenEveryOneIsAvailable(t *testing.T) {
	l := newRemoteLab(t)
	replicas := int32(1)
	for _, name := range []string{"source-controller", "helm-controller"} {
		_, err := l.remote.clientset.AppsV1().Deployments("flux-system").Create(t.Context(), &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "flux-system"},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
			Status:     appsv1.DeploymentStatus{ReadyReplicas: 1, AvailableReplicas: 1},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	require.NoError(t, l.remote.WaitDeployments(t.Context(), "flux-system", "source-controller", "helm-controller"))
}

func TestWaitDeploymentsFailsWhenOneNeverBecomesAvailable(t *testing.T) {
	l := newRemoteLab(t)
	replicas := int32(1)
	_, err := l.remote.clientset.AppsV1().Deployments("flux-system").Create(t.Context(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "source-controller", Namespace: "flux-system"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	err = l.remote.WaitDeployments(t.Context(), "flux-system", "source-controller")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "source-controller")
}

var _ context.Context
