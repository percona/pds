package managed

import (
	"context"
	"fmt"
)

// Cleaner is an environment's Kubernetes API, for deleting what holds AWS
// resources. kube.Remote is the implementation.
type Cleaner interface {
	Suspend(ctx context.Context, apiVersion, kind, namespace, name string) error
	DeleteAll(ctx context.Context, apiVersion, kind, namespace string) (left int, err error)
	DeleteLoadBalancerServices(ctx context.Context) (left int, err error)
}

// Drain removes, from inside an environment, what holds volumes and load
// balancers in AWS, so deleting the cluster leaves none of them behind.
type Drain struct {
	dial func(KubeAccess) (Cleaner, error)
}

// NewDrain builds a Drain. dial reaches an environment with what the Access
// window gives.
func NewDrain(dial func(KubeAccess) (Cleaner, error)) *Drain { return &Drain{dial: dial} }

// instanceAPI is the OpenEverest kind a database is.
const instanceAPI = "core.openeverest.io/v1alpha1"

// RemoveDatabases stops Flux from applying the environment's Git, then deletes
// every database so the operators remove their pods and claims while they are
// still running. It returns how many are left; it is idempotent.
func (d *Drain) RemoveDatabases(ctx context.Context, access KubeAccess) (int, error) {
	cluster, err := d.dial(access)
	if err != nil {
		return 0, err
	}
	// Suspending comes first: a Kustomization that prunes would put each Instance back.
	for _, name := range []string{"instances", "platform", ReleaseName} {
		if err := cluster.Suspend(ctx, "kustomize.toolkit.fluxcd.io/v1", "Kustomization", fluxNamespace, name); err != nil {
			return 0, fmt.Errorf("stop Flux applying %s: %w", name, err)
		}
	}

	return cluster.DeleteAll(ctx, instanceAPI, "Instance", "")
}

// RemoveVolumeUsers deletes every Service that asks for a load balancer and
// every volume claim, so the cloud removes the load balancers and the volumes.
// It returns how many are left; it is idempotent.
func (d *Drain) RemoveVolumeUsers(ctx context.Context, access KubeAccess) (int, error) {
	cluster, err := d.dial(access)
	if err != nil {
		return 0, err
	}
	services, err := cluster.DeleteLoadBalancerServices(ctx)
	if err != nil {
		return 0, err
	}
	claims, err := cluster.DeleteAll(ctx, "v1", "PersistentVolumeClaim", "")
	if err != nil {
		return 0, err
	}

	return services + claims, nil
}
