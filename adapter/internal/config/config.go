// Package config loads adapter configuration from the environment.
//
// Configuration is env first, twelve factor style. Every value has a default
// that works for local development, so `go run ./cmd/adapter` boots with no
// environment set. Secrets are read here and never printed.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/client/github"
	"github.com/percona/pds/adapter/internal/client/servicenow"
	"github.com/percona/pds/adapter/internal/features"
)

// Mode names the deployment mode the adapter runs in.
type Mode string

const (
	// EnvDev is local development: human readable text logs.
	EnvDev Mode = "dev"
	// EnvProd is a customer deployment: structured JSON logs.
	EnvProd Mode = "prod"
)

// IsProduction reports whether the adapter runs in a production environment.
func (e Mode) IsProduction() bool { return e == EnvProd }

// Config is the adapter configuration, loaded from the environment.
//
// Downstream URLs and credentials stay empty until the matching client lands;
// an empty ServiceNow key makes the support endpoints answer 501.
type Config struct {
	Addr            string
	Env             Mode
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	Role            string

	HubGitDir    string
	HubURL       string
	HubPublicURL string
	// HubOAuthIssuer is the address agents and people reach the authorization server at, which
	// is its issuer. Empty means HubPublicURL. It differs where the address spokes use is not
	// one a person's browser or agent can use as an issuer, as in the kind lab.
	HubOAuthIssuer string
	// CloudPrincipalARN is the role or user the control plane assumes customer roles
	// from, and the one their trust policies name. Empty turns cloud connections off.
	CloudPrincipalARN string
	// ManagedOutgoingCIDRs are the addresses this deployment's AWS calls to a
	// managed environment's Kubernetes API come from. Only they may reach the
	// endpoint while an Access window is open. Empty refuses managed
	// environments, and 0.0.0.0/0 is accepted when the operator chooses it.
	ManagedOutgoingCIDRs []string
	// ManagedChartVersion is the published pds chart a managed environment
	// installs. Empty refuses managed environments.
	ManagedChartVersion string
	// ManagedChartURL is the OCI registry a managed environment installs the pds
	// chart from. It defaults to the published one.
	ManagedChartURL string
	// AgentReleaseNamespace and AgentReleaseName name the Flux HelmRelease an
	// agent reports on its heartbeat. Empty means the environment has none, as a
	// connected environment does.
	AgentReleaseNamespace string
	AgentReleaseName      string
	// AgentBackup is the IAM user a managed environment's agent makes for its
	// backups. An empty User means the environment keeps its key some other way.
	AgentBackup AgentBackup
	// ProvisionLeaseTTL is how long a provisioning run's lease lasts unrenewed.
	ProvisionLeaseTTL time.Duration
	// ProvisionMaxAttempts caps the automatic attempts of one step.
	ProvisionMaxAttempts int
	// ProvisionWindowMaxAge is how long an Access window may stay open.
	ProvisionWindowMaxAge time.Duration
	HubGitURL             string
	HubGitHubOrg          string
	HubGitHubToken        string
	// The GitHub App credentials replace HubGitHubToken. The private key is
	// PEM text, read from a value or a file.
	HubGitHubAppID             string
	HubGitHubAppInstallationID string
	HubGitHubAppPrivateKey     string
	HubRegistryToken           string
	HubAgentToken              string
	HubOperatorToken           string
	HubAgentEnvironments       []string
	HubAgentImage              string
	HubClusterRegion           string
	EnvironmentName            string
	HeartbeatStale             time.Duration
	JoinTokenTTL               time.Duration

	EverestURL string
	PMMURL     string
	OIDCIssuer string
	// OIDCClientID and OIDCClientSecret are the adapter's credentials at the
	// issuer. The secret comes from a Secret, never chart values. OIDCScopes
	// empty means openid, email and profile.
	OIDCClientID     string
	OIDCClientSecret string
	OIDCScopes       []string

	// SignupEnabled lets an identity nobody has invited make an Organization
	// on its first sign in. Off by default, which is what a self-hosted
	// install wants. SignupOrganizationCeiling is how many Organizations the
	// deployment accepts before signup refuses.
	SignupEnabled             bool
	SignupOrganizationCeiling int

	// SessionLifetime is how long a customer session lasts however much it is
	// used, and SessionIdle how long it survives unused. Both are the outer
	// bound on a stolen cookie, from two directions.
	SessionLifetime time.Duration
	SessionIdle     time.Duration
	// InviteTTL is how long an invite link stays claimable after Percona
	// mints it.
	InviteTTL time.Duration

	// InvitationTTL is how long an invitation an Organization admin makes
	// stays claimable. InvitationFrom is the sender of the email that carries
	// it; without it, or without SESRegion, nothing is emailed and the
	// admin shares the link.
	InvitationTTL  time.Duration
	InvitationFrom string

	// DatabaseURL is the PostgreSQL connection URL the hub keeps its own
	// state in, read from a Secret. Empty means no store, which leaves
	// readiness free of it so an install that predates the database still
	// comes up.
	DatabaseURL string

	// PMMToken is the Grafana service account token PMM 3 authenticates with,
	// from a Secret. Empty only works against a PMM with anonymous access.
	PMMToken string

	// EverestToken is the Everest bearer token, from a Secret. Empty means
	// unauthenticated, which only works against a dev Everest. Everest issues
	// a token that expires, so this is a starting point rather than the only
	// one the adapter will ever hold.
	EverestToken string
	// EverestAccountsSecret holds the Everest admin account the adapter logs in
	// with once the token it started on has expired. It is read from
	// ConnectionsNamespace.
	EverestAccountsSecret string
	// EverestCluster is the OpenEverest cluster name the adapter scopes API
	// calls under. Empty means the Everest default cluster.
	EverestCluster string
	// EverestNamespace is the Everest managed namespace databases land in. It
	// is also the only namespace a create request may target.
	EverestNamespace string
	// EverestBackupStorage is the BackupStorage backups are written to. Empty
	// leaves the backup feature off, which is the default because nothing in
	// this repository registers one.
	EverestBackupStorage string
	// EverestMonitoringConfig is the MonitoringConfig new instances scrape
	// through. Empty leaves pmm-client off, so metrics stay pending.
	EverestMonitoringConfig string

	// ServiceNowURL is the ServiceNow instance support tickets go to. It has
	// no default: support stays off until an operator sets it.
	ServiceNowURL string
	// SupportGuideURL is the https address of the engineer's support access
	// steps. It has no default: case notes carry no link until an operator sets
	// one.
	SupportGuideURL       string
	ServiceNowAPIKey      string
	ServiceNowClientToken string

	// PodNamespace is the namespace the adapter pod runs in, where the
	// connection Secrets live. Empty means EverestNamespace.
	PodNamespace string
	// ServiceNowSecretName is the Secret PUT /api/connections/servicenow
	// writes. Empty means pds-servicenow.
	ServiceNowSecretName string
	// AssistantSecretName is the Secret PUT /api/connections/assistant writes.
	// Empty means pds-assistant.
	AssistantSecretName string
	// CursorSecretName is the Secret PUT /api/connections/cursor writes.
	// Empty means pds-cursor.
	CursorSecretName string

	// ActivityConfigMapName is the ConfigMap the activity ring is stored in.
	// Empty means pds-activity.
	ActivityConfigMapName string

	// HubJoinSecretName is the Secret hashed join tokens are stored in.
	// Empty means pds-join-tokens.
	HubJoinSecretName string
	// HubWorkConfigMapName is the ConfigMap the hub outbound work queue is
	// stored in, so a restart does not drop work a spoke has not collected.
	HubWorkConfigMapName string
	// HubInventoryConfigMapName is the prefix for hub inventory ConfigMaps.
	// Empty means pds-hub-inventory. The prefix object lists
	// environment names; snapshots live in <prefix>-0 through <prefix>-15.
	HubInventoryConfigMapName string

	// KubernetesServiceHost is set by the API server in every pod. It is read
	// rather than inferred, so a process can be told it is not in an environment.
	KubernetesServiceHost string

	// Organization is the one Organization this adapter serves, by name. Empty
	// means default, and a star means it serves every Organization in the
	// database rather than one named one.
	Organization string

	// StateEncryptionKey seals the credentials the adapter has to replay
	// downstream, base64 of 32 bytes, from a Secret. Without it those rows
	// cannot be written, so the connections routes report unavailable.
	StateEncryptionKey string
	// StateEncryptionKeysRetired are keys accepted for reading only, so a key
	// can be replaced without rewriting every row first.
	StateEncryptionKeysRetired []string

	// SESRegion is the Amazon SES region that emails an invitation when
	// InvitationFrom is also set. Credentials come from the AWS default
	// chain, never from configuration. Without it, the admin only sees the
	// link.
	SESRegion string

	// SupportTailnet is the private network support access runs over. Its
	// credentials come from Secrets, and only a hub uses them.
	SupportTailnet SupportTailnet

	// Features holds only the surface flags an operator set. A name absent
	// from the map keeps its default, which is on, so an empty map is the
	// whole product.
	Features map[string]bool
}

