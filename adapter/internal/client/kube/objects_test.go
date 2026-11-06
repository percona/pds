package kube_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/platform"
)

var widgets = schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}

func widget(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"name": name, "namespace": "pds"},
		"spec":       map[string]any{"size": "small"},
	}}
}

func newDynamic(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{widgets: "WidgetList"}, objects...)
}

func TestCreateThenGetObject(t *testing.T) {
	client := kube.New(clientfake.NewClientset()).WithDynamic(newDynamic())

	require.NoError(t, client.CreateObject(context.Background(), widgets, widget("w1")))
	got, err := client.GetObject(context.Background(), widgets, "pds", "w1")

	require.NoError(t, err)
	size, _, _ := unstructured.NestedString(got.Object, "spec", "size")
	assert.Equal(t, "small", size)
}

func TestDeleteObjectRemovesItAndReportsAnAbsentOne(t *testing.T) {
	client := kube.New(clientfake.NewClientset()).WithDynamic(newDynamic(widget("w1")))
	ctx := context.Background()

	require.NoError(t, client.DeleteObject(ctx, widgets, "pds", "w1"))
	_, err := client.GetObject(ctx, widgets, "pds", "w1")
	require.ErrorIs(t, err, kube.ErrObjectNotFound)

	err = client.DeleteObject(ctx, widgets, "pds", "w1")
	require.ErrorIs(t, err, kube.ErrObjectNotFound)
}

func TestObjectErrorsKeepTheirSentinels(t *testing.T) {
	client := kube.New(clientfake.NewClientset()).WithDynamic(newDynamic(widget("w1")))
	ctx := context.Background()

	_, err := client.GetObject(ctx, widgets, "pds", "absent")
	require.ErrorIs(t, err, kube.ErrObjectNotFound)

	err = client.CreateObject(ctx, widgets, widget("w1"))
	require.ErrorIs(t, err, kube.ErrObjectExists)
}

func TestAForbiddenObjectIsNamedAsSuch(t *testing.T) {
	dyn := newDynamic()
	dyn.PrependReactor("get", "widgets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(widgets.GroupResource(), "w1", nil)
	})

	_, err := kube.New(clientfake.NewClientset()).WithDynamic(dyn).GetObject(context.Background(), widgets, "pds", "w1")

	require.ErrorIs(t, err, kube.ErrObjectForbidden)
}

func TestObjectCallsWithoutADynamicClientAreUnavailable(t *testing.T) {
	_, err := kube.New(clientfake.NewClientset()).GetObject(context.Background(), widgets, "pds", "w1")

	require.ErrorIs(t, err, platform.ErrUnavailable)
}
