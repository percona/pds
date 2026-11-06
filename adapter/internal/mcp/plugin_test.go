package mcp_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/mcp"
)

func TestPluginGuidanceTextIsTheFullTierWordsOfTheRegistry(t *testing.T) {
	text := mcp.PluginGuidanceText()

	assert.Contains(t, text.Instructions, "## Making changes")
	assert.Contains(t, text.Instructions, "delete_database")
	assert.Contains(t, text.Instructions, text.Nudge)
	assert.NotEmpty(t, text.Nudge)

	names := []string{}
	for _, prompt := range text.Prompts {
		assert.NotEmpty(t, prompt.Title)
		assert.NotEmpty(t, prompt.Description)
		names = append(names, prompt.Name)
	}
	require.ElementsMatch(t, []string{"connect_app", "investigate_database", "new_database", "set_up_backups"}, names)
}