// DatabaseConfigured reports whether a connection URL was supplied. The
// external shape and the in-cluster one both arrive the same way, so there is
// one answer rather than one per shape.
func (c Config) DatabaseConfigured() bool {
	return strings.TrimSpace(c.DatabaseURL) != ""
}

// OrganizationName is the Organization this adapter serves. It is empty when
// the adapter serves every Organization, because there is no one to name then:
// the Organization comes from the request instead.
func (c Config) OrganizationName() string {
	if c.ServesEveryOrganization() {
		return ""
	}
	if name := strings.TrimSpace(c.Organization); name != "" {
		return name
	}

	return defaultOrganization
}

// ServesEveryOrganization reports whether this adapter serves every
// Organization rather than one named one. A name can only hold lower case
// letters, digits and hyphens and has to start with a letter, so the star can
// never collide with a real one.
func (c Config) ServesEveryOrganization() bool {
	return strings.TrimSpace(c.Organization) == everyOrganization
}

// StateEncryptionConfigured reports whether a key was supplied for the
// credentials that cannot be hashed.
func (c Config) StateEncryptionConfigured() bool {
	return strings.TrimSpace(c.StateEncryptionKey) != ""
}

// InCluster reports whether this process is running inside Kubernetes. The
// API server sets this for every pod, and it is what tells a laptop run, where
// having no Kubernetes client is ordinary, apart from a pod whose client failed.
func (c Config) InCluster() bool {
	return strings.TrimSpace(c.KubernetesServiceHost) != ""
}

// ServiceNowConfigured reports whether the ServiceNow Secret is mounted. Both
// tokens are needed: the connector authenticates every request with both, so
// one without the other is the same as neither.
func (c Config) ServiceNowConfigured() bool {
	return c.ServiceNowAPIKey != "" && c.ServiceNowClientToken != ""
}

// ConnectionsNamespace is where the connection Secrets are stored.
func (c Config) ConnectionsNamespace() string {
	if strings.TrimSpace(c.PodNamespace) != "" {
		return c.PodNamespace
	}

	return c.EverestNamespace
}

const (
	RoleHTTP  = "http"
	RoleAgent = "agent"
)

const (
	// defaultEverestAccountsSecret is the Secret Everest writes its admin
	// account into on first install.
	defaultEverestAccountsSecret = "everest-accounts"
	defaultServiceNowSecretName  = "pds-servicenow"
	defaultAssistantSecretName   = "pds-assistant"
	defaultCursorSecretName      = "pds-cursor"
	defaultActivityConfigMap     = "pds-activity"
	// publishedAgentToken is the value an earlier chart shipped. It is in the
	// repository history, so the adapter refuses to accept it. The string has
	// to stay exactly as published: renaming it would stop refusing the token
	// that is actually out there.
	publishedAgentToken = "poc-agent-token"

	// everyOrganization is the value of Organization that asks this adapter to
	// serve every Organization in the database rather than one named one.
	everyOrganization = "*"

	// defaultOrganization is what a deployment that never names one serves. One
	// name rather than an empty string keeps the rows greppable and keeps a
	// later Organization from inheriting the first one's state by default.
	defaultOrganization = "default"

	defaultHubJoinSecret         = "pds-join-tokens"
	defaultHubInventoryConfigMap = "pds-hub-inventory"
	defaultHubWorkConfigMap      = "pds-hub-work"
)

// ServiceNowSecret is the Secret name the connections service writes.
func (c Config) ServiceNowSecret() string {
	if name := strings.TrimSpace(c.ServiceNowSecretName); name != "" {
		return name
	}

	return defaultServiceNowSecretName
}

// AssistantSecret is the Secret name the connections service writes.
func (c Config) AssistantSecret() string {
	if name := strings.TrimSpace(c.AssistantSecretName); name != "" {
		return name
	}

	return defaultAssistantSecretName
}

// CursorSecret is the Secret name the connections service writes.
func (c Config) CursorSecret() string {
	if name := strings.TrimSpace(c.CursorSecretName); name != "" {
		return name
	}

	return defaultCursorSecretName
}

