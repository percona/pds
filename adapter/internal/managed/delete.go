package managed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/provision"
	"github.com/percona/pds/adapter/internal/store"
)

// KindDelete is the run kind that removes a managed environment. It is the one
// kind that may start beside an unfinished run.
const KindDelete = store.KindDelete

// The names of the delete steps, in order. assume-role and close-access are the
// create steps of the same name.
const (
	stepRemoveDatabases = "remove-databases"
	stepRemoveServices  = "remove-services"
	stepRemoveBackupKey = "remove-backup-user"
	stepDeleteStack     = "delete-stack"
	stepSweep           = "sweep"
	stepDeleteBucket    = "delete-bucket"
	stepForget          = "forget"
)

// DeleteStepNames lists the delete steps in the order they run. Teardown goes
// inside-out: what runs in the environment, then what holds AWS resources, then
// the stack, then what the stack leaves, then the backups.
var DeleteStepNames = []string{
	stepAssumeRole, stepRemoveDatabases, stepRemoveServices, stepRemoveBackupKey, stepCloseAccess, stepDeleteStack, stepSweep, stepDeleteBucket, stepForget,
}

// DeletePhase is the phase of a delete the console shows a step under, or empty
// for a name that is not a delete step.
func DeletePhase(step string) string {
	switch step {
	case stepAssumeRole, stepRemoveDatabases:
		return "Databases"
	case stepRemoveServices, stepCloseAccess:
		return "Load balancers"
	case stepDeleteStack:
		return "Kubernetes cluster"
	case stepSweep:
		return "Network"
	case stepRemoveBackupKey, stepDeleteBucket, stepForget:
		return "Backups"
	}

	return ""
}

// DeleteBlocked starts the sentence of a delete that stopped because AWS still
// holds something. The run is failed and Retry rediscovers.
const DeleteBlocked = "Delete blocked."

// Sentences a person reads while a delete works or when it stops.
const (
	stillRemoving = "Delete blocked. Databases or volume claims are still being removed from the environment. Retry in a few minutes."
	stackBlocked  = "Delete blocked. AWS could not delete the network and the Kubernetes cluster. Retry. Contact Percona support if it fails again."
	apiSilent     = "The Kubernetes API did not answer, so the %s were not removed from inside it. The cluster is deleted next and what it leaves is swept by tag."
	// backupUserStays is for a delete whose environment did not remove its IAM user.
	backupUserStays = "The IAM user %s stays in your AWS account, because the environment did not remove it. Delete it in the IAM console."
)

// maxListed bounds the resources a blocked sentence names: the sentence is kept
// in a column of 512 characters.
const maxListed = 4

// Sweeper finds and removes what an environment owns in AWS beyond what its
// Kubernetes API holds. *aws.Infra is the implementation.
type Sweeper interface {
	// Owned rediscovers the environment's resources by tag, every time.
	Owned(ctx context.Context, t aws.Target, environmentID string) ([]aws.Resource, error)
	Release(ctx context.Context, t aws.Target, r aws.Resource) error
	// DeleteBucket empties and deletes the bucket when it carries the
	// environment's tag, and reports whether it deleted one.
	DeleteBucket(ctx context.Context, t aws.Target, bucket, environmentID string) (bool, error)
}

// Drainer removes, from inside the environment, what holds volumes and load
// balancers in AWS. *Drain is the implementation.
type Drainer interface {
	// RemoveDatabases and RemoveVolumeUsers return what is left.
	RemoveDatabases(ctx context.Context, access KubeAccess) (int, error)
	RemoveVolumeUsers(ctx context.Context, access KubeAccess) (int, error)
	// RemoveBackupUser asks the environment's agent to remove the IAM user it
	// made for backups, and returns 1 until it has.
	RemoveBackupUser(ctx context.Context, access KubeAccess) (int, error)
}

// DeleteKind is the run that removes a managed environment, step by step. Every
// step is idempotent, nothing is remembered between attempts, and every attempt
// rediscovers by tag, so a restart or a Retry resumes from where AWS is.
func DeleteKind(d Deps) provision.Kind {
	d.Settings = d.Settings.withDefaults()
	steps := map[string]func(context.Context, provision.Env, scope) error{
		stepAssumeRole:      d.deleteAssumeRole,
		stepRemoveDatabases: d.removeDatabases,
		stepRemoveServices:  d.removeServices,
		stepRemoveBackupKey: d.removeBackupKey,
		stepCloseAccess:     d.closeAccess,
		stepDeleteStack:     d.deleteStack,
		stepSweep:           d.sweep,
		stepDeleteBucket:    d.deleteBucket,
	}
	kind := provision.Kind{Name: KindDelete}
	for _, name := range DeleteStepNames {
		if name == stepForget {
			kind.Steps = append(kind.Steps, provision.Step{Name: name, Run: d.forget})

			continue
		}
		run := steps[name]
		kind.Steps = append(kind.Steps, provision.Step{Name: name, Run: func(ctx context.Context, env provision.Env) error {
			s, err := d.scope(ctx, env.Run)
			if err != nil {
				return err
			}

			return run(ctx, env, s)
		}})
	}

	return kind
}

