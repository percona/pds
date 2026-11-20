package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/provision"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// What the lifecycle calls refuse beyond the stores' own errors.
var (
	// ErrNothingToUpgrade means no newer version or revision applies.
	ErrNothingToUpgrade = errors.New("there is nothing newer to upgrade to")
	// ErrAlreadyDeferred means the newest version was deferred before. A version
	// is deferred once.
	ErrAlreadyDeferred = errors.New("this version was already deferred")
	// ErrNotReady means the environment never reported to the control plane, so
	// there is nothing to upgrade.
	ErrNotReady = errors.New("the environment has not reported to the control plane yet")
	// ErrLost means the environment's infrastructure is missing, so only a
	// rebuild applies.
	ErrLost = errors.New("the environment's infrastructure is missing")
	// ErrNotLost means a rebuild was asked of an environment that is not lost.
	ErrNotLost = errors.New("the environment is not lost")
	// ErrWouldReplace means the update would replace the VPC or the EKS cluster.
	ErrWouldReplace = errors.New("the update would replace the network or the Kubernetes cluster")
	// ErrConfirmRequired means the update replaces the node group and was not
	// confirmed.
	ErrConfirmRequired = errors.New("the update replaces the node group and needs a confirm")
)

// LostReason is what a person reads next to a lost environment.
const LostReason = "Infrastructure missing"

// MaxDeferral is the longest a version may be put off.
const MaxDeferral = 30

// What a drift item says, and the source identity of the hourly check.
const (
	changedOutside = "Changed outside PDS"
	deletedOutside = "Deleted outside PDS"
	hourlyIdentity = "pds-job-hourly"
	checkTimeout   = 15 * time.Minute
)

// LifecycleRecords is the store the lifecycle reads and changes.
type LifecycleRecords interface {
	Records
	All(ctx context.Context) ([]store.ManagedEnvironment, error)
	SetLost(ctx context.Context, team uint, name string, lost bool, now time.Time) error
	SetDrift(ctx context.Context, team uint, name string, items []store.DriftItem, now time.Time) error
	SetObserved(ctx context.Context, team uint, name string, templateRevision int, kubernetesVersion string) error
	SetMaintenanceWindow(ctx context.Context, team uint, name string, w store.MaintenanceWindow) error
	SetDeferral(ctx context.Context, team uint, name, version string, until time.Time) error
}

// LifecycleRuns starts and counts provisioning runs. *provision.Engine is the
// implementation.
type LifecycleRuns interface {
	StartWith(ctx context.Context, team uint, environment, kind, params string) (store.ProvisioningRun, error)
	Busy(ctx context.Context, team uint, environment string) (bool, error)
}

// LifecycleOptions are the parts of a Lifecycle tests replace.
type LifecycleOptions struct {
	Now    func() time.Time
	Logger *slog.Logger
}

// Lifecycle is what happens to a managed environment after it is created: its
// maintenance window, chart upgrades, Kubernetes and template updates, drift,
// and loss with its rebuild. It starts provisioning runs for the changes and
// reads the rest.
type Lifecycle struct {
	deps    Deps
	records LifecycleRecords
	runs    LifecycleRuns
	paths   UpgradePaths
	opts    LifecycleOptions
}

// NewLifecycle builds a Lifecycle. paths holds the chart versions an
// environment may move to, from the pin.
func NewLifecycle(d Deps, records LifecycleRecords, runs LifecycleRuns, paths UpgradePaths, opts LifecycleOptions) *Lifecycle {
	d.Settings = d.Settings.withDefaults()
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	return &Lifecycle{deps: d, records: records, runs: runs, paths: paths, opts: opts}
}

