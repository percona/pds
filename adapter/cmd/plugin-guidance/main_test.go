package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/mcp"
)

// pluginDirectory is where the generated files are checked in.
const pluginDirectory = "../../../plugins/pds"

func TestCheckedInFilesMatchTheRegistry(t *testing.T) {
	for path, want := range generate(mcp.PluginGuidanceText()) {
		got, err := os.ReadFile(filepath.Join(pluginDirectory, path))
		require.NoError(t, err, "run: cd adapter && go run ./cmd/plugin-guidance ../plugins/pds")
		assert.Equal(t, want, string(got), "%s is out of date: run cd adapter && go run ./cmd/plugin-guidance ../plugins/pds, then bump the plugin version", path)
	}
}

func TestSkillCarriesTheRegistrysInstructionsAndOffer(t *testing.T) {
	text := mcp.PluginGuidanceText()
	files := generate(text)
	skill := files["skills/databases/SKILL.md"]

	assert.Contains(t, skill, text.Instructions)
	assert.Contains(t, skill, "name: databases\n")
	assert.Contains(t, skill, text.Nudge, "the description says when to use the skill, from the registry's offer")
	assert.Contains(t, text.Instructions, text.Nudge)
}

func TestSkillNamesEveryPromptOfTheRegistry(t *testing.T) {
	text := mcp.PluginGuidanceText()
	require.NotEmpty(t, text.Prompts)

	skill := generate(text)["skills/databases/SKILL.md"]
	for _, prompt := range text.Prompts {
		assert.Contains(t, skill, "`"+prompt.Name+"`")
		assert.Contains(t, skill, prompt.Title)
	}
}

func TestRuleIsAlwaysOnAndHoldsTheOffer(t *testing.T) {
	text := mcp.PluginGuidanceText()
	rule := generate(text)["rules/use-pds.mdc"]

	assert.True(t, strings.HasPrefix(rule, "---\n"))
	assert.Contains(t, rule, "alwaysApply: true\n")
	assert.Contains(t, rule, text.Nudge)
}

func TestDescriptionIsOneYAMLLine(t *testing.T) {
	skill := generate(mcp.PluginGuidanceText())["skills/databases/SKILL.md"]
	front, _, found := strings.Cut(strings.TrimPrefix(skill, "---\n"), "\n---\n")
	require.True(t, found)

	var description string
	for _, line := range strings.Split(front, "\n") {
		if value, ok := strings.CutPrefix(line, "description: "); ok {
			description = value
		}
	}
	require.NotEmpty(t, description)
	assert.True(t, strings.HasPrefix(description, `"`) && strings.HasSuffix(description, `"`))
	assert.NotContains(t, description, "\n")
	assert.True(t, strings.HasPrefix(description, `"Percona Database Service (PDS) runs `), "the description opens with the registry's first sentence, not its heading")
}

func TestGeneratedTextIsDeterministic(t *testing.T) {
	assert.Equal(t, generate(mcp.PluginGuidanceText()), generate(mcp.PluginGuidanceText()))
}