// deleteAssumeRole pauses the run while the connection is Unreachable. Any
// other state, an old template included, may still assume the role, and a
// delete should not wait on an upgrade the person has not made.
func (d Deps) deleteAssumeRole(ctx context.Context, _ provision.Env, s scope) error {
	if s.conn.State == store.CloudUnreachable {
		return fmt.Errorf("%w: the connection is %s", provision.ErrConnectionUnavailable, s.conn.State)
	}
	if err := d.Cloud.CheckRole(ctx, s.target); err != nil {
		return s.fail(err)
	}

	return nil
}

// insideEnvironment opens the Access window and runs the removal against the
// Kubernetes API until nothing is left. A cluster that does not exist has
// nothing in it. An API that stays silent for the APIWait is not waited on: the
// step records it, and the deletion goes on to the cluster and the sweep.
func (d Deps) insideEnvironment(
	ctx context.Context, env provision.Env, s scope, what string,
	wait time.Duration, remove func(KubeAccess) (int, error), timedOut func(left int) error,
) error {
	access, err := d.open(ctx, env, s)
	if aws.EKSMissing(err) {
		env.Note("The Kubernetes cluster was already gone, so nothing was removed from inside it.")

		return nil
	}
	if err != nil {
		return err
	}

	now := time.Now()
	deadline := now.Add(wait)
	silentSince := now
	for {
		left, err := remove(access)
		switch {
		case err != nil && time.Since(silentSince) >= d.Settings.APIWait:
			env.Note(fmt.Sprintf(apiSilent, what))

			return nil
		case err == nil && left == 0:
			return nil
		case err == nil:
			silentSince = time.Now()
		}
		if time.Now().After(deadline) {
			return timedOut(left)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.Settings.PollEvery):
		}
		// The token lasts a minute.
		if access.Token, err = d.Cloud.KubeToken(ctx, s.target); err != nil {
			return s.fail(err)
		}
	}
}

func (d Deps) removeDatabases(ctx context.Context, env provision.Env, s scope) error {
	return d.insideEnvironment(ctx, env, s, "databases", d.Settings.DrainWait, func(a KubeAccess) (int, error) {
		return d.Drainer.RemoveDatabases(ctx, a)
	}, stillInEnvironment("databases"))
}

func (d Deps) removeServices(ctx context.Context, env provision.Env, s scope) error {
	return d.insideEnvironment(ctx, env, s, "load balancers and volume claims", d.Settings.DrainWait, func(a KubeAccess) (int, error) {
		return d.Drainer.RemoveVolumeUsers(ctx, a)
	}, stillInEnvironment("load balancers and volume claims"))
}

func stillInEnvironment(what string) func(left int) error {
	return func(left int) error {
		return provision.Permanent(fmt.Errorf("%d %s are still in the environment", left, what), stillRemoving)
	}
}

// removeBackupKey asks the agent to remove the IAM user it made for backups,
// because the control plane's role cannot. An agent that does not answer in the
// time a delete waits for the environment is not waited on for ever: the step
// says which user stays, and the customer removes it.
func (d Deps) removeBackupKey(ctx context.Context, env provision.Env, s scope) error {
	return d.insideEnvironment(ctx, env, s, "backup users", d.Settings.BackupUserWait, func(a KubeAccess) (int, error) {
		return d.Drainer.RemoveBackupUser(ctx, a)
	}, func(int) error {
		env.Note(fmt.Sprintf(backupUserStays, BackupUserName(s.rec.ID)))

		return nil
	})
}

