package managed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/provision"
)

// KindRebuild is the run that builds a lost environment again from its stacks.
const KindRebuild = "rebuild"

// RebuildStepNames lists the steps of a rebuild run: the create steps after the
// old stack is gone.
var RebuildStepNames = []string{
	stepAssumeRole, stepClearStack, stepCreateStack, stepWaitStack, stepCreateBucket, stepOpenAccess, stepInstallFlux,
	stepWriteRelease, stepJoin, stepCloseAccess, stepWaitHeartbeat,
}

const (
	clearFailed  = "AWS could not remove what is left of the old environment. Fix what AWS reports, then retry the step. Contact Percona support if it fails again."
	clearPending = "What is left of the old environment is still being removed. Retry to keep waiting."
)

// RebuildKind is the run that builds a lost environment again: it removes what
// is left of the old stack, makes the stack again, and installs the chart
// version the environment ran. The backup bucket is kept, so backups restore
// afterwards.
func RebuildKind(d Deps) provision.Kind {
	d.Settings = d.Settings.withDefaults()
	steps := d.createSteps()
	steps[stepClearStack] = d.clearStack

	return d.kind(KindRebuild, RebuildStepNames, steps)
}

// clearStack deletes the environment's stack and waits until it is gone. A stack
// of an environment whose cluster was deleted outside PDS may be complete as far
// as CloudFormation knows, and CloudFormation cannot make a cluster it believes
// exists.
func (d Deps) clearStack(ctx context.Context, _ provision.Env, s scope) error {
	deadline := time.Now().Add(d.Settings.StackWait)
	deleted := false
	for {
		state, err := d.Cloud.Stack(ctx, s.target)
		if err != nil {
			return s.fail(err)
		}

		switch {
		case !state.Exists:
			return nil
		case state.Status == "DELETE_IN_PROGRESS":
		case state.Status == "DELETE_FAILED" && deleted:
			cause := errors.New(state.FailureReason)
			if refusal, ok := aws.Classify(cause, s.where); ok {
				return provision.Permanent(cause, refusal.Sentence(s.where))
			}

			return provision.Permanent(fmt.Errorf("stack status %s: %s", state.Status, state.FailureReason), clearFailed)
		case !deleted:
			if err := d.Cloud.DeleteStack(ctx, s.target); err != nil {
				return s.fail(err)
			}
			deleted = true
		}

		if time.Now().After(deadline) {
			return provision.Retryable(errors.New("the old stack is still being deleted"), clearPending)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.Settings.PollEvery):
		}
	}
}
