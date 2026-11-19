// Command adapter serves the Percona Database Service HTTP API that the UI talks to.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/percona/pds/adapter/internal/agent"
	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/client/github"
	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/client/pmm"
	"github.com/percona/pds/adapter/internal/client/resend"
	"github.com/percona/pds/adapter/internal/client/tailnet"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/mysql"
	"github.com/percona/pds/adapter/internal/engine/postgresql"
	"github.com/percona/pds/adapter/internal/features"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/mcp"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// version is the release the image was built from. The Dockerfile stamps it
// with -ldflags -X from the release tag; a build without that is "dev".
var version = "dev"

func main() {
	// The organization subcommand is how Percona onboards a customer and mints
	// an invite link. It shares this binary so the chart can run it as a Job in
	// the same image, and it is the only way a password login comes into
	// existence: nothing on the HTTP surface creates one.
	if len(os.Args) > 1 && isOrganizationCommand(os.Args[1]) {
		if err := runOrganization(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "adapter %s: %v\n", os.Args[1], err)
			os.Exit(1)
		}

		return
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "adapter failed to start:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := log.New(os.Stdout, log.FormatFor(cfg.Env.IsProduction()), cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	environment := newKubeClient(logger)
	activity := newActivityService(cfg, environment)

	everestClient, err := newEverestClient(cfg, logger, environment)
	if err != nil {
		return err
	}

	// The database opens before the services that keep their state in it. The
	// connection lands in the background, so this returns at once and every
	// store below is wired against state that reports not connected until it
	// does.
	db := openStore(ctx, cfg, logger)
	if db != nil {
		defer func() {
			if err := db.Close(); err != nil {
				logger.Warn("could not close the database", slog.String("cause", err.Error()))
			}
		}()
	}

	state, err := newHubState(cfg, db, logger)
	if err != nil {
		return err
	}
	fallback := servedTeam(cfg, db)
	// The hub keeps its spokes' usage samples, and a Team whose environments
	// stopped reporting sends no heartbeat to prune on, so the hub sweeps
	// every Team on its own schedule too. An agent keeps none.
	if db != nil && cfg.Role != config.RoleAgent {
		go service.KeepUsageRetention(ctx, db, service.UsageRetentionEvery, time.Now, logger)
	}
	if state != nil {
		if activity == nil {
			activity = service.NewActivity(nil, service.ActivityOptions{})
		}
		book := service.NewDBBook(state, fallback)
		activity.UseBook(book)
		activity.UseActors(book)
	}

	connections := newConnectionsService(cfg, environment, activity, state)
	support := newSupportService(cfg, logger, connections)

	databases, hydrator, err := newDatabaseService(ctx, cfg, activity, environment, everestClient, state, fallback, support, logger)
	if err != nil {
		return err
	}
	if hydrator != nil {
		defer func() { _ = hydrator.Close() }()
	}

	metrics, alerts, err := newPMMServices(cfg, logger)
	if err != nil {
		return err
	}

	// Conversations are kept per Team, resolved from the request on every
	// call, so one process serving several Teams never mixes their chats.
	// The Chat's tools are the registry's, which call the API's own router. The
	// router serves the Chat, so it is handed to the registry once it is built.
	router := &lateHandler{}
	tools := mcp.New(router, logger, version)
	chat := service.NewChat(databases, connections,
		service.WithTools(tools),
		service.WithAccessLimits(accessLimits(db)),
		service.WithChatStore(service.TeamChatStores(state, fallback)))
	// The flag set is read before the services that consult it. It depends on
	// configuration alone, so an agent builds one it never serves.
	flags, err := features.New(surfaceFlags(cfg))
	if err != nil {
		return err
	}
	defer func() { _ = flags.Close(context.Background()) }()

	if cfg.Role == config.RoleAgent {
		opts := agent.Options{
			HubURL:      cfg.HubURL,
			Token:       cfg.HubAgentToken,
			Environment: cfg.EnvironmentName,
			Namespace:   cfg.EverestNamespace,
			Storage:     cfg.EverestBackupStorage,
			Databases:   databases,
			Logger:      logger,
			// The hub points new databases here at this config, so it is
			// only reported when this environment's chart created one.
			MonitoringConfig: cfg.EverestMonitoringConfig,
			MonitoringExists: monitoringExists(everestClient, cfg.EverestNamespace, cfg.EverestMonitoringConfig),
			Monitor:          agentMonitor(metrics, logger),
			Alerts:           alerts,
		}
		if environment != nil {
			opts.Secrets = newSecretReader(environment, cfg.PodNamespace)
			opts.MonitoringAddress = monitoringAddress(environment, cfg.PodNamespace)
			opts.Seed = func(ctx context.Context, namespace, source, backupID, name string) error {
				return postgresql.Seed(ctx, environment, namespace, source, backupID, name)
			}
			opts.Credentials = newUsersSecrets(environment)
			opts.Support = environment
			opts.Cleanup = func(ctx context.Context, namespace, name string) (int, error) {
				return environment.DeleteSecrets(ctx, namespace, postgresql.LeftoverSecrets(name))
			}
			opts.Collect = func(ctx context.Context, namespace string, items []engine.Instance) ([]hub.HeartbeatEvent, []hub.HeartbeatUsage) {
				events, usage, err := environment.SampleDatabases(ctx, namespace, items)
				if err != nil {
					logger.Warn("usage sample failed", slog.String("cause", err.Error()))

					return nil, nil
				}
				outEvents := make([]hub.HeartbeatEvent, 0, len(events))
				for _, event := range events {
					outEvents = append(outEvents, hub.HeartbeatEvent{
						DatabaseID: event.DatabaseID, At: event.At, Type: event.Type,
						Reason: event.Reason, Message: event.Message, Count: event.Count,
					})
				}
				outUsage := make([]hub.HeartbeatUsage, 0, len(usage))
				for _, point := range usage {
					outUsage = append(outUsage, hub.HeartbeatUsage{
						DatabaseID: point.DatabaseID, At: point.At,
						CPUAvg: point.CPUAvg, CPUMax: point.CPUMax,
						MemoryAvg: point.MemoryAvg, MemoryMax: point.MemoryMax,
						StorageBytes: point.StorageBytes, MetricsReason: point.MetricsReason,
					})
				}

				return outEvents, outUsage
			}
		}

		return agent.Serve(ctx, cfg, logger, opts)
	}

	// Login guards the customer API, so it is built here rather than at the
	// top: the agent role above never serves that API, and it has no database
	// to keep sessions in.
	oidcClient := newOIDCClient(ctx, cfg, logger)

	invitations, err := newInvitationService(cfg, db, activity, oidcClient != nil, logger)
	if err != nil {
		return err
	}

	identity, err := newIdentityService(cfg, db, invitations, logger)
	if err != nil {
		return err
	}

	directory := directoryService(identity, db, activity)

	cloudConnections, err := newCloudConnections(ctx, cfg, db, logger)
	if err != nil {
		return err
	}

	managedEnvironments, managedManager, err := newManagedEnvironments(ctx, cfg, db, cloudConnections, databases, logger)
	if err != nil {
		return err
	}

	server := api.NewServer(api.Deps{
		Config:              cfg,
		Version:             version,
		Logger:              logger,
		Auth:                api.NewSessionAuthenticator(identity, cfg.HubOperatorToken),
		Databases:           databases,
		Metrics:             metrics,
		Support:             support,
		Connections:         connections,
		CloudConnections:    cloudConnections,
		ManagedEnvironments: managedEnvironments,
		Managed:             managedManager,
		Chat:                chat,
		Activity:            activity,
		ActivityDetails: service.NewActivityDetails(
			activity,
			newActivityDatabaseReader(databases),
		),
		Features:    flags,
		Store:       readyChecker(storeReadyChecker(db), hubReadyChecker(hydrator)),
		Identity:    identity,
		Directory:   directory,
		OAuth:       newOAuthService(cfg, db, identity, directory, directory, activity),
		MCP:         tools,
		Invitations: invitations,
		OIDC:        oidcClient,
		TokenTeams:  tokenTeamLookup(db),
	})

	router.set(server)

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      180 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		logger.LogAttrs(ctx, slog.LevelInfo, "adapter listening", cfg.LogAttrs()...)

		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		stop()
		logger.Info("shutting down", slog.String("timeout", cfg.ShutdownTimeout.String()))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	logger.Info("stopped")

	return nil
}

