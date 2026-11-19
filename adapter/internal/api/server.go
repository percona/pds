package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/features"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/mcp"
	"github.com/percona/pds/adapter/internal/service"
)

// Deps are everything the HTTP layer needs. Tests inject fakes through the
// same struct.
type Deps struct {
	Config config.Config
	Logger *slog.Logger
	Auth   auth.Authenticator

	// Version is the control plane release this binary was built from. The
	// session route reports it to signed-in callers only, never /healthz.
	Version string

	// Databases is wired on every start. A nil value panics in the
	// database handlers rather than answering 501.
	Databases *service.Database

	// Metrics is nil when the adapter has no PMM connection, and the metrics
	// route then answers pending rather than an error.
	Metrics *service.Metrics

	// Support is nil when the adapter has no ServiceNow Secret, and the
	// support routes then answer 501 so the UI hides its actions.
	Support *service.Support

	// CloudConnections is nil where no control plane principal is configured,
	// and its routes then answer 501.
	CloudConnections *service.CloudConnections
	// ManagedEnvironments starts the creation and the deletion of a managed
	// environment. It is nil where this deployment cannot build one.
	ManagedEnvironments *managed.Environments

	// Connections stores UI-pasted ServiceNow and Anthropic credentials.
	Connections *service.Connections

	// Chat runs environment chat. Provider keys are checked per request in Reply.
	Chat *service.Chat

	// ChatHeartbeat is how often a streamed chat turn writes a comment while
	// the model is still working, so no proxy sees an idle connection. Zero
	// means the default.
	ChatHeartbeat time.Duration

	// Activity records control plane mutations. It is nil only when the
	// adapter was built without one, and the route then answers 501.
	Activity *service.Activity

	// ActivityDetails serves one activity row expanded into a timeline. It is
	// nil whenever Activity is.
	ActivityDetails *service.ActivityDetails

	// Features says which product surfaces this adapter serves. A nil set is
	// the whole product, which is what an adapter configured with no flags
	// has always served.
	Features *features.Set

	// Store is the adapter's own database. It is nil when none is configured,
	// and readiness then reports only that the process can serve HTTP.
	Store ReadyChecker

	// Identity is customer login. It is nil only where there is no database to
	// keep sessions in, and the login routes then answer 501.
	Identity *service.Identity

	// Directory is what an Organization admin changes: Teams and Users. It is
	// nil where there is no database, and its routes then answer 501.
	Directory *service.Directory

	// Invitations is how people join an Organization. It is nil where there is
	// no database, and its routes then answer 501.
	Invitations *service.Invitations

	// OIDC signs customers in through an identity provider. It is nil when
	// OIDC_ISSUER is empty or discovery failed at start, and the password
	// login is then the only way in.
	OIDC *auth.OIDCClient

	// OAuth is the authorization server agents sign in through. It is nil where
	// there is no database or no public address to be an issuer at, and its
	// endpoints then answer 404.
	OAuth *service.OAuth

	// MCP is the registry /mcp serves to agents signed in through OAuth. It is
	// nil where there is none, and /mcp then answers 404.
	MCP *mcp.Registry

	// TokenTeams looks up the Team that minted an agent bearer. It is
	// nil in tests that pin one WithHub Service, and those requests keep
	// the served Team fallback.
	TokenTeams TokenTeamLookup
}

// TokenTeamLookup returns the Team that minted a token hash. The hash is
// already SHA-256 hex; the lookup must not be team scoped.
type TokenTeamLookup interface {
	TokenTeam(ctx context.Context, hash string) (uint, error)
}

// ReadyChecker reports whether a dependency this pod cannot serve without is
// usable. The store is the only one: OpenEverest, PMM, and ServiceNow are
// deliberately excluded, because taking the pod out of the Service on their
// outage would hide the UI that explains the outage.
type ReadyChecker interface {
	Ready(ctx context.Context) error
}

