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
	clientfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/platform"
)

func newClientset(_ *testing.T, objects ...runtime.Object) *clientfake.Clientset {
	return clientfake.NewClientset(objects...)
}

func TestGetSecretReturnsTheValues(t *testing.T) {
	cs := newClientset(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pds-servicenow", Namespace: "pds"},
		Data: map[string][]byte{
			"SN_API_KEY":      []byte("api-key-value"),
			"SN_CLIENT_TOKEN": []byte("client-token-value"),
		},
	})

	got, err := kube.New(cs).GetSecret(context.Background(), "pds", "pds-servicenow")

	require.NoError(t, err)
	assert.Equal(t, "api-key-value", got["SN_API_KEY"])
	assert.Equal(t, "client-token-value", got["SN_CLIENT_TOKEN"])
}

func TestGetSecretReportsNotFound(t *testing.T) {
	_, err := kube.New(newClientset(t)).GetSecret(
		context.Background(), "pds", "pds-servicenow",
	)

	require.ErrorIs(t, err, kube.ErrSecretNotFound)
}

func TestGetSecretReportsUnavailableWithoutAClientset(t *testing.T) {
	_, err := kube.New(nil).GetSecret(context.Background(), "pds", "pds-servicenow")

	require.ErrorIs(t, err, platform.ErrUnavailable)
}

func TestPutSecretCreatesWhenMissing(t *testing.T) {
	cs := newClientset(t)
	client := kube.New(cs)

	err := client.PutSecret(context.Background(), "pds", "pds-assistant", map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-test",
	})
	require.NoError(t, err)

	got, err := client.GetSecret(context.Background(), "pds", "pds-assistant")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-test", got["ANTHROPIC_API_KEY"])
}

func TestPutSecretUpdatesAndKeepsOtherKeys(t *testing.T) {
	cs := newClientset(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pds-servicenow", Namespace: "pds"},
		Data: map[string][]byte{
			"SN_API_KEY":      []byte("old-key"),
			"SN_CLIENT_TOKEN": []byte("keep-me"),
		},
	})
	client := kube.New(cs)

	err := client.PutSecret(context.Background(), "pds", "pds-servicenow", map[string]string{
		"SN_API_KEY": "new-key",
	})
	require.NoError(t, err)

	got, err := client.GetSecret(context.Background(), "pds", "pds-servicenow")
	require.NoError(t, err)
	assert.Equal(t, "new-key", got["SN_API_KEY"])
	assert.Equal(t, "keep-me", got["SN_CLIENT_TOKEN"])
}

func TestDeleteSecretRemovesIt(t *testing.T) {
	cs := newClientset(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pds-assistant", Namespace: "pds"},
		Data:       map[string][]byte{"ANTHROPIC_API_KEY": []byte("sk-ant-test")},
	})
	client := kube.New(cs)

	err := client.DeleteSecret(context.Background(), "pds", "pds-assistant")
	require.NoError(t, err)

	_, err = client.GetSecret(context.Background(), "pds", "pds-assistant")
	require.ErrorIs(t, err, kube.ErrSecretNotFound)
}

func TestDeleteSecretIsIdempotentWhenMissing(t *testing.T) {
	err := kube.New(newClientset(t)).DeleteSecret(
		context.Background(), "pds", "pds-assistant",
	)

	require.NoError(t, err)
}

func TestPutSecretReportsForbidden(t *testing.T) {
	cs := newClientset(t)
	cs.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "secrets"},
			"pds-assistant",
			errors.New("secrets is forbidden"),
		)
	})

	err := kube.New(cs).PutSecret(context.Background(), "pds", "pds-assistant", map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-test",
	})

	require.ErrorIs(t, err, kube.ErrForbidden)
}

func TestDeleteSecretKeysRemovesOnlyThoseKeys(t *testing.T) {
	cs := newClientset(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pds-hub-join", Namespace: "everest-system"},
		Data: map[string][]byte{
			"keep": []byte("yes"),
			"drop": []byte("no"),
		},
	})
	client := kube.New(cs)

	err := client.DeleteSecretKeys(context.Background(), "everest-system", "pds-hub-join", []string{"drop"})
	require.NoError(t, err)

	got, err := client.GetSecret(context.Background(), "everest-system", "pds-hub-join")
	require.NoError(t, err)
	assert.Equal(t, "yes", got["keep"])
	_, ok := got["drop"]
	assert.False(t, ok)
}

