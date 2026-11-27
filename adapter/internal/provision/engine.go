// Package provision runs a managed environment's create, upgrade, rebuild and
// delete as provisioning runs: ordered steps kept in the adapter's database, so
// a restart resumes them. It knows nothing about AWS; a Kind brings its steps.
package provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/store"
)

// WaitingForConnection is what a run paused on a revoked connection shows.
const WaitingForConnection = "Waiting for the AWS connection"

// genericFailure is the sentence for a step that failed without one of its own.
const genericFailure = "A step did not finish. Retry it, or contact Percona support if it fails again."

// maxDetail is how much of a raw error a step record keeps.
const maxDetail = 1000

// ErrConnectionUnavailable is what a step returns when the cloud connection no
// longer works. The run pauses, spends no attempt and does not fail.
var ErrConnectionUnavailable = errors.New("the cloud connection is unavailable")

// ErrUnknownKind means the engine has no steps for the run kind it was asked to
// start.
var ErrUnknownKind = errors.New("provision: unknown run kind")

// Refusal types a step failure the customer fixes outside PDS: why, which
// service and quota or action, and where. It holds no raw error text.
type Refusal struct {
	Reason  string
	Service string
	Item    string
	Region  string
}

// StepError carries the sentence a person reads and whether the engine may
// retry the step by itself. A step error without one is permanent.
type StepError struct {
	Sentence  string
	Retryable bool
	// Refusal is set when the failure is one the customer fixes themselves.
	Refusal *Refusal
	Cause   error
}

func (e *StepError) Error() string { return fmt.Sprintf("%s: %v", e.Sentence, e.Cause) }
func (e *StepError) Unwrap() error { return e.Cause }

// Retryable marks err as transient: the engine retries the step with backoff.
func Retryable(err error, sentence string) error {
	return &StepError{Sentence: sentence, Retryable: true, Cause: err}
}

// Permanent marks err as one a retry cannot fix, with the sentence to show.
func Permanent(err error, sentence string) error {
	return &StepError{Sentence: sentence, Cause: err}
}

// Refused marks err as a refusal the customer fixes: a retry cannot help until
// they do, so the engine reports it and waits for Retry.
func Refused(err error, sentence string, refusal Refusal) error {
	return &StepError{Sentence: sentence, Refusal: &refusal, Cause: err}
}

// Env is what a step is given. The step must be idempotent: it is the unit of
// retry and runs again after a restart.
type Env struct {
	Run store.ProvisioningRun

	db  *store.Provisioning
	now func() time.Time
	// note is what the step recorded for the person reading its run.
	note *strings.Builder
}

// Note records what the step did that a person should be able to read after it
// ends, such as what a sweep removed. A successful step keeps it; a step's
// failure sentence is separate.
func (e Env) Note(text string) {
	if e.note == nil {
		return
	}
	if e.note.Len() > 0 {
		e.note.WriteByte(' ')
	}
	e.note.WriteString(text)
}

// WindowOpened records that this run opened the Access window, so the sweeper
// closes it if the run does not.
func (e Env) WindowOpened(ctx context.Context) error {
	return e.db.OpenWindow(ctx, e.Run.ID, e.Run.LeaseOwner, e.now())
}

// WindowClosed records that the window is closed and how.
func (e Env) WindowClosed(ctx context.Context, note string) error {
	opened := e.Run.WindowOpenedAt
	if current, err := e.db.Run(ctx, e.Run.ID); err == nil {
		opened = current.WindowOpenedAt
	}
	if opened == nil {
		return nil
	}

	return e.db.CloseWindow(ctx, e.Run.ID, *opened, note, e.now())
}

// Step is one idempotent unit of a run.
type Step struct {
	Name string
	Run  func(ctx context.Context, env Env) error
}

// Kind is one kind of run and its ordered steps.
type Kind struct {
	Name  string
	Steps []Step
}

