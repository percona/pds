package managed

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/provision"
	"github.com/percona/pds/adapter/internal/store"
)

// KindCreate is the run kind that builds a managed environment.
const KindCreate = "create"

// The names of the create steps, in order.
const (
	stepAssumeRole    = "assume-role"
	stepClearStack    = "clear-stack"
	stepCreateStack   = "create-stack"
	stepWaitStack     = "wait-stack"
	stepCreateBucket  = "create-bucket"
	stepOpenAccess    = "open-access"
	stepInstallFlux   = "install-flux"
	stepWriteRelease  = "write-release"
	stepJoin          = "join"
	stepCloseAccess   = "close-access"
	stepWaitHeartbeat = "wait-heartbeat"
)

// CreateStepNames lists the create steps in the order they run.
var CreateStepNames = []string{
	stepAssumeRole, stepCreateStack, stepWaitStack, stepCreateBucket, stepOpenAccess, stepInstallFlux,
	stepWriteRelease, stepJoin, stepCloseAccess, stepWaitHeartbeat,
}

// Phase is the phase of a create the console shows a step under, or empty for
// a name that is not a create step.
func Phase(step string) string {
	switch step {
	case stepAssumeRole, stepClearStack, stepCreateStack, stepWaitStack, stepCreateBucket:
		return "Creating network and Kubernetes cluster"
	case stepOpenAccess:
		return "Connecting to the environment"
	case stepInstallFlux, stepWriteRelease, stepJoin:
		return "Installing PDS"
	case stepCloseAccess, stepWaitHeartbeat:
		return "Finishing"
	}

	return ""
}

// Sentences a person reads when a step has nothing more specific to say.
const (
	busySentence = "AWS is asking the control plane to slow down. The step tries again by itself."
	// genericSentence is for an AWS error that is none of the typed refusals.
	genericSentence = "AWS reported an error that PDS cannot fix for you. Retry the step. Contact Percona support if it fails again."
	stillCreating   = "The network and the Kubernetes cluster are still being created. The step continues by itself."
	stackFailed     = "AWS could not create the network and the Kubernetes cluster. Retry the step. Contact Percona support if it fails again."
	notReporting    = "The environment has not reported to the control plane yet. Retry to keep waiting."
)

// Records reads the managed environment a run works on.
type Records interface {
	Get(ctx context.Context, team uint, name string) (store.ManagedEnvironment, error)
	SetStackFailureSeen(ctx context.Context, team uint, name string, seen bool) error
	SetChartVersion(ctx context.Context, team uint, name, version string) error
	SetSkippedVersion(ctx context.Context, team uint, name, version string) error
	MarkReady(ctx context.Context, team uint, name string, now time.Time) error
}

// Connections reads the cloud connection an environment was created through.
type Connections interface {
	Get(ctx context.Context, team uint, id string) (store.CloudConnection, error)
}

// Cloud is the customer's AWS account, through the connection's role. *aws.Infra
// is the implementation, and tests fake it.
type Cloud interface {
	CheckRole(ctx context.Context, t aws.Target) error
	Preflight(ctx context.Context, t aws.Target, needs aws.Needs) (aws.Refusal, bool)
	EnsureBucket(ctx context.Context, t aws.Target, bucket string, tags map[string]string) error
	EnsureStack(ctx context.Context, t aws.Target, spec aws.StackSpec) error
	DeleteStack(ctx context.Context, t aws.Target) error
	Stack(ctx context.Context, t aws.Target) (aws.StackState, error)
	OpenAccess(ctx context.Context, t aws.Target, cidrs []string) (aws.ClusterAccess, error)
	CloseAccess(ctx context.Context, t aws.Target) (string, error)
	KubeToken(ctx context.Context, t aws.Target) (string, error)
	PlanUpdate(ctx context.Context, t aws.Target, spec aws.UpdateSpec) (aws.ChangePlan, error)
	ApplyPlan(ctx context.Context, t aws.Target, name string) error
	DiscardPlan(ctx context.Context, t aws.Target, name string) error
	DetectDrift(ctx context.Context, t aws.Target) ([]aws.Drift, error)
	Cluster(ctx context.Context, t aws.Target) (aws.KubernetesState, error)
}

// KubeAccess is what the control plane calls an environment's Kubernetes API
// with while the Access window is open. The token lasts a minute.
type KubeAccess struct {
	Endpoint string
	CA       []byte
	Token    string
}