// ActivityConfigMap is the ConfigMap name the activity ring is stored in.
func (c Config) ActivityConfigMap() string {
	if name := strings.TrimSpace(c.ActivityConfigMapName); name != "" {
		return name
	}

	return defaultActivityConfigMap
}

// HubJoinSecret is the Secret name hashed join tokens are stored in.
func (c Config) HubJoinSecret() string {
	if name := strings.TrimSpace(c.HubJoinSecretName); name != "" {
		return name
	}

	return defaultHubJoinSecret
}

// HubWorkConfigMap is the ConfigMap name the hub work queue is stored in.
func (c Config) HubWorkConfigMap() string {
	if name := strings.TrimSpace(c.HubWorkConfigMapName); name != "" {
		return name
	}

	return defaultHubWorkConfigMap
}

// HubInventoryConfigMap is the prefix for hub inventory ConfigMaps. The
// prefix object lists names; snapshots live in prefix-0 through prefix-15.
func (c Config) HubInventoryConfigMap() string {
	if name := strings.TrimSpace(c.HubInventoryConfigMapName); name != "" {
		return name
	}

	return defaultHubInventoryConfigMap
}

// HubEnabled reports whether this process is the multi-environment hub.
func (c Config) HubEnabled() bool {
	return strings.TrimSpace(c.HubGitDir) != "" || c.HubGitHubConfigured()
}

// ServesHubAPI reports whether this process exposes the hub HTTP API.
// The HTTP role always does. The agent role never does.
func (c Config) ServesHubAPI() bool {
	return c.Role != RoleAgent
}

// HubGitHubConfigured reports whether the hub writes one GitHub repo per environment.
func (c Config) HubGitHubConfigured() bool {
	return strings.TrimSpace(c.HubGitHubOrg) != "" && (c.HubGitHubToken != "" || c.HubGitHubAppConfigured())
}

// HubGitHubAppConfigured reports whether the hub authenticates to GitHub as an
// App installation rather than with a static token.
func (c Config) HubGitHubAppConfigured() bool {
	return c.HubGitHubAppID != "" && c.HubGitHubAppInstallationID != "" && c.HubGitHubAppPrivateKey != ""
}

// HubGitHubCloneURL is the HTTPS remote for this repository. repo is the
// GitHub name, already prefixed when this adapter serves every Organization.
// Enrollment uses HubGitHubSSHURL instead so spokes never receive the org
// token.
func (c Config) HubGitHubCloneURL(repo string) string {
	return "https://github.com/" + strings.TrimSpace(c.HubGitHubOrg) + "/" + strings.TrimSpace(repo) + ".git"
}

// HubGitHubSSHURL is the SSH remote Flux clones for this repository. repo is
// the GitHub name, already prefixed when this adapter serves every Organization.
func (c Config) HubGitHubSSHURL(repo string) string {
	return "ssh://git@github.com/" + strings.TrimSpace(c.HubGitHubOrg) + "/" + strings.TrimSpace(repo) + ".git"
}

// Defaults applied when a variable is unset or empty.
const (
	defaultAddr            = ":8080"
	defaultEnv             = EnvDev
	defaultLogLevel        = "info"
	defaultShutdownTimeout = 15 * time.Second

	// defaultEverestNamespace is the release namespace, which is also the
	// Everest managed namespace the umbrella chart creates.
	defaultEverestNamespace = "pds"
	// defaultEverestCluster is the cluster name Everest registers when none is set.
	defaultEverestCluster = "main"
)

