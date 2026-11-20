package managed_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/percona/pds/adapter/internal/managed"
)

type template struct {
	Parameters map[string]map[string]any `json:"Parameters"`
	Resources  map[string]resource       `json:"Resources"`
	Outputs    map[string]struct {
		Value any `json:"Value"`
	} `json:"Outputs"`
}

type resource struct {
	Type       string         `json:"Type"`
	Properties map[string]any `json:"Properties"`
}

func parsed(t *testing.T) template {
	t.Helper()

	var tpl template
	require.NoError(t, yaml.Unmarshal([]byte(managed.NetworkTemplate), &tpl))

	return tpl
}

func TestTemplateStampsItsRevisionAsAnOutput(t *testing.T) {
	out, ok := parsed(t).Outputs["TemplateRevision"]
	require.True(t, ok)
	assert.Equal(t, strconv.Itoa(managed.TemplateRevision), out.Value)
}

func TestTemplateRevisionMatchesThePin(t *testing.T) {
	raw, err := os.ReadFile("../../../charts/pds/compatibility.yaml")
	require.NoError(t, err)
	var pin struct {
		ManagedTemplate struct {
			Revision int `json:"revision"`
		} `json:"managedTemplate"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &pin))

	assert.Equal(t, pin.ManagedTemplate.Revision, managed.TemplateRevision)
}

func TestTemplateTakesTheParametersTheCreateStepPasses(t *testing.T) {
	tpl := parsed(t)
	for _, name := range managed.StackParameterNames {
		assert.Contains(t, tpl.Parameters, name)
	}
	for name, p := range tpl.Parameters {
		if _, ok := p["Default"]; !ok {
			assert.Contains(t, managed.StackParameterNames, name, "a parameter with no default must be one the step passes")
		}
	}
}

func TestEveryTaggableResourceCarriesTheOwnershipTag(t *testing.T) {
	untagged := map[string]bool{
		"AWS::EC2::VPCGatewayAttachment": true, "AWS::EC2::Route": true,
		"AWS::EC2::SubnetRouteTableAssociation": true,
	}
	for name, res := range parsed(t).Resources {
		if untagged[res.Type] {
			continue
		}
		assert.True(t, tagsOwnership(res.Properties["Tags"]), "%s (%s) lacks the percona.com/pds tag", name, res.Type)
	}
}

func tagsOwnership(tags any) bool {
	switch v := tags.(type) {
	case map[string]any:
		_, ok := v["percona.com/pds"]

		return ok
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok && m["Key"] == "percona.com/pds" {
				return true
			}
		}
	}

	return false
}

func TestTheEKSIsPrivateAndUsesAccessEntries(t *testing.T) {
	cluster := parsed(t).Resources["Cluster"]
	require.Equal(t, "AWS::EKS::Cluster", cluster.Type)

	access, _ := cluster.Properties["AccessConfig"].(map[string]any)
	assert.Equal(t, "API", access["AuthenticationMode"])
	assert.Equal(t, false, access["BootstrapClusterCreatorAdminPermissions"], "the control plane holds no standing access")

	vpc, _ := cluster.Properties["ResourcesVpcConfig"].(map[string]any)
	assert.Equal(t, false, vpc["EndpointPublicAccess"], "the endpoint is private between runs")
	assert.Equal(t, true, vpc["EndpointPrivateAccess"])
}

func TestEbsCsiDriverTagsTheVolumesItCreates(t *testing.T) {
	addon := parsed(t).Resources["EbsCsiAddon"]
	assert.Contains(t, addon.Properties["ConfigurationValues"].(map[string]any)["Fn::Sub"], "extraVolumeTags")
}

func TestNodeGroupFollowsTheKubernetesVersionParameter(t *testing.T) {
	group := parsed(t).Resources["NodeGroup"]

	assert.Equal(t, map[string]any{"Ref": "KubernetesVersion"}, group.Properties["Version"],
		"a Kubernetes upgrade is one stack update that moves the cluster and then its node group")
}

func TestTheAgentRunsAsTheRoleItIsGivenWithoutStoringAKey(t *testing.T) {
	association := parsed(t).Resources["AgentPodIdentity"]
	require.Equal(t, "AWS::EKS::PodIdentityAssociation", association.Type)

	assert.Equal(t, managed.ReleaseNamespace, association.Properties["Namespace"])
	assert.Equal(t, managed.ReleaseName, association.Properties["ServiceAccount"], "the release's ServiceAccount is named after the release")
	assert.Equal(t, map[string]any{"Ref": "AgentRoleArn"}, association.Properties["RoleArn"])
	assert.Equal(t, map[string]any{"Ref": "Cluster"}, association.Properties["ClusterName"])
}
