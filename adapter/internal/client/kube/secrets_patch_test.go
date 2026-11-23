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

func TestPatchSecretChangesTheNamedKeysAndKeepsTheRest(t *testing.T) {
	cs := newClientset(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "everest-secrets-copy", Namespace: "pds"},
		Data: map[string][]byte{
			"MONGODB_DATABASE_ADMIN_USER":     []byte("databaseAdmin"),
			"MONGODB_DATABASE_ADMIN_PASSWORD": []byte("old"),
		},
	})

	err := kube.New(cs).PatchSecret(context.Background(), "pds", "everest-secrets-copy",
		map[string]string{"MONGODB_DATABASE_ADMIN_PASSWORD": "new"})

	require.NoError(t, err)
	got, err := cs.CoreV1().Secrets("pds").Get(context.Background(), "everest-secrets-copy", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "new", string(got.Data["MONGODB_DATABASE_ADMIN_PASSWORD"]))
	assert.Equal(t, "databaseAdmin", string(got.Data["MONGODB_DATABASE_ADMIN_USER"]))
}

// The agent's Role grants patch on Secrets and no verb that returns their
// contents, so rewriting a Secret must not get it first.
func TestPatchSecretNeverReadsTheSecret(t *testing.T) {
	cs := newClientset(t, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "pds"}})
	var verbs []string
	cs.PrependReactor("*", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		verbs = append(verbs, action.GetVerb())

		return false, nil, nil
	})

	require.NoError(t, kube.New(cs).PatchSecret(context.Background(), "pds", "a", map[string]string{"k": "v"}))

	assert.Equal(t, []string{"patch"}, verbs)
}

func TestPatchSecretOfAMissingSecretIsNotFound(t *testing.T) {
	err := kube.New(newClientset(t)).PatchSecret(context.Background(), "pds", "gone", map[string]string{"k": "v"})

	require.ErrorIs(t, err, kube.ErrSecretNotFound)
}

func TestPatchSecretReportsForbidden(t *testing.T) {
	cs := newClientset(t, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "pds"}})
	cs.PrependReactor("patch", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("forbidden"))
	})

	err := kube.New(cs).PatchSecret(context.Background(), "pds", "a", map[string]string{"k": "v"})

	require.ErrorIs(t, err, kube.ErrForbidden)
}
