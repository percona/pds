package kube

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

var (
	instanceKind = schema.GroupVersionKind{Group: "core.openeverest.io", Version: "v1alpha1", Kind: "Instance"}
	pvcKind      = schema.GroupVersionKind{Version: "v1", Kind: "PersistentVolumeClaim"}
	kustomizeGVR = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
)

func object(gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(namespace)
	u.SetName(name)

	return u
}

func deleteLab(objects ...runtime.Object) *Remote {
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		{Group: "core.openeverest.io", Version: "v1alpha1", Resource: "instances"}: "InstanceList",
		{Version: "v1", Resource: "persistentvolumeclaims"}:                        "PersistentVolumeClaimList",
		kustomizeGVR: "KustomizationList",
	}, objects...)
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(instanceKind, meta.RESTScopeNamespace)
	mapper.Add(pvcKind, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"}, meta.RESTScopeNamespace)

	return &Remote{
		dyn: dyn, clientset: k8sfake.NewSimpleClientset(),
		mapper: func() meta.RESTMapper { return mapper }, reset: func() {},
		poll: time.Millisecond, patience: 50 * time.Millisecond,
	}
}

func TestDeleteAllRemovesEveryObjectOfAKindInEveryNamespace(t *testing.T) {
	r := deleteLab(object(instanceKind, "dbs", "orders"), object(instanceKind, "other", "billing"))

	left, err := r.DeleteAll(t.Context(), "core.openeverest.io/v1alpha1", "Instance", "")
	require.NoError(t, err)

	assert.Zero(t, left)
}

func TestDeleteAllLimitsItselfToTheNamespace(t *testing.T) {
	r := deleteLab(object(pvcKind, "dbs", "data-0"), object(pvcKind, "other", "keep"))

	left, err := r.DeleteAll(t.Context(), "v1", "PersistentVolumeClaim", "dbs")
	require.NoError(t, err)
	assert.Zero(t, left)

	left, err = r.DeleteAll(t.Context(), "v1", "PersistentVolumeClaim", "")
	require.NoError(t, err)
	assert.Zero(t, left)
}

func TestDeleteAllIsDoneForAKindTheAPIDoesNotServe(t *testing.T) {
	r := deleteLab()

	left, err := r.DeleteAll(t.Context(), "example.com/v1", "Widget", "")
	require.NoError(t, err)

	assert.Zero(t, left)
}

func TestSuspendStopsFluxApplyingAnObject(t *testing.T) {
	r := deleteLab(object(schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"}, "flux-system", "instances"))

	require.NoError(t, r.Suspend(t.Context(), "kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "instances"))

	got, err := r.dyn.Resource(kustomizeGVR).Namespace("flux-system").Get(t.Context(), "instances", metav1.GetOptions{})
	require.NoError(t, err)
	suspended, _, _ := unstructured.NestedBool(got.Object, "spec", "suspend")
	assert.True(t, suspended)

	require.NoError(t, r.Suspend(t.Context(), "kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "missing"),
		"an object that is not there needs no suspending")
}

func TestDeleteLoadBalancerServicesLeavesOtherServices(t *testing.T) {
	lb := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "dbs", Name: "orders-lb"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer}}
	internal := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "dbs", Name: "orders"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP}}
	r := deleteLab()
	r.clientset = k8sfake.NewSimpleClientset(lb, internal)

	left, err := r.DeleteLoadBalancerServices(t.Context())
	require.NoError(t, err)

	assert.Zero(t, left)
	_, err = r.clientset.CoreV1().Services("dbs").Get(t.Context(), "orders-lb", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
	_, err = r.clientset.CoreV1().Services("dbs").Get(t.Context(), "orders", metav1.GetOptions{})
	assert.NoError(t, err)
}