// Join is what a new environment needs to connect itself to the control plane:
// the agent's credential and the Git remote Flux reads. It is written into the
// environment and kept nowhere else.
type Join struct {
	Environment    string
	AgentToken     string
	GitURL         string
	GitIdentity    string
	GitIdentityPub string
	// Namespace is where the environment's databases live.
	Namespace string
}

// Cluster is the environment's Kubernetes API.
type Cluster interface {
	// InstallFlux installs the pinned Flux and the ServiceAccount that applies
	// the pds release. It is idempotent.
	InstallFlux(ctx context.Context, access KubeAccess, version string) error
	// WriteJoin writes the agent's Secret and points Flux at the environment's
	// Git repository. It is idempotent.
	WriteJoin(ctx context.Context, access KubeAccess, join Join) error
}

// Hub is the control plane's side of an environment: its Git repository, its
// join token and its heartbeat.
type Hub interface {
	// Join mints the environment's join token, revokes any earlier one, and
	// enrolls it. Only hashes are stored.
	Join(ctx context.Context, team uint, environment string) (Join, error)
	// WriteRelease commits the files to the environment's Git repository.
	WriteRelease(ctx context.Context, team uint, environment string, files map[string][]byte) error
	// Connected reports whether the environment's agent has sent a heartbeat.
	Connected(ctx context.Context, team uint, environment string) (bool, error)
	// Release returns what the environment's agent last reported of its pds
	// release, and false while it has reported none.
	Release(ctx context.Context, team uint, environment string) (hub.HeartbeatRelease, bool, error)
}

// Settings are what the steps take from the deployment.
type Settings struct {
	// OutgoingCIDRs are the only addresses the Kubernetes API is open to
	// while an Access window is open.
	OutgoingCIDRs []string
	FluxVersion   string
	ChartVersion  string
	HubURL        string
	// ImageRepository and ImageTag are the agent image the release installs.
	ImageRepository string
	ImageTag        string
	// PollEvery is how often a wait looks again. StackWait bounds the wait for
	// the stack and JoinWait the wait for the first heartbeat.
	PollEvery time.Duration
	StackWait time.Duration
	JoinWait  time.Duration
	// ReleaseWait bounds the wait for an upgrade's outcome.
	ReleaseWait time.Duration
	// DeleteWait bounds how long a delete waits for AWS to release what holds
	// the environment's network, DrainWait how long it waits for databases and
	// volume claims to leave the environment, and APIWait how long it keeps
	// trying an environment's Kubernetes API that does not answer.
	DeleteWait time.Duration
	DrainWait  time.Duration
	APIWait    time.Duration
}

func (s Settings) withDefaults() Settings {
	if s.PollEvery <= 0 {
		s.PollEvery = 15 * time.Second
	}
	if s.StackWait <= 0 {
		s.StackWait = 45 * time.Minute
	}
	if s.JoinWait <= 0 {
		s.JoinWait = 20 * time.Minute
	}
	if s.ReleaseWait <= 0 {
		s.ReleaseWait = 30 * time.Minute
	}
	if s.DeleteWait <= 0 {
		s.DeleteWait = 30 * time.Minute
	}
	if s.DrainWait <= 0 {
		s.DrainWait = 20 * time.Minute
	}
	if s.APIWait <= 0 {
		s.APIWait = 3 * time.Minute
	}

	return s
}

// Deps is what the managed environment steps work through.
type Deps struct {
	Records     Records
	Connections Connections
	Cloud       Cloud
	Cluster     Cluster
	Hub         Hub
	Settings    Settings
	// Granted says whether the stack's service role is granted an action, so a
	// permission refusal can point at Changed outside PDS. Nil says it is not.
	Granted func(action string) bool
	// Sweeper, Drainer and Forget are what a delete works through.
	Sweeper Sweeper
	Drainer Drainer
	// Forget removes the environment from its Team's hub and forgets its record.
	Forget func(ctx context.Context, team uint, name string) error
}

// scope is what one step knows about its environment and where it works.
type scope struct {
	rec    store.ManagedEnvironment
	conn   store.CloudConnection
	target aws.Target
	where  aws.Where
}

func (d Deps) scope(ctx context.Context, run store.ProvisioningRun) (scope, error) {
	return d.scopeFor(ctx, run.TeamID, run.Environment, "pds-job-provision-"+strconv.FormatUint(uint64(run.ID), 10))
}

