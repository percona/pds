package kube_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientfake "k8s.io/client-go/kubernetes/fake"

	"github.com/percona/pds/adapter/internal/client/kube"
)

func serviceWithIngress(ingress ...corev1.LoadBalancerIngress) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "monitoring-service", Namespace: "everest-system"},
		Status:     corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: ingress}},
	}
}

func TestServiceAddressIsWhatTheCloudAssigned(t *testing.T) {
	for name, tt := range map[string]struct {
		svc  *corev1.Service
		want string
	}{
		"a hostname": {
			serviceWithIngress(corev1.LoadBalancerIngress{Hostname: "pmm-1.elb.us-east-1.amazonaws.com"}),
			"pmm-1.elb.us-east-1.amazonaws.com",
		},
		"an address": {serviceWithIngress(corev1.LoadBalancerIngress{IP: "10.0.4.9"}), "10.0.4.9"},
		// AWS sets the hostname. Where both are set the hostname is the name a
		// certificate or a DNS record would use.
		"both": {
			serviceWithIngress(corev1.LoadBalancerIngress{Hostname: "pmm.example.com", IP: "10.0.4.9"}),
			"pmm.example.com",
		},
		// A cloud that has not finished provisioning has assigned nothing.
		"nothing yet": {serviceWithIngress(), ""},
	} {
		t.Run(name, func(t *testing.T) {
			client := kube.New(clientfake.NewClientset(tt.svc))

			got, err := client.ServiceAddress(context.Background(), "everest-system", "monitoring-service")

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Monitoring that is cluster only, or not installed, has no load balancer. That
// is an answer, not a failure the agent should log on every beat.
func TestServiceAddressForAMissingServiceIsEmpty(t *testing.T) {
	client := kube.New(clientfake.NewClientset())

	got, err := client.ServiceAddress(context.Background(), "everest-system", "monitoring-service")

	require.NoError(t, err)
	assert.Empty(t, got)
}
