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

// managedSettings turns the deployment's configuration into what the create
// steps and the request that starts them depend on.
func managedSettings(cfg config.Config) (managed.Settings, managed.CreatorSettings) {
	repository, tag := splitImage(cfg.HubAgentImage)

	return managed.Settings{
		OutgoingCIDRs: cfg.ManagedOutgoingCIDRs, FluxVersion: managed.FluxVersion, ChartVersion: cfg.ManagedChartVersion,
		HubURL: cfg.HubPublicURL, ImageRepository: repository, ImageTag: tag,
	}, managed.CreatorSettings{
		OutgoingCIDRs: cfg.ManagedOutgoingCIDRs, ChartVersion: cfg.ManagedChartVersion, ImageRepository: repository,
	}
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

// lateRuns starts provisioning runs once the database has connected. The
// connection lands in the background, and a request before it has no run to
// queue.
type lateRuns struct {
	mu   sync.RWMutex
	runs managed.Runs
}

func (l *lateRuns) set(runs managed.Runs) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.runs = runs
}

func (l *lateRuns) Start(ctx context.Context, team uint, environment, kind string) (store.ProvisioningRun, error) {
	l.mu.RLock()
	runs := l.runs
	l.mu.RUnlock()
	if runs == nil {
		return store.ProvisioningRun{}, store.ErrNotConnected
	}

	return runs.Start(ctx, team, environment, kind)
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
// windows, and the request that starts a run. It returns nil where a managed
// environment cannot be built: no database, or no control plane principal for a
// customer's role to trust.
func newManagedEnvironments(
	ctx context.Context,
	cfg config.Config,
	db *store.Starter,
	cloud *service.CloudConnections,
	databases *service.Database,
	logger *slog.Logger,
) (*managed.Creator, error) {
	if db == nil || cloud == nil {
		return nil, nil
	}
	infra, err := aws.NewDefaultInfra(ctx)
	if err != nil {
		return nil, err
	}

	settings, creatorSettings := managedSettings(cfg)
	deps := managed.Deps{
		Records:     db.ManagedEnvironments(),
		Connections: db.CloudConnections(),
		Cloud:       infra,
		Cluster: managed.NewEnvironmentAPI(
			func(access managed.KubeAccess) (managed.Remote, error) {
				return kube.NewRemote(access.Endpoint, access.CA, access.Token)
			},
			managed.NewFluxSource(&http.Client{Timeout: 2 * time.Minute}, managed.FluxReleases, managed.PinnedFluxDigests),
		),
		Hub:      managed.NewHubSide(teamHubs(databases), managedGitURL(cfg)),
		Settings: settings,
	}

	late := &lateRuns{}
	go runProvisioning(ctx, cfg, db, deps, late, logger)

	return managed.NewCreator(db.ManagedEnvironments(), cloud, late, creatorSettings), nil
}

// runProvisioning waits for the database, then works runs and sweeps until the
// process stops. Every replica does both: the lease is the only coordination.
func runProvisioning(ctx context.Context, cfg config.Config, db *store.Starter, deps managed.Deps, late *lateRuns, logger *slog.Logger) {
	var st *store.Store
	for st = db.Store(); st == nil; st = db.Store() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}

	engine, err := provision.New(st.Provisioning(), []provision.Kind{managed.CreateKind(deps)}, provision.Options{
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
	late.set(engine)

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
