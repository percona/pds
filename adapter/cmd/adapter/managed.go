package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/provision"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// provisionSweepEvery is how often a replica closes stale Access windows,
// resumes paused runs and prunes old ones.
const provisionSweepEvery = time.Minute

// lifecycleUpgradeEvery is how often a replica looks for environments whose
// maintenance window is open, and lifecycleCheckEvery how often it reads their
// infrastructure for drift and loss.
const (
	lifecycleUpgradeEvery = 10 * time.Minute
	lifecycleCheckEvery   = time.Hour
)

// managedSettings turns the deployment's configuration into what the create
// steps and the request that starts them depend on.
func managedSettings(cfg config.Config) (managed.Settings, managed.CreatorSettings) {
	repository, tag := splitImage(cfg.HubAgentImage)

	settings := managed.Settings{
		OutgoingCIDRs: cfg.ManagedOutgoingCIDRs, FluxVersion: managed.FluxVersion, ChartVersion: cfg.ManagedChartVersion, ChartURL: cfg.ManagedChartURL,
		HubURL: cfg.HubPublicURL, ImageRepository: repository, ImageTag: tag,
	}
	creator := managed.CreatorSettings{
		OutgoingCIDRs: cfg.ManagedOutgoingCIDRs, ChartVersion: cfg.ManagedChartVersion, ImageRepository: repository,
	}

	return settings, creator
}

// splitImage separates an image reference's repository from its tag. A colon
// before the last slash belongs to the registry's port.
func splitImage(image string) (repository, tag string) {
	image = strings.TrimSpace(image)
	colon := strings.LastIndex(image, ":")
	if colon < 0 || colon < strings.LastIndex(image, "/") {
		return image, ""
	}

	return image[:colon], image[colon+1:]
}

// managedGitURL is the address Flux on a managed environment clones: the SSH
// remote of the environment's GitHub repository, or the Git address the
// operator configured for a hub with no GitHub.
func managedGitURL(cfg config.Config) func(repo string) string {
	return func(repo string) string {
		if cfg.HubGitHubConfigured() {
			return cfg.HubGitHubSSHURL(repo)
		}

		return cfg.HubGitURL
	}
}

// helmReleases is the Flux kind the control plane keeps the pds release in.
var helmReleases = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}

// fluxReleaseReader reads the HelmRelease an agent reports, and nothing when
// there is none: a connected environment has no such release.
func fluxReleaseReader(client *kube.Client, namespace, name string) func(ctx context.Context) (map[string]any, error) {
	return func(ctx context.Context) (map[string]any, error) {
		object, err := client.GetObject(ctx, helmReleases, namespace, name)
		if errors.Is(err, kube.ErrObjectNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}

		return object.Object, nil
	}
}

// lateRuns starts, retries and reads provisioning runs once the database has
// connected. The connection lands in the background, and a request before it
// has no run to queue or read.
type lateRuns struct {
	mu   sync.RWMutex
	runs managed.RunBook
}

func (l *lateRuns) set(runs managed.RunBook) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.runs = runs
}

func (l *lateRuns) book() (managed.RunBook, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.runs == nil {
		return nil, store.ErrNotConnected
	}

	return l.runs, nil
}

func (l *lateRuns) Start(ctx context.Context, team uint, environment, kind string) (store.ProvisioningRun, error) {
	runs, err := l.book()
	if err != nil {
		return store.ProvisioningRun{}, err
	}

	return runs.Start(ctx, team, environment, kind)
}

func (l *lateRuns) Retry(ctx context.Context, team uint, environment string) error {
	runs, err := l.book()
	if err != nil {
		return err
	}

	return runs.Retry(ctx, team, environment)
}

func (l *lateRuns) Latest(ctx context.Context, team uint, environment string) (store.ProvisioningRun, error) {
	runs, err := l.book()
	if err != nil {
		return store.ProvisioningRun{}, err
	}

	return runs.Latest(ctx, team, environment)
}

func (l *lateRuns) Steps(ctx context.Context, runID uint) ([]store.ProvisioningStep, error) {
	runs, err := l.book()
	if err != nil {
		return nil, err
	}

	return runs.Steps(ctx, runID)
}

// lateLifecycle is the managed lifecycle once the database has connected and the
// pin lists the upgrade paths. Until then there is no lifecycle to describe, and
// a change is unavailable.
type lateLifecycle struct {
	mu   sync.RWMutex
	life builtLifecycle
}

