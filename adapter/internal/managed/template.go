// Package managed builds a managed environment: the infrastructure in the
// customer's AWS account, the chart and Flux on it, and the join that makes it
// an ordinary environment. Its steps run as provisioning runs.
package managed

import _ "embed"

// TemplateRevision stamps the stack templates. It is the revision in
// charts/pds/compatibility.yaml, recorded on every stack as a tag and an
// output, and a test holds the two together.
const TemplateRevision = 1

// NewEnvironmentKubernetesVersion is the Kubernetes version a new environment
// starts on: the default of the template's KubernetesVersion parameter. An
// environment on an older minor version has a Kubernetes upgrade available.
const NewEnvironmentKubernetesVersion = "1.33"

// OwnershipTag is the tag key on everything an environment owns in AWS. Its
// value is the environment's ID.
const OwnershipTag = "percona.com/pds"

// NetworkTemplate is the CloudFormation template of an environment's
// network and Kubernetes cluster. Only Percona changes it, through a release.
//
//go:embed templates/network_cluster.yaml
var NetworkTemplate string

// StackParameterNames are the template parameters that have no default, which
// the create step always passes.
var StackParameterNames = []string{
	"EnvironmentId", "EksName", "EksRoleArn", "NodeRoleArn", "EbsCsiRoleArn",
}
