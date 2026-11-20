package managed

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/provision"
	"github.com/percona/pds/adapter/internal/store"
)

// Progress says where the latest run of an environment stands.
const (
	ProgressInProgress = "in_progress"
	// ProgressWaiting is a run paused on a connection that does not work.
	ProgressWaiting = "waiting"
	ProgressFailed  = "failed"
	// ProgressBlocked is a delete that stopped with something left behind.
	ProgressBlocked   = "blocked"
	ProgressReady     = "ready"
	ProgressCancelled = "cancelled"
)

// Phase statuses.
const (
	PhasePending   = "pending"
	PhaseRunning   = "running"
	PhaseWaiting   = "waiting"
	PhaseSucceeded = "succeeded"
	PhaseFailed    = "failed"
)

// The state of a managed environment as a whole.
const (
	StateCreating = "creating"
	StateReady    = "ready"
	StateDegraded = "degraded"
	StateFailed   = "failed"
	StateDeleting = "deleting"
	// StateLost is an environment whose infrastructure is gone from AWS while it
	// stopped reporting. The person rebuilds it or removes it from PDS.
	StateLost = "lost"
)

// The one action a failed run offers.
const ActionRetry = "retry"

// What the Manager refuses beyond the stores' own errors.
var (
	// ErrConfirm means the name typed to delete is not the environment's.
	ErrConfirm = errors.New("the typed name does not match the environment")
)

// Roster reads the managed environments a Team has.
type Roster interface {
	List(ctx context.Context, team uint) ([]store.ManagedEnvironment, error)
	Get(ctx context.Context, team uint, name string) (store.ManagedEnvironment, error)
}

// RunBook starts, retries and reads provisioning runs. The run store and the
// engine together are the implementation.
type RunBook interface {
	Runs
	Retry(ctx context.Context, team uint, environment string) error
	Latest(ctx context.Context, team uint, environment string) (store.ProvisioningRun, error)
	Steps(ctx context.Context, runID uint) ([]store.ProvisioningStep, error)
}

// Removal deletes an environment, or forgets it in PDS and leaves everything in
// the customer's account. *Remover is the implementation.
type Removal interface {
	Delete(ctx context.Context, scope auth.Scope, name string) (store.ProvisioningRun, error)
	RemoveFromPDS(ctx context.Context, scope auth.Scope, name string) error
}

// Manager is what a person does with a managed environment after it exists:
// read how its runs stand, start another, retry a failed one, delete it.
type Manager struct {
	roster  Roster
	book    RunBook
	removal Removal
}

// NewManager builds a Manager. removal may be nil, and deleting is then
// unavailable.
func NewManager(roster Roster, book RunBook, removal Removal) *Manager {
	return &Manager{roster: roster, book: book, removal: removal}
}

// PhaseView is one of the stages of a create the console shows.
type PhaseView struct {
	Name   string
	Status string
}

// Refusal is an AWS refusal in the shape every surface shares.
type Refusal struct {
	Reason  string
	Service string
	Item    string
	Region  string
}

// Failure is why a run stopped, and the one thing the person does about it.
type Failure struct {
	Sentence string
	Action   string
	Refusal  *Refusal
}

// RunView is the part of a run a person reads.
type RunView struct {
	ID         uint
	Kind       string
	Status     string
	StartedAt  time.Time
	FinishedAt *time.Time
}

// Provisioning is how the latest run of an environment stands.
type Provisioning struct {
	Run      RunView
	Progress string
	// Message is the sentence of a paused run.
	Message string
	Phases  []PhaseView
	Failure *Failure
}

// Managed is a managed environment with its provisioning.
type Managed struct {
	Record       store.ManagedEnvironment
	Provisioning Provisioning
}

// ProgressOf maps a run status onto the progress a person reads.
func ProgressOf(run store.ProvisioningRun) string {
	switch run.Status {
	case store.RunQueued, store.RunRunning:
		return ProgressInProgress
	case store.RunPaused:
		return ProgressWaiting
	case store.RunFailed:
		if run.Kind == store.KindDelete {
			return ProgressBlocked
		}

		return ProgressFailed
	case store.RunCancelled:
		return ProgressCancelled
	}

	return ProgressReady
}

