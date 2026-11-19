package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

// fieldManager owns the fields the control plane applies to an environment.
const fieldManager = "pds-control-plane"

var crdResource = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}

// Remote is another Kubernetes API, reached by address and bearer token for as
// long as a managed environment's Access window is open. It holds no kubeconfig.
type Remote struct {
	dyn       dynamic.Interface
	clientset kubernetes.Interface
	// mapper resolves kinds. reset drops what it learned, so a kind a CRD just
	// added is found.
	mapper func() meta.RESTMapper
	reset  func()
	// poll is how often a wait looks again, and patience how long it looks.
	poll     time.Duration
	patience time.Duration
}

// NewRemote connects to the API at endpoint, trusting the CA, with the token.
func NewRemote(endpoint string, ca []byte, token string) (*Remote, error) {
	cfg := &rest.Config{
		Host:            endpoint,
		TLSClientConfig: rest.TLSClientConfig{CAData: ca},
		BearerToken:     token,
		Timeout:         30 * time.Second,
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to the Kubernetes API: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to the Kubernetes API: %w", err)
	}
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to the Kubernetes API: %w", err)
	}
	cache := memory.NewMemCacheClient(disco)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cache)

	return &Remote{
		dyn: dyn, clientset: clientset,
		mapper: func() meta.RESTMapper { return mapper }, reset: mapper.Reset,
		poll: 2 * time.Second, patience: 5 * time.Minute,
	}, nil
}

// Apply server-side applies every object of the manifests. Namespaces and
// custom resource definitions go first and the definitions are waited for, so
// what uses them in the same manifests is found. Applying again changes
// nothing, and an error never carries an object's data.
func (r *Remote) Apply(ctx context.Context, manifests []byte) error {
	objects, err := decode(manifests)
	if err != nil {
		return err
	}

	var first, rest []*unstructured.Unstructured
	for _, obj := range objects {
		if obj.GetKind() == "Namespace" || obj.GetKind() == "CustomResourceDefinition" {
			first = append(first, obj)
		} else {
			rest = append(rest, obj)
		}
	}
	for _, obj := range first {
		if err := r.applyOne(ctx, obj); err != nil {
			return err
		}
	}
	for _, obj := range first {
		if obj.GetKind() == "CustomResourceDefinition" {
			if err := r.waitEstablished(ctx, obj.GetName()); err != nil {
				return err
			}
		}
	}
	for _, obj := range rest {
		if err := r.applyOne(ctx, obj); err != nil {
			return err
		}
	}

	return nil
}

func decode(manifests []byte) ([]*unstructured.Unstructured, error) {
	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifests), 4096)
	var out []*unstructured.Unstructured
	for {
		var raw map[string]any
		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}

			return nil, fmt.Errorf("read the manifests: %w", err)
		}
		if len(raw) == 0 {
			continue
		}
		out = append(out, &unstructured.Unstructured{Object: raw})
	}
}

func (r *Remote) applyOne(ctx context.Context, obj *unstructured.Unstructured) error {
	gvk := obj.GroupVersionKind()
	mapping, err := r.mapping(ctx, gvk)
	if err != nil {
		return err
	}
	body, err := json.Marshal(obj.Object)
	if err != nil {
		return fmt.Errorf("encode %s %s: %w", gvk.Kind, obj.GetName(), err)
	}

	var client dynamic.ResourceInterface = r.dyn.Resource(mapping.Resource)
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		client = r.dyn.Resource(mapping.Resource).Namespace(obj.GetNamespace())
	}
	force := true
	if _, err := client.Patch(ctx, obj.GetName(), types.ApplyPatchType, body, metav1.PatchOptions{FieldManager: fieldManager, Force: &force}); err != nil {
		return fmt.Errorf("apply %s %s: %w", gvk.Kind, obj.GetName(), scrubStatus(err))
	}

	return nil
}

// scrubStatus keeps the reason of an API error and drops the rest, which can
// echo a field of the object that was refused.
func scrubStatus(err error) error {
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		return fmt.Errorf("the Kubernetes API answered %s", apierrors.ReasonForError(err))
	}

	return err
}