// scopeFor reads an environment and its connection, and says where a call for it
// goes and who it is made as. Nothing here calls AWS.
func (d Deps) scopeFor(ctx context.Context, team uint, environment, sourceIdentity string) (scope, error) {
	rec, err := d.Records.Get(ctx, team, environment)
	if errors.Is(err, store.ErrManagedEnvironmentUnknown) {
		return scope{}, provision.Permanent(err, "This environment is not a managed environment. Contact Percona support.")
	}
	if err != nil {
		return scope{}, provision.Retryable(err, genericSentence)
	}
	conn, err := d.Connections.Get(ctx, team, rec.ConnectionID)
	if err != nil {
		return scope{}, provision.Retryable(err, genericSentence)
	}
	cluster := "pds-" + rec.ID

	return scope{
		rec: rec, conn: conn,
		target: aws.Target{
			RoleARN:        aws.RoleARN(conn.AccountID, conn.ID, "control-plane"),
			ExternalID:     conn.ExternalID,
			SourceIdentity: sourceIdentity,
			Region:         rec.Region,
			StackName:      "pds-env-" + rec.ID,
			EKSName:        cluster,
		},
		where: aws.Where{Account: conn.AccountID, Region: rec.Region, Cluster: environment, Granted: d.Granted},
	}, nil
}

// fail turns an AWS error into what the engine acts on: a pause for a revoked
// role, a retry for throttling, a typed refusal, or the generic sentence.
func (s scope) fail(err error) error {
	switch {
	case errors.Is(err, aws.ErrAssumeDenied):
		return fmt.Errorf("%w: %w", provision.ErrConnectionUnavailable, err)
	case aws.Transient(err):
		return provision.Retryable(err, busySentence)
	}
	if refusal, ok := aws.Classify(err, s.where); ok {
		return refused(err, refusal, s.where)
	}

	return provision.Retryable(err, genericSentence)
}

// refused is a step failure AWS caused and the customer fixes: it carries the
// typed refusal beside the sentence.
func refused(err error, refusal aws.Refusal, where aws.Where) error {
	return provision.Refused(err, refusal.Sentence(where), provision.Refusal{
		Reason: string(refusal.Reason), Service: refusal.Service, Item: refusal.Item, Region: refusal.Region,
	})
}

type stepFunc func(context.Context, provision.Env, scope) error

// CreateKind is the run that builds a managed environment, step by step. Every
// step is idempotent, and any step that needs the Kubernetes API opens the
// Access window again, because a run that waited for a lease holds none.
func CreateKind(d Deps) provision.Kind {
	d.Settings = d.Settings.withDefaults()

	return d.kind(KindCreate, CreateStepNames, d.createSteps())
}

func (d Deps) createSteps() map[string]stepFunc {
	return map[string]stepFunc{
		stepAssumeRole:    d.assumeRole,
		stepCreateStack:   d.createStack,
		stepWaitStack:     d.waitStack,
		stepCreateBucket:  d.createBucket,
		stepOpenAccess:    func(ctx context.Context, env provision.Env, s scope) error { _, err := d.open(ctx, env, s); return err },
		stepInstallFlux:   d.installFlux,
		stepWriteRelease:  d.writeRelease,
		stepJoin:          d.join,
		stepCloseAccess:   d.closeAccess,
		stepWaitHeartbeat: d.waitHeartbeat,
	}
}

// kind turns an ordered list of step names into a run kind whose steps each
// read the environment they work on.
func (d Deps) kind(name string, order []string, steps map[string]stepFunc) provision.Kind {
	kind := provision.Kind{Name: name}
	for _, stepName := range order {
		run := steps[stepName]
		kind.Steps = append(kind.Steps, provision.Step{Name: stepName, Run: func(ctx context.Context, env provision.Env) error {
			s, err := d.scope(ctx, env.Run)
			if err != nil {
				return err
			}

			return run(ctx, env, s)
		}})
	}

	return kind
}

// CloseWindow closes an environment's Access window for the sweeper, after
// reading the real endpoint, and returns what it did.
func CloseWindow(d Deps) func(ctx context.Context, run store.ProvisioningRun) (string, error) {
	return func(ctx context.Context, run store.ProvisioningRun) (string, error) {
		s, err := d.scope(ctx, run)
		if err != nil {
			return "", err
		}

		return d.Cloud.CloseAccess(ctx, s.target)
	}
}

// ConnectionReady reports whether the connection a paused run waits on works again.
func ConnectionReady(d Deps) func(ctx context.Context, run store.ProvisioningRun) bool {
	return func(ctx context.Context, run store.ProvisioningRun) bool {
		s, err := d.scope(ctx, run)

		return err == nil && s.conn.State == store.CloudReady
	}
}