// openStore starts connecting to the adapter's own database and reconciling
// its schema. Without DATABASE_URL there is nothing to open, so it returns nil
// and readiness stops asking about a store.
//
// The work runs in the background and the process serves throughout, reporting
// not ready until it lands. On a first install the database is still being
// created when the adapter starts, and a process that exited for that would
// restart into a backoff that grows into minutes.
//
// Migration runs on every replica. The store takes an advisory lock so pods
// that start together take turns rather than racing the same DDL.
func openStore(ctx context.Context, cfg config.Config, logger *slog.Logger) *store.Starter {
	if !cfg.DatabaseConfigured() {
		logger.Warn("DATABASE_URL is unset, the adapter keeps its state outside a database")

		return nil
	}

	// The upgrade from the release before Organizations re-seals values with
	// the state key. A missing or unusable one is reported by newHubState, so
	// it is only left out here.
	var cipher *store.Cipher
	if cfg.StateEncryptionConfigured() {
		cipher, _ = store.NewCipher(cfg.StateEncryptionKey, cfg.StateEncryptionKeysRetired...)
	}

	return store.Start(ctx, store.Options{
		URL: cfg.DatabaseURL, Logger: logger, Organization: cfg.Organization, Cipher: cipher,
	})
}

// storeReadyChecker hands the readiness probe its store. A nil *store.Starter
// inside a non nil interface would make every probe call through a nil
// receiver, so the absent case returns an untyped nil and readiness answers on
// the process alone.
func storeReadyChecker(db *store.Starter) api.ReadyChecker {
	if db == nil {
		return nil
	}

	return db
}