// Load reads configuration from the environment and applies defaults.
// It returns an error naming the offending variable when a value is unusable.
func Load() (Config, error) {
	cfg := Config{
		Addr:                       strings.TrimSpace(lookup("ADAPTER_ADDR", defaultAddr)),
		EverestURL:                 strings.TrimSpace(lookup("EVEREST_URL", "")),
		PMMURL:                     strings.TrimSpace(lookup("PMM_URL", "")),
		PMMToken:                   lookup("PMM_TOKEN", ""),
		OIDCIssuer:                 strings.TrimSpace(lookup("OIDC_ISSUER", "")),
		OIDCClientID:               strings.TrimSpace(lookup("OIDC_CLIENT_ID", "")),
		OIDCClientSecret:           lookup("OIDC_CLIENT_SECRET", ""),
		OIDCScopes:                 strings.Fields(lookup("OIDC_SCOPES", "")),
		EverestToken:               lookup("EVEREST_TOKEN", ""),
		EverestAccountsSecret:      strings.TrimSpace(lookup("EVEREST_ACCOUNTS_SECRET", defaultEverestAccountsSecret)),
		EverestCluster:             strings.TrimSpace(lookup("EVEREST_CLUSTER", defaultEverestCluster)),
		EverestNamespace:           strings.TrimSpace(lookup("EVEREST_NAMESPACE", defaultEverestNamespace)),
		EverestBackupStorage:       strings.TrimSpace(lookup("EVEREST_BACKUP_STORAGE", "")),
		EverestMonitoringConfig:    strings.TrimSpace(lookup("EVEREST_MONITORING_CONFIG", "")),
		ServiceNowURL:              strings.TrimSpace(lookup("SN_URL", "")),
		SupportGuideURL:            strings.TrimSpace(lookup("SUPPORT_GUIDE_URL", "")),
		ServiceNowAPIKey:           lookup("SN_API_KEY", ""),
		ServiceNowClientToken:      lookup("SN_CLIENT_TOKEN", ""),
		PodNamespace:               strings.TrimSpace(lookup("POD_NAMESPACE", "")),
		ServiceNowSecretName:       strings.TrimSpace(lookup("SN_SECRET_NAME", defaultServiceNowSecretName)),
		AssistantSecretName:        strings.TrimSpace(lookup("ASSISTANT_SECRET_NAME", defaultAssistantSecretName)),
		CursorSecretName:           strings.TrimSpace(lookup("CURSOR_SECRET_NAME", defaultCursorSecretName)),
		ActivityConfigMapName:      strings.TrimSpace(lookup("ACTIVITY_CONFIGMAP", defaultActivityConfigMap)),
		HubJoinSecretName:          strings.TrimSpace(lookup("HUB_JOIN_SECRET", defaultHubJoinSecret)),
		HubInventoryConfigMapName:  strings.TrimSpace(lookup("HUB_INVENTORY_CONFIGMAP", defaultHubInventoryConfigMap)),
		HubWorkConfigMapName:       strings.TrimSpace(lookup("HUB_WORK_CONFIGMAP", defaultHubWorkConfigMap)),
		KubernetesServiceHost:      strings.TrimSpace(lookup("KUBERNETES_SERVICE_HOST", "")),
		Organization:               strings.TrimSpace(lookup("HUB_ORGANIZATION", defaultOrganization)),
		StateEncryptionKey:         strings.TrimSpace(lookup("STATE_ENCRYPTION_KEY", "")),
		StateEncryptionKeysRetired: splitList(lookup("STATE_ENCRYPTION_KEYS_RETIRED", "")),
		HubGitDir:                  strings.TrimSpace(lookup("HUB_GIT_DIR", "")),
		HubURL:                     strings.TrimSpace(lookup("HUB_URL", "")),
		HubPublicURL:               strings.TrimSpace(lookup("HUB_PUBLIC_URL", "")),
		HubOAuthIssuer:             strings.TrimSpace(lookup("HUB_OAUTH_ISSUER", "")),
		CloudPrincipalARN:          strings.TrimSpace(lookup("HUB_CLOUD_PRINCIPAL_ARN", "")),
		HubGitURL:                  strings.TrimSpace(lookup("HUB_GIT_URL", "")),
		HubGitHubOrg:               strings.TrimSpace(lookup("HUB_GITHUB_ORG", "")),
		HubGitHubToken:             lookup("HUB_GITHUB_TOKEN", ""),
		HubGitHubAppID:             strings.TrimSpace(lookup("HUB_GITHUB_APP_ID", "")),
		HubGitHubAppInstallationID: strings.TrimSpace(lookup("HUB_GITHUB_APP_INSTALLATION_ID", "")),
		// Env files cannot hold a multi-line value portably, so a key written
		// on one line with \n escapes is accepted.
		HubGitHubAppPrivateKey: strings.ReplaceAll(lookup("HUB_GITHUB_APP_PRIVATE_KEY", ""), `\n`, "\n"),
		HubRegistryToken:       lookup("HUB_REGISTRY_TOKEN", ""),
		HubAgentToken:          lookup("HUB_AGENT_TOKEN", ""),
		HubOperatorToken:       lookup("HUB_OPERATOR_TOKEN", ""),
		HubAgentEnvironments:   splitList(lookupRenamed("HUB_AGENT_ENVIRONMENTS", "HUB_AGENT_CLUSTERS", "")), // allow-legacy-name
		HubAgentImage:          strings.TrimSpace(lookup("HUB_AGENT_IMAGE", "")),
		HubClusterRegion:       strings.TrimSpace(lookup("HUB_CLUSTER_REGION", "us-west-2")),
		EnvironmentName:        strings.TrimSpace(lookupRenamed("ENVIRONMENT_NAME", "CLUSTER_NAME", "")),
		DatabaseURL:            strings.TrimSpace(os.Getenv("DATABASE_URL")),
		SESRegion:              strings.TrimSpace(lookup("SES_REGION", "")),
	}

	flags, err := loadFeatures()
	if err != nil {
		return Config{}, err
	}
	cfg.Features = flags
	warnRemovedKeys()

	// These two are substituted into the shell script a customer pipes into
	// bash, and into the manifests Flux applies. They have to pass the same
	// origin check those endpoints apply, or the hub starts and Connect then
	// answers 400.
	for _, check := range []struct {
		name      string
		value     string
		allowPath bool
	}{
		{"HUB_PUBLIC_URL", cfg.HubPublicURL, false},
		{"HUB_OAUTH_ISSUER", cfg.HubOAuthIssuer, false},
		{"HUB_GIT_URL", cfg.HubGitURL, true},
	} {
		if err := validateHubURL(check.name, check.value, check.allowPath); err != nil {
			return Config{}, err
		}
	}

	if cfg.CloudPrincipalARN != "" && !cloudPrincipalARN.MatchString(cfg.CloudPrincipalARN) {
		return Config{}, errors.New("HUB_CLOUD_PRINCIPAL_ARN must be the ARN of an IAM role or user, never an account root")
	}

	// An absent variable is an adapter with no database. A variable that is
	// present but empty is a Secret with an empty value, which the chart only
	// renders when the database is turned on, so it is a broken Secret rather
	// than a request to run without a store. Treating it as unset would start
	// the adapter in the no-store path and report ready with nothing behind it.
	if raw, ok := os.LookupEnv("DATABASE_URL"); ok && strings.TrimSpace(raw) == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is set but empty. Remove it to run without a database, or point it at a Secret that holds a connection URL")
	}

	if err := validateDatabaseURL(cfg.DatabaseURL); err != nil {
		return Config{}, err
	}

	if err := loadGitHubAppKeyFile(&cfg); err != nil {
		return Config{}, err
	}
	if err := validateHubGitHub(cfg); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(cfg.HubGitDir) != "" && cfg.HubGitHubConfigured() {
		return Config{}, fmt.Errorf("HUB_GIT_DIR cannot be set with HUB_GITHUB_ORG. Pick one hub backend")
	}

	if strings.TrimSpace(cfg.HubAgentToken) == publishedAgentToken {
		return Config{}, fmt.Errorf("HUB_AGENT_TOKEN is the value that shipped in the chart, so it is public. Set a different one or leave it unset")
	}

	if cfg.ServiceNowURL != "" {
		if err := servicenow.ValidateInstanceURL(cfg.ServiceNowURL); err != nil {
			return Config{}, fmt.Errorf("SN_URL is not a usable ServiceNow instance: %w", err)
		}
	}

	if cfg.SupportGuideURL != "" {
		guide, err := url.Parse(cfg.SupportGuideURL)
		if err != nil || guide.Scheme != "https" || guide.Host == "" {
			return Config{}, fmt.Errorf("SUPPORT_GUIDE_URL must be an https address")
		}
	}

	if cfg.EverestNamespace == "" {
		return Config{}, fmt.Errorf("EVEREST_NAMESPACE must not be blank")
	}
	if cfg.EverestCluster == "" {
		cfg.EverestCluster = defaultEverestCluster
	}

	if cfg.Addr == "" {
		return Config{}, fmt.Errorf("ADAPTER_ADDR must not be blank")
	}

	env := Mode(strings.ToLower(strings.TrimSpace(lookup("ADAPTER_ENV", string(defaultEnv)))))
	switch env {
	case EnvDev, EnvProd:
		cfg.Env = env
	default:
		return Config{}, fmt.Errorf("ADAPTER_ENV must be %q or %q, got %q", EnvDev, EnvProd, env)
	}

	level, err := parseLevel(lookup("ADAPTER_LOG_LEVEL", defaultLogLevel))
	if err != nil {
		return Config{}, fmt.Errorf("ADAPTER_LOG_LEVEL: %w", err)
	}
	cfg.LogLevel = level

	timeout, err := time.ParseDuration(lookup("ADAPTER_SHUTDOWN_TIMEOUT", defaultShutdownTimeout.String()))
	if err != nil {
		return Config{}, fmt.Errorf("ADAPTER_SHUTDOWN_TIMEOUT: %w", err)
	}
	if timeout <= 0 {
		return Config{}, fmt.Errorf("ADAPTER_SHUTDOWN_TIMEOUT must be positive, got %s", timeout)
	}
	cfg.ShutdownTimeout = timeout

	role := strings.ToLower(strings.TrimSpace(lookup("ADAPTER_ROLE", RoleHTTP)))
	if role == "" {
		role = RoleHTTP
	}
	switch role {
	case RoleHTTP, RoleAgent:
		cfg.Role = role
	default:
		return Config{}, fmt.Errorf("ADAPTER_ROLE must be empty, %q, or %q, got %q", RoleHTTP, RoleAgent, role)
	}

	if cfg.Role == RoleHTTP && (!cfg.HubEnabled() || strings.TrimSpace(cfg.HubPublicURL) == "") {
		return Config{}, fmt.Errorf("the HTTP role requires a Git backend and HUB_PUBLIC_URL: set HUB_GIT_DIR, or HUB_GITHUB_ORG with HUB_GITHUB_TOKEN or the HUB_GITHUB_APP_* settings, and set HUB_PUBLIC_URL")
	}
	if cfg.ServesHubAPI() && strings.TrimSpace(cfg.HubOperatorToken) == "" {
		token, err := mintOperatorToken()
		if err != nil {
			return Config{}, err
		}
		cfg.HubOperatorToken = token
	}

	if err := loadProvisioning(&cfg, lookup); err != nil {
		return Config{}, err
	}

	// A join token is printed in a command an operator pastes around, so it
	// outlives the join unless it is given an end. Zero keeps a token valid
	// forever, which is what tokens minted before this did.
	ttl, err := time.ParseDuration(lookup("HUB_JOIN_TOKEN_TTL", "24h"))
	if err != nil {
		return Config{}, fmt.Errorf("HUB_JOIN_TOKEN_TTL: %w", err)
	}
	if ttl < 0 {
		return Config{}, fmt.Errorf("HUB_JOIN_TOKEN_TTL must not be negative, got %s", ttl)
	}
	cfg.JoinTokenTTL = ttl

	// Two bounds on a session, because they answer different questions: the
	// lifetime caps a cookie that keeps getting used, and the idle window
	// catches one that stopped being used at all.
	lifetime, err := time.ParseDuration(lookup("SESSION_LIFETIME", "720h"))
	if err != nil {
		return Config{}, fmt.Errorf("SESSION_LIFETIME: %w", err)
	}
	if lifetime <= 0 {
		return Config{}, fmt.Errorf("SESSION_LIFETIME must be positive, got %s", lifetime)
	}
	cfg.SessionLifetime = lifetime

	idle, err := time.ParseDuration(lookup("SESSION_IDLE_TIMEOUT", "168h"))
	if err != nil {
		return Config{}, fmt.Errorf("SESSION_IDLE_TIMEOUT: %w", err)
	}
	if idle <= 0 {
		return Config{}, fmt.Errorf("SESSION_IDLE_TIMEOUT must be positive, got %s", idle)
	}
	if idle > lifetime {
		return Config{}, fmt.Errorf(
			"SESSION_IDLE_TIMEOUT %s is longer than SESSION_LIFETIME %s, so it would never apply",
			idle, lifetime)
	}
	cfg.SessionIdle = idle

	inviteTTL, err := time.ParseDuration(lookup("INVITE_TTL", "48h"))
	if err != nil {
		return Config{}, fmt.Errorf("INVITE_TTL: %w", err)
	}
	if inviteTTL <= 0 {
		return Config{}, fmt.Errorf("INVITE_TTL must be positive, got %s", inviteTTL)
	}
	cfg.InviteTTL = inviteTTL

	invitationTTL, err := time.ParseDuration(lookup("INVITATION_TTL", "168h"))
	if err != nil {
		return Config{}, fmt.Errorf("INVITATION_TTL: %w", err)
	}
	if invitationTTL <= 0 {
		return Config{}, fmt.Errorf("INVITATION_TTL must be positive, got %s", invitationTTL)
	}
	cfg.InvitationTTL = invitationTTL
	cfg.InvitationFrom = strings.TrimSpace(lookup("INVITATION_FROM", ""))

	if raw := strings.TrimSpace(lookup("SIGNUP_ENABLED", "")); raw != "" {
		on, err := parseFeature(raw)
		if err != nil {
			return Config{}, fmt.Errorf("SIGNUP_ENABLED: %w", err)
		}
		cfg.SignupEnabled = on
	}

	ceiling, err := strconv.Atoi(strings.TrimSpace(lookup("SIGNUP_ORGANIZATION_CEILING", "1000")))
	if err != nil {
		return Config{}, fmt.Errorf("SIGNUP_ORGANIZATION_CEILING: must be a whole number")
	}
	if ceiling < 1 {
		return Config{}, fmt.Errorf("SIGNUP_ORGANIZATION_CEILING must be at least 1, got %d", ceiling)
	}
	cfg.SignupOrganizationCeiling = ceiling

	stale, err := time.ParseDuration(lookup("HUB_HEARTBEAT_STALE", "90s"))
	if err != nil {
		return Config{}, fmt.Errorf("HUB_HEARTBEAT_STALE: %w", err)
	}
	if stale <= 0 {
		return Config{}, fmt.Errorf("HUB_HEARTBEAT_STALE must be positive, got %s", stale)
	}
	cfg.HeartbeatStale = stale

	cfg.SupportTailnet, err = loadSupportTailnet()
	if err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// SupportTailnet is the private network a support engineer reaches an
