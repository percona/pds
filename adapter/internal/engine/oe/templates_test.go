package oe

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/percona/pds/adapter/internal/engine"
)

func TestALoadBalancerCarriesTheOwnershipTagWhenTheEnvironmentHasAnID(t *testing.T) {
	for name, exposure := range map[string]engine.Exposure{
		"public":  {External: true},
		"private": {External: true, Private: true},
	} {
		svc := LoadBalancerServiceFor(exposure, "k3j9x2m4p1q7")

		assert.Equal(t, "percona.com/pds=k3j9x2m4p1q7", svc.Annotations["service.beta.kubernetes.io/aws-load-balancer-additional-resource-tags"], name)
	}
}

func TestALoadBalancerWithoutAnEnvironmentIDCarriesNoTagAnnotation(t *testing.T) {
	svc := LoadBalancerServiceFor(engine.Exposure{External: true}, "")

	assert.NotContains(t, svc.Annotations, "service.beta.kubernetes.io/aws-load-balancer-additional-resource-tags")
	assert.Equal(t, awsSchemeInternetFacing, svc.Annotations[AnnotationAWSLoadBalancerScheme])
}