// newCloudConnections builds the cloud connection service and starts its
// hourly check. It is nil without a control plane principal, because no role in
// a customer's account could trust this deployment, and the routes then answer
// 501.
func newCloudConnections(ctx context.Context, cfg config.Config, db *store.Starter, logger *slog.Logger) (*service.CloudConnections, error) {
	if cfg.CloudPrincipalARN == "" || db == nil {
		return nil, nil
	}
	inspector, err := aws.NewDefaultInspector(ctx)
	if err != nil {
		return nil, err
	}
	svc := service.NewCloudConnections(db.CloudConnections(), inspector, service.CloudOptions{
		PrincipalARN: cfg.CloudPrincipalARN, Environments: db.ManagedEnvironments(), Logger: logger,
	})
	go svc.Run(ctx, cloudCheckEvery)

	return svc, nil
}

// cloudCheckEvery is how often every cloud connection is checked.
const cloudCheckEvery = time.Hour

// tokenTeamLookup hands agent routes the unscoped hash lookup once the
// database is connected. A nil starter is an untyped nil so tests and a
// process without a database keep the served Team fallback.
func tokenTeamLookup(db *store.Starter) api.TokenTeamLookup {
	if db == nil {
		return nil
	}

	return db
}

// hubReadyChecker hands the readiness probe the hub's hydration. A pod that
// has not loaded the fleet must not take traffic and answer with an empty one.
// The untyped nil matters for the same reason it does above.
func hubReadyChecker(hydrator *hub.Hydrator) api.ReadyChecker {
	if hydrator == nil {
		return nil
	}

	return hydrator
}

// readyChecker reports the first dependency that is not usable yet. A nil
// member is one this deployment does not have, and all nil is a process that
// answers on itself alone.
func readyChecker(checks ...api.ReadyChecker) api.ReadyChecker {
	present := make([]api.ReadyChecker, 0, len(checks))
	for _, check := range checks {
		if check != nil {
			present = append(present, check)
		}
	}
	if len(present) == 0 {
		return nil
	}

	return readyAll(present)
}

// readyAll is every readiness check this pod has to pass.
type readyAll []api.ReadyChecker

// Ready reports the first check that is not usable yet.
func (r readyAll) Ready(ctx context.Context) error {
	for _, check := range r {
		if err := check.Ready(ctx); err != nil {
			return err
		}
	}

	return nil
}

// newHubState returns the Team scoped database the adapter keeps its state
// in, or nil when none is configured.
//
// A missing or unusable key is fatal rather than a warning. Without one the
// credentials the adapter replays downstream can be neither written nor read,
// and a hub that came up anyway would either sit out of the Service forever,
// because the import cannot seal the clone identities it finds, or come up
// reporting every connection missing and invite a re-paste that could not be
// stored either. Refusing at start says which of the two is wrong once.
func newHubState(cfg config.Config, db *store.Starter, logger *slog.Logger) (*store.Lazy, error) {
	if db == nil {
		return nil, nil
	}

	if !cfg.StateEncryptionConfigured() {
		return nil, errors.New("STATE_ENCRYPTION_KEY is required when DATABASE_URL is set: " +
			"the credentials the adapter replays downstream are sealed with it, and without one " +
			"they can be neither stored nor read")
	}

	cipher, err := store.NewCipher(cfg.StateEncryptionKey, cfg.StateEncryptionKeysRetired...)
	if err != nil {
		return nil, fmt.Errorf("state encryption key: %w", err)
	}

	logger.Info("hub state is stored in the database",
		slog.String("organization", organizationLabel(cfg)),
		slog.String("encryption_key", cipher.KeyID()))

	return store.NewLazy(db, 0, cipher), nil
}

// oidcDiscoveryTimeout bounds the issuer lookup at start, so an issuer that
// hangs delays the start by this much and no more.
const oidcDiscoveryTimeout = 10 * time.Second

// newOIDCClient connects to the configured issuer. OIDC_ISSUER empty returns
// nil: only the password login is on offer. A failure also returns nil, with
// a logged error that carries no credential, because an issuer that is down
// or misconfigured must not lock customers out of the password login.
func newOIDCClient(ctx context.Context, cfg config.Config, logger *slog.Logger) *auth.OIDCClient {
	if cfg.OIDCIssuer == "" {
		return nil
	}
	if cfg.HubPublicURL == "" {
		logger.Error("OIDC sign in is off, only the password login is available",
			slog.String("issuer", cfg.OIDCIssuer),
			slog.String("reason", "HUB_PUBLIC_URL is required to build the redirect address"))

		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, oidcDiscoveryTimeout)
	defer cancel()

	client, err := auth.NewOIDCClient(ctx, auth.OIDCConfig{
		Issuer:       cfg.OIDCIssuer,
		ClientID:     cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSecret,
		Scopes:       cfg.OIDCScopes,
	})
	if err != nil {
		logger.Error("OIDC sign in is off, only the password login is available",
			slog.String("issuer", cfg.OIDCIssuer), slog.Any("err", err))

		return nil
	}

	logger.Info("OIDC sign in is on beside the password login", slog.String("issuer", cfg.OIDCIssuer))

	return client
}

