package managed_test

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

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/provision"
	"github.com/percona/pds/adapter/internal/store"
)

var counter atomic.Int64

type fakeCloud struct {
	mu        sync.Mutex
	calls     []string
	targets   []aws.Target
	specs     []aws.StackSpec
	cidrs     [][]string
	failAt    map[string][]error
	stacks    []aws.StackState
	closeNote string
	preflight *aws.Refusal
	needs     aws.Needs
	buckets   []string
	tags      []map[string]string

	// What the update, drift and cluster reads answer, and what they were asked.
	plans      []aws.ChangePlan
	updates    []aws.UpdateSpec
	applied    []string
	discarded  []string
	drifts     []aws.Drift
	clusters   []aws.ClusterState
	stackState *aws.StackState
}

func (f *fakeCloud) record(name string, t aws.Target) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	f.targets = append(f.targets, t)
	if q := f.failAt[name]; len(q) > 0 {
		err := q[0]
		f.failAt[name] = q[1:]

		return err
	}

	return nil
}

func (f *fakeCloud) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}

	return n
}

func (f *fakeCloud) CheckRole(_ context.Context, t aws.Target) error {
	return f.record("check-role", t)
}

func (f *fakeCloud) Preflight(_ context.Context, t aws.Target, needs aws.Needs) (aws.Refusal, bool) {
	_ = f.record("preflight", t)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.needs = needs
	if f.preflight == nil {
		return aws.Refusal{}, false
	}

	return *f.preflight, true
}

func (f *fakeCloud) EnsureBucket(_ context.Context, t aws.Target, bucket string, tags map[string]string) error {
	f.mu.Lock()
	f.buckets = append(f.buckets, bucket)
	f.tags = append(f.tags, tags)
	f.mu.Unlock()

	return f.record("ensure-bucket", t)
}

func (f *fakeCloud) EnsureStack(_ context.Context, t aws.Target, spec aws.StackSpec) error {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()

	return f.record("ensure-stack", t)
}

func (f *fakeCloud) DeleteStack(_ context.Context, t aws.Target) error {
	return f.record("delete-stack", t)
}

func (f *fakeCloud) Stack(_ context.Context, t aws.Target) (aws.StackState, error) {
	if err := f.record("stack", t); err != nil {
		return aws.StackState{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stacks) == 0 {
		if f.stackState != nil {
			return *f.stackState, nil
		}

		return aws.StackState{Exists: true, Status: "CREATE_COMPLETE"}, nil
	}
	state := f.stacks[0]
	if len(f.stacks) > 1 {
		f.stacks = f.stacks[1:]
	}

	return state, nil
}

func (f *fakeCloud) OpenAccess(_ context.Context, t aws.Target, cidrs []string) (aws.ClusterAccess, error) {
	f.mu.Lock()
	f.cidrs = append(f.cidrs, cidrs)
	f.mu.Unlock()

	return aws.ClusterAccess{Endpoint: "https://eks.example", CA: []byte("ca")}, f.record("open-access", t)
}

func (f *fakeCloud) CloseAccess(_ context.Context, t aws.Target) (string, error) {
	note := f.closeNote
	if note == "" {
		note = "Closed the public endpoint."
	}

	return note, f.record("close-access", t)
}

func (f *fakeCloud) KubeToken(_ context.Context, t aws.Target) (string, error) {
	return "k8s-aws-v1.token", f.record("kube-token", t)
}

type fakeKube struct {
	mu     sync.Mutex
	fluxes []string
	joins  []managed.Join
	access []managed.KubeAccess
}

func (f *fakeKube) InstallFlux(_ context.Context, access managed.KubeAccess, version string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fluxes = append(f.fluxes, version)
	f.access = append(f.access, access)

	return nil
}

func (f *fakeKube) WriteJoin(_ context.Context, access managed.KubeAccess, join managed.Join) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.joins = append(f.joins, join)
	f.access = append(f.access, access)

	return nil
}

type fakeHub struct {
	mu        sync.Mutex
	written   map[string][]byte
	joins     int
	connected atomic.Bool
	release   *hub.HeartbeatRelease
	releases  []string
}

func (f *fakeHub) Join(_ context.Context, _ uint, environment string) (managed.Join, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.joins++

	return managed.Join{Environment: environment, AgentToken: "agt_secret", GitURL: "ssh://git@github.com/o/r.git", Namespace: "pds"}, nil
}

func (f *fakeHub) WriteRelease(_ context.Context, _ uint, _ string, files map[string][]byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written = files

	return nil
}