// StateOf is the state of the environment as a whole. A run that builds it
// decides while it is unfinished; after that a missing infrastructure and then
// the heartbeat do.
func StateOf(p Provisioning, connected, lost bool) string {
	building := p.Run.Kind == KindCreate || p.Run.Kind == KindRebuild
	switch {
	case p.Run.Kind == store.KindDelete && p.Progress != ProgressReady && p.Progress != ProgressCancelled:
		return StateDeleting
	case building && p.Progress == ProgressFailed:
		return StateFailed
	case building && (p.Progress == ProgressInProgress || p.Progress == ProgressWaiting):
		return StateCreating
	case lost:
		return StateLost
	case connected:
		return StateReady
	}

	return StateDegraded
}

// Provisioning reads how the latest run of one of the Team's environments
// stands. An environment that never had a run reads as ready.
func (m *Manager) Provisioning(ctx context.Context, team uint, name string) (Provisioning, error) {
	run, err := m.book.Latest(ctx, team, name)
	if errors.Is(err, store.ErrRunNotFound) {
		return Provisioning{Progress: ProgressReady}, nil
	}
	if err != nil {
		return Provisioning{}, err
	}
	steps, err := m.book.Steps(ctx, run.ID)
	if err != nil {
		return Provisioning{}, err
	}

	p := Provisioning{
		Run:      RunView{ID: run.ID, Kind: run.Kind, Status: run.Status, StartedAt: run.CreatedAt, FinishedAt: run.FinishedAt},
		Progress: ProgressOf(run),
		Phases:   phasesOf(run, steps),
	}
	switch run.Status {
	case store.RunPaused:
		p.Message = run.Message
	case store.RunFailed:
		p.Failure = failureOf(steps, run.Message)
	}

	return p, nil
}

// phasesOf groups a create's steps under the four phases the console shows. A
// run of a kind whose steps have no phase has none.
func phasesOf(run store.ProvisioningRun, steps []store.ProvisioningStep) []PhaseView {
	var order []string
	byPhase := map[string][]store.ProvisioningStep{}
	for _, step := range steps {
		phase := Phase(step.Name)
		if phase == "" {
			continue
		}
		if _, seen := byPhase[phase]; !seen {
			order = append(order, phase)
		}
		byPhase[phase] = append(byPhase[phase], step)
	}

	active := run.Status == store.RunQueued || run.Status == store.RunRunning || run.Status == store.RunPaused
	behind := false
	out := make([]PhaseView, 0, len(order))
	for _, name := range order {
		status := statusOf(byPhase[name])
		if (status == PhasePending || status == PhaseRunning) && active && !behind {
			status = PhaseRunning
			if run.Status == store.RunPaused {
				status = PhaseWaiting
			}
		}
		behind = behind || status != PhaseSucceeded
		out = append(out, PhaseView{Name: name, Status: status})
	}

	return out
}

func statusOf(steps []store.ProvisioningStep) string {
	succeeded := 0
	status := PhasePending
	for _, step := range steps {
		switch step.Status {
		case store.StepFailed:
			return PhaseFailed
		case store.StepRunning:
			status = PhaseRunning
		case store.StepSucceeded:
			succeeded++
		}
	}
	switch {
	case succeeded == len(steps):
		return PhaseSucceeded
	case succeeded > 0 && status == PhasePending:
		return PhaseRunning
	}

	return status
}

// failureOf is the sentence of the failed step, or the run's own when no step
// is marked failed.
func failureOf(steps []store.ProvisioningStep, fallback string) *Failure {
	failure := &Failure{Sentence: fallback, Action: ActionRetry}
	for _, step := range steps {
		if step.Status != store.StepFailed {
			continue
		}
		if step.Failure != "" {
			failure.Sentence = step.Failure
		}
		if step.Refusal.Reason != "" {
			r := step.Refusal
			failure.Refusal = &Refusal{Reason: r.Reason, Service: r.Service, Item: r.Item, Region: r.Region}
		}

		break
	}

	return failure
}