// Options tunes an Engine. Zero fields take the defaults.
type Options struct {
	// Owner names this worker on the leases it holds. It must differ per replica.
	Owner string
	// LeaseTTL is how long a lease lasts unrenewed, and RenewEvery how often a
	// working step renews it.
	LeaseTTL   time.Duration
	RenewEvery time.Duration
	// PollEvery is how long Work sleeps when no run is ready.
	PollEvery time.Duration
	// MaxAttempts caps the automatic attempts of one step, and Backoff is the
	// wait after the given failed attempt. The defaults make five attempts over
	// about ten minutes.
	MaxAttempts int
	Backoff     func(attempt int) time.Duration
	// WindowMaxAge is how long an Access window may stay open.
	WindowMaxAge time.Duration
	// Retention is how long a deleted environment's runs are kept.
	Retention time.Duration
	// ConnectionReady reports whether a paused run's connection works again. Nil
	// leaves paused runs paused.
	ConnectionReady func(ctx context.Context, run store.ProvisioningRun) bool
	// CloseWindow closes a run's Access window after checking the real endpoint,
	// and returns what it did. Nil closes nothing.
	CloseWindow func(ctx context.Context, run store.ProvisioningRun) (string, error)
	Now         func() time.Time
	Logger      *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.LeaseTTL <= 0 {
		o.LeaseTTL = time.Minute
	}
	if o.RenewEvery <= 0 {
		o.RenewEvery = o.LeaseTTL / 3
	}
	if o.PollEvery <= 0 {
		o.PollEvery = 5 * time.Second
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	if o.Backoff == nil {
		o.Backoff = func(attempt int) time.Duration { return 40 * time.Second << (attempt - 1) }
	}
	if o.WindowMaxAge <= 0 {
		o.WindowMaxAge = 30 * time.Minute
	}
	if o.Retention <= 0 {
		o.Retention = 90 * 24 * time.Hour
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}

	return o
}

// Engine claims runs and works their steps. Any number of replicas may run one:
// the lease is the only coordination.
type Engine struct {
	db    *store.Provisioning
	kinds map[string]Kind
	names []string
	opts  Options
}

// New builds an Engine for the kinds it can run. It claims no other kind, so an
// older replica leaves a newer kind's runs to a replica that knows it.
func New(db *store.Provisioning, kinds []Kind, opts Options) (*Engine, error) {
	opts = opts.withDefaults()
	if opts.Owner == "" {
		return nil, errors.New("provision: the engine needs an owner name")
	}
	e := &Engine{db: db, kinds: map[string]Kind{}, opts: opts}
	for _, k := range kinds {
		if _, dup := e.kinds[k.Name]; dup || k.Name == "" || len(k.Steps) == 0 {
			return nil, fmt.Errorf("provision: kind %q is duplicated, unnamed or has no steps", k.Name)
		}
		e.kinds[k.Name] = k
		e.names = append(e.names, k.Name)
	}

	return e, nil
}

// Start queues a run of a kind for an environment. It returns store.ErrRunActive
// while the environment has an unfinished run, unless the kind is delete.
func (e *Engine) Start(ctx context.Context, team uint, environment, kind string) (store.ProvisioningRun, error) {
	return e.StartWith(ctx, team, environment, kind, "")
}

// StartWith is Start for a run that carries parameters, which its steps read
// from Env.Run.Params.
func (e *Engine) StartWith(ctx context.Context, team uint, environment, kind, params string) (store.ProvisioningRun, error) {
	k, ok := e.kinds[kind]
	if !ok {
		return store.ProvisioningRun{}, fmt.Errorf("%w %q", ErrUnknownKind, kind)
	}
	names := make([]string, len(k.Steps))
	for i, s := range k.Steps {
		names[i] = s.Name
	}

	return e.db.StartWith(ctx, team, environment, kind, names, params, e.opts.Now())
}

// Busy reports whether the environment has a run in hand, so a job that reads
// its infrastructure can leave one that a run is changing.
func (e *Engine) Busy(ctx context.Context, team uint, environment string) (bool, error) {
	return e.db.Unfinished(ctx, team, environment)
}

// Retry re-runs the environment's failed run from its failed step.
func (e *Engine) Retry(ctx context.Context, team uint, environment string) error {
	return e.db.Retry(ctx, team, environment, e.opts.Now())
}

// Work claims and works runs until ctx ends.
func (e *Engine) Work(ctx context.Context) error {
	for {
		ran, err := e.RunOnce(ctx)
		if err != nil {
			e.opts.Logger.ErrorContext(ctx, "provisioning run failed to work", "error", log.RedactText(err.Error()))
		}
		if ran && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(e.opts.PollEvery):
		}
	}
}

