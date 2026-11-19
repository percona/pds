package api

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/percona/pds/adapter/internal/service"
)

// managedPolicyLimit is what AWS allows in one managed policy, whitespace not
// counted. The permissions are split across several policies to fit under it.
const managedPolicyLimit = 6144

type cfn struct {
	Parameters map[string]map[string]any `json:"Parameters"`
	Resources  map[string]cfnResource    `json:"Resources"`
	Outputs    map[string]map[string]any `json:"Outputs"`
}

type cfnResource struct {
	Type       string         `json:"Type"`
	Properties map[string]any `json:"Properties"`
}

func loadCloudTemplate(t *testing.T) cfn {
	t.Helper()

	var tpl cfn
	require.NoError(t, yaml.Unmarshal([]byte(cloudConnectionTemplate), &tpl))

	return tpl
}

func (c cfn) ofType(kind string) map[string]cfnResource {
	out := map[string]cfnResource{}
	for name, resource := range c.Resources {
		if resource.Type == kind {
			out[name] = resource
		}
	}

	return out
}

func statements(t *testing.T, document any) []map[string]any {
	t.Helper()

	doc, ok := document.(map[string]any)
	require.True(t, ok, "a policy document is an object")
	raw, ok := doc["Statement"].([]any)
	require.True(t, ok, "a policy document has statements")
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(map[string]any))
	}

	return out
}

func asList(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, fmt.Sprint(item))
		}

		return out
	}

	return nil
}

func TestTheTemplateReportsTheVersionTheHubRequires(t *testing.T) {
	tpl := loadCloudTemplate(t)

	assert.Equal(t, fmt.Sprint(service.CloudTemplateVersion), fmt.Sprint(tpl.Outputs["TemplateVersion"]["Value"]))
}

func TestTheTrustPolicyNamesTheControlPlaneRequiresTheSecretAndWhoActed(t *testing.T) {
	tpl := loadCloudTemplate(t)
	role := tpl.Resources["ControlPlaneRole"]
	require.Equal(t, "AWS::IAM::Role", role.Type)

	statements := statements(t, role.Properties["AssumeRolePolicyDocument"])
	require.Len(t, statements, 1)
	trust := statements[0]

	assert.Equal(t, map[string]any{"AWS": map[string]any{"Ref": "ControlPlanePrincipalArn"}}, trust["Principal"])
	assert.ElementsMatch(t, []string{"sts:AssumeRole", "sts:SetSourceIdentity"}, asList(trust["Action"]))
	condition := trust["Condition"].(map[string]any)
	assert.Equal(t, map[string]any{"sts:ExternalId": map[string]any{"Ref": "ExternalId"}}, condition["StringEquals"])

	patterns := asList(condition["StringLike"].(map[string]any)["sts:SourceIdentity"])
	assert.ElementsMatch(t, []string{"pds-user-*", "pds-job-*"}, patterns)
	for _, pattern := range patterns {
		literal, _, _ := strings.Cut(pattern, "*")
		assert.GreaterOrEqual(t, len(literal), 6, "%s: AWS needs six literal characters before a wildcard", pattern)
	}
	assert.Equal(t, float64(3600), role.Properties["MaxSessionDuration"])
}

func TestThePrincipalParameterRefusesAnAccountRoot(t *testing.T) {
	pattern := loadCloudTemplate(t).Parameters["ControlPlanePrincipalArn"]["AllowedPattern"].(string)
	re := regexp.MustCompile(pattern)

	assert.True(t, re.MatchString("arn:aws:iam::111122223333:role/pds-control-plane"))
	assert.True(t, re.MatchString("arn:aws:iam::111122223333:role/path/pds-control-plane"))
	assert.False(t, re.MatchString("arn:aws:iam::111122223333:root"))
	assert.False(t, re.MatchString("111122223333"))
	assert.False(t, re.MatchString("*"))
}

func TestEveryRoleSitsUnderTheConnectionsPathWithAConnectionNamedRole(t *testing.T) {
	tpl := loadCloudTemplate(t)
	roles := tpl.ofType("AWS::IAM::Role")
	require.Len(t, roles, 5, "control plane, CloudFormation service, EKS cluster, node and EBS CSI")

	for name, role := range roles {
		assert.Equal(t, map[string]any{"Fn::Sub": "/pds/${ConnectionId}/"}, role.Properties["Path"], name)
		assert.Contains(t, fmt.Sprint(role.Properties["RoleName"]), "pds-${ConnectionId}-", name)
	}
	assert.Contains(t, tpl.Outputs, "ControlPlaneRoleArn")
}

func TestEachManagedPolicyFitsTheLimitWithRoomToGrow(t *testing.T) {
	policies := loadCloudTemplate(t).ofType("AWS::IAM::ManagedPolicy")
	require.GreaterOrEqual(t, len(policies), 2, "the permissions are split")

	for name, policy := range policies {
		raw, err := json.Marshal(policy.Properties["PolicyDocument"])
		require.NoError(t, err)
		size := len(regexp.MustCompile(`\s`).ReplaceAll(raw, nil))
		assert.LessOrEqual(t, size, managedPolicyLimit-500, "%s is %d characters", name, size)
	}
}

