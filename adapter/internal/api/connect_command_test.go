package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJoinCommandQuotesTheHubURLAndToken(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		"curl -fsSL 'https://hub.example/api/internal/connect/bootstrap.sh' | bash -s -- --token 'jnt_abc'",
		joinCommand("https://hub.example", "jnt_abc"),
	)
	assert.Equal(t,
		"curl -fsSL 'https://hub.example/; curl evil/api/internal/connect/bootstrap.sh' | bash -s -- --token 'jnt_abc'",
		joinCommand("https://hub.example/; curl evil", "jnt_abc"),
	)
	assert.NotContains(t, joinCommand("https://hub.example/;id", "jnt_abc"), "curl -fsSL https://hub.example/;id")
	assert.Equal(t, "", joinCommand("", "jnt_abc"))
	assert.Equal(t, "", joinCommand("https://hub.example", ""))
}

func TestCheckEmbeddableURLRejectsShellHosts(t *testing.T) {
	t.Parallel()

	assert.NoError(t, checkEmbeddableURL("https://hub.example", false))
	assert.NoError(t, checkEmbeddableURL("http://localhost:8080", false))
	assert.NoError(t, checkEmbeddableURL("https://github.com/example/pds.git", true))
	assert.Error(t, checkEmbeddableURL("http://$(id)", false))
	assert.Error(t, checkEmbeddableURL("https://hub.example/; curl evil", false))
	assert.Error(t, checkEmbeddableURL("https://hub.example/$(id)", true))
	assert.Error(t, checkEmbeddableURL("https://user:pass@hub.example", false))
	assert.Error(t, checkEmbeddableURL("https://hub.example?next=1", false))
	assert.Error(t, checkEmbeddableURL("https://hub.example#top", false))
	assert.Error(t, checkEmbeddableURL("ssh://git@github.com/percona/basecamp.git", true))
	assert.Error(t, checkEmbeddableURL("https://hub.example/connect", false))
}
