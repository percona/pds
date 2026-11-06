// Package kube talks to the Kubernetes API. It is the only package that does,
// so callers stay testable behind a plain interface and this package stays
// testable with a fake clientset.
package kube

import (
	"fmt"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/percona/pds/adapter/internal/platform"
)

// Client reads cluster state. A nil clientset is a valid Client: every read
// then reports platform.ErrUnavailable, which is what a local run without a
// kubeconfig looks like.
type Client struct {
	clientset kubernetes.Interface
	dynamic   dynamic.Interface
}

// New wraps a clientset, including the fake clientset tests use.
func New(clientset kubernetes.Interface) *Client {
	return &Client{clientset: clientset}
}

// NewInCluster builds a client from the ServiceAccount mounted in the pod.
// Outside a cluster it returns an error, and the caller runs without
// Kubernetes access rather than failing to start.
func NewInCluster() (*Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("in cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}

	return New(clientset).WithDynamic(dyn), nil
}

// available reports whether this client has a Kubernetes connection.
func (c *Client) available() error {
	if c == nil || c.clientset == nil {
		return platform.ErrUnavailable
	}

	return nil
}
