package kube

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const configMapPutAttempts = 3

// GetConfigMap returns the string data of one ConfigMap. A missing ConfigMap
// is ErrConfigMapNotFound.
func (c *Client) GetConfigMap(ctx context.Context, namespace, name string) (map[string]string, error) {
	if err := c.available(); err != nil {
		return nil, err
	}

	cm, err := c.clientset.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrConfigMapNotFound
		}

		return nil, configMapError("get", namespace, name, err)
	}

	out := make(map[string]string, len(cm.Data))
	for key, value := range cm.Data {
		out[key] = value
	}

	return out, nil
}

// PutConfigMap creates the ConfigMap or updates the keys that are present in
// values. Other keys already on the object stay.
func (c *Client) PutConfigMap(ctx context.Context, namespace, name string, values map[string]string) error {
	if err := c.available(); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("put configmap: name must not be blank")
	}

	maps := c.clientset.CoreV1().ConfigMaps(namespace)
	var last error
	for attempt := 0; attempt < configMapPutAttempts; attempt++ {
		existing, err := maps.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return configMapError("get", namespace, name, err)
			}

			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
					Labels: map[string]string{
						secretManagedByLabel: secretManagedByValue,
						secretComponentLabel: secretComponentValue,
					},
				},
				Data: values,
			}
			if _, err := maps.Create(ctx, cm, metav1.CreateOptions{}); err != nil {
				if apierrors.IsAlreadyExists(err) {
					last = err
					continue
				}

				return configMapError("create", namespace, name, err)
			}

			return nil
		}

		if existing.Data == nil {
			existing.Data = map[string]string{}
		}
		for key, value := range values {
			existing.Data[key] = value
		}
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		existing.Labels[secretManagedByLabel] = secretManagedByValue
		existing.Labels[secretComponentLabel] = secretComponentValue

		if _, err := maps.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsConflict(err) {
				last = err
				continue
			}

			return configMapError("update", namespace, name, err)
		}

		return nil
	}

	return configMapError("update", namespace, name, last)
}

// DeleteConfigMapKeys removes keys from a ConfigMap and leaves the rest.
// PutConfigMap only merges, so dropping one cluster's row needed a way to
// take a key away. A ConfigMap that is already gone is success.
func (c *Client) DeleteConfigMapKeys(ctx context.Context, namespace, name string, keys []string) error {
	if err := c.available(); err != nil {
		return err
	}
	if name == "" || len(keys) == 0 {
		return nil
	}

	maps := c.clientset.CoreV1().ConfigMaps(namespace)
	var last error
	for range configMapPutAttempts {
		existing, err := maps.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}

			return configMapError("get", namespace, name, err)
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

		if _, err := maps.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsConflict(err) {
				last = err

				continue
			}

			return configMapError("update", namespace, name, err)
		}

		return nil
	}

	return configMapError("update", namespace, name, last)
}

// DeleteConfigMap removes one ConfigMap. A missing object is success.
func (c *Client) DeleteConfigMap(ctx context.Context, namespace, name string) error {
	if err := c.available(); err != nil {
		return err
	}
	if name == "" {
		return nil
	}

	err := c.clientset.CoreV1().ConfigMaps(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return configMapError("delete", namespace, name, err)
	}

	return nil
}

func configMapError(op, namespace, name string, err error) error {
	if apierrors.IsForbidden(err) {
		return fmt.Errorf("%s configmap %s/%s: %w", op, namespace, name, ErrForbidden)
	}

	return fmt.Errorf("%s configmap %s/%s: %w", op, namespace, name, err)
}
