package kube

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/percona/pds/adapter/internal/platform"
)

var (
	// ErrObjectNotFound means the named object is not in the namespace.
	ErrObjectNotFound = errors.New("object not found")
	// ErrObjectExists means a create found the name taken.
	ErrObjectExists = errors.New("object already exists")
	// ErrObjectForbidden means the ServiceAccount cannot reach the object.
	ErrObjectForbidden = errors.New("object access is forbidden")
)

// WithDynamic gives the client access to custom resources. Without it every
// object call reports platform.ErrUnavailable.
func (c *Client) WithDynamic(dyn dynamic.Interface) *Client {
	c.dynamic = dyn

	return c
}

func (c *Client) objects(gvr schema.GroupVersionResource, namespace string) (dynamic.ResourceInterface, error) {
	if c == nil || c.dynamic == nil {
		return nil, platform.ErrUnavailable
	}

	return c.dynamic.Resource(gvr).Namespace(namespace), nil
}

// GetObject reads one namespaced custom resource.
func (c *Client) GetObject(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	res, err := c.objects(gvr, namespace)
	if err != nil {
		return nil, err
	}
	obj, err := res.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, objectError("get", gvr, namespace, name, err)
	}

	return obj, nil
}

// CreateObject creates a custom resource in the namespace its metadata names.
func (c *Client) CreateObject(ctx context.Context, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	res, err := c.objects(gvr, obj.GetNamespace())
	if err != nil {
		return err
	}
	if _, err := res.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		return objectError("create", gvr, obj.GetNamespace(), obj.GetName(), err)
	}

	return nil
}

// DeleteObject deletes one namespaced custom resource.
func (c *Client) DeleteObject(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	res, err := c.objects(gvr, namespace)
	if err != nil {
		return err
	}
	if err := res.Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return objectError("delete", gvr, namespace, name, err)
	}

	return nil
}

func objectError(op string, gvr schema.GroupVersionResource, namespace, name string, err error) error {
	sentinel := error(nil)
	switch {
	case apierrors.IsNotFound(err):
		sentinel = ErrObjectNotFound
	case apierrors.IsAlreadyExists(err):
		sentinel = ErrObjectExists
	case apierrors.IsForbidden(err):
		sentinel = ErrObjectForbidden
	}
	if sentinel != nil {
		return fmt.Errorf("%s %s %s/%s: %w", op, gvr.Resource, namespace, name, sentinel)
	}

	return fmt.Errorf("%s %s %s/%s: %w", op, gvr.Resource, namespace, name, err)
}