// environment through, and the two OAuth clients the hub holds for it: one that
// mints keys for EnvironmentTag only, and one limited to private addresses
// carrying ServiceTag. Both secrets are loaded into memory and never written
// anywhere.
type SupportTailnet struct {
	Name string
	// Domain is the network's MagicDNS domain, such as tail1234.ts.net. A
	// private address's DNS name sits under it, and the access workload keeps
	// its certificate in a Secret of that name.
	Domain              string
	URL                 string
	KeyClientID         string
	KeyClientSecret     string
	ServiceClientID     string
	ServiceClientSecret string
	EnvironmentTag      string
	ServiceTag          string
}

// Configured reports whether the hub can mint keys and create addresses.
func (t SupportTailnet) Configured() bool { return t.Name != "" }

// loadSupportTailnet reads the network's settings. They are all or nothing: a
// hub with half of them would open windows it cannot take back, so a partial
// set is treated as none, with a warning, and the hub never calls the network.
// It does not stop the hub, because a setting added in a later release would
// otherwise take down sign-in and databases on every hub with the old set.
func loadSupportTailnet() (SupportTailnet, error) {
	read := func(key string) string { return strings.TrimSpace(lookup(key, "")) }
	out := SupportTailnet{
		Name:                read("SUPPORT_TAILNET"),
		Domain:              read("SUPPORT_TAILNET_DOMAIN"),
		URL:                 read("SUPPORT_TAILNET_URL"),
		KeyClientID:         read("SUPPORT_TAILNET_KEY_CLIENT_ID"),
		KeyClientSecret:     read("SUPPORT_TAILNET_KEY_CLIENT_SECRET"),
		ServiceClientID:     read("SUPPORT_TAILNET_SERVICE_CLIENT_ID"),
		ServiceClientSecret: read("SUPPORT_TAILNET_SERVICE_CLIENT_SECRET"),
		EnvironmentTag:      read("SUPPORT_TAILNET_ENVIRONMENT_TAG"),
		ServiceTag:          read("SUPPORT_TAILNET_SERVICE_TAG"),
	}
	required := []struct{ key, value string }{
		{"SUPPORT_TAILNET", out.Name},
		{"SUPPORT_TAILNET_DOMAIN", out.Domain},
		{"SUPPORT_TAILNET_KEY_CLIENT_ID", out.KeyClientID},
		{"SUPPORT_TAILNET_KEY_CLIENT_SECRET", out.KeyClientSecret},
		{"SUPPORT_TAILNET_SERVICE_CLIENT_ID", out.ServiceClientID},
		{"SUPPORT_TAILNET_SERVICE_CLIENT_SECRET", out.ServiceClientSecret},
	}
	var missing []string
	for _, r := range required {
		if r.value == "" {
			missing = append(missing, r.key)
		}
	}
	if len(missing) == len(required) {
		return SupportTailnet{}, nil
	}
	if len(missing) > 0 {
		slog.Warn("support access is off until these network settings are also set: " + strings.Join(missing, ", "))
		return SupportTailnet{}, nil
	}
	if out.EnvironmentTag == "" {
		out.EnvironmentTag = "tag:pds-environment"
	}
	if out.ServiceTag == "" {
		out.ServiceTag = "tag:pds-support"
	}
	if out.EnvironmentTag == out.ServiceTag {
		return SupportTailnet{}, errors.New("SUPPORT_TAILNET_SERVICE_TAG must differ from SUPPORT_TAILNET_ENVIRONMENT_TAG")
	}

	return out, nil
}

