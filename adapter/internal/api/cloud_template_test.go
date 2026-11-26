package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

	// AWS evaluates each action of AssumeRole on its own, and sts:ExternalId is
	// only in the request context of sts:AssumeRole. A SetSourceIdentity grant
	// that also requires the external ID never matches, so the two are apart.
	byAction := map[string]map[string]any{}
	for _, statement := range statements(t, role.Properties["AssumeRolePolicyDocument"]) {
		actions := asList(statement["Action"])
		require.Len(t, actions, 1, "one action per trust statement")
		assert.Equal(t, map[string]any{"AWS": map[string]any{"Ref": "ControlPlanePrincipalArn"}}, statement["Principal"])
		byAction[actions[0]] = statement["Condition"].(map[string]any)
	}
	require.Len(t, byAction, 2)

	assume := byAction["sts:AssumeRole"]
	require.NotNil(t, assume)
	assert.Equal(t, map[string]any{"sts:ExternalId": map[string]any{"Ref": "ExternalId"}}, assume["StringEquals"])

	source := byAction["sts:SetSourceIdentity"]
	require.NotNil(t, source)
	assert.NotContains(t, source, "StringEquals", "the external ID is not in the SetSourceIdentity context")

	for action, condition := range byAction {
		patterns := asList(condition["StringLike"].(map[string]any)["sts:SourceIdentity"])
		assert.ElementsMatch(t, []string{"pds-user-*", "pds-job-*"}, patterns, action)
		for _, pattern := range patterns {
			literal, _, _ := strings.Cut(pattern, "*")
			assert.GreaterOrEqual(t, len(literal), 6, "%s: AWS needs six literal characters before a wildcard", pattern)
		}
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
	require.Len(t, roles, 6, "control plane, CloudFormation service, EKS cluster, node, EBS CSI and agent")

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

func TestTheServiceRoleCarriesTheBuildPoliciesAndTheControlPlaneCarriesTheSweepPolicy(t *testing.T) {
	tpl := loadCloudTemplate(t)
	policies := tpl.ofType("AWS::IAM::ManagedPolicy")
	attached := asList(tpl.Resources["ServiceRole"].Properties["ManagedPolicyArns"])
	for name := range policies {
		if name == "BackupUserBoundary" {
			assert.NotContains(t, fmt.Sprint(tpl.Resources["ServiceRole"].Properties["ManagedPolicyArns"]), name, "a boundary caps a user and grants nothing to a role")
			assert.NotContains(t, fmt.Sprint(tpl.Resources["ControlPlaneRole"].Properties["ManagedPolicyArns"]), name)

			continue
		}
		if name == "CleanupPolicy" {
			assert.NotContains(t, fmt.Sprint(tpl.Resources["ServiceRole"].Properties["ManagedPolicyArns"]), name, "the sweep runs as the control plane")
			assert.Contains(t, fmt.Sprint(tpl.Resources["ControlPlaneRole"].Properties["ManagedPolicyArns"]), name, "%s is attached", name)

			continue
		}
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

// CloudFormation checks that it may tag an EKS resource before the resource
// exists, and deletes one that may never have been made, so neither can depend
// on the ownership tag. Both are scoped by the exact names PDS gives instead:
// pds- and a 12 character environment ID, and what sits under such a cluster.
func TestEksTagsAndDeletesAreScopedByTheNamesPdsGives(t *testing.T) {
	const id = "pds-????????????"
	names := []any{
		map[string]any{"Fn::Sub": "arn:aws:eks:*:${AWS::AccountId}:cluster/" + id},
		map[string]any{"Fn::Sub": "arn:aws:eks:*:${AWS::AccountId}:nodegroup/" + id + "/*"},
		map[string]any{"Fn::Sub": "arn:aws:eks:*:${AWS::AccountId}:addon/" + id + "/*"},
		map[string]any{"Fn::Sub": "arn:aws:eks:*:${AWS::AccountId}:podidentityassociation/" + id + "/*"},
	}
	region := map[string]any{"aws:RequestedRegion": map[string]any{"Ref": "AllowedRegions"}}
	policy := loadCloudTemplate(t).Resources["EksPolicy"]

	var tag, remove map[string]any
	for _, statement := range statements(t, policy.Properties["PolicyDocument"]) {
		actions := asList(statement["Action"])
		if statement["Effect"] == "Deny" {
			assert.NotContains(t, actions, "eks:TagResource", "a deny would refuse CloudFormation's check before create")
			continue
		}
		if contains(actions, "eks:TagResource") {
			tag = statement
		}
		if statement["Sid"] == "DeleteEksResourcesNamedByPds" {
			remove = statement
		}
	}

	require.NotNil(t, tag, "CloudFormation can tag what it creates")
	assert.Equal(t, []string{"eks:TagResource"}, asList(tag["Action"]))
	assert.Equal(t, names, tag["Resource"])
	assert.Equal(t, map[string]any{
		"Null":       map[string]any{"aws:RequestTag/percona.com/pds": "false"},
		"StringLike": region,
	}, tag["Condition"], "a tag request always carries the ownership tag")

	require.NotNil(t, remove, "CloudFormation can delete what was never made")
	deletes := asList(remove["Action"])
	assert.Len(t, deletes, 4, "one delete for each kind of name")
	for _, action := range deletes {
		assert.True(t, strings.HasPrefix(action, "eks:Delete"), "%s: only deletes are scoped by name alone", action)
	}
	assert.Equal(t, names, remove["Resource"])
	assert.Equal(t, map[string]any{"StringLike": region}, remove["Condition"])
}

// EC2 authorizes CreateNatGateway against the existing subnet and Elastic IP as
// well as the new gateway, and request tags are only in the context of the
// resource being created. The subnet and the address are matched by the
// ownership tag they already carry.
func TestANatGatewayIsCreatedFromTheOwnedSubnetAndAddress(t *testing.T) {
	policy := loadCloudTemplate(t).Resources["NetworkPolicy"]

	var owned map[string]any
	for _, statement := range statements(t, policy.Properties["PolicyDocument"]) {
		if statement["Effect"] == "Allow" && statement["Sid"] == "CreateNatGatewayFromOwnedParts" {
			owned = statement
		}
	}

	require.NotNil(t, owned, "CloudFormation can use the subnet and the address it made")
	assert.Equal(t, []string{"ec2:CreateNatGateway"}, asList(owned["Action"]))
	assert.Equal(t, []any{
		map[string]any{"Fn::Sub": "arn:aws:ec2:*:${AWS::AccountId}:subnet/*"},
		map[string]any{"Fn::Sub": "arn:aws:ec2:*:${AWS::AccountId}:elastic-ip/*"},
	}, owned["Resource"])
	assert.Equal(t, map[string]any{
		"Null":       map[string]any{"aws:ResourceTag/percona.com/pds": "false"},
		"StringLike": map[string]any{"aws:RequestedRegion": map[string]any{"Ref": "AllowedRegions"}},
	}, owned["Condition"])
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

// EKS reads a service-linked role to see whether it exists before it asks for
// one, so the service role may read exactly the ones it may have made.
func TestTheServiceRoleMayReadTheServiceLinkedRolesItMayCreate(t *testing.T) {
	var made []string
	var read []any
	for _, statement := range statements(t, loadCloudTemplate(t).Resources["IamPolicy"].Properties["PolicyDocument"]) {
		actions := asList(statement["Action"])
		if contains(actions, "iam:CreateServiceLinkedRole") {
			made = asList(statement["Condition"].(map[string]any)["StringEquals"].(map[string]any)["iam:AWSServiceName"])
		}
		if statement["Sid"] == "ReadTheServiceLinkedRoles" {
			assert.Equal(t, []string{"iam:GetRole"}, actions)
			read, _ = statement["Resource"].([]any)
		}
	}

	require.NotEmpty(t, made)
	want := make([]any, 0, len(made))
	for _, service := range made {
		want = append(want, map[string]any{"Fn::Sub": "arn:aws:iam::${AWS::AccountId}:role/aws-service-role/" + service + "/*"})
	}
	assert.Equal(t, want, read)
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

// sdkAction maps a method of an AWS SDK service the aws client calls to the IAM
// actions the call needs. A method the client declares and this table lacks
// fails the audit below, so a new call cannot ship without being granted.
var sdkActions = map[string][]string{
	"cloudformation.CreateStack":                       {"cloudformation:CreateStack"},
	"cloudformation.DeleteStack":                       {"cloudformation:DeleteStack"},
	"cloudformation.DescribeStacks":                    {"cloudformation:DescribeStacks"},
	"cloudformation.DescribeStackEvents":               {"cloudformation:DescribeStackEvents"},
	"cloudformation.CreateChangeSet":                   {"cloudformation:CreateChangeSet"},
	"cloudformation.DescribeChangeSet":                 {"cloudformation:DescribeChangeSet"},
	"cloudformation.ExecuteChangeSet":                  {"cloudformation:ExecuteChangeSet"},
	"cloudformation.DeleteChangeSet":                   {"cloudformation:DeleteChangeSet"},
	"cloudformation.DetectStackDrift":                  {"cloudformation:DetectStackDrift"},
	"cloudformation.DescribeStackDriftDetectionStatus": {"cloudformation:DescribeStackDriftDetectionStatus"},
	"cloudformation.DescribeStackResourceDrifts":       {"cloudformation:DescribeStackResourceDrifts"},
	"eks.UpdateClusterConfig":                          {"eks:UpdateClusterConfig"},
	"eks.CreateAccessEntry":                            {"eks:CreateAccessEntry"},
	"eks.DeleteAccessEntry":                            {"eks:DeleteAccessEntry"},
	"eks.AssociateAccessPolicy":                        {"eks:AssociateAccessPolicy"},
	"eks.DescribeCluster":                              {"eks:DescribeCluster"},
	"eks.DescribeUpdate":                               {"eks:DescribeUpdate"},
	"eks.ListNodegroups":                               {"eks:ListNodegroups"},
	"eks.DescribeNodegroup":                            {"eks:DescribeNodegroup"},
	"ec2.DescribeRegions":                              {"ec2:DescribeRegions"},
	"ec2.DeleteVolume":                                 {"ec2:DeleteVolume"},
	"ec2.DeleteSnapshot":                               {"ec2:DeleteSnapshot"},
	"ec2.DeleteNetworkInterface":                       {"ec2:DeleteNetworkInterface"},
	"ec2.DeleteSecurityGroup":                          {"ec2:DeleteSecurityGroup"},
	"ec2.DescribeInstances":                            {"ec2:DescribeInstances"},
	"ec2.DescribeNatGateways":                          {"ec2:DescribeNatGateways"},
	"elasticloadbalancingv2.DeleteLoadBalancer":        {"elasticloadbalancing:DeleteLoadBalancer"},
	"elasticloadbalancingv2.DeleteTargetGroup":         {"elasticloadbalancing:DeleteTargetGroup"},
	"resourcegroupstaggingapi.GetResources":            {"tag:GetResources"},
	"servicequotas.GetServiceQuota":                    {"servicequotas:GetServiceQuota"},
	"s3.CreateBucket":                                  {"s3:CreateBucket"},
	"s3.PutPublicAccessBlock":                          {"s3:PutBucketPublicAccessBlock"},
	"s3.PutBucketTagging":                              {"s3:PutBucketTagging"},
	"s3.GetBucketTagging":                              {"s3:GetBucketTagging"},
	"s3.ListObjectVersions":                            {"s3:ListBucketVersions"},
	"s3.DeleteObjects":                                 {"s3:DeleteObject", "s3:DeleteObjectVersion"},
	"s3.DeleteBucket":                                  {"s3:DeleteBucket"},
}

// declaredSDKCalls is every SDK method a client package names in an interface,
// which is how each of its calls is declared and faked.
func declaredSDKCalls(t *testing.T, dir string) []string {
	t.Helper()

	files, err := filepath.Glob(dir + "/*.go")
	require.NoError(t, err)
	declared := regexp.MustCompile(`(?m)^\s+([A-Z][A-Za-z]+)\(context\.Context, \*([a-z0-9]+)\.[A-Za-z]+Input`)
	seen := map[string]bool{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		for _, m := range declared.FindAllStringSubmatch(string(body), -1) {
			seen[m[2]+"."+m[1]] = true
		}
	}
	require.NotEmpty(t, seen)

	out := make([]string, 0, len(seen))
	for call := range seen {
		out = append(out, call)
	}

	return out
}

// grantedTo is every action a role's policies allow: its inline policies and the
// managed policies it lists.
func grantedTo(t *testing.T, tpl cfn, role string) []string {
	t.Helper()

	var documents []any
	if inline, ok := tpl.Resources[role].Properties["Policies"].([]any); ok {
		for _, entry := range inline {
			documents = append(documents, entry.(map[string]any)["PolicyDocument"])
		}
	}
	if attached, ok := tpl.Resources[role].Properties["ManagedPolicyArns"].([]any); ok {
		for _, arn := range attached {
			if ref, ok := arn.(map[string]any)["Ref"].(string); ok {
				if policy, ok := tpl.Resources[ref]; ok && policy.Type == "AWS::IAM::ManagedPolicy" {
					documents = append(documents, policy.Properties["PolicyDocument"])
				}
			}
		}
	}
	var actions []string
	for _, document := range documents {
		for _, statement := range statements(t, document) {
			if statement["Effect"] == "Allow" {
				actions = append(actions, asList(statement["Action"])...)
			}
		}
	}

	return actions
}

func allows(granted []string, action string) bool {
	for _, pattern := range granted {
		if ok, _ := filepath.Match(pattern, action); ok {
			return true
		}
	}

	return false
}

func TestEveryAWSCallTheControlPlaneMakesIsGrantedToItsRole(t *testing.T) {
	tpl := loadCloudTemplate(t)
	granted := grantedTo(t, tpl, "ControlPlaneRole")

	for _, call := range declaredSDKCalls(t, "../client/aws") {
		needs, known := sdkActions[call]
		require.True(t, known, "%s is called by the aws client and the audit does not know which actions it needs", call)
		for _, action := range needs {
			assert.True(t, allows(granted, action), "%s needs %s, which the control plane role is not granted", call, action)
		}
	}
	assert.True(t, allows(granted, "iam:GetRole"), "the connection check reads the roles")
}

func TestTheControlPlaneRoleIsNotGrantedTheCreateActionsOfTheServiceRole(t *testing.T) {
	granted := grantedTo(t, loadCloudTemplate(t), "ControlPlaneRole")

	for _, action := range granted {
		assert.NotRegexp(t, `^(ec2:(Create|Allocate|Attach|Authorize|Run)|eks:(CreateCluster|CreateNodegroup|CreateAddon)|iam:(Create|Put|Attach|Update))`, action,
			"%s lets the control plane build what only the service role may", action)
	}
}

func TestCloudFormationCallsThatCarryNoRoleAreNotRequiredToNameOne(t *testing.T) {
	tpl := loadCloudTemplate(t)
	carries := map[string]bool{"cloudformation:CreateStack": true, "cloudformation:CreateChangeSet": true, "cloudformation:UpdateStack": true}

	found := map[string]bool{}
	for _, entry := range tpl.Resources["ControlPlaneRole"].Properties["Policies"].([]any) {
		for _, statement := range statements(t, entry.(map[string]any)["PolicyDocument"]) {
			if statement["Effect"] != "Allow" {
				continue
			}
			condition, _ := statement["Condition"].(map[string]any)
			for _, action := range asList(statement["Action"]) {
				if !strings.HasPrefix(action, "cloudformation:") {
					continue
				}
				found[action] = true
				if carries[action] {
					assert.Contains(t, condition, "StringEquals", "%s names the service role", action)
					assert.Contains(t, fmt.Sprint(condition["StringEquals"]), "cloudformation:RoleArn", action)

					continue
				}
				assert.NotContains(t, fmt.Sprint(condition["StringEquals"]), "cloudformation:RoleArn",
					"%s is called without a role, so a StringEquals on it would refuse every call", action)
			}
		}
	}
	for _, action := range []string{"cloudformation:DeleteStack", "cloudformation:ExecuteChangeSet", "cloudformation:DeleteChangeSet"} {
		assert.True(t, found[action], action)
	}
}

func TestEveryDestructiveActionOfTheControlPlaneNeedsAnOwnershipTag(t *testing.T) {
	tpl := loadCloudTemplate(t)
	destructive := regexp.MustCompile(`^(ec2:Delete|elasticloadbalancing:Delete)`)

	for _, policy := range tpl.ofType("AWS::IAM::ManagedPolicy") {
		for _, statement := range statements(t, policy.Properties["PolicyDocument"]) {
			if statement["Effect"] != "Allow" {
				continue
			}
			for _, action := range asList(statement["Action"]) {
				if !destructive.MatchString(action) || !allows(grantedTo(t, tpl, "ControlPlaneRole"), action) {
					continue
				}
				condition, _ := statement["Condition"].(map[string]any)
				assert.Regexp(t, `aws:ResourceTag/(percona\.com/pds|elbv2\.k8s\.aws/cluster|aws:eks:cluster-name)`, fmt.Sprint(condition),
					"%s may delete anything in the account without an ownership tag", action)
			}
		}
	}
}

func TestEveryAWSCallTheAgentMakesIsGrantedToItsRole(t *testing.T) {
	granted := grantedTo(t, loadCloudTemplate(t), "AgentRole")

	for _, call := range declaredSDKCalls(t, "../client/backupkey") {
		service, method, _ := strings.Cut(call, ".")
		require.Equal(t, "iam", service, "the agent talks to IAM and nothing else")
		assert.True(t, allows(granted, "iam:"+method), "%s needs iam:%s, which the agent role is not granted", call, method)
	}
}

func TestTheAgentRoleIsAssumedOnlyByPodIdentity(t *testing.T) {
	role := loadCloudTemplate(t).Resources["AgentRole"]
	require.Equal(t, "AWS::IAM::Role", role.Type)

	trust := statements(t, role.Properties["AssumeRolePolicyDocument"])
	require.Len(t, trust, 1)
	assert.Equal(t, map[string]any{"Service": "pods.eks.amazonaws.com"}, trust[0]["Principal"])
	assert.ElementsMatch(t, []string{"sts:AssumeRole", "sts:TagSession"}, asList(trust[0]["Action"]),
		"TagSession is what puts the cluster's name on the session, which the policy reads")
}

func TestTheAgentRoleMayOnlyTouchTheUserNamedAfterItsOwnEKSCluster(t *testing.T) {
	tpl := loadCloudTemplate(t)
	inline := tpl.Resources["AgentRole"].Properties["Policies"].([]any)
	require.Len(t, inline, 1)

	var created bool
	for _, statement := range statements(t, inline[0].(map[string]any)["PolicyDocument"]) {
		require.Equal(t, "Allow", statement["Effect"])
		resource := fmt.Sprint(statement["Resource"])
		assert.Contains(t, resource, "user/pds/${ConnectionId}/${!aws:PrincipalTag/eks-cluster-name}-backup", "a user of another cluster is out of reach")
		for _, action := range asList(statement["Action"]) {
			assert.Regexp(t, `^iam:(CreateUser|TagUser|PutUserPolicy|DeleteUserPolicy|ListUserPolicies|CreateAccessKey|ListAccessKeys|DeleteAccessKey|DeleteUser)$`, action)
		}
		if contains(asList(statement["Action"]), "iam:CreateUser") {
			created = true
			condition := fmt.Sprint(statement["Condition"])
			assert.Contains(t, condition, "iam:PermissionsBoundary")
			assert.Contains(t, condition, "BackupUserBoundary", "a user is only made with the boundary that caps it")
		}
	}
	assert.True(t, created)
}

func TestTheBackupBoundaryReachesBackupBucketsAndNothingElse(t *testing.T) {
	boundary := loadCloudTemplate(t).Resources["BackupUserBoundary"]
	require.Equal(t, "AWS::IAM::ManagedPolicy", boundary.Type)

	for _, statement := range statements(t, boundary.Properties["PolicyDocument"]) {
		require.Equal(t, "Allow", statement["Effect"])
		for _, action := range asList(statement["Action"]) {
			assert.Regexp(t, `^s3:`, action)
		}
		for _, resource := range asList(statement["Resource"]) {
			assert.NotEqual(t, "*", resource)
		}
		assert.Contains(t, fmt.Sprint(statement["Resource"]), "arn:aws:s3:::pds-${AWS::AccountId}-", "the bucket name follows from the account and the environment")
	}
}

func TestTheServiceRoleMayHandTheAgentRoleToPodIdentity(t *testing.T) {
	tpl := loadCloudTemplate(t)
	var passed bool
	for _, statement := range statements(t, tpl.Resources["IamPolicy"].Properties["PolicyDocument"]) {
		if contains(asList(statement["Action"]), "iam:PassRole") && strings.Contains(fmt.Sprint(statement["Resource"]), "AgentRole") {
			passed = true
			assert.Contains(t, fmt.Sprint(statement["Condition"]), "pods.eks.amazonaws.com")
		}
	}
	assert.True(t, passed)
	assert.Contains(t, tpl.Outputs, "AgentRoleArn")
}

// EKS reads the policies attached to the node role with the caller's
// credentials before it creates a node group.
func TestTheServiceRoleMayListThePoliciesOfTheConnectionsRoles(t *testing.T) {
	var listed bool
	for _, statement := range statements(t, loadCloudTemplate(t).Resources["IamPolicy"].Properties["PolicyDocument"]) {
		if statement["Effect"] == "Allow" && contains(asList(statement["Action"]), "iam:ListAttachedRolePolicies") {
			listed = true
			assert.Equal(t, map[string]any{"Fn::Sub": "arn:aws:iam::${AWS::AccountId}:role/pds/${ConnectionId}/*"}, statement["Resource"])
		}
	}
	assert.True(t, listed, "CloudFormation can create the node group")
}

func TestTheGrantsAnAWSRefusalIsComparedWithAreTheTemplatesOwn(t *testing.T) {
	granted, err := CloudTemplateGrants()
	require.NoError(t, err)

	assert.True(t, granted("ec2:CreateVpc"), "the service role may create a VPC")
	assert.True(t, granted("elasticloadbalancing:DeleteLoadBalancer"), "the control plane role may sweep load balancers")
	assert.True(t, granted("ec2:DescribeVpcs"), "a wildcard grant covers what it names")
	assert.True(t, granted("eks:CreateCluster"))
	assert.False(t, granted("ec2:RunInstances"), "nothing grants it, so a refusal is an account policy's doing")
	assert.False(t, granted("iam:CreateRole"))
	assert.False(t, granted(""))
}

// holdsAllowedRegions reports whether a condition operator's map names the
// AllowedRegions parameter.
func holdsAllowedRegions(keys map[string]any) bool {
	ref, ok := keys["aws:RequestedRegion"].(map[string]any)
	return ok && ref["Ref"] == "AllowedRegions"
}

// walkConditions calls visit for every condition operator map in the template.
func walkConditions(node any, visit func(operator string, keys map[string]any)) {
	switch v := node.(type) {
	case map[string]any:
		if condition, ok := v["Condition"].(map[string]any); ok {
			for operator, keys := range condition {
				if m, ok := keys.(map[string]any); ok {
					visit(operator, m)
				}
			}
		}
		for _, child := range v {
			walkConditions(child, visit)
		}
	case []any:
		for _, child := range v {
			walkConditions(child, visit)
		}
	}
}

// The regions can be the single value "*", and StringEquals does not read a
// wildcard, so a region condition has to use StringLike. Exact region names
// match the same way under either.
func TestEveryRegionConditionMatchesWildcardsSoAllRegionsCanBeAllowed(t *testing.T) {
	tpl := loadCloudTemplate(t)
	found := 0
	for name, resource := range tpl.Resources {
		walkConditions(resource.Properties, func(operator string, keys map[string]any) {
			if !holdsAllowedRegions(keys) {
				return
			}
			found++
			assert.Equal(t, "StringLike", operator, "%s: aws:RequestedRegion needs StringLike", name)
		})
	}
	assert.Positive(t, found, "the template limits what it grants by region")
}