func TestTheServiceRoleCarriesEveryManagedPolicyAndTheControlPlaneOnlyPassesRoleAndDrivesStacks(t *testing.T) {
	tpl := loadCloudTemplate(t)
	policies := tpl.ofType("AWS::IAM::ManagedPolicy")
	attached := asList(tpl.Resources["ServiceRole"].Properties["ManagedPolicyArns"])
	for name := range policies {
		assert.Contains(t, fmt.Sprint(tpl.Resources["ServiceRole"].Properties["ManagedPolicyArns"]), name, "%s is attached", name)
	}
	assert.LessOrEqual(t, len(attached), 10, "a role holds ten managed policies by default")

	inline := tpl.Resources["ControlPlaneRole"].Properties["Policies"].([]any)
	var actions []string
	for _, entry := range inline {
		for _, statement := range statements(t, entry.(map[string]any)["PolicyDocument"]) {
			if statement["Effect"] == "Allow" {
				actions = append(actions, asList(statement["Action"])...)
			}
		}
	}
	for _, action := range actions {
		assert.Regexp(t, `^(cloudformation:|iam:PassRole$|iam:GetRole$|[a-z0-9-]+:(Describe|Get|List)|eks:(Update|Create|Delete|Associate|Disassociate)|sts:GetCallerIdentity$|tag:GetResources$|account:ListRegions$)`, action,
			"the control plane role holds %s, which belongs on the service role", action)
		assert.NotContains(t, action, "ec2:Create", "tag-scoped writes live on the service role")
	}
}

func TestPassRoleIsLimitedToTheServiceItHandsTheRoleTo(t *testing.T) {
	tpl := loadCloudTemplate(t)
	check := func(label string, docs []any) {
		found := false
		for _, document := range docs {
			for _, statement := range statements(t, document) {
				if statement["Effect"] != "Allow" || !contains(asList(statement["Action"]), "iam:PassRole") {
					continue
				}
				found = true
				condition, _ := statement["Condition"].(map[string]any)
				assert.Contains(t, condition, "StringEquals", "%s: PassRole needs iam:PassedToService", label)
				assert.NotEqual(t, "*", statement["Resource"], label)
			}
		}
		assert.True(t, found, label)
	}
	var inline []any
	for _, entry := range tpl.Resources["ControlPlaneRole"].Properties["Policies"].([]any) {
		inline = append(inline, entry.(map[string]any)["PolicyDocument"])
	}
	check("control plane", inline)

	var managed []any
	for _, policy := range tpl.ofType("AWS::IAM::ManagedPolicy") {
		managed = append(managed, policy.Properties["PolicyDocument"])
	}
	check("service role", managed)
}

func TestTaggingIsAllowedOnlyWhileCreatingAndNeverOnWhatExists(t *testing.T) {
	for name, policy := range loadCloudTemplate(t).ofType("AWS::IAM::ManagedPolicy") {
		for _, statement := range statements(t, policy.Properties["PolicyDocument"]) {
			actions := asList(statement["Action"])
			if statement["Effect"] == "Allow" {
				assert.NotContains(t, actions, "ec2:DeleteTags", name)
				assert.NotContains(t, actions, "eks:TagResource", name)
				assert.NotContains(t, actions, "eks:UntagResource", name)
			}
			if contains(actions, "ec2:CreateTags") && statement["Effect"] == "Allow" {
				condition, _ := statement["Condition"].(map[string]any)
				require.NotNil(t, condition, "%s: CreateTags carries a condition", name)
				assert.Contains(t, fmt.Sprint(condition), "ec2:CreateAction", "%s: CreateTags only at creation", name)
			}
		}
	}
}

func TestTheServiceLinkedRolesItMayCreateAreNamedNotOpen(t *testing.T) {
	found := false
	for _, policy := range loadCloudTemplate(t).ofType("AWS::IAM::ManagedPolicy") {
		for _, statement := range statements(t, policy.Properties["PolicyDocument"]) {
			if !contains(asList(statement["Action"]), "iam:CreateServiceLinkedRole") {
				continue
			}
			found = true
			condition := statement["Condition"].(map[string]any)["StringEquals"].(map[string]any)
			assert.ElementsMatch(t,
				[]string{"eks.amazonaws.com", "eks-nodegroup.amazonaws.com", "elasticloadbalancing.amazonaws.com", "autoscaling.amazonaws.com"},
				asList(condition["iam:AWSServiceName"]))
		}
	}
	assert.True(t, found)
}

func TestTheStackCannotBeChangedByTheControlPlane(t *testing.T) {
	var denied bool
	inline := loadCloudTemplate(t).Resources["ControlPlaneRole"].Properties["Policies"].([]any)
	for _, entry := range inline {
		for _, statement := range statements(t, entry.(map[string]any)["PolicyDocument"]) {
			if statement["Effect"] == "Deny" && contains(asList(statement["Action"]), "cloudformation:DeleteStack") {
				denied = true
				assert.Contains(t, fmt.Sprint(statement["Resource"]), "stack/pds-${ConnectionId}/")
			}
		}
	}
	assert.True(t, denied, "the connection's own stack is outside the control plane's reach")
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}

	return false
}