// String renders the configuration for startup logs. Secrets are reported as
// set or unset, never by value.
func (c Config) String() string {
	return fmt.Sprintf(
		"addr=%s env=%s log_level=%s shutdown_timeout=%s everest_url=%s everest_namespace=%s everest_token=%s pmm_url=%s pmm_token=%s oidc_issuer=%s servicenow_url=%s servicenow_api_key=%s servicenow_client_token=%s",
		c.Addr, c.Env, c.LogLevel, c.ShutdownTimeout,
		orUnset(c.EverestURL), c.EverestNamespace, presence(c.EverestToken),
		orUnset(c.PMMURL), presence(c.PMMToken), orUnset(c.OIDCIssuer),
		orUnset(c.ServiceNowURL), presence(c.ServiceNowAPIKey), presence(c.ServiceNowClientToken),
	)
}

// LogAttrs returns the startup log attributes, secrets excluded.
func (c Config) LogAttrs() []slog.Attr {
	return []slog.Attr{
		slog.String("addr", c.Addr),
		slog.String("env", string(c.Env)),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("everest_url", orUnset(c.EverestURL)),
		slog.String("everest_cluster", c.EverestCluster),
		slog.String("everest_namespace", c.EverestNamespace),
		slog.Bool("everest_token_configured", c.EverestToken != ""),
		slog.String("everest_monitoring_config", orUnset(c.EverestMonitoringConfig)),
		slog.String("pmm_url", orUnset(c.PMMURL)),
		slog.Bool("pmm_token_configured", c.PMMToken != ""),
		slog.String("oidc_issuer", orUnset(c.OIDCIssuer)),
		slog.Bool("oidc_client_secret_configured", c.OIDCClientSecret != ""),
		slog.String("servicenow_url", orUnset(c.ServiceNowURL)),
		slog.Bool("servicenow_configured", c.ServiceNowConfigured()),
		slog.Bool("database_configured", c.DatabaseConfigured()),
	}
}

// lookup returns the environment value, treating blank as unset.
func lookup(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

// lookupRenamed reads a setting that used to be called legacy. Deployments
// that have not renamed it keep working for one release; the new name wins.
func lookupRenamed(key, legacy, fallback string) string {
	return lookup(key, lookup(legacy, fallback))
}

func parseLevel(raw string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.TrimSpace(raw))); err != nil {
		return 0, fmt.Errorf("unknown level %q, want debug, info, warn, or error", raw)
	}

	return level, nil
}

func orUnset(v string) string {
	if v == "" {
		return "unset"
	}

	return v
}

func presence(v string) string {
	if v == "" {
		return "unset"
	}

	return "set"
}

// splitList reads a comma-separated environment value, dropping blanks.
func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}

	return out
}

// mintOperatorToken returns a random hub operator token. A hub that was not
// given one generates its own rather than refusing to start, so an operator
// never has to create a Secret by hand. The value lives for the life of the
// process: a restart mints a new one, and the UI reads the current value back
// from the adapter, so nothing has to be pasted anywhere.
func mintOperatorToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint hub operator token: %w", err)
	}

	return hex.EncodeToString(buf), nil
}

// validateHubURL rejects a value that cannot safely be written into a shell
// assignment or a YAML field, using the same origin rules the generated
// scripts apply at request time.
func validateHubURL(name, value string, allowPath bool) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if strings.ContainsAny(value, "\"'`$\\\n\r \t") {
		return fmt.Errorf("%s must not contain quotes, backticks, dollar signs, or whitespace", name)
	}
	if err := CheckEmbeddableURL(value, allowPath); err != nil {
		return fmt.Errorf("%s is not a usable origin", name)
	}

	return nil
}