// deleteStack deletes the network and the cluster and waits until the stack is
// gone. A stack that fails to delete is almost always held by a load balancer,
// a network interface or a security group a controller made, so each such
// failure sweeps by tag and asks again, until the bounded wait ends.
func (d Deps) deleteStack(ctx context.Context, _ provision.Env, s scope) error {
	deadline := time.Now().Add(d.Settings.DeleteWait)
	for {
		state, err := d.Cloud.Stack(ctx, s.target)
		if err != nil {
			return s.fail(err)
		}

		switch {
		case !state.Exists:
			return nil
		case state.InProgress() || state.Status == "DELETE_IN_PROGRESS" || state.Status == "ROLLBACK_IN_PROGRESS":
		default:
			if state.Status == "DELETE_FAILED" {
				if _, _, err := d.sweepOnce(ctx, s); errors.Is(err, aws.ErrAssumeDenied) {
					return s.fail(err)
				}
			}
			if err := d.Cloud.DeleteStack(ctx, s.target); err != nil {
				return s.fail(err)
			}
		}

		if time.Now().After(deadline) {
			return d.blocked(ctx, s, nil)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.Settings.PollEvery):
		}
	}
}

// sweepOnce removes what it can of what the environment owns and returns what it
// could not remove and the resources it removed. A resource AWS has already
// forgotten counts as removed.
func (d Deps) sweepOnce(ctx context.Context, s scope) (remaining, removed []aws.Resource, err error) {
	found, err := d.Sweeper.Owned(ctx, s.target, s.rec.ID)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range found {
		if err := d.Sweeper.Release(ctx, s.target, r); err != nil {
			if errors.Is(err, aws.ErrAssumeDenied) {
				return nil, removed, err
			}
			remaining = append(remaining, r)

			continue
		}
		removed = append(removed, r)
	}

	return remaining, removed, nil
}

// sweep removes what the stack and the controllers left, rediscovered by tag
// on every pass. It waits a bounded time for AWS to release what is still in
// use, then stops as blocked.
func (d Deps) sweep(ctx context.Context, env provision.Env, s scope) error {
	deadline := time.Now().Add(d.Settings.DeleteWait)
	var removed []string
	for {
		remaining, gone, err := d.sweepOnce(ctx, s)
		if err != nil {
			return s.fail(err)
		}
		for _, r := range gone {
			if !contains(removed, r.String()) {
				removed = append(removed, r.String())
			}
		}
		if len(remaining) == 0 {
			if len(removed) > 0 {
				env.Note("Removed " + strings.Join(removed, ", ") + ".")
			}

			return nil
		}
		if time.Now().After(deadline) {
			return d.blocked(ctx, s, remaining)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.Settings.PollEvery):
		}
	}
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}

	return false
}

// blocked is the failure of a delete that waited as long as it may. It names
// the resources AWS still holds, rediscovered when the caller has none.
func (d Deps) blocked(ctx context.Context, s scope, remaining []aws.Resource) error {
	if remaining == nil {
		found, err := d.Sweeper.Owned(ctx, s.target, s.rec.ID)
		if err != nil {
			return provision.Permanent(err, stackBlocked)
		}
		remaining = found
	}
	if len(remaining) == 0 {
		return provision.Permanent(errors.New("the stack did not delete and nothing is left to sweep"), stackBlocked)
	}

	names := make([]string, 0, maxListed)
	for _, r := range remaining[:min(len(remaining), maxListed)] {
		names = append(names, r.String())
	}
	list := strings.Join(names, ", ")
	if extra := len(remaining) - len(names); extra > 0 {
		list += fmt.Sprintf(" and %d more", extra)
	}

	return provision.Permanent(
		fmt.Errorf("%d resources are still in the account", len(remaining)),
		fmt.Sprintf("%s These stay in your AWS account: %s. They may still be releasing. Retry in a few minutes, or remove them in AWS.", DeleteBlocked, list),
	)
}

func (d Deps) deleteBucket(ctx context.Context, env provision.Env, s scope) error {
	removed, err := d.Sweeper.DeleteBucket(ctx, s.target, bucketName(s), s.rec.ID)
	if err != nil {
		return s.fail(err)
	}
	if removed {
		env.Note("Deleted the backup bucket and every backup in it.")
	} else {
		env.Note("No backup bucket carried this environment's tag, so none was deleted.")
	}

	return nil
}

// forget removes the environment from its Team's hub and forgets its record. A
// rerun that finds the record gone has nothing left to do: the record goes last.
func (d Deps) forget(ctx context.Context, env provision.Env) error {
	_, err := d.Records.Get(ctx, env.Run.TeamID, env.Run.Environment)
	if errors.Is(err, store.ErrManagedEnvironmentUnknown) {
		return nil
	}
	if err != nil {
		return provision.Retryable(err, genericSentence)
	}
	if err := d.Forget(ctx, env.Run.TeamID, env.Run.Environment); err != nil {
		return provision.Retryable(err, genericSentence)
	}

	return nil
}