func TestDeleteSecretKeysRetriesAConflict(t *testing.T) {
	cs := newClientset(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pds-hub-join", Namespace: "everest-system"},
		Data:       map[string][]byte{"token": []byte("jnt_old")},
	})
	updates := 0
	cs.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates == 1 {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Resource: "secrets"},
				"pds-hub-join",
				errors.New("conflict"),
			)
		}

		return false, nil, nil
	})

	err := kube.New(cs).DeleteSecretKeys(context.Background(), "everest-system", "pds-hub-join", []string{"token"})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, updates, 2)

	_, err = kube.New(cs).GetSecret(context.Background(), "everest-system", "pds-hub-join")
	require.NoError(t, err)
}

func TestPutSecretRetriesAConflict(t *testing.T) {
	cs := newClientset(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pds-hub-join", Namespace: "everest-system"},
		Data:       map[string][]byte{"token": []byte("jnt_old")},
	})
	updates := 0
	cs.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates == 1 {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Resource: "secrets"},
				"pds-hub-join",
				errors.New("conflict"),
			)
		}

		return false, nil, nil
	})

	err := kube.New(cs).PutSecret(context.Background(), "everest-system", "pds-hub-join", map[string]string{
		"token": "jnt_new",
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, updates, 2)

	got, err := kube.New(cs).GetSecret(context.Background(), "everest-system", "pds-hub-join")
	require.NoError(t, err)
	assert.Equal(t, "jnt_new", got["token"])
}

func databaseSecret(namespace, name, cluster string) *corev1.Secret {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if cluster != "" {
		secret.Labels = map[string]string{"postgres-operator.crunchydata.com/cluster": cluster}
	}

	return secret
}

func TestDeleteSecretsRemovesOnlyTheNamedSecrets(t *testing.T) {
	cs := newClientset(t,
		databaseSecret("pds", "orders-pguser-orders", "orders"),
		databaseSecret("pds", "orders-cluster-cert", "orders"),
		databaseSecret("pds", "billing-pguser-billing", "billing"),
		databaseSecret("pds", "unlabelled", ""),
		databaseSecret("other", "orders-pguser-orders", "orders"),
	)

	removed, err := kube.New(cs).DeleteSecrets(
		context.Background(), "pds", []string{"orders-pguser-orders", "orders-cluster-cert", "orders-gone"},
	)

	require.NoError(t, err)
	assert.Equal(t, 2, removed, "a Secret that is already gone does not count")
	left, err := cs.CoreV1().Secrets("pds").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	names := make([]string, 0, len(left.Items))
	for _, item := range left.Items {
		names = append(names, item.Name)
	}
	assert.ElementsMatch(t, []string{"billing-pguser-billing", "unlabelled"}, names)
	other, err := cs.CoreV1().Secrets("other").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, other.Items, 1, "another namespace is never touched")
}

// The agent's Role grants no verb that returns Secret contents, so removing
// Secrets must not list, get or watch them.
func TestDeleteSecretsNeverReadsSecrets(t *testing.T) {
	cs := newClientset(t, databaseSecret("pds", "orders-cluster-cert", "orders"))
	var verbs []string
	cs.PrependReactor("*", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		verbs = append(verbs, action.GetVerb())

		return false, nil, nil
	})

	_, err := kube.New(cs).DeleteSecrets(context.Background(), "pds", []string{"orders-cluster-cert"})

	require.NoError(t, err)
	assert.Equal(t, []string{"delete"}, verbs)
}

func TestDeleteSecretsWithNothingToRemoveIsZero(t *testing.T) {
	removed, err := kube.New(newClientset(t)).DeleteSecrets(context.Background(), "pds", []string{"orders-cluster-cert"})

	require.NoError(t, err)
	assert.Zero(t, removed)
}