// loadGitHubAppKeyFile fills the App private key from HUB_GITHUB_APP_PRIVATE_KEY_FILE.
func loadGitHubAppKeyFile(cfg *Config) error {
	path := strings.TrimSpace(lookup("HUB_GITHUB_APP_PRIVATE_KEY_FILE", ""))
	if path == "" {
		return nil
	}
	if cfg.HubGitHubAppPrivateKey != "" {
		return fmt.Errorf("HUB_GITHUB_APP_PRIVATE_KEY_FILE cannot be set with HUB_GITHUB_APP_PRIVATE_KEY. Pick one")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("HUB_GITHUB_APP_PRIVATE_KEY_FILE cannot be read: %w", err)
	}
	cfg.HubGitHubAppPrivateKey = string(raw)

	return nil
}

func validateHubGitHub(cfg Config) error {
	org := strings.TrimSpace(cfg.HubGitHubOrg)
	token := cfg.HubGitHubToken
	appSettings := map[string]string{
		"HUB_GITHUB_APP_ID":              cfg.HubGitHubAppID,
		"HUB_GITHUB_APP_INSTALLATION_ID": cfg.HubGitHubAppInstallationID,
		"HUB_GITHUB_APP_PRIVATE_KEY":     cfg.HubGitHubAppPrivateKey,
	}
	appSet := false
	for _, v := range appSettings {
		appSet = appSet || v != ""
	}
	if appSet {
		if token != "" {
			return fmt.Errorf("HUB_GITHUB_TOKEN cannot be set with the HUB_GITHUB_APP_ID settings. Pick one")
		}
		for _, name := range []string{"HUB_GITHUB_APP_ID", "HUB_GITHUB_APP_INSTALLATION_ID", "HUB_GITHUB_APP_PRIVATE_KEY"} {
			if appSettings[name] == "" {
				return fmt.Errorf("%s is required when another HUB_GITHUB_APP setting is set", name)
			}
		}
		if _, err := github.ParsePrivateKey([]byte(cfg.HubGitHubAppPrivateKey)); err != nil {
			return fmt.Errorf("HUB_GITHUB_APP_PRIVATE_KEY is not usable: %w", err)
		}
	}
	if org == "" && token == "" && !appSet {
		return nil
	}
	if org == "" {
		return fmt.Errorf("HUB_GITHUB_ORG is required when GitHub credentials are set")
	}
	if token == "" && !appSet {
		return fmt.Errorf("HUB_GITHUB_TOKEN or the HUB_GITHUB_APP_ID settings are required when HUB_GITHUB_ORG is set")
	}
	if strings.ContainsAny(org, "/\\\"'`$\n\r \t") {
		return fmt.Errorf("HUB_GITHUB_ORG must be an organization name")
	}

	return nil
}

// removedKeys are variables an earlier release read and this one does not.
// Each carries what to do instead, because a deployment that still sets one
// expects the old behavior.
var removedKeys = []struct{ name, instead string }{
	{"ANTHROPIC_API_KEY", "Paste the key in the console instead"},
	{"ANTHROPIC_BASE_URL", "Paste the key in the console instead"},
	{"CURSOR_API_KEY", "Paste the key in the console instead"},
	{"OIDC_AUDIENCE", "MCP clients sign in with OAuth at /oauth now, and the console sign in does not use it"},
	{"OIDC_DEVICE_CLIENT_ID", "MCP clients sign in with OAuth at /oauth now, and the console sign in does not use it"},
}

// warnRemovedKeys says, once per variable that is still set, that it is no
// longer read. The warning names the variable and never its value. An empty
// value stays quiet, the way every other variable here treats it.
func warnRemovedKeys() {
	for _, key := range removedKeys {
		if strings.TrimSpace(os.Getenv(key.name)) != "" {
			slog.Warn(key.name + " is no longer read. " + key.instead)
		}
	}
}

// featureEnvPrefix is how a surface flag is spelled in the environment.
const featureEnvPrefix = "ADAPTER_FEATURE_"

// loadFeatures reads the surface flags an operator set, leaving every other
// flag to its default. An empty value is treated as unset, the way every other
// variable here is, so blanking one in a shell or writing a null chart value
// leaves that surface alone rather than failing to parse.
//
// It walks the environment rather than the list of surfaces, so a variable
// that names no surface is refused instead of quietly doing nothing. A typo in
// a chart value would otherwise read as a surface left on.
//
// A retired name is the exception. The surface is gone, but a deployment may
// still set the variable for one release. The value is skipped, and one
// warning says the variable will be refused from a later release. An empty
// value stays unset and does not warn.
func loadFeatures() (map[string]bool, error) {
	byKey := make(map[string]string, len(features.Names()))
	for _, name := range features.Names() {
		byKey[features.EnvVar(name)] = name
	}

	retiredKey := make(map[string]struct{}, len(features.Retired()))
	for _, name := range features.Retired() {
		key := features.EnvVar(name)
		// A name in both lists is still published, so skipping the variable
		// would leave the surface on and ignore an operator who set it off.
		if _, published := byKey[key]; published {
			return nil, fmt.Errorf("%s is both a published surface and a retired flag", key)
		}
		retiredKey[key] = struct{}{}
	}

	var flags map[string]bool

	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(key, featureEnvPrefix) {
			continue
		}

		raw := strings.TrimSpace(value)
		if raw == "" {
			continue
		}

		if _, retired := retiredKey[key]; retired {
			slog.Warn(key + " is ignored and will be refused from a later release")

			continue
		}

		name, known := byKey[key]
		if !known {
			return nil, fmt.Errorf("%s names no product surface", key)
		}

		on, err := parseFeature(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}

		if flags == nil {
			flags = make(map[string]bool, len(byKey))
		}
		flags[name] = on
	}

	return flags, nil
}

// parseFeature reads the spellings of on and off that an operator reaches for
// in a values file.
func parseFeature(raw string) (bool, error) {
	switch strings.ToLower(raw) {
	case "true", "t", "1", "on", "yes", "y":
		return true, nil
	case "false", "f", "0", "off", "no", "n":
		return false, nil
	default:
		return false, fmt.Errorf("must be true or false, got %q", raw)
	}
}

// validateDatabaseURL checks the connection URL is one the PostgreSQL driver
// can use, so a typo fails the start rather than the first query.
//
// The error names the variable and nothing else. The URL carries the password,
// and a failed start prints this to stderr where it would be captured.
func validateDatabaseURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("DATABASE_URL is not a usable PostgreSQL connection URL")
	}

	switch parsed.Scheme {
	case "postgres", "postgresql":
	default:
		return fmt.Errorf("DATABASE_URL must use the postgres or postgresql scheme")
	}

	// Hostname rather than Host: url.Parse leaves Host as ":5432" for a URL
	// that names a port and no server, which is not empty.
	if parsed.Hostname() == "" {
		return fmt.Errorf("DATABASE_URL must name a host")
	}

	return nil
}

