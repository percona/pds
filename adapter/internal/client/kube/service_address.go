package kube

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ServiceAddress returns the address a cloud assigned to a LoadBalancer
// Service: its hostname when it has one, its IP otherwise. A Service that does
// not exist, is not a load balancer, or has not been given an address yet
// answers empty, because that is a state the caller reports rather than a
// failure to log on every beat.
func (c *Client) ServiceAddress(ctx context.Context, namespace, name string) (string, error) {
	if err := c.available(); err != nil {
		return "", err
	}

	svc, err := c.clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}

		return "", err
	}

	for _, ingress := range svc.Status.LoadBalancer.Ingress {
		if ingress.Hostname != "" {
			return ingress.Hostname, nil
		}
		if ingress.IP != "" {
			return ingress.IP, nil
		}
	}

	return "", nil
}