// Status is what the control plane keeps of an environment's upgrades, drift and
// loss. It reads no AWS: the hourly check wrote what it found.
type Status struct {
	ChartVersion string
	// NextChart is the newest version the pin lets the environment move to and
	// that was not rolled back, or empty.
	NextChart string
	// SkippedVersion is the newest version that failed here.
	SkippedVersion string
	// Deferred says NextChart was put off until DeferredUntil.
	Deferred      bool
	DeferredUntil *time.Time
	Window        store.MaintenanceWindow
	// WindowStart and WindowEnd are the window that is open now, or the next one.
	WindowStart time.Time
	WindowEnd   time.Time
	Lost        bool
	LostAt      *time.Time
	// Drift is what the latest check found changed outside PDS.
	Drift          []store.DriftItem
	DriftCheckedAt *time.Time
	// TemplateRevision, KubernetesVersion and KubernetesUpgradeAvailable come from
	// what the latest check read. Zero and empty mean it has read nothing yet.
	TemplateRevision           int
	LatestTemplateRevision     int
	KubernetesVersion          string
	KubernetesUpgradeAvailable bool
}

// TemplateOutOfDate reports whether the stack carries an older template
// revision than this release embeds. An environment the check has not read yet
// is not reported out of date.
func (s Status) TemplateOutOfDate() bool {
	return s.DriftCheckedAt != nil && s.TemplateRevision < s.LatestTemplateRevision
}

// Status reads an environment's lifecycle state.
func (l *Lifecycle) Status(ctx context.Context, who auth.Scope, name string) (Status, error) {
	rec, err := l.records.Get(ctx, who.Team.ID, name)
	if err != nil {
		return Status{}, err
	}

	return l.Describe(rec), nil
}

// Describe is the lifecycle state a stored environment record says.
func (l *Lifecycle) Describe(rec store.ManagedEnvironment) Status {
	now := l.opts.Now()
	next, _ := l.paths.Next(rec.ChartVersion, rec.SkippedVersion)
	start, end := windowAt(now, rec.Window())

	return Status{
		ChartVersion: rec.ChartVersion, NextChart: next, SkippedVersion: rec.SkippedVersion,
		Deferred:      next != "" && rec.DeferredVersion == next && rec.DeferredUntil != nil && now.Before(*rec.DeferredUntil),
		DeferredUntil: rec.DeferredUntil,
		Window:        rec.Window(), WindowStart: start, WindowEnd: end,
		Lost: rec.LostAt != nil, LostAt: rec.LostAt, Drift: rec.Drift, DriftCheckedAt: rec.DriftCheckedAt,
		TemplateRevision: rec.TemplateRevision, LatestTemplateRevision: TemplateRevision,
		KubernetesVersion: rec.KubernetesVersion, KubernetesUpgradeAvailable: behindNewEnvironments(rec.KubernetesVersion),
	}
}

// behindNewEnvironments reports whether version is an older minor than the one
// a new environment starts on. An unreadable or unknown version is not.
func behindNewEnvironments(version string) bool {
	_, have, ok := strings.Cut(version, ".")
	_, want, _ := strings.Cut(NewEnvironmentKubernetesVersion, ".")
	h, errHave := strconv.Atoi(have)
	w, errWant := strconv.Atoi(want)

	return ok && errHave == nil && errWant == nil && h < w
}

// SetWindow records the weekly window the environment may be upgraded in.
func (l *Lifecycle) SetWindow(ctx context.Context, who auth.Scope, name string, w store.MaintenanceWindow) error {
	if w.Day < time.Sunday || w.Day > time.Saturday || w.Hour < 0 || w.Hour > 23 {
		return fmt.Errorf("%w: the day is 0 to 6 and the hour 0 to 23", ErrInvalid)
	}
	if _, err := time.LoadLocation(w.Zone); err != nil || w.Zone == "" {
		return fmt.Errorf("%w: %q is not a time zone", ErrInvalid, w.Zone)
	}
	if _, err := l.records.Get(ctx, who.Team.ID, name); err != nil {
		return err
	}

	return l.records.SetMaintenanceWindow(ctx, who.Team.ID, name, w)
}