// AgentBackup names the IAM user an agent makes for its environment's backups
// and the Secret it writes the key to.
type AgentBackup struct {
	User        string
	Path        string
	BoundaryARN string
	Bucket      string
	// Secret is the Secret in the pod's namespace that holds the key.
	Secret string
	// Tag is the value of the percona.com/pds tag on the user.
	Tag    string
	Region string
}

var (
	iamName     = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{1,64}$`)
	iamPath     = regexp.MustCompile(`^/([A-Za-z0-9+=,.@_-]+/)+$`)
	boundaryARN = regexp.MustCompile(`^arn:aws:iam::[0-9]{12}:policy/([A-Za-z0-9+=,.@_-]+/)*[A-Za-z0-9+=,.@_-]+$`)
	bucketName  = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{2,62}$`)
	tagValue    = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)
)

// loadAgentBackup reads the backup key settings. The release sets them all
// together, so one that is set without the rest, or that cannot be used, is
// refused: the agent would otherwise never make a key and say nothing.
func loadAgentBackup(cfg *Config, lookup func(key, fallback string) string) error {
	user := strings.TrimSpace(lookup("AGENT_BACKUP_USER", ""))
	if user == "" {
		return nil
	}
	backup := AgentBackup{
		User:        user,
		Path:        strings.TrimSpace(lookup("AGENT_BACKUP_USER_PATH", "")),
		BoundaryARN: strings.TrimSpace(lookup("AGENT_BACKUP_BOUNDARY_ARN", "")),
		Bucket:      strings.TrimSpace(lookup("AGENT_BACKUP_BUCKET", "")),
		Secret:      strings.TrimSpace(lookup("AGENT_BACKUP_SECRET", "")),
		Tag:         strings.TrimSpace(lookup("AGENT_BACKUP_TAG", "")),
		Region:      strings.TrimSpace(lookup("AWS_REGION", "")),
	}
	for _, check := range []struct {
		name  string
		value string
		ok    *regexp.Regexp
	}{
		{"AGENT_BACKUP_USER", backup.User, iamName},
		{"AGENT_BACKUP_USER_PATH", backup.Path, iamPath},
		{"AGENT_BACKUP_BOUNDARY_ARN", backup.BoundaryARN, boundaryARN},
		{"AGENT_BACKUP_BUCKET", backup.Bucket, bucketName},
		{"AGENT_BACKUP_SECRET", backup.Secret, dnsLabel},
		{"AGENT_BACKUP_TAG", backup.Tag, tagValue},
		{"AWS_REGION", backup.Region, regionName},
	} {
		if !check.ok.MatchString(check.value) {
			return fmt.Errorf("%s: %q is not usable for the backup key", check.name, check.value)
		}
	}
	cfg.AgentBackup = backup

	return nil
}

var regionName = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)

// loadProvisioning reads the settings of provisioning runs and the Access
// window. Each value that is set must be usable: a typo would otherwise turn
// into a run that never gets a lease or a window that never closes.
func loadProvisioning(cfg *Config, lookup func(key, fallback string) string) error {
	for _, raw := range splitList(lookup("HUB_MANAGED_OUTGOING_CIDRS", "")) {
		if _, err := netip.ParsePrefix(raw); err != nil {
			return fmt.Errorf("HUB_MANAGED_OUTGOING_CIDRS: %q is not a CIDR such as 203.0.113.7/32", raw)
		}
		cfg.ManagedOutgoingCIDRs = append(cfg.ManagedOutgoingCIDRs, raw)
	}

	for _, d := range []struct {
		name     string
		fallback string
		into     *time.Duration
	}{
		{"HUB_PROVISION_LEASE_TTL", "1m", &cfg.ProvisionLeaseTTL},
		{"HUB_PROVISION_WINDOW_MAX_AGE", "30m", &cfg.ProvisionWindowMaxAge},
	} {
		value, err := time.ParseDuration(lookup(d.name, d.fallback))
		if err != nil {
			return fmt.Errorf("%s: %w", d.name, err)
		}
		if value <= 0 {
			return fmt.Errorf("%s must be positive, got %s", d.name, value)
		}
		*d.into = value
	}

	// The version is written into a HelmRelease, so only a release number passes.
	if version := strings.TrimSpace(lookup("HUB_MANAGED_CHART_VERSION", "")); version != "" {
		if !chartVersion.MatchString(version) {
			return fmt.Errorf("HUB_MANAGED_CHART_VERSION: %q is not a release such as 0.1.0", version)
		}
		cfg.ManagedChartVersion = version
	}

	// The address is written into a Flux HelmRepository, so only an OCI one passes.
	cfg.ManagedChartURL = strings.TrimSpace(lookup("HUB_MANAGED_CHART_URL", DefaultManagedChartURL))
	if !chartRegistry.MatchString(cfg.ManagedChartURL) {
		return fmt.Errorf("HUB_MANAGED_CHART_URL: %q is not an OCI registry such as %s", cfg.ManagedChartURL, DefaultManagedChartURL)
	}

	if ref := strings.TrimSpace(lookup("AGENT_RELEASE_REF", "")); ref != "" {
		namespace, name, ok := strings.Cut(ref, "/")
		if !ok || !dnsLabel.MatchString(namespace) || !dnsLabel.MatchString(name) {
			return fmt.Errorf("AGENT_RELEASE_REF: %q is not a namespace and a name such as flux-system/pds", ref)
		}
		cfg.AgentReleaseNamespace, cfg.AgentReleaseName = namespace, name
	}

	if err := loadAgentBackup(cfg, lookup); err != nil {
		return err
	}

	attempts, err := strconv.Atoi(lookup("HUB_PROVISION_MAX_ATTEMPTS", "5"))
	if err != nil || attempts < 1 {
		return errors.New("HUB_PROVISION_MAX_ATTEMPTS must be a whole number of at least 1")
	}
	cfg.ProvisionMaxAttempts = attempts

	return nil
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// DefaultManagedChartURL is where the published pds chart lives.
const DefaultManagedChartURL = "oci://ghcr.io/percona/charts"

var chartRegistry = regexp.MustCompile(`^oci://[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

var chartVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// cloudPrincipalARN is a role or user ARN. An account root is refused: a trust
// policy that names one lets anyone in that account assume the role.
var cloudPrincipalARN = regexp.MustCompile(`^arn:aws:iam::[0-9]{12}:(role|user)/[A-Za-z0-9+=,.@_/-]+$`)