func (f *fakeHub) Connected(context.Context, uint, string) (bool, error) {
	return f.connected.Load(), nil
}

func (f *fakeHub) Release(context.Context, uint, string) (hub.HeartbeatRelease, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.release == nil {
		return hub.HeartbeatRelease{}, false, nil
	}

	return *f.release, true, nil
}

type lab struct {
	t      *testing.T
	db     *store.Store
	prov   *store.Provisioning
	cloud  *fakeCloud
	kube   *fakeKube
	hub    *fakeHub
	team   uint
	env    string
	connID string
	envID  string
	deps   managed.Deps
	kind   string
}

func newLab(t *testing.T) *lab {
	t.Helper()

	url := strings.TrimSpace(os.Getenv("ADAPTER_TEST_DATABASE_URL"))
	if url == "" {
		t.Skip("set ADAPTER_TEST_DATABASE_URL to run managed environment steps against a live PostgreSQL")
	}
	db, err := store.Open(t.Context(), store.Options{URL: url})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Migrate(t.Context()))

	n := counter.Add(1)
	now := time.Now().UTC()
	org := store.Organization{Name: fmt.Sprintf("managed-%d-%d", now.UnixNano(), n), CreatedAt: now}
	require.NoError(t, db.Gorm().WithContext(t.Context()).Create(&org).Error)
	team := store.Team{OrganizationID: org.ID, Name: "default", CreatedAt: now}
	require.NoError(t, db.Gorm().WithContext(t.Context()).Create(&team).Error)

	connID := fmt.Sprintf("%012x", (now.UnixNano()/1000+n)%(1<<48))
	envID := fmt.Sprintf("%012x", (now.UnixNano()/1000+n+1<<40)%(1<<48))
	conn := store.CloudConnection{
		ID: connID, TeamID: team.ID, AccountID: "111122223333", Alias: "acme", ExternalID: "external",
		StackName: "pds-aaaaaaaaaaaa", StackRegion: "eu-west-1", AllowedRegions: []string{"eu-west-1"},
		State: store.CloudReady, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Gorm().WithContext(t.Context()).Create(&conn).Error)
	_, err = db.ManagedEnvironments().Create(t.Context(), store.NewManagedEnvironment{
		ID: envID, TeamID: team.ID, Name: "orders", ConnectionID: conn.ID, Region: "eu-west-1",
	}, now)
	require.NoError(t, err)

	l := &lab{
		t: t, db: db, prov: db.Provisioning(), team: team.ID, env: "orders", connID: connID, envID: envID,
		cloud: &fakeCloud{failAt: map[string][]error{}}, kube: &fakeKube{}, hub: &fakeHub{},
		kind: fmt.Sprintf("create-%d-%d", now.UnixNano(), n),
	}
	l.deps = managed.Deps{
		Records: db.ManagedEnvironments(), Connections: db.CloudConnections(),
		Cloud: l.cloud, Cluster: l.kube, Hub: l.hub,
		Settings: managed.Settings{
			OutgoingCIDRs: []string{"203.0.113.7/32"}, FluxVersion: "2.5.1", ChartVersion: "0.1.0",
			HubURL: "https://hub.example", ImageRepository: "ghcr.io/percona/pds-adapter", ImageTag: "0.1.0",
			PollEvery: time.Millisecond, StackWait: time.Second, JoinWait: 50 * time.Millisecond,
			ReleaseWait: 50 * time.Millisecond,
		},
	}

	return l
}

// engine builds an engine that runs only this lab's create kind, so a run another
// test left behind is never claimed.
func (l *lab) engine() *provision.Engine {
	l.t.Helper()

	kind := managed.CreateKind(l.deps)
	kind.Name = l.kind
	e, err := provision.New(l.prov, []provision.Kind{kind}, provision.Options{
		Owner: "worker-1", LeaseTTL: time.Minute, RenewEvery: 10 * time.Millisecond,
		MaxAttempts: 3, Backoff: func(int) time.Duration { return time.Minute },
	})
	require.NoError(l.t, err)

	return e
}

func (l *lab) start(e *provision.Engine) store.ProvisioningRun {
	l.t.Helper()

	run, err := e.Start(l.t.Context(), l.team, l.env, l.kind)
	require.NoError(l.t, err)

	return run
}

func (l *lab) run(id uint) store.ProvisioningRun {
	l.t.Helper()

	r, err := l.prov.Run(l.t.Context(), id)
	require.NoError(l.t, err)

	return r
}