// Defer puts the next chart version off for the given days, at most MaxDeferral.
// A version is deferred once: after the deferral it rolls out in the window.
func (l *Lifecycle) Defer(ctx context.Context, who auth.Scope, name string, days int) error {
	if days < 1 || days > MaxDeferral {
		return fmt.Errorf("%w: a version can be deferred from 1 to %d days", ErrInvalid, MaxDeferral)
	}
	rec, err := l.records.Get(ctx, who.Team.ID, name)
	if err != nil {
		return err
	}
	next, ok := l.paths.Next(rec.ChartVersion, rec.SkippedVersion)
	if !ok {
		return ErrNothingToUpgrade
	}
	if rec.DeferredVersion == next {
		return ErrAlreadyDeferred
	}

	return l.records.SetDeferral(ctx, who.Team.ID, name, next, l.opts.Now().Add(time.Duration(days)*24*time.Hour))
}

// UpgradeNow starts the chart upgrade without waiting for the window. It
// tries a version that was rolled back or deferred, since a person asked.
func (l *Lifecycle) UpgradeNow(ctx context.Context, who auth.Scope, name string) (store.ProvisioningRun, error) {
	rec, err := l.active(ctx, who, name)
	if err != nil {
		return store.ProvisioningRun{}, err
	}
	next, ok := l.paths.Next(rec.ChartVersion, "")
	if !ok {
		return store.ProvisioningRun{}, ErrNothingToUpgrade
	}

	return l.startUpgrade(ctx, rec, next)
}

func (l *Lifecycle) startUpgrade(ctx context.Context, rec store.ManagedEnvironment, version string) (store.ProvisioningRun, error) {
	params, err := json.Marshal(UpgradeParams{From: rec.ChartVersion, Version: version})
	if err != nil {
		return store.ProvisioningRun{}, fmt.Errorf("encode the upgrade: %w", err)
	}

	return l.runs.StartWith(ctx, rec.TeamID, rec.Name, KindUpgradePDS, string(params))
}

// active reads an environment that has reported and is not lost.
func (l *Lifecycle) active(ctx context.Context, who auth.Scope, name string) (store.ManagedEnvironment, error) {
	rec, err := l.records.Get(ctx, who.Team.ID, name)
	if err != nil {
		return store.ManagedEnvironment{}, err
	}
	if rec.ReadyAt == nil {
		return store.ManagedEnvironment{}, ErrNotReady
	}
	if rec.LostAt != nil {
		return store.ManagedEnvironment{}, ErrLost
	}

	return rec, nil
}

// UpgradeDue starts the chart upgrade of every environment whose maintenance
// window is open, that has a version to move to which was neither deferred nor
// rolled back. An environment with a run in hand is left alone.
func (l *Lifecycle) UpgradeDue(ctx context.Context) error {
	all, err := l.records.All(ctx)
	if err != nil {
		return err
	}
	now := l.opts.Now()
	for _, rec := range all {
		if rec.ReadyAt == nil || rec.LostAt != nil || rec.ChartVersion == "" || !inWindow(now, rec.Window()) {
			continue
		}
		next, ok := l.paths.Next(rec.ChartVersion, rec.SkippedVersion)
		if !ok || (rec.DeferredVersion == next && rec.DeferredUntil != nil && now.Before(*rec.DeferredUntil)) {
			continue
		}
		if _, err := l.startUpgrade(ctx, rec, next); err != nil && !errors.Is(err, store.ErrRunActive) {
			l.opts.Logger.WarnContext(ctx, "could not start a chart upgrade", "environment", rec.Name, "error", err)
		}
	}

	return nil
}

