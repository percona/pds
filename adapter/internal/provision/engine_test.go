package provision_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/provision"
	"github.com/percona/pds/adapter/internal/store"
)

// clock is a settable time source, so a lapsed lease, a retry backoff and a
// 90 day retention need no sleeping.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Now().UTC().Truncate(time.Millisecond)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var counter atomic.Int64

// lab is one engine over a live PostgreSQL, with a Team and an environment
// nobody else uses. The kinds are named per test so a test never claims the
// runs another package's test left behind in the shared database.
type lab struct {
	t     *testing.T
	db    *store.Store
	prov  *store.Provisioning
	clock *clock
	team  uint
	env   string
	kind  string
	steps *steps
}

// steps records which steps ran and lets a test script how each one ends.
type steps struct {
	mu    sync.Mutex
	ran   []string
	plan  map[string][]error
	hooks map[string]func(ctx context.Context, env provision.Env) error
}

func (s *steps) ranList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.ran...)
}

// step builds a step that records its run, then runs its hook if the test set
// one, then returns the next scripted error, or nil once the script is spent.
func (s *steps) step(name string) provision.Step {
	return provision.Step{Name: name, Run: func(ctx context.Context, env provision.Env) error {
		s.mu.Lock()
		s.ran = append(s.ran, name)
		hook := s.hooks[name]
		var err error
		if q := s.plan[name]; len(q) > 0 {
			err, s.plan[name] = q[0], q[1:]
		}
		s.mu.Unlock()

		if hook != nil {
			if herr := hook(ctx, env); herr != nil {
				return herr
			}
		}

		return err
	}}
}

func liveStore(t *testing.T) *store.Store {
	t.Helper()

	url := strings.TrimSpace(os.Getenv("ADAPTER_TEST_DATABASE_URL"))
	if url == "" {
		t.Skip("set ADAPTER_TEST_DATABASE_URL to run provisioning runs against a live PostgreSQL")
	}
	db, err := store.Open(t.Context(), store.Options{URL: url})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Migrate(t.Context()))

	return db
}

func newLab(t *testing.T) *lab {
	t.Helper()

	db := liveStore(t)
	n := counter.Add(1)
	now := time.Now().UTC()
	org := store.Organization{Name: fmt.Sprintf("provision-%d-%d", now.UnixNano(), n), CreatedAt: now}
	require.NoError(t, db.Gorm().WithContext(t.Context()).Create(&org).Error)
	team := store.Team{OrganizationID: org.ID, Name: "default", CreatedAt: now}
	require.NoError(t, db.Gorm().WithContext(t.Context()).Create(&team).Error)

	return &lab{
		t: t, db: db, prov: db.Provisioning(), clock: newClock(),
		team: team.ID, env: "prod",
		kind:  fmt.Sprintf("create-%d-%d", now.UnixNano(), n),
		steps: &steps{plan: map[string][]error{}, hooks: map[string]func(context.Context, provision.Env) error{}},
	}
}

// engine builds an engine whose create kind has the named steps and whose delete
// kind has one step.
func (l *lab) engine(mutate func(*provision.Options), stepNames ...string) *provision.Engine {
	l.t.Helper()

	create := provision.Kind{Name: l.kind}
	for _, n := range stepNames {
		create.Steps = append(create.Steps, l.steps.step(n))
	}
	remove := provision.Kind{Name: store.KindDelete, Steps: []provision.Step{l.steps.step("teardown")}}
	opts := provision.Options{
		Owner: "worker-1", LeaseTTL: time.Minute, RenewEvery: 10 * time.Millisecond,
		MaxAttempts: 3, Backoff: func(int) time.Duration { return time.Minute },
		WindowMaxAge: 30 * time.Minute, Now: l.clock.Now,
	}
	if mutate != nil {
		mutate(&opts)
	}
	e, err := provision.New(l.prov, []provision.Kind{create, remove}, opts)
	require.NoError(l.t, err)

	return e
}

func (l *lab) run(id uint) store.ProvisioningRun {
	l.t.Helper()

	r, err := l.prov.Run(l.t.Context(), id)
	require.NoError(l.t, err)

	return r
}

func (l *lab) stepRows(id uint) []store.ProvisioningStep {
	l.t.Helper()

	rows, err := l.prov.Steps(l.t.Context(), id)
	require.NoError(l.t, err)

	return rows
}