func (l *lab) steps(id uint) map[string]store.ProvisioningStep {
	l.t.Helper()

	rows, err := l.prov.Steps(l.t.Context(), id)
	require.NoError(l.t, err)
	out := map[string]store.ProvisioningStep{}
	for _, r := range rows {
		out[r.Name] = r
	}

	return out
}

func apiErr(code, message string) error { return &smithy.GenericAPIError{Code: code, Message: message} }

func TestCreateBuildsTheEnvironmentAndMakesItReady(t *testing.T) {
	l := newLab(t)
	l.hub.connected.Store(true)
	e := l.engine()
	run := l.start(e)

	ran, err := e.RunOnce(t.Context())
	require.NoError(t, err)
	require.True(t, ran)

	got := l.run(run.ID)
	require.Equal(t, store.RunSucceeded, got.Status, got.Message)

	spec := l.cloud.specs[0]
	assert.Equal(t, managed.NetworkTemplate, spec.Template)
	assert.Equal(t, l.envID, spec.Parameters["EnvironmentId"])
	assert.Equal(t, "pds-"+l.envID, spec.Parameters["EksName"])
	assert.Equal(t, "arn:aws:iam::111122223333:role/pds/"+l.connID+"/pds-"+l.connID+"-eks-cluster", spec.Parameters["EksRoleArn"])
	assert.Equal(t, "arn:aws:iam::111122223333:role/pds/"+l.connID+"/pds-"+l.connID+"-cloudformation", spec.ServiceRoleARN)
	assert.Equal(t, l.envID, spec.Tags["percona.com/pds"])
	assert.Equal(t, "1", spec.Tags["percona.com/pds-template-revision"])

	target := l.cloud.targets[0]
	assert.Equal(t, "arn:aws:iam::111122223333:role/pds/"+l.connID+"/pds-"+l.connID+"-control-plane", target.RoleARN)
	assert.Equal(t, "external", target.ExternalID)
	assert.Equal(t, "pds-env-"+l.envID, target.StackName)
	assert.True(t, strings.HasPrefix(target.SourceIdentity, "pds-job-"))

	assert.Equal(t, []string{"2.5.1"}, l.kube.fluxes)
	assert.Equal(t, "https://eks.example", l.kube.access[0].Endpoint)
	assert.Equal(t, "k8s-aws-v1.token", l.kube.access[0].Token)
	require.Len(t, l.kube.joins, 1)
	assert.Equal(t, "agt_secret", l.kube.joins[0].AgentToken)

	release := string(l.hub.written["clusters/orders/platform/pds/helmrelease.yaml"])
	assert.Contains(t, release, "version: 0.1.0")
	assert.NotContains(t, release, "agt_secret", "the token never reaches Git")
	assert.Contains(t, string(l.hub.written["clusters/orders/platform/pds/helmrepository.yaml"]), "type: oci")
	assert.Contains(t, string(l.hub.written["clusters/orders/platform/pds/kustomization.yaml"]), "helmrelease.yaml")

	for _, cidrs := range l.cloud.cidrs {
		assert.Equal(t, []string{"203.0.113.7/32"}, cidrs)
	}
	assert.GreaterOrEqual(t, l.cloud.count("open-access"), 3, "each step that needs the cluster reopens the window")
	assert.Equal(t, 1, l.cloud.count("close-access"))
	assert.Nil(t, got.WindowOpenedAt, "the window is recorded closed")
}

func TestARevokedRoleParksTheRunInsteadOfFailingIt(t *testing.T) {
	l := newLab(t)
	l.cloud.failAt["check-role"] = []error{fmt.Errorf("%w: denied", aws.ErrAssumeDenied)}
	e := l.engine()
	run := l.start(e)

	_, err := e.RunOnce(t.Context())
	require.NoError(t, err)

	got := l.run(run.ID)
	assert.Equal(t, store.RunPaused, got.Status)
	assert.Equal(t, provision.WaitingForConnection, got.Message)
}

func TestAQuotaRefusalFailsTheStepWithASentenceThatNamesTheAccount(t *testing.T) {
	l := newLab(t)
	l.cloud.failAt["ensure-stack"] = []error{apiErr("VpcLimitExceeded", "The maximum number of VPCs has been reached. arn:aws:sts::1:assumed-role/x")}
	e := l.engine()
	run := l.start(e)

	_, err := e.RunOnce(t.Context())
	require.NoError(t, err)

	got := l.run(run.ID)
	assert.Equal(t, store.RunFailed, got.Status)
	step := l.steps(run.ID)["create-stack"]
	assert.Equal(t, store.StepFailed, step.Status)
	assert.Contains(t, step.Failure, "111122223333")
	assert.Contains(t, step.Failure, "VPCs per Region")
	assert.Contains(t, step.Failure, "eu-west-1")
	assert.NotContains(t, step.Failure, "arn:")
	assert.Equal(t, step.Failure, got.Message)
}

