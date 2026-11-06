package features_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/features"
)

func newSet(t *testing.T, values map[string]bool) *features.Set {
	t.Helper()

	set, err := features.New(values)
	require.NoError(t, err)
	t.Cleanup(func() { _ = set.Close(context.Background()) })

	return set
}

func TestNamesCoverEverySurface(t *testing.T) {
	want := []string{
		features.Databases,
		features.Clusters,
		features.Backups,
		features.Connections,
		features.Support,
		features.SupportAccess,
		features.Chat,
		features.Assistant,
		features.Metrics,
		features.Activity,
		features.MCP,
	}

	assert.Equal(t, want, features.Names())
}

func TestRetiredList(t *testing.T) {
	assert.Equal(t, []string{
		features.AlertAnalysis,
		features.Logs,
		features.Recommendations,
		features.Investigate,
		features.PlatformStatus,
		features.Alerts,
		features.AssistantOpenAI,
		features.AssistantGemini,
		features.AssistantOpenRouter,
	}, features.Retired())
	assert.False(t, features.Known(features.AlertAnalysis))
	assert.False(t, features.Known(features.Logs))
	assert.False(t, features.Known(features.Recommendations))
	assert.False(t, features.Known(features.Investigate))
	assert.False(t, features.Known(features.PlatformStatus))
	assert.False(t, features.Known(features.Alerts))
	assert.False(t, features.Known(features.AssistantOpenAI))
	assert.False(t, features.Known(features.AssistantGemini))
	assert.False(t, features.Known(features.AssistantOpenRouter))
	assert.False(t, features.Known("logs"))
	assert.False(t, features.Known("alerts"))
	for _, name := range features.Retired() {
		assert.False(t, features.Known(name), "%s is both published and retired", name)
		assert.NotContains(t, features.Names(), name)
	}
}

func TestResolveOmitsARetiredName(t *testing.T) {
	const retired = "retired-surface"
	restore := features.SetRetiredForTest([]string{retired})
	t.Cleanup(restore)

	assert.NotContains(t, features.Names(), retired)

	set := newSet(t, nil)
	_, published := set.Resolve(context.Background())[retired]
	assert.False(t, published, "a retired name is not a flag the UI is told about")
}

func TestNamesAreStableAcrossCalls(t *testing.T) {
	first := features.Names()
	first[0] = "mutated"

	assert.Equal(t, features.Databases, features.Names()[0])
}

func TestDefaultsLeaveEverySurfaceOn(t *testing.T) {
	set := newSet(t, nil)

	for _, name := range features.Names() {
		assert.True(t, set.Enabled(context.Background(), name), "%s should default on", name)
	}
}

func TestUnknownFlagIsOn(t *testing.T) {
	set := newSet(t, nil)

	// A name the adapter does not publish must not read as off, or a stale UI
	// asking about a surface that moved would hide it.
	assert.True(t, set.Enabled(context.Background(), "not-a-surface"))
}

func TestNewRejectsAnUnknownName(t *testing.T) {
	_, err := features.New(map[string]bool{"not-a-surface": false})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-a-surface")
}

func TestMCPIsOnByDefaultAndOffWhenConfigured(t *testing.T) {
	ctx := context.Background()

	assert.True(t, features.Known(features.MCP))
	assert.True(t, newSet(t, nil).Enabled(ctx, features.MCP))
	assert.False(t, newSet(t, map[string]bool{features.MCP: false}).Enabled(ctx, features.MCP))
}

func TestEnabledReadsTheConfiguredValue(t *testing.T) {
	set := newSet(t, map[string]bool{features.Chat: false})

	ctx := context.Background()
	assert.False(t, set.Enabled(ctx, features.Chat))
	assert.True(t, set.Enabled(ctx, features.Databases))
}

func TestResolvePublishesEveryName(t *testing.T) {
	set := newSet(t, map[string]bool{features.Assistant: false})

	got := set.Resolve(context.Background())

	require.Len(t, got, len(features.Names()))
	for _, name := range features.Names() {
		_, ok := got[name]
		assert.True(t, ok, "%s missing from the published set", name)
	}
	assert.False(t, got[features.Assistant])
	for _, name := range features.Retired() {
		_, ok := got[name]
		assert.False(t, ok, "%s is retired and not published", name)
	}
	_, recommendations := got[features.Recommendations]
	assert.False(t, recommendations, "a retired name is absent from the resolved set")
	_, investigate := got[features.Investigate]
	assert.False(t, investigate, "a retired name is absent from the resolved set")
	_, platformStatus := got[features.PlatformStatus]
	assert.False(t, platformStatus, "a retired name is absent from the resolved set")
	_, alerts := got[features.Alerts]
	assert.False(t, alerts, "a retired name is absent from the resolved set")
	assert.True(t, got[features.Databases])
}

func TestEnvVarNamesTheVariableAnOperatorSets(t *testing.T) {
	tests := []struct{ flag, want string }{
		{features.Databases, "ADAPTER_FEATURE_DATABASES"},
		{features.PlatformStatus, "ADAPTER_FEATURE_PLATFORM_STATUS"},
		{features.AlertAnalysis, "ADAPTER_FEATURE_ALERT_ANALYSIS"},
		{features.AssistantOpenAI, "ADAPTER_FEATURE_ASSISTANT_OPENAI"},
		{features.MCP, "ADAPTER_FEATURE_MCP"},
	}

	for _, tc := range tests {
		assert.Equal(t, tc.want, features.EnvVar(tc.flag))
	}
}

func TestLabelReadsAsASentence(t *testing.T) {
	assert.Equal(t, "databases", features.Label(features.Databases))
	assert.Equal(t, "platform status", features.Label(features.PlatformStatus))
	assert.Equal(t, "alert analysis", features.Label(features.AlertAnalysis))
}