func TestARunResumesAfterItsLeaseLapses(t *testing.T) {
	l := newLab(t)
	e := l.engine(nil, "role", "stack", "wait")
	run, err := e.Start(t.Context(), l.team, l.env, l.kind)
	require.NoError(t, err)

	// A worker that died after finishing the first step: it holds the lease and
	// wrote that step done, then never came back.
	dead, err := l.prov.Claim(t.Context(), "dead-worker", []string{l.kind}, time.Minute, l.clock.Now())
	require.NoError(t, err)
	require.NotNil(t, dead)
	require.NoError(t, l.prov.BeginStep(t.Context(), dead.ID, "dead-worker", 0, l.clock.Now()))
	require.NoError(t, l.prov.EndStep(t.Context(), dead.ID, "dead-worker", 0, store.StepEnd{StepStatus: store.StepSucceeded}, l.clock.Now()))

	ran, err := e.RunOnce(t.Context())
	require.NoError(t, err)
	assert.False(t, ran, "a live lease is another worker's run")

	l.clock.Advance(2 * time.Minute)
	ran, err = e.RunOnce(t.Context())
	require.NoError(t, err)
	require.True(t, ran, "a lapsed lease is a run to resume")

	assert.Equal(t, []string{"stack", "wait"}, l.steps.ranList(), "the finished step is not run again")
	assert.Equal(t, store.RunSucceeded, l.run(run.ID).Status)
}

func TestOnlyOneRunIsUnfinishedPerEnvironment(t *testing.T) {
	l := newLab(t)
	e := l.engine(nil, "role")
	first, err := e.Start(t.Context(), l.team, l.env, l.kind)
	require.NoError(t, err)

	_, err = e.Start(t.Context(), l.team, l.env, l.kind)
	require.ErrorIs(t, err, store.ErrRunActive, "a second create while one is unfinished")

	other, err := e.Start(t.Context(), l.team, "staging", l.kind)
	require.NoError(t, err, "another environment has its own run")
	assert.NotEqual(t, first.ID, other.ID)

	require.ErrorIs(t, e.Retry(t.Context(), l.team, l.env), store.ErrRunActive, "Retry on a run that is not failed")

	ran, err := e.RunOnce(t.Context())
	require.NoError(t, err)
	require.True(t, ran)
	ran, err = e.RunOnce(t.Context())
	require.NoError(t, err)
	require.True(t, ran)

	_, err = e.Start(t.Context(), l.team, l.env, l.kind)
	assert.NoError(t, err, "a finished run frees the environment")
}

func TestDeletePreemptsARunningRun(t *testing.T) {
	l := newLab(t)
	started := make(chan struct{})
	l.steps.hooks["stack"] = func(ctx context.Context, _ provision.Env) error {
		close(started)
		<-ctx.Done()

		return ctx.Err()
	}
	e := l.engine(nil, "role", "stack", "wait")
	create, err := e.Start(t.Context(), l.team, l.env, l.kind)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, err := e.RunOnce(t.Context())
		done <- err
	}()
	<-started

	del, err := e.Start(t.Context(), l.team, l.env, store.KindDelete)
	require.NoError(t, err, "Delete is the one run allowed beside an active one")
	assert.True(t, l.run(create.ID).CancelRequested)

	require.Eventually(t, func() bool { return len(done) == 1 }, 5*time.Second, 10*time.Millisecond, "the cancel flag stops the running step")
	require.NoError(t, <-done)
	assert.Equal(t, store.RunCancelled, l.run(create.ID).Status)
	assert.NotContains(t, l.steps.ranList(), "wait", "a cancelled run starts no further step")

	ran, err := e.RunOnce(t.Context())
	require.NoError(t, err)
	require.True(t, ran)
	assert.Equal(t, store.RunSucceeded, l.run(del.ID).Status)
	assert.Contains(t, l.steps.ranList(), "teardown")
}