// newIdentityService builds customer login over the adapter's own database.
//
// A deployment with no DATABASE_URL is refused rather than served open: the
// sessions and password hashes live in rows, so without a database there is
// nothing to check a request against, and the alternative is every route
// answering to anybody.
func newIdentityService(
	cfg config.Config,
	db *store.Starter,
	invitations *service.Invitations,
	logger *slog.Logger,
) (*service.Identity, error) {
	if db == nil {
		return nil, errors.New("DATABASE_URL is required: customer login keeps its sessions and " +
			"password hashes in the adapter database, and without one every route would be open")
	}

	logger.Info("customer login is required",
		slog.String("organization", organizationLabel(cfg)),
		slog.Duration("session_lifetime", cfg.SessionLifetime),
		slog.Duration("session_idle", cfg.SessionIdle),
		slog.Bool("signup_enabled", cfg.SignupEnabled))

	opts := []service.IdentityOption{
		service.WithServedOrganization(cfg.OrganizationName()),
		service.WithSessionLifetime(cfg.SessionLifetime),
		service.WithSessionIdle(cfg.SessionIdle),
		service.WithInviteTTL(cfg.InviteTTL),
		service.WithSignup(service.Signup{
			Enabled:             cfg.SignupEnabled,
			OrganizationCeiling: cfg.SignupOrganizationCeiling,
		}),
	}
	// A nil service in the option would be a non-nil interface holding nil.
	if invitations != nil {
		opts = append(opts, service.WithInvitationClaims(invitations))
	}

	return service.NewIdentity(store.NewLazyIdentity(db), opts...), nil
}

// newInvitationService builds how people join an Organization. It is nil
// without a database, where there is no login either. Email goes out only when
// both the Resend key and a sender are set; otherwise the admin shares the
// link. A password may claim an invitation only where no issuer verifies the
// address and the deployment serves every Organization.
func newInvitationService(
	cfg config.Config,
	db *store.Starter,
	activity *service.Activity,
	issuerOn bool,
	logger *slog.Logger,
) (*service.Invitations, error) {
	if db == nil {
		return nil, nil
	}

	opts := []service.InvitationOption{
		service.WithInvitationLinks(cfg.HubPublicURL),
		service.WithInvitationTTL(cfg.InvitationTTL),
		service.WithInvitationRecorder(activity),
		service.WithInvitationPasswords(!issuerOn && cfg.ServesEveryOrganization()),
	}
	if cfg.ResendAPIKey != "" && cfg.InvitationFrom != "" {
		mailer, err := resend.New(resend.Options{APIKey: cfg.ResendAPIKey})
		if err != nil {
			return nil, fmt.Errorf("invitation email: %w", err)
		}
		opts = append(opts, service.WithInvitationMailer(mailer, cfg.InvitationFrom))
	} else {
		logger.Info("invitation email is not configured, admins share the invitation link")
	}

	return service.NewInvitations(store.NewLazyIdentity(db), opts...), nil
}

// newEverestClient builds the shared OpenEverest client. Without EVEREST_URL
// there is nothing to talk to, so it returns nil. The HTTP role does not use
// this client for database routes.
//
// environment is what the token source reads the Everest admin account with. A nil
// one leaves the adapter on the configured token, which is a laptop run.
func newEverestClient(
	cfg config.Config,
	logger *slog.Logger,
	environment *kube.Client,
) (*everest.Client, error) {
	if cfg.EverestURL == "" {
		if cfg.Role == config.RoleAgent {
			logger.Warn("EVEREST_URL is unset, the agent has no database platform to call")
		}

		return nil, nil
	}

	// The minting client carries no token source, which keeps the wiring one
	// direction: this one logs in, the one below sends the result. Session
	// bypasses the source on any client, so this is for legibility rather than
	// to avoid a cycle.
	minter, err := everest.New(everest.Options{BaseURL: cfg.EverestURL})
	if err != nil {
		return nil, fmt.Errorf("everest client: %w", err)
	}

	var accounts service.SecretReader
	if environment != nil {
		accounts = environment
	} else {
		logger.Warn("no Kubernetes access, the Everest token will not be renewed when it expires")
	}

	tokens := service.NewEverestToken(accounts, minter.Session, service.EverestTokenOptions{
		Namespace: cfg.ConnectionsNamespace(),
		Secret:    cfg.EverestAccountsSecret,
		Static:    cfg.EverestToken,
	})

	client, err := everest.New(everest.Options{
		BaseURL:     cfg.EverestURL,
		Cluster:     cfg.EverestCluster,
		TokenSource: tokens.Bearer,
	})
	if err != nil {
		return nil, fmt.Errorf("everest client: %w", err)
	}

	return client, nil
}