// RunOnce claims one ready run and works it until it finishes, pauses, waits
// out a backoff, fails or is cancelled. It reports whether it claimed one.
func (e *Engine) RunOnce(ctx context.Context) (bool, error) {
	run, err := e.db.Claim(ctx, e.opts.Owner, e.names, e.opts.LeaseTTL, e.opts.Now())
	if err != nil || run == nil {
		return false, err
	}
	err = e.work(ctx, *run)
	if errors.Is(err, store.ErrLeaseLost) {
		return true, nil
	}

	return true, err
}

func (e *Engine) work(ctx context.Context, run store.ProvisioningRun) error {
	kind, ok := e.kinds[run.Kind]
	if !ok {
		return fmt.Errorf("provision: run %d has unknown kind %q", run.ID, run.Kind)
	}
	steps, err := e.db.Steps(ctx, run.ID)
	if err != nil {
		return err
	}

	for _, row := range steps {
		if row.Status == store.StepSucceeded {
			continue
		}
		cancel, err := e.db.Renew(ctx, run.ID, e.opts.Owner, e.opts.LeaseTTL, e.opts.Now())
		if err != nil {
			return err
		}
		if cancel && run.Kind != store.KindDelete {
			return e.db.EndRun(ctx, run.ID, e.opts.Owner, store.RunCancelled, e.opts.Now())
		}
		if row.Position >= len(kind.Steps) || kind.Steps[row.Position].Name != row.Name {
			return e.after(ctx, run, row, Permanent(errors.New("step list changed"), genericFailure))
		}
		done, err := e.step(ctx, run, row, kind.Steps[row.Position])
		if err != nil || done {
			return err
		}
	}

	return e.db.EndRun(ctx, run.ID, e.opts.Owner, store.RunSucceeded, e.opts.Now())
}

// step works one step and reports whether the run stopped there.
func (e *Engine) step(ctx context.Context, run store.ProvisioningRun, row store.ProvisioningStep, step Step) (bool, error) {
	if err := e.db.BeginStep(ctx, run.ID, e.opts.Owner, row.Position, e.opts.Now()); err != nil {
		return true, err
	}
	row.Attempts++

	stepCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	cancelled := make(chan struct{})
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		e.keepLease(stepCtx, stop, run, cancelled)
	}()

	var note strings.Builder
	err := step.Run(stepCtx, Env{Run: run, db: e.db, now: e.opts.Now, note: &note})
	stop(nil)
	<-renewed

	if ctx.Err() != nil {
		// Shutting down: the lease lapses and another worker resumes the step.
		return true, nil //nolint:nilerr // a stopped worker leaves the run to the next lease
	}
	select {
	case <-cancelled:
		err := e.db.EndStep(ctx, run.ID, e.opts.Owner, row.Position,
			store.StepEnd{StepStatus: store.StepPending, RunStatus: store.RunCancelled}, e.opts.Now())

		return true, err
	default:
	}
	if lost := context.Cause(stepCtx); errors.Is(lost, store.ErrLeaseLost) {
		return true, store.ErrLeaseLost
	}

	if err == nil {
		return false, e.db.EndStep(ctx, run.ID, e.opts.Owner, row.Position, store.StepEnd{StepStatus: store.StepSucceeded, Detail: truncate(log.RedactText(note.String()))}, e.opts.Now())
	}

	return true, e.after(ctx, run, row, err)
}

