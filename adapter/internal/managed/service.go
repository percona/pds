package managed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/store"
)

// What Create refuses beyond the stores' own errors.
var (
	// ErrUnavailable means this control plane cannot build managed
	// environments: it declares no outgoing addresses, or has no published
	// chart to install.
	ErrUnavailable = errors.New("managed environments are not available on this control plane")
	// ErrInvalid means the request cannot be a managed environment as it is.
	ErrInvalid = errors.New("invalid managed environment request")
	// ErrConnectionNotReady means the cloud connection did not pass its check.
	ErrConnectionNotReady = errors.New("the cloud connection is not ready")
)

// CreateInput names the managed environment a person asks for.
type CreateInput struct {
	ConnectionID string
	Name         string
	Region       string
}

// Created is a managed environment that is now creating, and its run.
type Created struct {
	Environment store.ManagedEnvironment
	Run         store.ProvisioningRun
}

// NewRecords is the store a Creator records environments in.
type NewRecords interface {
	Create(ctx context.Context, in store.NewManagedEnvironment, now time.Time) (store.ManagedEnvironment, error)
	Remove(ctx context.Context, team uint, name string) error
}

// Verifier runs the connection check. *service.CloudConnections is the
// implementation.
type Verifier interface {
	Verify(ctx context.Context, scope auth.Scope, id string) (store.CloudConnection, error)
}

// Runs starts provisioning runs. *provision.Engine is the implementation.
type Runs interface {
	Start(ctx context.Context, team uint, environment, kind string) (store.ProvisioningRun, error)
}

// CreatorSettings are the facts about this deployment a create depends on.
type CreatorSettings struct {
	OutgoingCIDRs   []string
	ChartVersion    string
	ImageRepository string
}

// Creator accepts the request to create a managed environment: it refuses
// what cannot work before anything exists in AWS, then records the environment
// and queues its run.
type Creator struct {
	records  NewRecords
	verifier Verifier
	runs     Runs
	settings CreatorSettings
	now      func() time.Time
}

// NewCreator builds a Creator.
func NewCreator(records NewRecords, verifier Verifier, runs Runs, settings CreatorSettings) *Creator {
	return &Creator{records: records, verifier: verifier, runs: runs, settings: settings, now: time.Now}
}

// Create checks the connection, records the environment and starts its create
// run. It returns before anything is built.
func (c *Creator) Create(ctx context.Context, scope auth.Scope, in CreateInput) (Created, error) {
	if len(c.settings.OutgoingCIDRs) == 0 || c.settings.ChartVersion == "" || c.settings.ImageRepository == "" {
		return Created{}, ErrUnavailable
	}
	if !hub.ValidEnvironmentName(in.Name) {
		return Created{}, fmt.Errorf("%w: %q is not an environment name", ErrInvalid, in.Name)
	}

	conn, err := c.verifier.Verify(ctx, scope, in.ConnectionID)
	if err != nil {
		return Created{}, err
	}
	if conn.State != store.CloudReady {
		return Created{}, ErrConnectionNotReady
	}
	if !slices.Contains(conn.AllowedRegions, in.Region) {
		return Created{}, fmt.Errorf("%w: the connection does not allow the region %q", ErrInvalid, in.Region)
	}

	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return Created{}, fmt.Errorf("generate environment id: %w", err)
	}
	row, err := c.records.Create(ctx, store.NewManagedEnvironment{
		ID: hex.EncodeToString(raw), TeamID: scope.Team.ID, Name: in.Name, ConnectionID: conn.ID, Region: in.Region,
	}, c.now())
	if err != nil {
		return Created{}, err
	}
	run, err := c.runs.Start(ctx, scope.Team.ID, in.Name, KindCreate)
	if err != nil {
		// A name must not stay held by an environment that was never started.
		return Created{}, errors.Join(err, c.records.Remove(ctx, scope.Team.ID, in.Name))
	}

	return Created{Environment: row, Run: run}, nil
}