// windowAt returns the window that is open at now, or the next one to open.
func windowAt(now time.Time, w store.MaintenanceWindow) (time.Time, time.Time) {
	loc, err := time.LoadLocation(w.Zone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	for offset := -1; offset <= 7; offset++ {
		day := local.AddDate(0, 0, offset)
		start := time.Date(day.Year(), day.Month(), day.Day(), w.Hour, 0, 0, 0, loc)
		end := start.Add(store.MaintenanceLength)
		if start.Weekday() == w.Day && end.After(local) {
			return start.UTC(), end.UTC()
		}
	}

	return time.Time{}, time.Time{}
}

func inWindow(now time.Time, w store.MaintenanceWindow) bool {
	start, end := windowAt(now, w)

	return !now.Before(start) && now.Before(end)
}

// Infrastructure is what AWS says of an environment's stack and cluster.
type Infrastructure struct {
	TemplateRevision       int
	LatestTemplateRevision int
	KubernetesVersion      string
	// NextKubernetesVersion is the one minor version up, or empty.
	NextKubernetesVersion string
	NodeGroups            []aws.NodeGroupState
}

// TemplateOutOfDate reports whether the stack carries an older template
// revision than this release embeds.
func (i Infrastructure) TemplateOutOfDate() bool {
	return i.TemplateRevision < i.LatestTemplateRevision
}

// Infrastructure reads the stack's template revision and the cluster's version
// from AWS.
func (l *Lifecycle) Infrastructure(ctx context.Context, who auth.Scope, name string) (Infrastructure, error) {
	s, err := l.target(ctx, who, name)
	if err != nil {
		return Infrastructure{}, err
	}

	return l.read(ctx, s)
}

func (l *Lifecycle) read(ctx context.Context, s scope) (Infrastructure, error) {
	stack, err := l.deps.Cloud.Stack(ctx, s.target)
	if err != nil {
		return Infrastructure{}, awsError(err)
	}
	if !stack.Exists {
		return Infrastructure{}, ErrLost
	}
	cluster, err := l.deps.Cloud.Cluster(ctx, s.target)
	if err != nil {
		return Infrastructure{}, awsError(err)
	}
	if !cluster.Exists {
		return Infrastructure{}, ErrLost
	}
	revision, _ := strconv.Atoi(stack.Outputs["TemplateRevision"])

	return Infrastructure{
		TemplateRevision: revision, LatestTemplateRevision: TemplateRevision,
		KubernetesVersion: cluster.Version, NextKubernetesVersion: nextMinor(cluster.Version), NodeGroups: cluster.NodeGroups,
	}, nil
}

// nextMinor is the Kubernetes minor version one above v, or empty for a version
// that is not major.minor.
func nextMinor(v string) string {
	major, minor, ok := strings.Cut(v, ".")
	n, err := strconv.Atoi(minor)
	if !ok || err != nil || major != "1" {
		return ""
	}

	return fmt.Sprintf("%s.%d", major, n+1)
}

// awsError keeps the cause of a failed AWS call, and says a revoked role is a
// connection that does not work.
func awsError(err error) error {
	if errors.Is(err, aws.ErrAssumeDenied) {
		return fmt.Errorf("%w: %w", ErrConnectionNotReady, err)
	}

	return err
}

// InfraUpdate asks for the stack to be updated.
type InfraUpdate struct {
	// KubernetesVersion is the next minor version, or empty for a template update.
	KubernetesVersion string
	// ConfirmNodeGroupReplace allows an update that replaces the node group.
	ConfirmNodeGroupReplace bool
}

// PreviewUpdate makes the change set an update would execute, reads what it
// would do, and removes it. It changes nothing in AWS.
func (l *Lifecycle) PreviewUpdate(ctx context.Context, who auth.Scope, name string, in InfraUpdate) (Verdict, error) {
	_, verdict, err := l.preview(ctx, who, name, in)

	return verdict, err
}

func (l *Lifecycle) preview(ctx context.Context, who auth.Scope, name string, in InfraUpdate) (store.ManagedEnvironment, Verdict, error) {
	rec, err := l.active(ctx, who, name)
	if err != nil {
		return store.ManagedEnvironment{}, Verdict{}, err
	}
	s, err := l.target(ctx, who, name)
	if err != nil {
		return store.ManagedEnvironment{}, Verdict{}, err
	}
	infra, err := l.read(ctx, s)
	if err != nil {
		return store.ManagedEnvironment{}, Verdict{}, err
	}
	switch {
	case in.KubernetesVersion != "" && in.KubernetesVersion != infra.NextKubernetesVersion:
		return store.ManagedEnvironment{}, Verdict{}, fmt.Errorf("%w: Kubernetes moves one minor version at a time, from %s to %s",
			ErrInvalid, infra.KubernetesVersion, infra.NextKubernetesVersion)
	case in.KubernetesVersion == "" && !infra.TemplateOutOfDate():
		return store.ManagedEnvironment{}, Verdict{}, ErrNothingToUpgrade
	}

	spec := l.deps.updateSpec(s, fmt.Sprintf("pds-preview-%d", l.opts.Now().UnixNano()), in.KubernetesVersion)
	plan, err := l.deps.Cloud.PlanUpdate(ctx, s.target, spec)
	if err != nil {
		return store.ManagedEnvironment{}, Verdict{}, awsError(err)
	}
	if plan.Empty {
		return store.ManagedEnvironment{}, Verdict{}, ErrNothingToUpgrade
	}
	if err := l.deps.Cloud.DiscardPlan(ctx, s.target, plan.Name); err != nil {
		return store.ManagedEnvironment{}, Verdict{}, awsError(err)
	}

	return rec, Judge(plan), nil
}

// UpdateInfrastructure starts the run that updates the stack. It reads the change
// set first and refuses an update that would replace the network or the
// cluster, or the node group without a confirm.
func (l *Lifecycle) UpdateInfrastructure(ctx context.Context, who auth.Scope, name string, in InfraUpdate) (store.ProvisioningRun, error) {
	rec, verdict, err := l.preview(ctx, who, name, in)
	if err != nil {
		return store.ProvisioningRun{}, err
	}
	switch {
	case verdict.ReplacesNetwork:
		return store.ProvisioningRun{}, ErrWouldReplace
	case verdict.ReplacesNodeGroup && !in.ConfirmNodeGroupReplace:
		return store.ProvisioningRun{}, ErrConfirmRequired
	}
	params, err := json.Marshal(KubernetesParams(in))
	if err != nil {
		return store.ProvisioningRun{}, fmt.Errorf("encode the update: %w", err)
	}

	return l.runs.StartWith(ctx, rec.TeamID, rec.Name, KindKubernetes, string(params))
}

// Rebuild starts the run that builds a lost environment again.
func (l *Lifecycle) Rebuild(ctx context.Context, who auth.Scope, name string) (store.ProvisioningRun, error) {
	rec, err := l.records.Get(ctx, who.Team.ID, name)
	if err != nil {
		return store.ProvisioningRun{}, err
	}
	if rec.LostAt == nil {
		return store.ProvisioningRun{}, ErrNotLost
	}

	return l.runs.StartWith(ctx, rec.TeamID, rec.Name, KindRebuild, "")
}

// target says where calls for the environment go, made as the person acting.
func (l *Lifecycle) target(ctx context.Context, who auth.Scope, name string) (scope, error) {
	if _, err := l.records.Get(ctx, who.Team.ID, name); err != nil {
		return scope{}, err
	}
	s, err := l.deps.scopeFor(ctx, who.Team.ID, name, service.UserSourceIdentity(who.UserID))
	var step *provision.StepError
	if errors.As(err, &step) {
		return scope{}, step.Cause
	}

	return s, err
}

// Run starts the chart upgrades each upgradeEvery and the infrastructure check
// each checkEvery, until the context ends.
func (l *Lifecycle) Run(ctx context.Context, upgradeEvery, checkEvery time.Duration) {
	upgrades, checks := time.NewTicker(upgradeEvery), time.NewTicker(checkEvery)
	defer upgrades.Stop()
	defer checks.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-upgrades.C:
			if err := l.UpgradeDue(ctx); err != nil {
				l.opts.Logger.WarnContext(ctx, "could not list environments to upgrade", "error", err)
			}
		case <-checks.C:
			if err := l.Check(ctx); err != nil {
				l.opts.Logger.WarnContext(ctx, "could not list environments to check", "error", err)
			}
		}
	}
}