func TestDeletePreemptsAFailedRun(t *testing.T) {
	l := newLab(t)
	l.steps.plan["role"] = []error{errors.New("denied")}
	e := l.engine(nil, "role")
	create, err := e.Start(t.Context(), l.team, l.env, l.kind)
	require.NoError(t, err)
	_, err = e.RunOnce(t.Context())
	require.NoError(t, err)
	require.Equal(t, store.RunFailed, l.run(create.ID).Status)

	_, err = e.Start(t.Context(), l.team, l.env, l.kind)
	require.ErrorIs(t, err, store.ErrRunActive, "a failed run still holds the environment")

	del, err := e.Start(t.Context(), l.team, l.env, store.KindDelete)
	require.NoError(t, err)
	assert.Equal(t, store.RunCancelled, l.run(create.ID).Status)

	ran, err := e.RunOnce(t.Context())
	require.NoError(t, err)
	require.True(t, ran)
	assert.Equal(t, store.RunSucceeded, l.run(del.ID).Status)
}

func TestTransientErrorsRetryToACapThenRetryRerunsOnlyTheFailedStep(t *testing.T) {
	l := newLab(t)
	transient := provision.Retryable(errors.New(strings.Repeat("throttled ", 400)), "AWS is slow to answer. Retry the step.")
	l.steps.plan["stack"] = []error{transient, transient, transient}
	e := l.engine(nil, "role", "stack", "wait")
	run, err := e.Start(t.Context(), l.team, l.env, l.kind)
	require.NoError(t, err)

	for attempt := 1; attempt <= 3; attempt++ {
		ran, err := e.RunOnce(t.Context())
		require.NoError(t, err)
		require.True(t, ran, "attempt %d", attempt)
		if attempt < 3 {
			ran, err = e.RunOnce(t.Context())
			require.NoError(t, err)
			assert.False(t, ran, "the backoff has not passed")
			assert.Equal(t, store.RunQueued, l.run(run.ID).Status)
			l.clock.Advance(time.Minute)
		}
	}

	failed := l.run(run.ID)
	assert.Equal(t, store.RunFailed, failed.Status, "a run fails when the cap is spent, and does not roll back")
	rows := l.stepRows(run.ID)
	assert.Equal(t, store.StepSucceeded, rows[0].Status)
	assert.Equal(t, store.StepFailed, rows[1].Status)
	assert.Equal(t, 3, rows[1].Attempts)
	assert.Equal(t, store.StepPending, rows[2].Status)
	assert.Equal(t, "AWS is slow to answer. Retry the step.", rows[1].Failure, "the record holds the safe sentence")
	assert.LessOrEqual(t, len(rows[1].Detail), 1024, "raw detail is truncated")
	assert.Equal(t, rows[1].Failure, failed.Message)

	require.NoError(t, e.Retry(t.Context(), l.team, l.env))
	l.steps.mu.Lock()
	l.steps.ran = nil
	l.steps.mu.Unlock()
	ran, err := e.RunOnce(t.Context())
	require.NoError(t, err)
	require.True(t, ran)

	assert.Equal(t, []string{"stack", "wait"}, l.steps.ranList(), "Retry re-runs the failed step and what follows, never a finished one")
	assert.Equal(t, store.RunSucceeded, l.run(run.ID).Status)
}

func TestAnUnmarkedErrorFailsTheStepAtOnce(t *testing.T) {
	l := newLab(t)
	l.steps.plan["role"] = []error{errors.New("AccessDenied: arn:aws:iam::123456789012:role/pds")}
	e := l.engine(nil, "role")
	run, err := e.Start(t.Context(), l.team, l.env, l.kind)
	require.NoError(t, err)

	_, err = e.RunOnce(t.Context())
	require.NoError(t, err)

	rows := l.stepRows(run.ID)
	assert.Equal(t, store.RunFailed, l.run(run.ID).Status)
	assert.Equal(t, 1, rows[0].Attempts)
	assert.NotContains(t, rows[0].Failure, "AccessDenied", "the sentence a person reads never carries the raw error")
	assert.NotContains(t, l.run(run.ID).Message, "123456789012")
}

func TestARefusedStepKeepsItsTypedRefusal(t *testing.T) {
	l := newLab(t)
	l.steps.plan["role"] = []error{provision.Refused(errors.New("raw AWS text"), "Your AWS account is out of Elastic IPs.",
		provision.Refusal{Reason: "quota", Service: "Amazon EC2", Item: "Elastic IP addresses per Region", Region: "eu-west-1"})}
	e := l.engine(nil, "role")
	run, err := e.Start(t.Context(), l.team, l.env, l.kind)
	require.NoError(t, err)

	_, err = e.RunOnce(t.Context())
	require.NoError(t, err)

	rows := l.stepRows(run.ID)
	assert.Equal(t, store.RunFailed, l.run(run.ID).Status)
	assert.Equal(t, 1, rows[0].Attempts, "a refusal is reported, never retried by itself")
	assert.Equal(t, "Your AWS account is out of Elastic IPs.", rows[0].Failure)
	assert.Equal(t, store.StepRefusal{Reason: "quota", Service: "Amazon EC2", Item: "Elastic IP addresses per Region", Region: "eu-west-1"}, rows[0].Refusal)
}