// Environments lists the Team's managed environments with their provisioning.
func (m *Manager) Environments(ctx context.Context, team uint) ([]Managed, error) {
	records, err := m.roster.List(ctx, team)
	if err != nil {
		return nil, err
	}
	out := make([]Managed, 0, len(records))
	for _, record := range records {
		p, err := m.Provisioning(ctx, team, record.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, Managed{Record: record, Provisioning: p})
	}

	return out, nil
}

// Has reports whether the Team has a managed environment of that name.
func (m *Manager) Has(ctx context.Context, team uint, name string) bool {
	_, err := m.roster.Get(ctx, team, name)

	return err == nil
}

// Start queues a run on one of the Team's managed environments. A second run
// while one is unfinished is store.ErrRunActive.
func (m *Manager) Start(ctx context.Context, scope auth.Scope, name, kind string) (store.ProvisioningRun, error) {
	if !slices.Contains([]string{KindUpgradePDS, KindKubernetes, KindRebuild}, kind) {
		return store.ProvisioningRun{}, fmt.Errorf("%w: %q is not a run kind a person starts", ErrInvalid, kind)
	}
	if _, err := m.roster.Get(ctx, scope.Team.ID, name); err != nil {
		return store.ProvisioningRun{}, err
	}
	run, err := m.book.Start(ctx, scope.Team.ID, name, kind)
	if errors.Is(err, provision.ErrUnknownKind) {
		return store.ProvisioningRun{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	return run, err
}

// Retry re-runs the failed step of the environment's latest run. Only the
// latest run is retried, so a stale console cannot revive an old one.
func (m *Manager) Retry(ctx context.Context, scope auth.Scope, name string, runID uint) error {
	if _, err := m.roster.Get(ctx, scope.Team.ID, name); err != nil {
		return err
	}
	latest, err := m.book.Latest(ctx, scope.Team.ID, name)
	if err != nil {
		return err
	}
	if latest.ID != runID {
		return store.ErrRunNotFound
	}

	return m.book.Retry(ctx, scope.Team.ID, name)
}

// Delete queues the environment's delete run once the person typed its name.
// It preempts a run in progress.
func (m *Manager) Delete(ctx context.Context, scope auth.Scope, name, typed string) (store.ProvisioningRun, error) {
	if _, err := m.confirmed(ctx, scope, name, typed); err != nil {
		return store.ProvisioningRun{}, err
	}
	if m.removal == nil {
		return store.ProvisioningRun{}, fmt.Errorf("%w: deleting is not offered", ErrUnavailable)
	}
	run, err := m.removal.Delete(ctx, scope, name)
	if errors.Is(err, provision.ErrUnknownKind) {
		return store.ProvisioningRun{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	return run, err
}

// RemoveFromPDS forgets the environment in PDS and leaves what is in the AWS
// account. It is for an account the control plane can no longer reach, and the
// removal refuses with ErrConnectionReachable otherwise.
func (m *Manager) RemoveFromPDS(ctx context.Context, scope auth.Scope, name, typed string) error {
	if _, err := m.confirmed(ctx, scope, name, typed); err != nil {
		return err
	}
	if m.removal == nil {
		return fmt.Errorf("%w: removing from PDS only is not offered", ErrUnavailable)
	}

	return m.removal.RemoveFromPDS(ctx, scope, name)
}

func (m *Manager) confirmed(ctx context.Context, scope auth.Scope, name, typed string) (store.ManagedEnvironment, error) {
	rec, err := m.roster.Get(ctx, scope.Team.ID, name)
	if err != nil {
		return store.ManagedEnvironment{}, err
	}
	if typed == "" || typed != rec.Name {
		return store.ManagedEnvironment{}, ErrConfirm
	}

	return rec, nil
}