// Server routes and serves the adapter API.
type Server struct {
	cfg             config.Config
	version         string
	logger          *slog.Logger
	router          chi.Router
	validate        *validator.Validate
	databases       *service.Database
	metrics         *service.Metrics
	support         *service.Support
	connections     *service.Connections
	cloud           *service.CloudConnections
	chat            *service.Chat
	chatHeartbeat   time.Duration
	activity        *service.Activity
	activityDetails *service.ActivityDetails
	features        *features.Set
	store           ReadyChecker
	identity        *service.Identity
	directory       *service.Directory
	invitations     *service.Invitations
	oidc            *auth.OIDCClient
	oauth           *service.OAuth
	mcp             *mcp.Registry
	tokenTeams      TokenTeamLookup
	// usagePrune spaces the heartbeat's usage prunes, per Team.
	usagePrune pruneGate
}

// NewServer wires middleware and routes. It never returns nil dependencies, so
// a zero Deps still produces a working handler in tests.
func NewServer(deps Deps) *Server {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// A missing authenticator refuses everything rather than letting
	// everything through. A wiring mistake then shows up as an API that
	// answers 401 to its own UI, which somebody notices, instead of one that
	// serves every route to anybody, which nobody does. A test that wants the
	// old behaviour asks for auth.NewStub() by name.
	authenticator := deps.Auth
	if authenticator == nil {
		authenticator = denyEveryRequest{}
	}
	authenticator = inProcessAuthenticator{session: authenticator}

	s := &Server{
		cfg:             deps.Config,
		version:         deps.Version,
		logger:          logger,
		router:          chi.NewRouter(),
		validate:        validator.New(validator.WithRequiredStructEnabled()),
		databases:       deps.Databases,
		metrics:         deps.Metrics,
		support:         deps.Support,
		connections:     deps.Connections,
		cloud:           deps.CloudConnections,
		chat:            deps.Chat,
		chatHeartbeat:   deps.ChatHeartbeat,
		activity:        deps.Activity,
		activityDetails: deps.ActivityDetails,
		features:        deps.Features,
		store:           deps.Store,
		identity:        deps.Identity,
		directory:       deps.Directory,
		invitations:     deps.Invitations,
		oidc:            deps.OIDC,
		oauth:           deps.OAuth,
		mcp:             deps.MCP,
		tokenTeams:      deps.TokenTeams,
	}

	s.router.Use(RequestID)
	s.router.Use(RequestLogger(logger))
	s.router.Use(Recoverer(logger))
	s.router.Use(LegacyNames)

	s.router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		Respond(w, r, nil, NotFound("That endpoint does not exist."))
	})
	s.router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		Respond(w, r, nil, Validation("That method is not allowed on this endpoint.").
			WithStatus(http.StatusMethodNotAllowed))
	})

	// Kubernetes probes these without a session, so they sit outside the API group.
	s.router.Get("/healthz", s.handleHealthz)
	s.router.Get("/readyz", s.handleReadyz)

	// Spoke agents reach these without a session. Enrollment and heartbeat
	// check a join token or the shared lab token; the join script and the
	// chart below are served to anybody who can reach the hub, and carry no
	// credential of their own, which is why nothing is substituted into them
	// but a validated hub URL, git URL and region.
	s.router.Post("/api/internal/environments/{name}/heartbeat", s.handleHeartbeat)
	s.router.Post("/api/internal/environments/{name}/join", s.handleJoinPhase)
	s.router.Get("/api/internal/connect/bootstrap.sh", s.handleConnectBootstrap)
	s.router.Get("/api/internal/connect/pds.tgz", s.handleConnectChart)
	s.router.Get("/api/internal/connect/enrollment", s.handleConnectEnrollment)

	// A customer's AWS account reaches these with a connect token, or with no
	// credential at all for the generic template and script.
	s.router.Get(cloudRoutePrefix+"template.yaml", s.handleCloudTemplate)
	s.router.Get(cloudRoutePrefix+"connect.sh", s.handleCloudConnectScript)
	s.router.Post(cloudRoutePrefix+"enroll", s.handleCloudEnroll)
	s.router.Post(cloudRoutePrefix+"events", s.handleCloudEvent)
	s.router.Post(cloudRoutePrefix+"verify", s.handleCloudVerify)

	s.registerOAuthRoutes()
	s.registerMCPRoutes()

	s.router.Route("/api", func(open chi.Router) {
		// Signing in cannot require a session, so these four sit outside the
		// guard. Nothing here creates a login: Percona onboards one from the
		// command line, and a customer can only claim a link it already has.
		open.Post("/auth/login", s.handleLogin)
		open.Post("/auth/logout", s.handleLogout)
		open.Get("/auth/session", s.handleAuthSession)
		open.Get("/auth/methods", s.handleAuthMethods)
		open.Get("/auth/oidc/start", s.handleOIDCStart)
		open.Get("/auth/oidc/callback", s.handleOIDCCallback)
		open.Post("/auth/oidc/logout", s.handleOIDCLogout)
		open.Post("/auth/organizations", s.handleCreateOrganization)
		open.Post("/auth/organization", s.handleSwitchOrganization)
		open.Get("/auth/invite", s.handleInvitePreview)
		open.Post("/auth/invite", s.handleClaimInvite)

		s.registerConsentRoutes(open)
		s.registerAgentSessionRoutes(open)

		open.Group(func(api chi.Router) {
			api.Use(agentCredentialAuthenticator{server: s, session: authenticator}.Middleware)
			api.Use(s.actingTeam)

			// The UI seeds its own flag provider from here, so this route answers
			// whatever the rest of the surfaces are set to.
			api.Get("/features", s.handleFeatures)

			// Clients that predate the hub field still read it, so the list stays
			// on even when the environments surface is off. Create would otherwise
			// omit the environment.
			api.Get("/environments", s.handleListEnvironments)

			api.Get("/alerts", s.handleListAlerts)

			// Every member reads what agents may do; only an admin changes it.
			api.Get("/organization/ai", s.handleGetAI)

			api.Group(func(directory chi.Router) {
				directory.Use(s.requireAdmin)

				directory.Patch("/organization", s.handleRenameOrganization)
				directory.Put("/organization/ai", s.handleSetAI)
				directory.Get("/teams", s.handleListTeams)
				directory.Post("/teams", s.handleCreateTeam)
				directory.Patch("/teams/{name}", s.handleRenameTeam)
				directory.Delete("/teams/{name}", s.handleDeleteTeam)
				directory.Get("/users", s.handleListMembers)
				directory.Patch("/users/{id}", s.handleUpdateMember)
				directory.Delete("/users/{id}", s.handleRemoveMember)
				directory.Get("/invitations", s.handleListInvitations)
				directory.Post("/invitations", s.handleCreateInvitation)
				directory.Post("/invitations/{id}/resend", s.handleResendInvitation)
				directory.Delete("/invitations/{id}", s.handleRevokeInvitation)
			})

			api.Group(func(environments chi.Router) {
				environments.Use(s.requireFeature(features.Clusters))

				environments.Get("/connect", s.handleConnectGuide)
				environments.With(s.requireCaller).Post("/connect/tokens", s.handleIssueJoinToken)

				environments.Group(func(environment chi.Router) {
					environment.Use(s.objectTeam("name"))

					environment.With(s.requireCaller).Delete("/environments/{name}", s.handleDisconnectEnvironment)
					environment.Get("/environments/{name}/history", s.handleEnvironmentHistory)
					environment.Get("/environments/{name}/monitoring", s.handleGetMonitoring)
					environment.Get("/environments/{name}/monitoring/password", s.handleGetMonitoringPassword)
					environment.With(s.requireCaller).Put("/environments/{name}/monitoring", s.handlePutMonitoring)
					environment.Group(func(support chi.Router) {
						support.Use(s.requireFeature(features.SupportAccess))

						support.Get("/environments/{name}/support-access", s.handleGetSupportAccess)
						support.With(s.requireSupportAccessWriter).
							Post("/environments/{name}/support-access", s.handleOpenSupportAccess)
						support.With(s.requireSupportAccessWriter).
							Patch("/environments/{name}/support-access", s.handleExtendSupportAccess)
						support.With(s.requireSupportAccessWriter).
							Delete("/environments/{name}/support-access", s.handleEndSupportAccess)
					})
				})
			})

			api.Group(func(connections chi.Router) {
				connections.Use(s.requireFeature(features.Connections))

				connections.Get("/connections", s.handleGetConnections)
				connections.Group(func(writes chi.Router) {
					writes.Use(s.requireAdmin)
					writes.Put("/connections/servicenow", s.handlePutServiceNow)
					writes.Put("/connections/assistant", s.handlePutAssistant)
					writes.Put("/connections/cursor", s.handlePutCursor)
					writes.Delete("/connections/{kind}", s.handleDeleteConnection)
				})
			})

			api.With(s.requireFeature(features.Assistant)).Get("/assistant/models", s.handleListModels)
			api.Route("/chat", func(chat chi.Router) {
				chat.Use(s.requireFeature(features.Chat))

				chat.Post("/", s.handleChat)
				chat.Post("/approvals/{id}", s.handleChatApproval)
				chat.Get("/conversations", s.handleListChatConversations)
				chat.Get("/conversations/{id}", s.handleGetChatConversation)
				chat.Delete("/conversations/{id}", s.handleDeleteChatConversation)
			})
			api.Route("/activity", func(activity chi.Router) {
				activity.Use(s.requireFeature(features.Activity))
				activity.Use(s.requireActivity)

				activity.Get("/", s.handleListActivity)
				activity.Get("/{id}", s.handleGetActivityDetail)
			})

			api.Route("/support/tickets", func(tickets chi.Router) {
				tickets.Use(s.requireFeature(features.Support))
				tickets.Use(s.requireSupport)

				tickets.Post("/", s.handleCreateTicket)
				tickets.Get("/", s.handleListTickets)
				tickets.Get("/{number}", s.handleGetTicket)
				tickets.Post("/{number}/replies", s.handleReplyTicket)
			})

			api.Route("/databases", func(databases chi.Router) {
				databases.Use(s.requireFeature(features.Databases))

				databases.With(s.requireCaller).Post("/", s.handleCreateDatabase)
				databases.Get("/", s.handleListDatabases)

				databases.Route("/{id}/{hubID}", func(database chi.Router) {
					database.Use(s.objectTeam("id"))

					database.Get("/", s.handleGetDatabase)
					database.Get("/credential", s.handleDatabaseCredential)
					database.With(s.requireCaller).Delete("/", s.handleDeleteDatabase)
					database.With(s.requireCaller).Post("/scale", s.handleScaleDatabase)

					database.Group(func(backups chi.Router) {
						backups.Use(s.requireFeature(features.Backups))

						backups.With(s.requireCaller).Post("/backup", s.handleBackupDatabase)
						backups.Get("/backups", s.handleListBackups)
						backups.With(s.requireCaller).Post("/backup-schedule", s.handleSetBackupSchedule)
						backups.With(s.requireCaller).Post("/restore", s.handleRestoreDatabase)
					})

					database.With(s.requireFeature(features.Metrics)).
						Get("/metrics", s.handleDatabaseMetrics)
					database.Get("/history", s.handleDatabaseHistory)
					database.Get("/usage", s.handleDatabaseUsage)
					database.Get("/alerts", s.handleDatabaseAlerts)
				})
			})
		})
	})

	return s
}

// denyEveryRequest is the authenticator a zero Deps gets. It exists so the
// default is closed: nothing reaches a handler without somebody having chosen
// how requests are authenticated.
type denyEveryRequest struct{}

func (denyEveryRequest) Middleware(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Respond(w, r, nil, Unauthorized("Sign in is not configured on this control plane."))
	})
}

// requireSupport short circuits the support routes when the adapter runs
// without a ServiceNow Secret. The UI reads the 501 and hides its support
// actions, linking to the public portal instead.
func (s *Server) requireSupport(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.support.Configured(r.Context()) {
			Respond(w, r, nil, supportError(service.ErrSupportUnavailable, ""))

			return
		}

		next.ServeHTTP(w, r)
	})
}

// requireActivity short circuits the activity route when the adapter was built
// without a ring. The UI reads the 501 and drops the nav entry, the same way
// it does for support.
func (s *Server) requireActivity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.activity == nil || s.activityDetails == nil {
			Respond(w, r, nil, NotImplemented("The activity log is not enabled."))

			return
		}

		next.ServeHTTP(w, r)
	})
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// Router exposes the chi router for mounting in cmd.
func (s *Server) Router() chi.Router { return s.router }