// newDatabaseService builds the database service for this role.
//
// The HTTP role builds the hub service. The agent builds the engine plugin
// registry and has no Git backend, so Hub() stays nil and its calls take the
// local branch. A nil result is an agent with no OpenEverest to call.
func newDatabaseService(
	ctx context.Context,
	cfg config.Config,
	activity *service.Activity,
	environment *kube.Client,
	client *everest.Client,
	state *store.Lazy,
	fallback service.TeamFallback,
	support *service.Support,
	logger *slog.Logger,
) (*service.Database, *hub.Hydrator, error) {
	if cfg.Role == config.RoleAgent {
		if client == nil {
			return nil, nil, nil
		}

		return service.NewDatabase(engineRegistry(cfg, client), service.WithRecorder(activity)), nil, nil
	}

	hubReg, hydrator, err := newHubRegistry(ctx, cfg, environment, state, fallback, activity, support, logger)
	if err != nil {
		return nil, nil, err
	}

	return service.NewDatabase(engine.NewRegistry(),
		service.WithRecorder(activity),
		service.WithHubFor(func(ctx context.Context) (*hub.Service, error) {
			return hubForTeam(ctx, fallback, hubReg)
		}),
	), hydrator, nil
}

// engineRegistry is the OpenEverest plugins the agent calls on this environment.
func engineRegistry(cfg config.Config, client *everest.Client) *engine.Registry {
	registry := engine.NewRegistry()
	registry.Register(mysql.New(
		client,
		mysql.WithNamespace(cfg.EverestNamespace),
		mysql.WithBackupStorage(cfg.EverestBackupStorage),
	))
	registry.Register(postgresql.New(
		client,
		postgresql.WithNamespace(cfg.EverestNamespace),
		postgresql.WithBackupStorage(cfg.EverestBackupStorage),
	))

	return registry
}

// errHubTeamRequired is a signed-in request that still has no Team, or an
// agent call on a deployment that serves every Organization before a token can
// name one.
var errHubTeamRequired = errors.New("a team is required")

func hubForTeam(ctx context.Context, fallback service.TeamFallback, reg *hub.Registry) (*hub.Service, error) {
	team, ok := auth.TeamFromContext(ctx)
	if !ok && fallback != nil {
		team = fallback(ctx)
	}
	if team == 0 {
		return nil, errHubTeamRequired
	}

	return reg.Get(ctx, team)
}

// noDatabaseTeam is the one Team a deployment with no database has. Without
// accounts there is nobody to tell Teams apart, so its state is kept under this
// id and the Organization it serves is the one that was configured.
const noDatabaseTeam uint = 1

// servedTeam is the Team a call that names none acts in on a deployment that
// serves one Organization: the oldest Team of that Organization, or the only
// one when there is no database. A deployment that serves every Organization
// has none, and its callers must name a Team.
//
// The Team is looked up once the database answers and then kept. Before that,
// and when the Organization has no Team yet, it is zero and the caller refuses.
func servedTeam(cfg config.Config, db *store.Starter) service.TeamFallback {
	if cfg.ServesEveryOrganization() {
		return nil
	}
	if db == nil {
		return func(context.Context) uint { return noDatabaseTeam }
	}

	var mu sync.Mutex
	var found uint

	return func(ctx context.Context) uint {
		mu.Lock()
		defer mu.Unlock()
		if found != 0 {
			return found
		}
		st := db.Store()
		if st == nil {
			return 0
		}
		team, err := st.Identity().ServedTeam(ctx, cfg.OrganizationName())
		if err != nil {
			return 0
		}
		found = team.ID

		return found
	}
}

// organizationLabel is the Organization a log line names. A star reads the same
// in a log as a name nobody set, so say which one it is.
func organizationLabel(cfg config.Config) string {
	if cfg.ServesEveryOrganization() {
		return "every organization"
	}

	return cfg.OrganizationName()
}

func newGitHubClient(cfg config.Config) (*github.Client, error) {
	if !cfg.HubGitHubAppConfigured() {
		return github.New(cfg.HubGitHubOrg, cfg.HubGitHubToken), nil
	}
	key, err := github.ParsePrivateKey([]byte(cfg.HubGitHubAppPrivateKey))
	if err != nil {
		return nil, err
	}

	return github.NewApp(cfg.HubGitHubOrg, cfg.HubGitHubAppID, cfg.HubGitHubAppInstallationID, key), nil
}

// newHubRegistry builds the Teams' hub services. ctx is the process's own:
// it outlives any request, and ends when the process is asked to stop.
func newHubRegistry(
	ctx context.Context,
	cfg config.Config,
	environment *kube.Client,
	state *store.Lazy,
	fallback service.TeamFallback,
	activity *service.Activity,
	support *service.Support,
	logger *slog.Logger,
) (*hub.Registry, *hub.Hydrator, error) {
	var gh *github.Client
	if cfg.HubGitHubConfigured() {
		var err error
		gh, err = newGitHubClient(cfg)
		if err != nil {
			return nil, nil, err
		}
	}

	if err := startStraySupportSweeper(ctx, cfg, state); err != nil {
		return nil, nil, err
	}

	reg := hub.NewRegistry(func(reqCtx context.Context, team uint) (*hub.Service, error) {
		return buildHubService(reqCtx, ctx, cfg, environment, state, activity, support, logger, gh, team)
	})

	if cfg.ServesEveryOrganization() {
		return reg, nil, nil
	}

	if state == nil {
		if _, err := reg.Get(context.Background(), noDatabaseTeam); err != nil {
			return nil, nil, fmt.Errorf("hub state: %w", err)
		}

		return reg, nil, nil
	}

	// The connection lands in the background, so the load does too.
	// Readiness is red until it finishes, which is what keeps an empty
	// fleet from being served as an answer.
	hydrator := hub.StartHydration(context.Background(), func(ctx context.Context) error {
		if _, err := state.Wait(ctx); err != nil {
			return err
		}
		team := fallback(ctx)
		if team == 0 {
			return errHubTeamRequired
		}
		_, err := reg.Get(ctx, team)

		return err
	}, hub.Hydration{}, logger)

	return reg, hydrator, nil
}

