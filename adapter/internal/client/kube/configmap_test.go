package kube_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	"github.com/percona/pds/adapter/internal/client/kube"
)

func TestGetConfigMapMissingIsNotFound(t *testing.T) {
	_, err := kube.New(newClientset(t)).GetConfigMap(
		context.Background(), "everest-system", "pds-alert-analysis",
	)

	require.ErrorIs(t, err, kube.ErrConfigMapNotFound)
}

func TestPutConfigMapCreatesAndGetReturnsData(t *testing.T) {
	client := kube.New(newClientset(t))

	err := client.PutConfigMap(context.Background(), "everest-system", "pds-alert-analysis", map[string]string{
		"data.json": `{"settings":{"enabled":false}}`,
	})
	require.NoError(t, err)

	got, err := client.GetConfigMap(context.Background(), "everest-system", "pds-alert-analysis")
	require.NoError(t, err)
	assert.Equal(t, `{"settings":{"enabled":false}}`, got["data.json"])
}

func TestPutConfigMapUpdatesExistingKeys(t *testing.T) {
	cs := newClientset(t, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "pds-alert-analysis", Namespace: "everest-system"},
		Data:       map[string]string{"keep": "yes", "data.json": "old"},
	})
	client := kube.New(cs)

	err := client.PutConfigMap(context.Background(), "everest-system", "pds-alert-analysis", map[string]string{
		"data.json": "new",
	})
	require.NoError(t, err)

	got, err := client.GetConfigMap(context.Background(), "everest-system", "pds-alert-analysis")
	require.NoError(t, err)
	assert.Equal(t, "new", got["data.json"])
	assert.Equal(t, "yes", got["keep"])
}

func TestPutConfigMapReportsForbidden(t *testing.T) {
	cs := newClientset(t)
	cs.PrependReactor("create", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "configmaps"},
			"pds-alert-analysis",
			errors.New("configmaps is forbidden"),
		)
	})

	err := kube.New(cs).PutConfigMap(context.Background(), "everest-system", "pds-alert-analysis", map[string]string{
		"data.json": "{}",
	})

	require.ErrorIs(t, err, kube.ErrForbidden)
}

func TestDeleteConfigMapRemovesIt(t *testing.T) {
	cs := newClientset(t, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "pds-hub-inventory-spoke-a", Namespace: "everest-system"},
		Data:       map[string]string{"cluster": "{}"},
	})
	client := kube.New(cs)

	err := client.DeleteConfigMap(context.Background(), "everest-system", "pds-hub-inventory-spoke-a")
	require.NoError(t, err)

	_, err = client.GetConfigMap(context.Background(), "everest-system", "pds-hub-inventory-spoke-a")
	require.ErrorIs(t, err, kube.ErrConfigMapNotFound)
}

func TestDeleteConfigMapIsIdempotentWhenMissing(t *testing.T) {
	err := kube.New(newClientset(t)).DeleteConfigMap(
		context.Background(), "everest-system", "pds-hub-inventory-spoke-a",
	)
	require.NoError(t, err)
}
