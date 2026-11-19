// Package managed builds a managed environment: the infrastructure in the
// customer's AWS account, the chart and Flux on it, and the join that makes it
// an ordinary environment. Its steps run as provisioning runs.
package managed

import _ "embed"

// TemplateRevision stamps the stack templates. It is the revision in
// charts/pds/compatibility.yaml, recorded on every stack as a tag and an
// output, and a test holds the two together.
const TemplateRevision = 1

// NetworkClusterTemplate is the CloudFormation template of an environment's
// network and Kubernetes cluster. Only Percona changes it, through a release.
//
//go:embed templates/network_cluster.yaml
var NetworkClusterTemplate string

// StackParameterNames are the template parameters that have no default, which
// the create step always passes.
var StackParameterNames = []string{
	"EnvironmentId", "ClusterName", "EksClusterRoleArn", "NodeRoleArn", "EbsCsiRoleArn",
}
