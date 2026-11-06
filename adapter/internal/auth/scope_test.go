package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
)

func TestAnIdentityCarriesItsScopeAndActsInItsOwnTeam(t *testing.T) {
	scope := auth.Scope{
		UserID: 7, OrganizationID: 3, Role: auth.RoleUser,
		Team: auth.Team{ID: 11, Name: "payments"},
	}

	ctx := auth.WithIdentity(t.Context(), auth.Identity{Subject: "bob", Scope: scope})

	got, ok := auth.ScopeFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, scope, got)
	team, ok := auth.TeamFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, uint(11), team)
}

func TestActingInAnotherTeamMovesTheTeamTheRowsAreReadUnder(t *testing.T) {
	ctx := auth.WithIdentity(t.Context(), auth.Identity{
		Subject: "root",
		Scope:   auth.Scope{OrganizationID: 3, Role: auth.RoleAdmin, Team: auth.Team{ID: 1, Name: "default"}},
	})

	ctx = auth.WithActingTeam(ctx, auth.Team{ID: 2, Name: "payments"})

	scope, _ := auth.ScopeFromContext(ctx)
	assert.Equal(t, "payments", scope.Team.Name)
	assert.Equal(t, auth.RoleAdmin, scope.Role, "acting in a Team does not change the role")
	team, _ := auth.TeamFromContext(ctx)
	assert.Equal(t, uint(2), team)
}

func TestAnIdentityWithoutAScopeAnswersNoScope(t *testing.T) {
	_, ok := auth.ScopeFromContext(auth.WithIdentity(t.Context(), auth.Identity{Subject: "x"}))

	assert.False(t, ok, "the pass-through stub and a bare login carry no Organization")
}

func TestOnlyAnAdminIsAnAdmin(t *testing.T) {
	assert.True(t, auth.Scope{Role: auth.RoleAdmin}.Admin())
	assert.False(t, auth.Scope{Role: auth.RoleUser}.Admin())
	assert.False(t, auth.Scope{}.Admin())
}
