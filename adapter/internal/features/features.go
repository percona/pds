// Package features resolves which product surfaces this adapter serves.
//
// Every surface has a named flag, every flag defaults on, and resolution runs
// in process through OpenFeature against an in-memory provider built from
// configuration. There is no flag server to reach and nothing to poll: the set
// is fixed for the life of the process, so a flag change is a restart.
//
// A flag whose surface has been deleted is listed in retired for one release
// and is not published. The release after that drops the name, and a variable
// that still uses it is refused.
package features

import (
	"context"
	"fmt"

	"slices"
	"strings"

	"github.com/open-feature/go-sdk/openfeature"
	"github.com/open-feature/go-sdk/openfeature/isolated"
	"github.com/open-feature/go-sdk/openfeature/memprovider"
)

// Flag names. These are the contract the UI reads, so they are also the keys
// GET /api/features publishes and the suffixes of the environment variables an
// operator sets.
const (
	Databases   = "databases"
	Clusters    = "clusters"
	Backups     = "backups"
	Connections = "connections"
	Support     = "support"
	// SupportAccess is the support access window on an environment. It is
	// off on a hub without the private network the window runs over.
	SupportAccess = "support-access"
	Chat          = "chat"
	Assistant     = "assistant"
	Metrics       = "metrics"
	Activity      = "activity"
	MCP           = "mcp"
)

// names is the published order: the core paths first, then the surfaces built
// on top of them.
var names = []string{
	Databases,
	Clusters,
	Backups,
	Connections,
	Support,
	SupportAccess,
	Chat,
	Assistant,
	Metrics,
	Activity,
	MCP,
}

// AlertAnalysis, Logs, Recommendations, Investigate, PlatformStatus, Alerts and
// the three model provider flags name flags whose surfaces were deleted. A
// deployment that still sets one of these variables starts, with one warning
// per variable, for this one release. The next release drops the name and the
// variable is refused.
const (
	AlertAnalysis   = "alert-analysis"
	Logs            = "logs"
	Recommendations = "recommendations"
	Investigate     = "investigate"
	PlatformStatus  = "platform-status"
	Alerts          = "alerts"

	// The Chat keeps Anthropic and Cursor. The providers that had a flag each
	// are gone.
	AssistantOpenAI     = "assistant-openai"
	AssistantGemini     = "assistant-gemini"
	AssistantOpenRouter = "assistant-openrouter"
)

// retired is that one-release list, in the order the names were retired.
// GET /api/features publishes names, never this list. A name in both lists is
// refused at startup: skipping the variable would leave the surface on.
var retired = []string{
	AlertAnalysis, Logs, Recommendations, Investigate, PlatformStatus, Alerts,
	AssistantOpenAI, AssistantGemini, AssistantOpenRouter,
}

// retiredOverride is the test seam. Nil uses retired. A test installs names
// and restores the previous value, so the production list is unchanged.
var retiredOverride *[]string

// Retired returns the flags ignored for one release after their surface is
// deleted. The caller gets a copy.
func Retired() []string {
	if retiredOverride != nil {
		return slices.Clone(*retiredOverride)
	}

	return slices.Clone(retired)
}

// SetRetiredForTest installs names until restore runs. Pass a name that is not
// a published surface. The production list is left as it is.
func SetRetiredForTest(names []string) (restore func()) {
	previous := retiredOverride
	copied := slices.Clone(names)
	retiredOverride = &copied

	return func() { retiredOverride = previous }
}

// envPrefix is how an operator names a flag in the environment. The chart sets
// these from its values.
const envPrefix = "ADAPTER_FEATURE_"

// Names returns every flag name, in publication order. The caller gets a copy,
// so a caller that sorts or filters cannot reorder the package's own list.
func Names() []string { return slices.Clone(names) }

// Known reports whether a name is a flag this adapter publishes.
func Known(name string) bool { return slices.Contains(names, name) }

// EnvVar returns the environment variable a flag is read from.
func EnvVar(name string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// Label renders a flag name the way a sentence reads it.
func Label(name string) string {
	return strings.ReplaceAll(name, "-", " ")
}

// The two variants every flag carries. OpenFeature resolves a variant rather
// than a bare value, so an on and an off variant is the smallest shape that
// still reads as a boolean flag.
const (
	variantOn  = "on"
	variantOff = "off"
)

// Set answers whether a surface is on. It holds its own OpenFeature API rather
// than the package-level singleton, so two sets in one process (a test suite,
// say) cannot overwrite each other's flags.
type Set struct {
	api    *openfeature.EvaluationAPI
	client *openfeature.Client
}

// New builds a set from explicit values. A name missing from values keeps its
// default, which is on. An unrecognized name is an error rather than a silent
// no-op, because it is almost always a typo in a chart value.
func New(values map[string]bool) (*Set, error) {
	for name := range values {
		if !Known(name) {
			return nil, fmt.Errorf("%q is not a feature flag this adapter publishes", name)
		}
	}

	flags := make(map[string]memprovider.InMemoryFlag, len(names))
	for _, name := range names {
		variant := variantOn
		if on, ok := values[name]; ok && !on {
			variant = variantOff
		}

		flags[name] = memprovider.InMemoryFlag{
			Key:            name,
			State:          memprovider.Enabled,
			DefaultVariant: variant,
			Variants:       map[string]any{variantOn: true, variantOff: false},
		}
	}

	api := isolated.NewAPI()
	provider := memprovider.NewInMemoryProvider(flags)
	if err := api.SetProviderAndWait(context.Background(), provider); err != nil {
		return nil, fmt.Errorf("install the feature flag provider: %w", err)
	}

	return &Set{api: api, client: api.NewClient()}, nil
}

// Enabled reports whether a surface is on. An unknown name reads as on: the UI
// and the adapter ship separately, so a name one side has not heard of yet must
// not hide the surface behind it.
func (s *Set) Enabled(ctx context.Context, name string) bool {
	if s == nil {
		return true
	}

	return s.client.Boolean(ctx, name, true, openfeature.EvaluationContext{})
}

// Resolve returns every published flag, which is what the UI reads.
func (s *Set) Resolve(ctx context.Context) map[string]bool {
	resolved := make(map[string]bool, len(names))
	for _, name := range names {
		resolved[name] = s.Enabled(ctx, name)
	}

	return resolved
}

// Close releases the OpenFeature API this set owns.
func (s *Set) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}

	return s.api.Shutdown(ctx)
}