func (d Deps) assumeRole(ctx context.Context, env provision.Env, s scope) error {
	if err := d.checkRole(ctx, env, s); err != nil {
		return err
	}
	if refusal, found := d.Cloud.Preflight(ctx, s.target, aws.Needs{VCPUs: NodeVCPUs}); found {
		return refused(errors.New("the preflight found the account cannot build the environment"), refusal, s.where)
	}

	return nil
}

// NodeVCPUs is what the template's default node group takes: three m6i.xlarge
// nodes of four vCPUs. The preflight compares the account's vCPU quota with it.
const NodeVCPUs = 12

// bucketName is the environment's backup bucket. The pds prefix and the account
// are what the control plane's role is allowed to create buckets under.
func bucketName(s scope) string {
	return "pds-" + s.conn.AccountID + "-" + s.rec.ID
}

func (d Deps) createBucket(ctx context.Context, _ provision.Env, s scope) error {
	if err := d.Cloud.EnsureBucket(ctx, s.target, bucketName(s), map[string]string{OwnershipTag: s.rec.ID}); err != nil {
		return s.fail(err)
	}

	return nil
}

func (d Deps) stackSpec(s scope) aws.StackSpec {
	return aws.StackSpec{
		Template:       NetworkTemplate,
		ServiceRoleARN: aws.RoleARN(s.conn.AccountID, s.conn.ID, "cloudformation"),
		Parameters: map[string]string{
			"EnvironmentId": s.rec.ID,
			"EksName":       s.target.EKSName,
			"EksRoleArn":    aws.RoleARN(s.conn.AccountID, s.conn.ID, "eks-cluster"),
			"NodeRoleArn":   aws.RoleARN(s.conn.AccountID, s.conn.ID, "node"),
			"EbsCsiRoleArn": aws.RoleARN(s.conn.AccountID, s.conn.ID, "ebs-csi"),
		},
		Tags: map[string]string{
			OwnershipTag:                        s.rec.ID,
			"percona.com/pds-template-revision": strconv.Itoa(TemplateRevision),
			"percona.com/pds-connection":        s.conn.ID,
		},
	}
}

func (d Deps) createStack(ctx context.Context, _ provision.Env, s scope) error {
	if err := d.Cloud.EnsureStack(ctx, s.target, d.stackSpec(s)); err != nil {
		return s.fail(err)
	}

	return nil
}

// waitStack waits for the stack to finish. A stack that failed is reported once
// and left as it is; the next attempt deletes it and starts the stack again,
// since CloudFormation cannot resume a failed create.
func (d Deps) waitStack(ctx context.Context, _ provision.Env, s scope) error {
	deadline := time.Now().Add(d.Settings.StackWait)
	for {
		state, err := d.Cloud.Stack(ctx, s.target)
		if err != nil {
			return s.fail(err)
		}

		switch {
		case !state.Exists:
			if err := d.Cloud.EnsureStack(ctx, s.target, d.stackSpec(s)); err != nil {
				return s.fail(err)
			}
			if err := d.Records.SetStackFailureSeen(ctx, s.rec.TeamID, s.rec.Name, false); err != nil {
				return provision.Retryable(err, genericSentence)
			}
		case state.Complete():
			return nil
		case state.InProgress() || state.Status == "DELETE_IN_PROGRESS" || state.Status == "ROLLBACK_IN_PROGRESS":
		case state.Failed():
			rec, err := d.Records.Get(ctx, s.rec.TeamID, s.rec.Name)
			if err != nil {
				return provision.Retryable(err, genericSentence)
			}
			if !rec.StackFailureSeen {
				if err := d.Records.SetStackFailureSeen(ctx, s.rec.TeamID, s.rec.Name, true); err != nil {
					return provision.Retryable(err, genericSentence)
				}

				return stackFailure(state, s)
			}
			if err := d.Cloud.DeleteStack(ctx, s.target); err != nil {
				return s.fail(err)
			}
		}

		if time.Now().After(deadline) {
			return provision.Retryable(errors.New("the stack is still being created"), stillCreating)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.Settings.PollEvery):
		}
	}
}

