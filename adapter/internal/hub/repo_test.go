package hub_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/percona/pds/adapter/internal/hub"
)

func TestGitDirKeepsTheRootWhenNotPrefixed(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/git", hub.GitDir("/git", 7, false))
}

func TestGitDirUsesATeamSubdirectoryWhenPrefixed(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/git/7", hub.GitDir("/git", 7, true))
}

func TestGitDirKeepsTheRootForNoTeam(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/git", hub.GitDir("/git", 0, true))
}

func TestRepoPrefixJoinsOrganizationAndTeamAsSlugs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		organization string
		team         string
		want         string
	}{
		{name: "plain", organization: "acme", team: "platform", want: "acme-platform"},
		{name: "free text", organization: "Acme Co.", team: "Data & ML", want: "acme-co-data-ml"},
		{name: "long names are cut", organization: "percona-engineering-platform", team: "database-reliability-team", want: "percona-engineer-database-reliabi"},
		{name: "blank team", organization: "acme", team: " ", want: "acme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, hub.RepoPrefix(tc.organization, tc.team))
		})
	}
}