// mapping finds the resource of a kind, waiting for a definition that was just
// applied to be served.
func (r *Remote) mapping(ctx context.Context, gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	deadline := time.Now().Add(r.patience)
	for {
		mapping, err := r.mapper().RESTMapping(gvk.GroupKind(), gvk.Version)
		if err == nil {
			return mapping, nil
		}
		if !meta.IsNoMatchError(err) {
			return nil, fmt.Errorf("find %s in the Kubernetes API: %w", gvk.Kind, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the Kubernetes API does not serve %s in %s", gvk.Kind, gvk.GroupVersion())
		}
		r.reset()
		if err := sleep(ctx, r.poll); err != nil {
			return nil, err
		}
	}
}

func (r *Remote) waitEstablished(ctx context.Context, name string) error {
	return r.until(ctx, "the definition "+name+" to be established", func() (bool, error) {
		crd, err := r.dyn.Resource(crdResource).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		conditions, _, _ := unstructured.NestedSlice(crd.Object, "status", "conditions")
		for _, c := range conditions {
			condition, ok := c.(map[string]any)
			if ok && condition["type"] == "Established" && condition["status"] == "True" {
				return true, nil
			}
		}

		return false, nil
	})
}

// WaitDeployments returns once each Deployment has all its replicas ready.
func (r *Remote) WaitDeployments(ctx context.Context, namespace string, names ...string) error {
	for _, name := range names {
		err := r.until(ctx, "the Deployment "+name+" to be ready", func() (bool, error) {
			d, err := r.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			want := int32(1)
			if d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}

			return d.Status.ReadyReplicas >= want && d.Status.AvailableReplicas >= want, nil
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// served finds the resource of a kind, or nil when the API does not serve it:
// an environment whose create stopped early has no such definitions yet.
func (r *Remote) served(apiVersion, kind string) (dynamic.NamespaceableResourceInterface, bool, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, false, fmt.Errorf("read the API version %q: %w", apiVersion, err)
	}
	mapping, err := r.mapper().RESTMapping(schema.GroupKind{Group: gv.Group, Kind: kind}, gv.Version)
	if meta.IsNoMatchError(err) {
		r.reset()
		mapping, err = r.mapper().RESTMapping(schema.GroupKind{Group: gv.Group, Kind: kind}, gv.Version)
	}
	if meta.IsNoMatchError(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("find %s in the Kubernetes API: %w", kind, err)
	}

	return r.dyn.Resource(mapping.Resource), mapping.Scope.Name() == meta.RESTScopeNameNamespace, nil
}

// Suspend stops Flux applying an object by setting spec.suspend. An object that
// is not there needs no suspending.
func (r *Remote) Suspend(ctx context.Context, apiVersion, kind, namespace, name string) error {
	res, namespaced, err := r.served(apiVersion, kind)
	if err != nil || res == nil {
		return err
	}
	var client dynamic.ResourceInterface = res
	if namespaced {
		client = res.Namespace(namespace)
	}
	_, err = client.Patch(ctx, name, types.MergePatchType, []byte(`{"spec":{"suspend":true}}`), metav1.PatchOptions{FieldManager: fieldManager})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("suspend %s %s: %w", kind, name, scrubStatus(err))
	}

	return nil
}

// DeleteAll asks for the deletion of every object of the kind in the namespace,
// or in every namespace when it is empty, and returns how many are still there:
// an object with a finalizer outlives the request. A kind the API does not
// serve has no objects.
func (r *Remote) DeleteAll(ctx context.Context, apiVersion, kind, namespace string) (int, error) {
	res, namespaced, err := r.served(apiVersion, kind)
	if err != nil || res == nil {
		return 0, err
	}
	var scope dynamic.ResourceInterface = res
	if namespaced && namespace != "" {
		scope = res.Namespace(namespace)
	}
	listed, err := scope.List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("list %s: %w", kind, scrubStatus(err))
	}
	for _, obj := range listed.Items {
		var one dynamic.ResourceInterface = res
		if namespaced {
			one = res.Namespace(obj.GetNamespace())
		}
		if err := one.Delete(ctx, obj.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return 0, fmt.Errorf("delete %s %s: %w", kind, obj.GetName(), scrubStatus(err))
		}
	}
	rest, err := scope.List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("list %s: %w", kind, scrubStatus(err))
	}

	return len(rest.Items), nil
}

// DeleteLoadBalancerServices asks for the deletion of every Service of type
// LoadBalancer, so the cloud removes the load balancers, and returns how many
// remain.
func (r *Remote) DeleteLoadBalancerServices(ctx context.Context) (int, error) {
	services, err := r.clientset.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("list Services: %w", scrubStatus(err))
	}
	left := 0
	for _, svc := range services.Items {
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}
		if err := r.clientset.CoreV1().Services(svc.Namespace).Delete(ctx, svc.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return 0, fmt.Errorf("delete Service %s: %w", svc.Name, scrubStatus(err))
		}
	}
	services, err = r.clientset.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("list Services: %w", scrubStatus(err))
	}
	for _, svc := range services.Items {
		if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
			left++
		}
	}

	return left, nil
}

// until looks every poll until done, an error or the patience runs out.
func (r *Remote) until(ctx context.Context, what string, done func() (bool, error)) error {
	deadline := time.Now().Add(r.patience)
	for {
		ok, err := done()
		if err != nil {
			return fmt.Errorf("wait for %s: %w", what, err)
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", what)
		}
		if err := sleep(ctx, r.poll); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