// Check looks at every environment's infrastructure: it marks an environment
// Lost when its heartbeat is stale and AWS confirms the cluster is gone, and it
// records what was changed outside PDS. It only reports and never changes AWS.
// An environment with a run in hand, one that never reported, and one whose
// connection does not work are left alone.
func (l *Lifecycle) Check(ctx context.Context) error {
	all, err := l.records.All(ctx)
	if err != nil {
		return err
	}
	for _, rec := range all {
		if rec.ReadyAt == nil {
			continue
		}
		if busy, err := l.runs.Busy(ctx, rec.TeamID, rec.Name); err != nil || busy {
			continue
		}
		one, cancel := context.WithTimeout(ctx, checkTimeout)
		err := l.checkOne(one, rec)
		cancel()
		if err != nil {
			l.opts.Logger.WarnContext(ctx, "could not check an environment's infrastructure", "environment", rec.Name, "error", err)
		}
	}

	return nil
}

func (l *Lifecycle) checkOne(ctx context.Context, rec store.ManagedEnvironment) error {
	s, err := l.deps.scopeFor(ctx, rec.TeamID, rec.Name, hourlyIdentity)
	if err != nil {
		return err
	}
	if s.conn.State != store.CloudReady {
		return nil
	}
	connected, err := l.deps.Hub.Connected(ctx, rec.TeamID, rec.Name)
	if err != nil {
		return err
	}
	cluster, err := l.deps.Cloud.Cluster(ctx, s.target)
	if err != nil {
		return err
	}
	if !cluster.Exists {
		if connected {
			return nil
		}

		return l.records.SetLost(ctx, rec.TeamID, rec.Name, true, l.opts.Now())
	}
	if rec.LostAt != nil {
		if err := l.records.SetLost(ctx, rec.TeamID, rec.Name, false, l.opts.Now()); err != nil {
			return err
		}
	}

	stack, err := l.deps.Cloud.Stack(ctx, s.target)
	if err != nil {
		return err
	}
	cfn, err := l.deps.Cloud.DetectDrift(ctx, s.target)
	if err != nil {
		return err
	}

	revision, _ := strconv.Atoi(stack.Outputs["TemplateRevision"])
	if err := l.records.SetObserved(ctx, rec.TeamID, rec.Name, revision, cluster.Version); err != nil {
		return err
	}

	return l.records.SetDrift(ctx, rec.TeamID, rec.Name, driftItems(stack, cluster, cfn), l.opts.Now())
}