func TestStartingAKindTheEngineDoesNotKnowIsRefusedWithASentinel(t *testing.T) {
	l := newLab(t)
	e := l.engine(nil, "role")

	_, err := e.Start(t.Context(), l.team, l.env, "upgrade_pds")

	require.ErrorIs(t, err, provision.ErrUnknownKind)
}

func TestARevokedConnectionPausesWithoutSpendingRetries(t *testing.T) {
	l := newLab(t)
	ready := atomic.Bool{}
	l.steps.plan["role"] = []error{provision.ErrConnectionUnavailable, provision.ErrConnectionUnavailable, provision.ErrConnectionUnavailable, provision.ErrConnectionUnavailable}
	e := l.engine(func(o *provision.Options) {
		o.ConnectionReady = func(context.Context, store.ProvisioningRun) bool { return ready.Load() }
	}, "role", "stack")
	run, err := e.Start(t.Context(), l.team, l.env, l.kind)
	require.NoError(t, err)

	_, err = e.RunOnce(t.Context())
	require.NoError(t, err)
	paused := l.run(run.ID)
	assert.Equal(t, store.RunPaused, paused.Status)
	assert.Equal(t, provision.WaitingForConnection, paused.Message)
	assert.Zero(t, l.stepRows(run.ID)[0].Attempts, "a pause spends no attempt")

	require.NoError(t, e.Sweep(t.Context()))
	assert.Equal(t, store.RunPaused, l.run(run.ID).Status, "the connection is still not Ready")
	ran, err := e.RunOnce(t.Context())
	require.NoError(t, err)
	assert.False(t, ran, "a paused run is not claimed")

	_, err = e.Start(t.Context(), l.team, l.env, l.kind)
	require.ErrorIs(t, err, store.ErrRunActive, "a paused run still holds the environment")

	ready.Store(true)
	require.NoError(t, e.Sweep(t.Context()))
	assert.Equal(t, store.RunQueued, l.run(run.ID).Status, "the check returned Ready")
	for range 4 { // three more pauses, more than the cap, then the step passes
		_, err = e.RunOnce(t.Context())
		require.NoError(t, err)
		if l.run(run.ID).Status == store.RunPaused {
			require.NoError(t, e.Sweep(t.Context()))
		}
	}
	assert.Equal(t, store.RunSucceeded, l.run(run.ID).Status)
}

func TestTheSweeperClosesEveryStaleAccessWindow(t *testing.T) {
	l := newLab(t)
	var mu sync.Mutex
	var closed []uint
	var closeErr error
	e := l.engine(func(o *provision.Options) {
		o.CloseWindow = func(_ context.Context, r store.ProvisioningRun) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if closeErr != nil {
				return "", closeErr
			}
			closed = append(closed, r.ID)

			return "endpoint was public, made private", nil
		}
	}, "open", "install")
	l.steps.hooks["open"] = func(ctx context.Context, env provision.Env) error { return env.WindowOpened(ctx) }
	l.steps.plan["install"] = []error{errors.New("install failed")}
	run, err := e.Start(t.Context(), l.team, l.env, l.kind)
	require.NoError(t, err)
	_, err = e.RunOnce(t.Context())
	require.NoError(t, err)
	require.Equal(t, store.RunFailed, l.run(run.ID).Status)
	require.NotNil(t, l.run(run.ID).WindowOpenedAt)

	mu.Lock()
	closeErr = provision.ErrConnectionUnavailable
	mu.Unlock()
	require.NoError(t, e.Sweep(t.Context()), "a window that cannot close yet is retried, not an error")
	assert.NotNil(t, l.run(run.ID).WindowOpenedAt, "the window stays recorded until it is closed")

	mu.Lock()
	closeErr = nil
	mu.Unlock()
	require.NoError(t, e.Sweep(t.Context()))
	got := l.run(run.ID)
	assert.Nil(t, got.WindowOpenedAt)
	assert.Equal(t, "endpoint was public, made private", got.WindowNote, "the sweeper records what it did")
	require.NoError(t, e.Sweep(t.Context()))
	mu.Lock()
	assert.Equal(t, []uint{run.ID}, closed, "a closed window is not closed twice")
	mu.Unlock()
}

