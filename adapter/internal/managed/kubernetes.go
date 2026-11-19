package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/provision"
	"github.com/percona/pds/adapter/internal/store"
)

// KindKubernetes is the run kind that updates an environment's stack: the
// template revision this release embeds and, when asked, the next Kubernetes
// minor version of the cluster and its node group.
const KindKubernetes = "upgrade_kubernetes"

const stepUpdateStack = "update-stack"

// KubernetesStepNames lists the steps of an upgrade_kubernetes run.
var KubernetesStepNames = []string{stepAssumeRole, stepUpdateStack}

// KubernetesParams are the parameters of an upgrade_kubernetes run.
type KubernetesParams struct {
	// KubernetesVersion is the minor version to move to, or empty to update only
	// the template revision.
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`
	// ConfirmNodeGroupReplace allows an update that replaces the node group,
	// which restarts every database.
	ConfirmNodeGroupReplace bool `json:"confirmNodeGroupReplace,omitempty"`
}

var minorVersion = regexp.MustCompile(`^1\.\d{2,3}$`)

// ChangeSet resource types that decide whether an update may go on.
const (
	typeVPC       = "AWS::EC2::VPC"
	typeCluster   = "AWS::EKS::Cluster"
	typeNodeGroup = "AWS::EKS::Nodegroup"
)

const (
	notUpdating   = "The update names no version it can move to. Contact Percona support."
	wouldReplace  = "This update would replace the network or the Kubernetes cluster, so PDS stopped before changing anything. Contact Percona support."
	needsConfirm  = "This update replaces the node group, which restarts every database. Start the update again and confirm the replacement to go on."
	updateFailed  = "AWS could not apply the update and put the environment back as it was. Retry the step. Contact Percona support if it fails again."
	updatePending = "The update has not finished in AWS. Retry to keep waiting."
	clusterBehind = "AWS finished the update but the Kubernetes cluster is not on the new version yet. Retry to check again."
)

// Verdict is what a change set would do to the parts of an environment that
// matter for a decision.
type Verdict struct {
	Plan aws.ChangePlan
	// ReplacesNetwork means the VPC or the EKS cluster would be replaced or
	// removed. No update goes on from there.
	ReplacesNetwork bool
	// ReplacesNodeGroup means the node group would be replaced. It goes on only
	// with an explicit confirm.
	ReplacesNodeGroup bool
}

// Judge reads a change set for the resources whose replacement decides the update.
func Judge(plan aws.ChangePlan) Verdict {
	v := Verdict{Plan: plan}
	for _, c := range plan.Changes {
		replaced := c.Action == "Remove" || c.Replacement == "True" || c.Replacement == "Conditional"
		switch {
		case replaced && (c.Type == typeVPC || c.Type == typeCluster):
			v.ReplacesNetwork = true
		case replaced && c.Type == typeNodeGroup:
			v.ReplacesNodeGroup = true
		}
	}

	return v
}

// KubernetesKind is the run that updates an environment's stack through a
// change set. It stops before changing anything when the change set would
// replace the VPC or the EKS cluster, and when it would replace the node group
// unless the run was confirmed.
func KubernetesKind(d Deps) provision.Kind {
	d.Settings = d.Settings.withDefaults()

	return d.kind(KindKubernetes, KubernetesStepNames, map[string]stepFunc{
		stepAssumeRole:  d.checkRole,
		stepUpdateStack: d.updateStack,
	})
}

// checkRole is the first step of a run that builds nothing new: it needs the
// connection to work, and no quota check.
func (d Deps) checkRole(ctx context.Context, _ provision.Env, s scope) error {
	if err := d.connectionReady(s); err != nil {
		return err
	}
	if err := d.Cloud.CheckRole(ctx, s.target); err != nil {
		return s.fail(err)
	}

	return nil
}

func (d Deps) connectionReady(s scope) error {
	if s.conn.State != store.CloudReady {
		return fmt.Errorf("%w: the connection is %s", provision.ErrConnectionUnavailable, s.conn.State)
	}

	return nil
}

// UpdateSpec is the update of the stack to the template this release embeds,
// moving the Kubernetes version when one is given.
func (d Deps) updateSpec(s scope, name, kubernetesVersion string) aws.UpdateSpec {
	base := d.stackSpec(s)
	spec := aws.UpdateSpec{Name: name, Template: base.Template, ServiceRoleARN: base.ServiceRoleARN, Tags: base.Tags}
	if kubernetesVersion != "" {
		spec.Parameters = map[string]string{"KubernetesVersion": kubernetesVersion}
	}

	return spec
}

// updated reports whether the stack carries what the update asked for.
func updated(state aws.StackState, params KubernetesParams) bool {
	return state.Complete() &&
		state.Outputs["TemplateRevision"] == strconv.Itoa(TemplateRevision) &&
		(params.KubernetesVersion == "" || state.Parameters["KubernetesVersion"] == params.KubernetesVersion)
}

func (d Deps) updateStack(ctx context.Context, env provision.Env, s scope) error {
	var params KubernetesParams
	if err := json.Unmarshal([]byte(env.Run.Params), &params); err != nil ||
		(params.KubernetesVersion != "" && !minorVersion.MatchString(params.KubernetesVersion)) {
		return provision.Permanent(fmt.Errorf("the run parameters %q are not an update", env.Run.Params), notUpdating)
	}

	state, err := d.Cloud.Stack(ctx, s.target)
	if err != nil {
		return s.fail(err)
	}
	if !state.Exists {
		return provision.Permanent(aws.ErrStackMissing, "The environment's stack is missing from the AWS account. Rebuild the environment.")
	}
	if !updated(state, params) && !stackBusy(state) {
		if err := d.applyUpdate(ctx, env, s, params); err != nil {
			return err
		}
	}

	return d.waitUpdate(ctx, s, params)
}

// stackBusy reports whether CloudFormation is already working on the stack, as
// it is when an earlier attempt applied the change set before a restart.
func stackBusy(state aws.StackState) bool {
	return strings.HasSuffix(state.Status, "_IN_PROGRESS")
}

// applyUpdate makes a fresh change set, reads what it would do, and executes it
// unless that would replace what must not be replaced.
func (d Deps) applyUpdate(ctx context.Context, env provision.Env, s scope, params KubernetesParams) error {
	name := fmt.Sprintf("pds-run-%d-%d", env.Run.ID, time.Now().UnixNano())
	plan, err := d.Cloud.PlanUpdate(ctx, s.target, d.updateSpec(s, name, params.KubernetesVersion))
	if err != nil {
		return s.fail(err)
	}
	if plan.Empty {
		return nil
	}

	verdict := Judge(plan)
	refusal := error(nil)
	switch {
	case verdict.ReplacesNetwork:
		refusal = provision.Permanent(errors.New("the change set replaces the VPC or the EKS cluster"), wouldReplace)
	case verdict.ReplacesNodeGroup && !params.ConfirmNodeGroupReplace:
		refusal = provision.Permanent(errors.New("the change set replaces the node group"), needsConfirm)
	}
	if refusal != nil {
		if err := d.Cloud.DiscardPlan(ctx, s.target, plan.Name); err != nil {
			return errors.Join(refusal, s.fail(err))
		}

		return refusal
	}
	if err := d.Cloud.ApplyPlan(ctx, s.target, plan.Name); err != nil {
		return s.fail(err)
	}

	return nil
}

// waitUpdate waits for the stack to carry the update, then checks the cluster
// says so too. A rollback that finished is the failure; one seen before this
// attempt's change set started is not, which is why it counts only after the
// stack was seen working.
func (d Deps) waitUpdate(ctx context.Context, s scope, params KubernetesParams) error {
	deadline := time.Now().Add(d.Settings.StackWait)
	sawWork := false
	for {
		state, err := d.Cloud.Stack(ctx, s.target)
		if err != nil {
			return s.fail(err)
		}
		switch {
		case updated(state, params):
			return d.confirmCluster(ctx, s, params)
		case stackBusy(state):
			sawWork = true
		case sawWork && (state.Status == "UPDATE_ROLLBACK_COMPLETE" || state.Status == "UPDATE_ROLLBACK_FAILED" || state.Failed()):
			return updateFailure(state, s)
		}
		if time.Now().After(deadline) {
			return provision.Permanent(errors.New("the stack did not finish updating before the deadline"), updatePending)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.Settings.PollEvery):
		}
	}
}

func (d Deps) confirmCluster(ctx context.Context, s scope, params KubernetesParams) error {
	if params.KubernetesVersion == "" {
		return nil
	}
	cluster, err := d.Cloud.Cluster(ctx, s.target)
	if err != nil {
		return s.fail(err)
	}
	if cluster.Version != params.KubernetesVersion {
		return provision.Permanent(fmt.Errorf("the cluster is on %q, not %q", cluster.Version, params.KubernetesVersion), clusterBehind)
	}

	return nil
}

// updateFailure says why an update was rolled back: a typed refusal when the
// first failed resource names one, otherwise the generic sentence.
func updateFailure(state aws.StackState, s scope) error {
	cause := errors.New(state.FailureReason)
	if refusal, ok := aws.Classify(cause, s.where); ok {
		return provision.Permanent(cause, refusal.Sentence(s.where))
	}

	return provision.Permanent(fmt.Errorf("stack status %s: %s", state.Status, state.FailureReason), updateFailed)
}
