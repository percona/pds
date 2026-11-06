package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/percona/pds/adapter/internal/hub"
)

const (
	// platformNamespace is where the platform release, and so its monitoring
	// admin Secret, runs when the pod does not say.
	platformNamespace = "everest-system"
	// pmmSecretName and pmmPasswordKey name the Secret the chart's hook creates.
	pmmSecretName  = "pmm-secret"
	pmmPasswordKey = "PMM_ADMIN_PASSWORD"
)

// secretGetter is the one Kubernetes call the reader needs.
type secretGetter interface {
	GetSecret(ctx context.Context, namespace, name string) (map[string]string, error)
}

// secretReader answers the hub's secret reads from this environment. It reads the
// monitoring admin password and nothing else: a kind it does not know is
// refused before anything reaches the API server, so the hub cannot use the
// agent to read arbitrary Secrets.
type secretReader struct {
	secrets   secretGetter
	namespace string
}

func newSecretReader(secrets secretGetter, namespace string) *secretReader {
	if namespace == "" {
		namespace = platformNamespace
	}

	return &secretReader{secrets: secrets, namespace: namespace}
}

func (r *secretReader) Read(ctx context.Context, kind string) (string, error) {
	if kind != hub.SecretPMMAdminPassword {
		return "", fmt.Errorf("unknown secret kind %q", kind)
	}
	values, err := r.secrets.GetSecret(ctx, r.namespace, pmmSecretName)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", pmmSecretName, err)
	}
	password := values[pmmPasswordKey]
	if password == "" {
		return "", errors.New("the monitoring password is not set on this Kubernetes cluster")
	}

	return password, nil
}

// monitoringServiceName is the Service the hub's monitoring release names.
const monitoringServiceName = "monitoring-service"

// serviceAddresser is the one Kubernetes call the address read needs.
type serviceAddresser interface {
	ServiceAddress(ctx context.Context, namespace, name string) (string, error)
}

// monitoringAddress reads the address the cloud gave monitoring's load
// balancer, for the heartbeat to report.
func monitoringAddress(services serviceAddresser, namespace string) func(context.Context) (string, error) {
	if namespace == "" {
		namespace = platformNamespace
	}

	return func(ctx context.Context) (string, error) {
		return services.ServiceAddress(ctx, namespace, monitoringServiceName)
	}
}