func TestDeleteSecretsReportsForbidden(t *testing.T) {
	cs := newClientset(t, databaseSecret("pds", "a", "orders"))
	cs.PrependReactor("delete", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("forbidden"))
	})

	_, err := kube.New(cs).DeleteSecrets(context.Background(), "pds", []string{"a"})

	require.ErrorIs(t, err, kube.ErrForbidden)
}

func TestDeleteSecretsWithoutKubernetesIsUnavailable(t *testing.T) {
	_, err := kube.New(nil).DeleteSecrets(context.Background(), "pds", []string{"a"})

	require.ErrorIs(t, err, platform.ErrUnavailable)
}

func TestDeleteSecretsTriesTheRestAfterOneFails(t *testing.T) {
	cs := newClientset(t,
		databaseSecret("pds", "a", "orders"),
		databaseSecret("pds", "b", "orders"),
		databaseSecret("pds", "c", "orders"),
	)
	cs.PrependReactor("delete", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.(k8stesting.DeleteAction).GetName() == "a" {
			return true, nil, errors.New("boom")
		}

		return false, nil, nil
	})

	removed, err := kube.New(cs).DeleteSecrets(context.Background(), "pds", []string{"a", "b", "c"})

	require.Error(t, err)
	assert.Equal(t, 2, removed)
}

func TestDeleteSecretsReportsEveryFailure(t *testing.T) {
	cs := newClientset(t,
		databaseSecret("pds", "a", "orders"),
		databaseSecret("pds", "b", "orders"),
	)
	cs.PrependReactor("delete", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		switch action.(k8stesting.DeleteAction).GetName() {
		case "a":
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "a", errors.New("forbidden"))
		case "b":
			return true, nil, errors.New("boom")
		}

		return false, nil, nil
	})

	_, err := kube.New(cs).DeleteSecrets(context.Background(), "pds", []string{"a", "b"})

	require.ErrorIs(t, err, kube.ErrForbidden)
	assert.Contains(t, err.Error(), "pds/a")
	assert.Contains(t, err.Error(), "pds/b")
}

func TestCreateSecretCreatesWhenMissing(t *testing.T) {
	cs := newClientset(t)

	created, err := kube.New(cs).CreateSecret(context.Background(), "pds", "everest-secrets-orders",
		map[string]string{"app.kubernetes.io/instance": "orders"},
		map[string]string{"monitor": "first-value"})

	require.NoError(t, err)
	assert.True(t, created)
	got, err := cs.CoreV1().Secrets("pds").Get(context.Background(), "everest-secrets-orders", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []byte("first-value"), got.Data["monitor"])
	assert.Equal(t, "orders", got.Labels["app.kubernetes.io/instance"])
}

// A Secret that is already there belongs to whoever wrote it first, so its
// values are never replaced.
func TestCreateSecretLeavesAnExistingOneAlone(t *testing.T) {
	cs := newClientset(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "everest-secrets-orders", Namespace: "pds"},
		Data:       map[string][]byte{"monitor": []byte("existing-value")},
	})

	created, err := kube.New(cs).CreateSecret(context.Background(), "pds", "everest-secrets-orders",
		nil, map[string]string{"monitor": "second-value"})

	require.NoError(t, err)
	assert.False(t, created)
	got, err := cs.CoreV1().Secrets("pds").Get(context.Background(), "everest-secrets-orders", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []byte("existing-value"), got.Data["monitor"])
}

func TestCreateSecretReportsForbidden(t *testing.T) {
	cs := newClientset(t)
	cs.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("forbidden"))
	})

	_, err := kube.New(cs).CreateSecret(context.Background(), "pds", "everest-secrets-orders", nil, map[string]string{"monitor": "x"})

	require.ErrorIs(t, err, kube.ErrForbidden)
}

func TestCreateSecretWithoutKubernetesIsUnavailable(t *testing.T) {
	_, err := kube.New(nil).CreateSecret(context.Background(), "pds", "everest-secrets-orders", nil, map[string]string{"monitor": "x"})

	require.ErrorIs(t, err, platform.ErrUnavailable)
}