// driftItems names what differs from what PDS made: the resources
// CloudFormation found changed or deleted, and the cluster and node group,
// which CloudFormation does not check, compared with the stack's parameters.
func driftItems(stack aws.StackState, cluster aws.KubernetesState, cfn []aws.Drift) []store.DriftItem {
	var items []store.DriftItem
	if !stack.Exists {
		items = append(items, store.DriftItem{Resource: "CloudFormation stack", Detail: deletedOutside})
	}
	for _, d := range cfn {
		detail := changedOutside
		if d.Status == "DELETED" {
			detail = deletedOutside
		}
		items = append(items, store.DriftItem{Resource: d.Resource + " (" + d.Type + ")", Detail: detail})
	}
	if want := stack.Parameters["KubernetesVersion"]; want != "" && cluster.Version != want {
		items = append(items, store.DriftItem{Resource: "EKS cluster Kubernetes version", Detail: changedOutside})
	}
	if cluster.PublicEndpoint {
		items = append(items, store.DriftItem{Resource: "EKS cluster public endpoint", Detail: changedOutside})
	}
	var group *aws.NodeGroupState
	for i := range cluster.NodeGroups {
		if cluster.NodeGroups[i].Name == "pds-nodes" {
			group = &cluster.NodeGroups[i]
		}
	}
	if group == nil {
		return append(items, store.DriftItem{Resource: "EKS node group pds-nodes", Detail: deletedOutside})
	}
	if count, err := strconv.Atoi(stack.Parameters["NodeCount"]); err == nil && (int(group.Min) != count || int(group.Desired) != count || int(group.Max) != count) {
		items = append(items, store.DriftItem{Resource: "EKS node group pds-nodes size", Detail: changedOutside})
	}
	if want := stack.Parameters["NodeInstanceType"]; want != "" && (len(group.InstanceTypes) != 1 || group.InstanceTypes[0] != want) {
		items = append(items, store.DriftItem{Resource: "EKS node group pds-nodes instance type", Detail: changedOutside})
	}

	return items
}
