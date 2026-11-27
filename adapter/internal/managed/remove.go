package managed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/store"
)

// ErrConnectionReachable means the cloud connection works and the environment's
// infrastructure is there, so the environment is deleted in AWS and not only
// removed from PDS.
var ErrConnectionReachable = errors.New("the cloud connection works, so delete the environment instead")

// removedFromPDS is the message of the run that records a removal from PDS alone.
const removedFromPDS = "Removed from PDS only. Everything in AWS stays."

// Abandoner ends what an environment has unfinished and records its removal.
// *store.Provisioning is the implementation.
type Abandoner interface {
	Abandon(ctx context.Context, team uint, environment, message string, now time.Time) (store.ProvisioningRun, error)
}

// EnvironmentRecords reads the Team's managed environments.
type EnvironmentRecords interface {
	Get(ctx context.Context, team uint, name string) (store.ManagedEnvironment, error)
}

// Environments is what the API calls to create and to delete managed
// environments.
type Environments struct {
	*Creator
	*Remover
}

// Remover accepts the requests to delete a managed environment, in AWS or from
// PDS alone.
type Remover struct {
	records     EnvironmentRecords
	connections Connections
	runs        Runs
	abandon     Abandoner
	forget      func(ctx context.Context, team uint, name string) error
	now         func() time.Time
}

// NewRemover builds a Remover. forget removes the environment from its Team's
// hub and forgets its record, the way the delete run's last step does.
func NewRemover(
	records EnvironmentRecords, connections Connections, runs Runs, abandon Abandoner,
	forget func(ctx context.Context, team uint, name string) error,
) *Remover {
	return &Remover{records: records, connections: connections, runs: runs, abandon: abandon, forget: forget, now: time.Now}
}

// Delete starts the run that deletes the Team's environment in AWS. It is
// allowed from any state and preempts a run in progress. It returns before
// anything is deleted. The typed-name confirmation is the caller's.
func (r *Remover) Delete(ctx context.Context, scope auth.Scope, name string) (store.ProvisioningRun, error) {
	if _, err := r.records.Get(ctx, scope.Team.ID, name); err != nil {
		return store.ProvisioningRun{}, err
	}

	return r.runs.Start(ctx, scope.Team.ID, name, KindDelete)
}

// RemoveFromPDS forgets the environment without touching AWS, for the cases that
// cannot do more: the cloud connection is Unreachable, so the control plane
// cannot assume the role, or the environment is Lost, so its infrastructure is
// already gone. The caller shows Remains first and never claims AWS was cleaned
// up.
func (r *Remover) RemoveFromPDS(ctx context.Context, scope auth.Scope, name string) error {
	rec, err := r.records.Get(ctx, scope.Team.ID, name)
	if err != nil {
		return err
	}
	conn, err := r.connections.Get(ctx, scope.Team.ID, rec.ConnectionID)
	if err != nil && !errors.Is(err, store.ErrCloudConnectionUnknown) {
		return fmt.Errorf("read the cloud connection: %w", err)
	}
	if err == nil && conn.State != store.CloudUnreachable && rec.LostAt == nil {
		return ErrConnectionReachable
	}

	// Runs end first: while one works on the environment, forgetting it would
	// leave that worker with nothing to read.
	if _, err := r.abandon.Abandon(ctx, scope.Team.ID, name, removedFromPDS, r.now()); err != nil {
		return err
	}

	return r.forget(ctx, scope.Team.ID, name)
}

// Remains is what stays in the customer's AWS account when an environment is
// removed from PDS alone.
type Remains struct {
	AccountID   string
	Region      string
	StackName   string
	ClusterName string
	Bucket      string
}

// Remains names what Remove from PDS only leaves in AWS, for the dialog.
func (r *Remover) Remains(ctx context.Context, scope auth.Scope, name string) (Remains, error) {
	rec, err := r.records.Get(ctx, scope.Team.ID, name)
	if err != nil {
		return Remains{}, err
	}
	conn, err := r.connections.Get(ctx, scope.Team.ID, rec.ConnectionID)
	if err != nil && !errors.Is(err, store.ErrCloudConnectionUnknown) {
		return Remains{}, fmt.Errorf("read the cloud connection: %w", err)
	}

	return Remains{
		AccountID: conn.AccountID, Region: rec.Region, StackName: "pds-env-" + rec.ID, ClusterName: "pds-" + rec.ID,
		Bucket: "pds-" + conn.AccountID + "-" + rec.ID,
	}, nil
}