func TestAPreflightRefusalFailsBeforeAnythingIsBuilt(t *testing.T) {
	l := newLab(t)
	l.cloud.preflight = &aws.Refusal{Reason: aws.ReasonQuota, Service: "Amazon EC2", Item: "Elastic IP addresses per Region", Region: "eu-west-1"}
	e := l.engine()
	run := l.start(e)

	_, err := e.RunOnce(t.Context())
	require.NoError(t, err)

	got := l.run(run.ID)
	assert.Equal(t, store.RunFailed, got.Status)
	step := l.steps(run.ID)["assume-role"]
	assert.Equal(t, store.StepFailed, step.Status)
	assert.Contains(t, step.Failure, "Elastic IP addresses per Region")
	assert.Contains(t, step.Failure, "111122223333")
	assert.Equal(t, 0, l.cloud.count("ensure-stack"), "nothing exists in AWS yet")
	assert.Equal(t, 12, l.cloud.needs.VCPUs, "three m6i.xlarge nodes")
}

func TestTheBackupBucketIsMadeInTheAccountAndTaggedWithTheEnvironment(t *testing.T) {
	l := newLab(t)
	l.hub.connected.Store(true)
	e := l.engine()
	run := l.start(e)

	_, err := e.RunOnce(t.Context())
	require.NoError(t, err)

	require.Equal(t, store.RunSucceeded, l.run(run.ID).Status)
	assert.Equal(t, []string{"pds-111122223333-" + l.envID}, l.cloud.buckets)
	assert.Equal(t, l.envID, l.cloud.tags[0]["percona.com/pds"])
	release := string(l.hub.written["clusters/orders/platform/pds/helmrelease.yaml"])
	assert.Contains(t, release, "bucket: pds-111122223333-"+l.envID)
	assert.Contains(t, release, "region: eu-west-1")
}

func TestABucketRefusalFailsTheStepWithATypedSentence(t *testing.T) {
	l := newLab(t)
	l.cloud.failAt["ensure-bucket"] = []error{apiErr("AccessDenied", "User: arn:aws:sts::1:assumed-role/x is not authorized to perform: s3:CreateBucket")}
	e := l.engine()
	run := l.start(e)

	_, err := e.RunOnce(t.Context())
	require.NoError(t, err)

	step := l.steps(run.ID)["create-bucket"]
	assert.Equal(t, store.StepFailed, step.Status)
	assert.Contains(t, step.Failure, "s3:CreateBucket")
	assert.NotContains(t, step.Failure, "arn:")
}

func TestThrottlingRetriesTheStepByItself(t *testing.T) {
	l := newLab(t)
	l.cloud.failAt["ensure-stack"] = []error{apiErr("Throttling", "Rate exceeded")}
	e := l.engine()
	run := l.start(e)

	_, err := e.RunOnce(t.Context())
	require.NoError(t, err)

	assert.Equal(t, store.RunQueued, l.run(run.ID).Status, "a throttled step is queued to retry, not failed")
	assert.Equal(t, store.StepPending, l.steps(run.ID)["create-stack"].Status)
}

func TestAnUnknownFailureGetsTheGenericSentence(t *testing.T) {
	l := newLab(t)
	l.cloud.failAt["ensure-stack"] = []error{apiErr("InternalFailure", "boom"), apiErr("InternalFailure", "boom"), apiErr("InternalFailure", "boom")}
	e := l.engine()
	run := l.start(e)

	for range 3 {
		_, err := e.RunOnce(t.Context())
		require.NoError(t, err)
		l.requeue(run.ID)
	}

	step := l.steps(run.ID)["create-stack"]
	assert.Equal(t, store.StepFailed, step.Status)
	assert.Contains(t, step.Failure, "Contact Percona support")
}

// requeue makes a backing-off run claimable again without waiting out its backoff.
func (l *lab) requeue(id uint) {
	l.t.Helper()

	require.NoError(l.t, l.db.Gorm().WithContext(l.t.Context()).Model(&store.ProvisioningRun{}).
		Where("id = ? AND status = ?", id, store.RunQueued).Update("not_before", time.Now().Add(-time.Second)).Error)
}