func buildHubService(
	ctx context.Context,
	processCtx context.Context,
	cfg config.Config,
	environment *kube.Client,
	state *store.Lazy,
	activity *service.Activity,
	support *service.Support,
	logger *slog.Logger,
	gh *github.Client,
	team uint,
) (*hub.Service, error) {
	prefix := cfg.ServesEveryOrganization()
	var gitStore hub.Store
	var gitHost hub.GitHost
	if gh != nil {
		files := hub.NewTeamGit(gh, team)
		files.NameNewRepos(repoPrefixOf(state, team))
		if state != nil {
			files.UseRepoClaims(state)
		}
		gitStore = hub.NewGitHubStore(files)
		gitHost = files
	} else {
		root := hub.GitDir(cfg.HubGitDir, team, prefix)
		dirStore, err := hub.NewDirStore(root)
		if err != nil {
			return nil, fmt.Errorf("hub git dir: %w", err)
		}
		gitStore = dirStore
	}

	inv := hub.NewInventory(cfg.HeartbeatStale)
	if files, ok := gitHost.(*hub.TeamGit); ok {
		files.UseRecordedRepos(inv.Repo)
	}
	persists := hubPersists(cfg, environment, state, team)
	tokens := hub.NewTokenStore(persists.Tokens, cfg.JoinTokenTTL)
	inv.SetPersist(persists.Inventory)

	if cfg.HubGitDir != "" {
		if err := hub.LoadInventory(hub.GitDir(cfg.HubGitDir, team, prefix), inv); err != nil {
			return nil, fmt.Errorf("hub inventory: %w", err)
		}
	}

	supportTailnet, err := newSupportTailnet(cfg)
	if err != nil {
		return nil, err
	}

	hubSvc := hub.NewService(gitStore, inv, hub.Options{
		Tailnet:          supportTailnet,
		Namespace:        cfg.EverestNamespace,
		BackupStorage:    cfg.EverestBackupStorage,
		Tokens:           tokens,
		GitHost:          gitHost,
		CloneKeys:        persists.CloneKeys,
		SupportAccess:    persists.SupportAccess,
		Context:          processCtx,
		OnSupportExpired: supportExpired(activity, support, team),
	})

	if err := (hub.Hydration{
		Inventory: inv,
		Tokens:    tokens,
		Service:   hubSvc,
		Work:      persists.Work,
	}).Run(ctx); err != nil {
		return nil, err
	}

	// The reconcile reads and commits to Git for every environment, which a
	// request waiting on this service must not wait for.
	go hubSvc.ReconcileAlertRules(processCtx)
	go hubSvc.RunSupportSweeper(processCtx)

	return hubSvc, nil
}

// supportExpired records that the sweep closed a window and tells its case. No
// User caused it, so the entry's actor is the system.
func supportExpired(
	activity *service.Activity,
	support *service.Support,
	team uint,
) func(context.Context, string, hub.SupportWindow) {
	return func(ctx context.Context, environment string, window hub.SupportWindow) {
		ctx = auth.WithTeam(ctx, team)
		activity.Record(ctx, service.ActivityEvent{
			Action:      service.ActionSupportAccessExpire,
			Actor:       service.ActorSystem,
			Environment: environment,
			Target:      environment,
			Outcome:     service.OutcomeSucceeded,
			Detail:      "Access expired and was closed.",
		})
		support.NoteAccessExpired(ctx, service.SupportAccessNote{
			CaseReference: window.CaseReference,
			Environment:   environment,
		})
	}
}

// repoPrefixOf names the Organization and Team of a Team's new repositories. A
// deployment with no database, or a Team no Organization owns yet, has neither,
// and its environments are named after the environment alone.
func repoPrefixOf(state *store.Lazy, team uint) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if state == nil {
			return "", nil
		}
		organization, team, err := state.For(team).Owner(ctx)
		if errors.Is(err, store.ErrTeamUnknown) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("read the organization and team: %w", err)
		}

		return hub.RepoPrefix(organization, team), nil
	}
}

// surfaceFlags is the operator's flags with support access turned off on a hub
// without the private network: a window could never open there, so the console
// must not offer one. The operator's map is left as it was.
func surfaceFlags(cfg config.Config) map[string]bool {
	values := maps.Clone(cfg.Features)
	if values == nil {
		values = map[string]bool{}
	}
	if !cfg.SupportTailnet.Configured() {
		values[features.SupportAccess] = false
	}

	return values
}

