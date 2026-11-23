package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var (
	// ErrSecretNotFound means the named Secret is not in the namespace. A
	// missing Secret is how a cluster with no pasted credentials looks, not a
	// request failure.
	ErrSecretNotFound = errors.New("secret not found")
	// ErrForbidden means the ServiceAccount cannot read or write the Secret.
	// The chart grants this on two names; a missing Role is this error.
	ErrForbidden = errors.New("secrets access is forbidden")
	// ErrConfigMapNotFound means the named ConfigMap is not in the namespace.
	ErrConfigMapNotFound = errors.New("configmap not found")
)

const (
	secretManagedByLabel = "app.kubernetes.io/managed-by"
	secretManagedByValue = "pds-adapter"
	secretComponentLabel = "app.kubernetes.io/component"
	secretComponentValue = "adapter"
	secretPutAttempts    = 3
)

// GetSecret returns the string data of one Secret. A missing Secret is
// ErrSecretNotFound. A client with no Kubernetes access is
// platform.ErrUnavailable.
func (c *Client) GetSecret(ctx context.Context, namespace, name string) (map[string]string, error) {
	if err := c.available(); err != nil {
		return nil, err
	}

	secret, err := c.clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrSecretNotFound
		}

		return nil, secretError("get", namespace, name, err)
	}

	return secretStrings(secret), nil
}

// PutSecret creates the Secret or updates the keys that are present in values.
// Other keys already on the Secret stay, so a ServiceNow paste does not wipe
// an unrelated field a platform engineer put there.
func (c *Client) PutSecret(ctx context.Context, namespace, name string, values map[string]string) error {
	if err := c.available(); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("put secret: name must not be blank")
	}

	secrets := c.clientset.CoreV1().Secrets(namespace)
	var last error
	for range secretPutAttempts {
		existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return secretError("get", namespace, name, err)
			}

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
					Labels: map[string]string{
						secretManagedByLabel: secretManagedByValue,
						secretComponentLabel: secretComponentValue,
					},
				},
				Type:       corev1.SecretTypeOpaque,
				StringData: values,
			}
			if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
				if apierrors.IsAlreadyExists(err) {
					last = err

					continue
				}

				return secretError("create", namespace, name, err)
			}

			return nil
		}

		if existing.Data == nil {
			existing.Data = map[string][]byte{}
		}
		for key, value := range values {
			existing.Data[key] = []byte(value)
		}
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		existing.Labels[secretManagedByLabel] = secretManagedByValue
		existing.Labels[secretComponentLabel] = secretComponentValue
		existing.StringData = nil

		if _, err := secrets.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsConflict(err) {
				last = err

				continue
			}

			return secretError("update", namespace, name, err)
		}

		return nil
	}

	return secretError("update", namespace, name, last)
}

// CreateSecret creates the Secret and reports whether it did. A Secret that is
// already there is left exactly as it is, values and labels both, and is not an
// error: whoever created it first owns it.
func (c *Client) CreateSecret(ctx context.Context, namespace, name string, labels, values map[string]string) (bool, error) {
	if err := c.available(); err != nil {
		return false, err
	}

	data := make(map[string][]byte, len(values))
	for key, value := range values {
		data[key] = []byte(value)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}
	if _, err := c.clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}

		return false, secretError("create", namespace, name, err)
	}

	return true, nil
}

// PatchSecret sets the named keys of a Secret that already exists and leaves
// every other key as it is. It never reads the Secret, so the grant behind it
// needs only patch. A missing Secret is ErrSecretNotFound.
func (c *Client) PatchSecret(ctx context.Context, namespace, name string, values map[string]string) error {
	if err := c.available(); err != nil {
		return err
	}

	data := make(map[string][]byte, len(values))
	for key, value := range values {
		data[key] = []byte(value)
	}
	patch, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		return fmt.Errorf("patch secret %s/%s: %w", namespace, name, err)
	}
	_, err = c.clientset.CoreV1().Secrets(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return ErrSecretNotFound
	}
	if err != nil {
		return secretError("patch", namespace, name, err)
	}

	return nil
}

// DeleteSecretKeys removes keys from a Secret and leaves the rest. PutSecret
// only merges, so revoking one join token needed a way to take a key away
// without touching the others. A Secret that is already gone is success.
func (c *Client) DeleteSecretKeys(ctx context.Context, namespace, name string, keys []string) error {
	if err := c.available(); err != nil {
		return err
	}
	if name == "" || len(keys) == 0 {
		return nil
	}

	secrets := c.clientset.CoreV1().Secrets(namespace)
	var last error
	for range secretPutAttempts {
		existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}

			return secretError("get", namespace, name, err)
		}
		if existing.Data == nil {
			return nil
		}

		removed := false
		for _, key := range keys {
			if _, ok := existing.Data[key]; ok {
				delete(existing.Data, key)
				removed = true
			}
		}
		if !removed {
			return nil
		}
		existing.StringData = nil

		if _, err := secrets.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsConflict(err) {
				last = err

				continue
			}

			return secretError("update", namespace, name, err)
		}

		return nil
	}

	return secretError("update", namespace, name, last)
}

// DeleteSecret removes the Secret. A Secret that is already gone is success:
// clearing credentials is idempotent.
func (c *Client) DeleteSecret(ctx context.Context, namespace, name string) error {
	if err := c.available(); err != nil {
		return err
	}

	err := c.clientset.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return secretError("delete", namespace, name, err)
	}

	return nil
}

// DeleteSecrets removes the named Secrets from the namespace and returns how
// many it removed. A Secret that is already gone does not count and is not an
// error. It never lists or reads Secrets, so the grant behind it needs only
// delete.
func (c *Client) DeleteSecrets(ctx context.Context, namespace string, names []string) (int, error) {
	if err := c.available(); err != nil {
		return 0, err
	}

	secrets := c.clientset.CoreV1().Secrets(namespace)
	removed := 0
	var failed []error
	for _, name := range names {
		err := secrets.Delete(ctx, name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			failed = append(failed, secretError("delete", namespace, name, err))

			continue
		}
		removed++
	}

	return removed, errors.Join(failed...)
}

func secretError(op, namespace, name string, err error) error {
	if apierrors.IsForbidden(err) {
		return fmt.Errorf("%s secret %s/%s: %w", op, namespace, name, ErrForbidden)
	}

	return fmt.Errorf("%s secret %s/%s: %w", op, namespace, name, err)
}

func secretStrings(secret *corev1.Secret) map[string]string {
	out := make(map[string]string, len(secret.Data)+len(secret.StringData))
	for key, value := range secret.Data {
		out[key] = string(value)
	}
	for key, value := range secret.StringData {
		out[key] = value
	}

	return out
}