func TestTheSweeperLeavesAFreshWindowOfALiveRunAndClosesAnOldOne(t *testing.T) {
	l := newLab(t)
	var closed atomic.Int32
	e := l.engine(func(o *provision.Options) {
		o.CloseWindow = func(context.Context, store.ProvisioningRun) (string, error) {
			closed.Add(1)

			return "closed", nil
		}
	}, "open", "wait")
	opened := make(chan struct{})
	release := make(chan struct{})
	l.steps.hooks["open"] = func(ctx context.Context, env provision.Env) error { return env.WindowOpened(ctx) }
	l.steps.hooks["wait"] = func(ctx context.Context, _ provision.Env) error {
		close(opened)
		select {
		case <-release:
		case <-ctx.Done():
		}

		return nil
	}
	_, err := e.Start(t.Context(), l.team, l.env, l.kind)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := e.RunOnce(t.Context())
		done <- err
	}()
	<-opened

	require.NoError(t, e.Sweep(t.Context()))
	assert.Zero(t, closed.Load(), "a live run with a fresh window keeps it")

	l.clock.Advance(31 * time.Minute)
	require.NoError(t, e.Sweep(t.Context()))
	assert.Equal(t, int32(1), closed.Load(), "a window older than the limit closes whatever its run is doing")

	close(release)
	require.NoError(t, <-done)
}

func TestRunsAreKeptNinetyDaysAfterTheEnvironmentIsDeleted(t *testing.T) {
	l := newLab(t)
	e := l.engine(nil, "role")
	create, err := e.Start(t.Context(), l.team, l.env, l.kind)
	require.NoError(t, err)
	_, err = e.RunOnce(t.Context())
	require.NoError(t, err)
	del, err := e.Start(t.Context(), l.team, l.env, store.KindDelete)
	require.NoError(t, err)
	_, err = e.RunOnce(t.Context())
	require.NoError(t, err)
	require.Equal(t, store.RunSucceeded, l.run(del.ID).Status)

	l.clock.Advance(89 * 24 * time.Hour)
	require.NoError(t, e.Sweep(t.Context()))
	_, err = l.prov.Run(t.Context(), create.ID)
	require.NoError(t, err, "still inside the retention")

	l.clock.Advance(2 * 24 * time.Hour)
	require.NoError(t, e.Sweep(t.Context()))
	_, err = l.prov.Run(t.Context(), create.ID)
	require.ErrorIs(t, err, store.ErrRunNotFound)
	_, err = l.prov.Run(t.Context(), del.ID)
	require.ErrorIs(t, err, store.ErrRunNotFound)
	assert.Empty(t, l.stepRows(create.ID), "the steps go with their run")
}

func TestARunOfAnotherTeamIsNotAffected(t *testing.T) {
	l := newLab(t)
	other := newLab(t)
	e := l.engine(nil, "role")
	_, err := e.Start(t.Context(), l.team, l.env, l.kind)
	require.NoError(t, err)
	_, err = e.Start(t.Context(), other.team, l.env, l.kind)
	require.NoError(t, err, "two Teams may name an environment the same")
}

func TestWorkersNeverClaimTheSameRun(t *testing.T) {
	l := newLab(t)
	engines := make([]*provision.Engine, 3)
	for i := range engines {
		owner := fmt.Sprintf("worker-%d", i)
		engines[i] = l.engine(func(o *provision.Options) { o.Owner = owner }, "role")
	}
	const runs = 6
	for i := range runs {
		_, err := engines[0].Start(t.Context(), l.team, fmt.Sprintf("env-%d", i), l.kind)
		require.NoError(t, err)
	}

	var wg sync.WaitGroup
	for _, e := range engines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				ran, err := e.RunOnce(t.Context())
				assert.NoError(t, err)
				if !ran {
					return
				}
			}
		}()
	}
	wg.Wait()

	assert.Len(t, l.steps.ranList(), runs, "each run's step ran once across all the workers")
}