// newSupportTailnet builds the client for the private network support access
// runs over. It returns nil, not a client that fails, when the hub has no
// credentials for it: the interface must then be nil so the hub makes no call.
func newSupportTailnet(cfg config.Config) (hub.Tailnet, error) {
	n := cfg.SupportTailnet
	if !n.Configured() {
		return nil, nil //nolint:nilnil // no network configured is a state, not a failure
	}
	client, err := tailnet.New(tailnet.Options{
		BaseURL:             n.URL,
		Tailnet:             n.Name,
		Domain:              n.Domain,
		KeyClientID:         n.KeyClientID,
		KeyClientSecret:     n.KeyClientSecret,
		ServiceClientID:     n.ServiceClientID,
		ServiceClientSecret: n.ServiceClientSecret,
		EnvironmentTag:      n.EnvironmentTag,
		ServiceTag:          n.ServiceTag,
	})
	if err != nil {
		return nil, fmt.Errorf("support access network: %w", err)
	}

	return client, nil
}

// startStraySupportSweeper deletes network devices and addresses no Team's
// records account for. It needs the database, because the records of every Team
// are what tells a stray from a live one, and the network credentials.
func startStraySupportSweeper(ctx context.Context, cfg config.Config, state *store.Lazy) error {
	if state == nil {
		return nil
	}
	tn, err := newSupportTailnet(cfg)
	if err != nil || tn == nil {
		return err
	}
	go hub.NewStraySupportSweeper(tn, hub.NewDBSupportCensus(state), time.Now).Run(ctx)

	return nil
}

// hubPersists picks where the hub keeps its state. With a database the rows are
// the store. Without one the ConfigMaps and Secrets are, which is what a laptop
// run has. A deployment that serves every Organization and has no database
// keeps state in memory: one ConfigMap cannot be shared.
func hubPersists(cfg config.Config, environment *kube.Client, state *store.Lazy, team uint) hub.Persists {
	if state != nil {
		pinned := state.For(team)

		return hub.Persists{
			Inventory:     hub.NewDBInventoryPersist(pinned),
			Tokens:        hub.NewDBTokenPersist(pinned),
			Work:          hub.NewDBWorkPersist(pinned),
			CloneKeys:     hub.NewDBCloneKeyPersist(pinned),
			SupportAccess: hub.NewDBSupportPersist(pinned),
		}
	}

	if environment == nil || cfg.ServesEveryOrganization() {
		return hub.Persists{}
	}

	ns := cfg.ConnectionsNamespace()

	return hub.Persists{
		Inventory: hub.NewConfigMapInventoryPersist(environment, ns, cfg.HubInventoryConfigMap()),
		Tokens:    hub.NewSecretTokenPersist(environment, ns, cfg.HubJoinSecret()),
		Work:      hub.NewConfigMapWorkPersist(environment, ns, cfg.HubWorkConfigMap()),
		CloneKeys: hub.NewSecretCloneKeyPersist(environment, ns, cfg.HubJoinSecret()),
	}
}

// newActivityService builds the activity ring. Without Kubernetes access it keeps
// the ring in memory, so a laptop run still has a feed; only the in-cluster
// path survives a restart. A star pin has no ring: one ConfigMap cannot be
// shared across Organizations.
func newActivityService(cfg config.Config, client *kube.Client) *service.Activity {
	if cfg.ServesEveryOrganization() {
		return nil
	}

	var store service.ConfigMapStore
	if client != nil {
		store = client
	}

	return service.NewActivity(store, service.ActivityOptions{
		Namespace: cfg.ConnectionsNamespace(),
		ConfigMap: cfg.ActivityConfigMap(),
	})
}

// newActivityDatabaseReader hands the activity detail its read path. A nil
// *service.Database inside a non-nil interface would answer every read with an
// engine error, so the absent case returns an untyped nil and the detail says
// the database could not be followed instead.
func newActivityDatabaseReader(databases *service.Database) service.ActivityDatabaseReader {
	if databases == nil {
		return nil
	}

	return databases
}

// monitoringExists asks the database platform whether the MonitoringConfig is
// there. The chart's hook creates it after the agent has started, and a
// environment where the hook failed never gets one. Only a 404 says it is not
// there; any other failure is returned so the agent keeps what it last
// confirmed. Without a platform client nothing can be confirmed.
func monitoringExists(client *everest.Client, namespace, name string) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		if client == nil || name == "" {
			return false, nil
		}
		_, err := client.GetMonitoringConfig(ctx, namespace, name)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, everest.ErrNotFound):
			return false, nil
		default:
			return false, err
		}
	}
}

// agentMonitor reads the local PMM for one database on each usage sample. A
// PMM that answers with an error is logged and sent as not serving readings,
// so the hub shows a pending chart instead of the last reading as current.
func agentMonitor(metrics *service.Metrics, logger *slog.Logger) func(context.Context, engine.Instance) *hub.HeartbeatMonitoring {
	return func(ctx context.Context, item engine.Instance) *hub.HeartbeatMonitoring {
		reading, err := metrics.Latest(ctx, item.Name, item.Namespace, item.Engine)
		if errors.Is(err, service.ErrInvalidMetricTarget) {
			// A name PMM cannot be asked about stays that way, so this
			// sample goes without a reading instead of claiming an outage.
			return nil
		}
		if err != nil {
			logger.WarnContext(ctx, "monitoring read failed",
				slog.String("database", item.Name), slog.String("cause", err.Error()))

			return &hub.HeartbeatMonitoring{Pending: service.MonitoringUnavailable}
		}
		out := hub.HeartbeatMonitoring(*reading)

		return &out
	}
}