func TestAFailedStackIsReportedThenStartedAgainOnRetry(t *testing.T) {
	l := newLab(t)
	l.cloud.stacks = []aws.StackState{
		{Exists: true, Status: "CREATE_FAILED", FailureReason: "The maximum number of VPCs has been reached. (Service: AmazonEC2; Status Code: 400; Error Code: VpcLimitExceeded; Request ID: x)"},
		{Exists: true, Status: "CREATE_FAILED"},
		{Exists: false},
		{Exists: true, Status: "CREATE_COMPLETE"},
	}
	l.hub.connected.Store(true)
	e := l.engine()
	run := l.start(e)

	_, err := e.RunOnce(t.Context())
	require.NoError(t, err)
	step := l.steps(run.ID)["wait-stack"]
	require.Equal(t, store.StepFailed, step.Status)
	assert.Contains(t, step.Failure, "VPCs per Region")
	assert.Equal(t, 0, l.cloud.count("delete-stack"), "a failed create is not rolled back by itself")

	require.NoError(t, e.Retry(t.Context(), l.team, l.env))
	_, err = e.RunOnce(t.Context())
	require.NoError(t, err)

	assert.Equal(t, store.RunSucceeded, l.run(run.ID).Status)
	assert.Equal(t, 1, l.cloud.count("delete-stack"))
	assert.Equal(t, 2, l.cloud.count("ensure-stack"), "the stack is made again once the failed one is gone")
	assert.Equal(t, 1, l.cloud.count("check-role"), "a retry re-runs only the failed step")
}

func TestAnEnvironmentThatNeverReportsFailsInFinishingAndRetryOnlyWaits(t *testing.T) {
	l := newLab(t)
	e := l.engine()
	run := l.start(e)

	_, err := e.RunOnce(t.Context())
	require.NoError(t, err)
	steps := l.steps(run.ID)
	require.Equal(t, store.StepFailed, steps["wait-heartbeat"].Status)
	assert.Contains(t, steps["wait-heartbeat"].Failure, "has not reported")
	assert.Equal(t, store.StepSucceeded, steps["close-access"].Status, "the window is closed before the wait")

	l.hub.connected.Store(true)
	require.NoError(t, e.Retry(t.Context(), l.team, l.env))
	_, err = e.RunOnce(t.Context())
	require.NoError(t, err)

	assert.Equal(t, store.RunSucceeded, l.run(run.ID).Status)
	assert.Equal(t, 1, l.cloud.count("ensure-stack"))
	assert.Equal(t, 1, l.hub.joins)
}

func TestCloseWindowClosesWhatTheSweeperFinds(t *testing.T) {
	l := newLab(t)

	note, err := managed.CloseWindow(l.deps)(t.Context(), store.ProvisioningRun{TeamID: l.team, Environment: l.env})
	require.NoError(t, err)

	assert.Equal(t, "Closed the public endpoint.", note)
	assert.Equal(t, "pds-"+l.envID, l.cloud.targets[0].EKSName)
}

func TestConnectionReadyFollowsTheConnectionState(t *testing.T) {
	l := newLab(t)
	run := store.ProvisioningRun{TeamID: l.team, Environment: l.env}
	assert.True(t, managed.ConnectionReady(l.deps)(t.Context(), run))

	_, err := l.db.CloudConnections().SaveCheck(t.Context(), l.connID, store.CloudCheck{State: store.CloudUnreachable, Reason: "x"}, time.Now())
	require.NoError(t, err)
	assert.False(t, managed.ConnectionReady(l.deps)(t.Context(), run))
}

func TestPhasesGroupTheStepsInTheFourPhasesTheConsoleShows(t *testing.T) {
	got := map[string]bool{}
	for _, name := range managed.CreateStepNames {
		got[managed.Phase(name)] = true
	}
	assert.Equal(t, map[string]bool{
		"Creating network and Kubernetes cluster": true, "Connecting to the environment": true,
		"Installing PDS": true, "Finishing": true,
	}, got)
	assert.Empty(t, managed.Phase("nonsense"))
}

var _ = errors.New

func TestCreateRecordsTheChartVersionItInstalledAndWhenTheEnvironmentWasReady(t *testing.T) {
	l := newLab(t)
	l.hub.connected.Store(true)
	e := l.engine()
	l.start(e)

	_, err := e.RunOnce(t.Context())
	require.NoError(t, err)

	rec, err := l.db.ManagedEnvironments().Get(t.Context(), l.team, l.env)
	require.NoError(t, err)
	assert.Equal(t, "0.1.0", rec.ChartVersion)
	assert.NotNil(t, rec.ReadyAt)
}