// keepLease renews the lease while a step runs and stops the step when a cancel
// was requested or the lease was lost.
func (e *Engine) keepLease(ctx context.Context, stop context.CancelCauseFunc, run store.ProvisioningRun, cancelled chan<- struct{}) {
	t := time.NewTicker(e.opts.RenewEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cancel, err := e.db.Renew(ctx, run.ID, e.opts.Owner, e.opts.LeaseTTL, e.opts.Now())
		switch {
		case errors.Is(err, store.ErrLeaseLost):
			stop(store.ErrLeaseLost)

			return
		case err != nil:
			e.opts.Logger.WarnContext(ctx, "provisioning lease renewal failed", "run", run.ID, "error", log.RedactText(err.Error()))
		case cancel && run.Kind != store.KindDelete:
			close(cancelled)
			stop(context.Canceled)

			return
		}
	}
}

// after records how a failed step ends: a pause, a scheduled retry or a failure.
func (e *Engine) after(ctx context.Context, run store.ProvisioningRun, row store.ProvisioningStep, err error) error {
	now := e.opts.Now()
	detail := truncate(log.RedactText(err.Error()))

	if errors.Is(err, ErrConnectionUnavailable) {
		return e.db.EndStep(ctx, run.ID, e.opts.Owner, row.Position, store.StepEnd{
			StepStatus: store.StepPending, RunStatus: store.RunPaused, Message: WaitingForConnection, Detail: detail, Refund: true,
		}, now)
	}

	var se *StepError
	sentence := genericFailure
	retryable := false
	var refusal store.StepRefusal
	if errors.As(err, &se) {
		sentence, retryable = se.Sentence, se.Retryable
		if se.Refusal != nil {
			refusal = store.StepRefusal(*se.Refusal)
		}
	}
	if retryable && row.Attempts < e.opts.MaxAttempts {
		return e.db.EndStep(ctx, run.ID, e.opts.Owner, row.Position, store.StepEnd{
			StepStatus: store.StepPending, RunStatus: store.RunQueued, Detail: detail, NotBefore: now.Add(e.opts.Backoff(row.Attempts)),
		}, now)
	}

	return e.db.EndStep(ctx, run.ID, e.opts.Owner, row.Position, store.StepEnd{
		StepStatus: store.StepFailed, RunStatus: store.RunFailed, Failure: sentence, Detail: detail, Refusal: refusal, Message: sentence,
	}, now)
}

// Sweep does the periodic upkeep any replica may do: it closes the Access
// windows of runs that are not live, resumes paused runs whose connection works
// again, and drops the runs of environments deleted past the retention.
func (e *Engine) Sweep(ctx context.Context) error {
	now := e.opts.Now()
	var errs []error

	if e.opts.CloseWindow != nil {
		runs, err := e.db.StaleWindows(ctx, e.opts.WindowMaxAge, now)
		errs = append(errs, err)
		for _, run := range runs {
			note, err := e.opts.CloseWindow(ctx, run)
			if err != nil {
				// A revoked connection cannot close a window yet; the next sweep tries again.
				e.opts.Logger.WarnContext(ctx, "access window not closed", "run", run.ID, "error", log.RedactText(err.Error()))

				continue
			}
			errs = append(errs, e.db.CloseWindow(ctx, run.ID, *run.WindowOpenedAt, truncate(note), now))
		}
	}

	if e.opts.ConnectionReady != nil {
		paused, err := e.db.Paused(ctx, e.names)
		errs = append(errs, err)
		for _, run := range paused {
			if e.opts.ConnectionReady(ctx, run) {
				errs = append(errs, e.db.Resume(ctx, run.ID, now))
			}
		}
	}

	_, err := e.db.PruneRuns(ctx, now.Add(-e.opts.Retention))
	errs = append(errs, err)

	return errors.Join(errs...)
}

func truncate(s string) string {
	if len(s) > maxDetail {
		return s[:maxDetail]
	}

	return s
}