// newPMMServices builds the metrics service and the alert reader. The agent
// reads its own monitoring through the PMM client. The HTTP role charts the
// samples environments post, so it keeps the service and no client. Without
// PMM_URL the alert reader is nil, which the heartbeat reports as no
// monitoring.
//
// The range querier is held in an interface variable that stays nil when
// there is nothing to assign. Handing a typed nil pointer to an interface
// parameter produces a non nil interface, and the pending path would then
// call through it.
func newPMMServices(cfg config.Config, logger *slog.Logger) (*service.Metrics, func(context.Context) (pmm.AlertList, error), error) {
	if cfg.Role != config.RoleAgent {
		return service.NewMetrics(nil), nil, nil
	}

	var ranges service.RangeQuerier
	var alerts func(context.Context) (pmm.AlertList, error)
	if cfg.PMMURL == "" {
		logger.Warn("PMM_URL is unset, metrics will answer pending")
	} else {
		client, err := pmm.New(pmm.Options{
			BaseURL: cfg.PMMURL,
			Token:   cfg.PMMToken,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("pmm client: %w", err)
		}

		ranges = client
		alerts = client.Alerts
	}

	return service.NewMetrics(ranges), alerts, nil
}

// newKubeClient builds the in-cluster Kubernetes client used by event sampling
// and by the connections service. Outside an environment it returns nil, which both
// callers already treat as a supported local run.
func newKubeClient(logger *slog.Logger) *kube.Client {
	client, err := kube.NewInCluster()
	if err != nil {
		logger.Warn("no Kubernetes access, event sampling is off and credentials stay in env",
			slog.String("reason", err.Error()))

		return nil
	}

	return client
}

// newConnectionsService stores UI-pasted ServiceNow and Anthropic credentials.
// A nil store falls back to env for GET and refuses PUT.
func newConnectionsService(
	cfg config.Config,
	client *kube.Client,
	activity *service.Activity,
	state *store.Lazy,
) *service.Connections {
	var secrets service.SecretStore
	if client != nil {
		secrets = client
	}

	// With a database the credentials live in rows, sealed. Without one they
	// stay in the Secrets they have always been in.
	if state != nil {
		secrets = service.NewVault(func(ctx context.Context) service.ConnectionState {
			team, _ := auth.TeamFromContext(ctx)
			if resolved := state.For(team).State(); resolved != nil {
				return resolved
			}

			return nil
		})
	}

	return service.NewConnections(secrets, service.ConnectionsOptions{
		Namespace:                cfg.ConnectionsNamespace(),
		ServiceNowSecretName:     cfg.ServiceNowSecret(),
		AssistantSecretName:      cfg.AssistantSecret(),
		CursorSecretName:         cfg.CursorSecret(),
		EnvServiceNowURL:         cfg.ServiceNowURL,
		EnvServiceNowAPIKey:      cfg.ServiceNowAPIKey,
		EnvServiceNowClientToken: cfg.ServiceNowClientToken,
	}, service.WithConnectionsRecorder(activity))
}

// newSupportService builds the support service on top of live credentials.
// Without tokens the routes still answer 501 until a platform engineer pastes
// them in the connections panel.
func newSupportService(cfg config.Config, logger *slog.Logger, source service.ServiceNowSource) *service.Support {
	if cfg.ServiceNowURL == "" {
		logger.Warn("no ServiceNow URL in env, support is not provisioned")
	} else if cfg.ServiceNowAPIKey == "" {
		logger.Warn("no ServiceNow API key in env, support is not provisioned")
	} else if !cfg.ServiceNowConfigured() {
		logger.Warn("no ServiceNow client token in env, support waits for a paste in the connections panel")
	}

	opts := []service.SupportOption{
		service.WithServiceNowSource(source),
		service.WithSupportGuideURL(cfg.SupportGuideURL),
	}
	if cfg.ServiceNowURL != "" {
		opts = append(opts, service.WithSupportPortalURL(cfg.ServiceNowURL+"/percona"))
	}

	return service.NewSupport(nil, opts...)
}

// accessLimits reads the Organization's AI settings that cap the Chat's tools.
// Without a database every Organization stays at read.
func accessLimits(db *store.Starter) service.AccessLimits {
	if db == nil {
		return nil
	}

	return service.NewDirectory(store.NewLazyIdentity(db), nil)
}

// directoryService is the Organization directory, present wherever login is.
func directoryService(identity *service.Identity, db *store.Starter, activity *service.Activity) *service.Directory {
	if identity == nil {
		return nil
	}

	return service.NewDirectory(store.NewLazyIdentity(db), activity)
}