// stackFailure says why the stack failed: a typed refusal when the first
// failed resource names one, otherwise the generic sentence.
func stackFailure(state aws.StackState, s scope) error {
	cause := errors.New(state.FailureReason)
	if refusal, ok := aws.Classify(cause, s.where); ok {
		return refused(cause, refusal, s.where)
	}

	return provision.Permanent(fmt.Errorf("stack status %s: %s", state.Status, state.FailureReason), stackFailed)
}

// open opens the Access window, recording it first so the sweeper closes it if
// this worker dies, and returns what the control plane calls the API with.
func (d Deps) open(ctx context.Context, env provision.Env, s scope) (KubeAccess, error) {
	if len(d.Settings.OutgoingCIDRs) == 0 {
		return KubeAccess{}, provision.Permanent(errors.New("no outgoing addresses are declared"),
			"This control plane has no declared outgoing addresses, so it cannot reach a managed environment. Contact Percona support.")
	}
	if err := env.WindowOpened(ctx); err != nil {
		return KubeAccess{}, err
	}
	access, err := d.Cloud.OpenAccess(ctx, s.target, d.Settings.OutgoingCIDRs)
	if err != nil {
		return KubeAccess{}, s.fail(err)
	}
	token, err := d.Cloud.KubeToken(ctx, s.target)
	if err != nil {
		return KubeAccess{}, s.fail(err)
	}

	return KubeAccess{Endpoint: access.Endpoint, CA: access.CA, Token: token}, nil
}

func (d Deps) installFlux(ctx context.Context, env provision.Env, s scope) error {
	access, err := d.open(ctx, env, s)
	if err != nil {
		return err
	}
	if err := d.Cluster.InstallFlux(ctx, access, d.Settings.FluxVersion); err != nil {
		return provision.Retryable(err, genericSentence)
	}

	return nil
}

func (d Deps) writeRelease(ctx context.Context, _ provision.Env, s scope) error {
	// A rebuild installs the version the environment ran, not the newest one.
	version := s.rec.ChartVersion
	if version == "" {
		version = d.Settings.ChartVersion
	}
	if err := d.commitRelease(ctx, s, version); err != nil {
		return err
	}
	if err := d.Records.SetChartVersion(ctx, s.rec.TeamID, s.rec.Name, version); err != nil {
		return provision.Retryable(err, genericSentence)
	}

	return nil
}

// commitRelease writes the pds release of a chart version into the
// environment's Git repository, where Flux applies it.
func (d Deps) commitRelease(ctx context.Context, s scope, version string) error {
	files, err := ReleaseFiles(s.rec.Name, ReleaseInput{
		ChartVersion: version, HubURL: d.Settings.HubURL, EnvironmentName: s.rec.Name, EnvironmentID: s.rec.ID,
		ImageRepository: d.Settings.ImageRepository, ImageTag: d.Settings.ImageTag,
		Bucket: bucketName(s), Region: s.rec.Region,
	})
	if err != nil {
		return provision.Permanent(err, genericSentence)
	}
	if err := d.Hub.WriteRelease(ctx, s.rec.TeamID, s.rec.Name, files); err != nil {
		return provision.Retryable(err, genericSentence)
	}

	return nil
}

func (d Deps) join(ctx context.Context, env provision.Env, s scope) error {
	join, err := d.Hub.Join(ctx, s.rec.TeamID, s.rec.Name)
	if err != nil {
		return provision.Retryable(err, genericSentence)
	}
	access, err := d.open(ctx, env, s)
	if err != nil {
		return err
	}
	if err := d.Cluster.WriteJoin(ctx, access, join); err != nil {
		return provision.Retryable(err, genericSentence)
	}

	return nil
}

func (d Deps) closeAccess(ctx context.Context, env provision.Env, s scope) error {
	note, err := d.Cloud.CloseAccess(ctx, s.target)
	if err != nil {
		return s.fail(err)
	}

	return env.WindowClosed(ctx, note)
}

func (d Deps) waitHeartbeat(ctx context.Context, _ provision.Env, s scope) error {
	deadline := time.Now().Add(d.Settings.JoinWait)
	for {
		connected, err := d.Hub.Connected(ctx, s.rec.TeamID, s.rec.Name)
		if err != nil {
			return provision.Retryable(err, genericSentence)
		}
		if connected {
			if err := d.Records.MarkReady(ctx, s.rec.TeamID, s.rec.Name, time.Now()); err != nil {
				return provision.Retryable(err, genericSentence)
			}

			return nil
		}
		if time.Now().After(deadline) {
			return provision.Permanent(errors.New("no heartbeat before the deadline"), notReporting)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.Settings.PollEvery):
		}
	}
}