// builtLifecycle is *managed.Lifecycle: managed.Lifecycler, but its Describe
// always has an answer.
type builtLifecycle interface {
	Describe(rec store.ManagedEnvironment) managed.Status
	SetWindow(ctx context.Context, who auth.Scope, name string, w store.MaintenanceWindow) error
	Defer(ctx context.Context, who auth.Scope, name string, days int) error
	UpgradeNow(ctx context.Context, who auth.Scope, name string) (store.ProvisioningRun, error)
	PreviewUpdate(ctx context.Context, who auth.Scope, name string, in managed.InfraUpdate) (managed.Verdict, error)
	UpdateInfrastructure(ctx context.Context, who auth.Scope, name string, in managed.InfraUpdate) (store.ProvisioningRun, error)
	Rebuild(ctx context.Context, who auth.Scope, name string) (store.ProvisioningRun, error)
}

func (l *lateLifecycle) set(life builtLifecycle) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.life = life
}

func (l *lateLifecycle) get() (builtLifecycle, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.life == nil {
		return nil, managed.ErrUnavailable
	}

	return l.life, nil
}

func (l *lateLifecycle) Describe(rec store.ManagedEnvironment) (managed.Status, bool) {
	life, err := l.get()
	if err != nil {
		return managed.Status{}, false
	}

	return life.Describe(rec), true
}

func (l *lateLifecycle) SetWindow(ctx context.Context, who auth.Scope, name string, w store.MaintenanceWindow) error {
	life, err := l.get()
	if err != nil {
		return err
	}

	return life.SetWindow(ctx, who, name, w)
}

func (l *lateLifecycle) Defer(ctx context.Context, who auth.Scope, name string, days int) error {
	life, err := l.get()
	if err != nil {
		return err
	}

	return life.Defer(ctx, who, name, days)
}

func (l *lateLifecycle) UpgradeNow(ctx context.Context, who auth.Scope, name string) (store.ProvisioningRun, error) {
	life, err := l.get()
	if err != nil {
		return store.ProvisioningRun{}, err
	}

	return life.UpgradeNow(ctx, who, name)
}

func (l *lateLifecycle) PreviewUpdate(ctx context.Context, who auth.Scope, name string, in managed.InfraUpdate) (managed.Verdict, error) {
	life, err := l.get()
	if err != nil {
		return managed.Verdict{}, err
	}

	return life.PreviewUpdate(ctx, who, name, in)
}

func (l *lateLifecycle) UpdateInfrastructure(ctx context.Context, who auth.Scope, name string, in managed.InfraUpdate) (store.ProvisioningRun, error) {
	life, err := l.get()
	if err != nil {
		return store.ProvisioningRun{}, err
	}

	return life.UpdateInfrastructure(ctx, who, name, in)
}

func (l *lateLifecycle) Rebuild(ctx context.Context, who auth.Scope, name string) (store.ProvisioningRun, error) {
	life, err := l.get()
	if err != nil {
		return store.ProvisioningRun{}, err
	}

	return life.Rebuild(ctx, who, name)
}

// runBook is the engine that starts and retries runs together with the store
// that reads them.
type runBook struct {
	engine *provision.Engine
	reads  *store.Provisioning
}

func (b runBook) Start(ctx context.Context, team uint, environment, kind string) (store.ProvisioningRun, error) {
	return b.engine.Start(ctx, team, environment, kind)
}

func (b runBook) Retry(ctx context.Context, team uint, environment string) error {
	return b.engine.Retry(ctx, team, environment)
}

func (b runBook) Latest(ctx context.Context, team uint, environment string) (store.ProvisioningRun, error) {
	return b.reads.Latest(ctx, team, environment)
}

func (b runBook) Steps(ctx context.Context, runID uint) ([]store.ProvisioningStep, error) {
	return b.reads.Steps(ctx, runID)
}

// lateAbandoner ends an environment's runs once the database has connected.
type lateAbandoner struct{ db *store.Starter }

func (l lateAbandoner) Abandon(ctx context.Context, team uint, environment, message string, now time.Time) (store.ProvisioningRun, error) {
	st := l.db.Store()
	if st == nil {
		return store.ProvisioningRun{}, store.ErrNotConnected
	}

	return st.Provisioning().Abandon(ctx, team, environment, message, now)
}

// workerOwner names this replica on the leases it holds. Two replicas of one
// host differ by the random part.
func workerOwner() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "adapter"
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)

	return host + "-" + hex.EncodeToString(suffix)
}

// teamHubs resolves the hub service of a Team for the create steps, which run
// on a worker with no request to name one.
func teamHubs(databases *service.Database) func(ctx context.Context, team uint) (managed.TeamHub, error) {
	return func(ctx context.Context, team uint) (managed.TeamHub, error) {
		svc, err := databases.Hub(auth.WithTeam(ctx, team))
		if err != nil {
			return nil, err
		}
		if svc == nil {
			return nil, errors.New("this control plane has no hub")
		}

		return svc, nil
	}
}

// newManagedEnvironments wires managed environments: the create run's steps
// against the customer's AWS account, the environment's Kubernetes API and each
// Team's hub, the worker that claims runs, the sweeper that closes Access
// windows, and the request that starts a run, and the manager that reads and
// changes them afterwards. It returns nil where a managed environment cannot
// be built: no database, or no control plane principal for a customer's role to
// trust.
func newManagedEnvironments(
	ctx context.Context,
	cfg config.Config,
	db *store.Starter,
	cloud *service.CloudConnections,
	databases *service.Database,
	logger *slog.Logger,
) (*managed.Environments, *managed.Manager, error) {
	if db == nil || cloud == nil {
		return nil, nil, nil
	}
	infra, err := aws.NewDefaultInfra(ctx)
	if err != nil {
		return nil, nil, err
	}
	hubSide := managed.NewHubSide(teamHubs(databases), managedGitURL(cfg))
	records := db.ManagedEnvironments()
	// The hub goes first and the record last, so a rerun that finds the record
	// gone knows the hub is clean.
	forget := func(ctx context.Context, team uint, name string) error {
		if err := hubSide.Disconnect(ctx, team, name); err != nil {
			return err
		}

		return records.Remove(ctx, team, name)
	}

	settings, creatorSettings := managedSettings(cfg)
	granted, err := api.CloudTemplateGrants()
	if err != nil {
		return nil, nil, err
	}
	deps := managed.Deps{
		Records:     records,
		Connections: db.CloudConnections(),
		Cloud:       infra,
		Sweeper:     infra,
		Forget:      forget,
		Drainer: managed.NewDrain(func(access managed.KubeAccess) (managed.Cleaner, error) {
			return kube.NewRemote(access.Endpoint, access.CA, access.Token)
		}),
		Cluster: managed.NewEnvironmentAPI(
			func(access managed.KubeAccess) (managed.Remote, error) {
				return kube.NewRemote(access.Endpoint, access.CA, access.Token)
			},
			managed.NewFluxSource(&http.Client{Timeout: 2 * time.Minute}, managed.FluxReleases, managed.PinnedFluxDigests),
		),
		Hub:      hubSide,
		Settings: settings,
		Granted:  granted,
	}

	late, lateLife := &lateRuns{}, &lateLifecycle{}
	go runProvisioning(ctx, cfg, db, deps, late, lateLife, logger)

	remover := managed.NewRemover(records, db.CloudConnections(), late, lateAbandoner{db: db}, forget)
	environments := &managed.Environments{Creator: managed.NewCreator(records, cloud, late, creatorSettings), Remover: remover}

	return environments, managed.NewManager(records, late, remover, lateLife), nil
}

// runProvisioning waits for the database, then works runs and sweeps until the
// process stops. Every replica does both: the lease is the only coordination.
func runProvisioning(ctx context.Context, cfg config.Config, db *store.Starter, deps managed.Deps, late *lateRuns, lateLife *lateLifecycle, logger *slog.Logger) {
	var st *store.Store
	for st = db.Store(); st == nil; st = db.Store() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}

	engine, err := provision.New(st.Provisioning(), []provision.Kind{
		managed.CreateKind(deps), managed.DeleteKind(deps),
		managed.UpgradeKind(deps), managed.KubernetesKind(deps), managed.RebuildKind(deps),
	}, provision.Options{
		Owner: workerOwner(), LeaseTTL: cfg.ProvisionLeaseTTL, MaxAttempts: cfg.ProvisionMaxAttempts,
		WindowMaxAge:    cfg.ProvisionWindowMaxAge,
		ConnectionReady: managed.ConnectionReady(deps),
		CloseWindow:     managed.CloseWindow(deps),
		Logger:          logger,
	})
	if err != nil {
		logger.ErrorContext(ctx, "provisioning is off", slog.String("cause", log.RedactText(err.Error())))

		return
	}
	late.set(runBook{engine: engine, reads: st.Provisioning()})

	paths, err := managed.PinnedUpgrades()
	if err != nil {
		logger.ErrorContext(ctx, "managed upgrades are off", slog.String("cause", log.RedactText(err.Error())))
	} else {
		life := managed.NewLifecycle(deps, st.ManagedEnvironments(), engine, paths, managed.LifecycleOptions{Logger: logger})
		lateLife.set(life)
		go life.Run(ctx, lifecycleUpgradeEvery, lifecycleCheckEvery)
	}

	go func() {
		ticker := time.NewTicker(provisionSweepEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := engine.Sweep(ctx); err != nil {
					logger.WarnContext(ctx, "provisioning sweep failed", slog.String("cause", log.RedactText(err.Error())))
				}
			}
		}
	}()

	if err := engine.Work(ctx); err != nil {
		logger.ErrorContext(ctx, "provisioning worker stopped", slog.String("cause", log.RedactText(err.Error())))
	}
}
