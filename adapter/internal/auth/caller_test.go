package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
)

func TestACallerRidesTheContextItWasPutOn(t *testing.T) {
	caller := auth.Caller{
		Identity: auth.Identity{
			Subject: "ann",
			Scope:   auth.Scope{UserID: 2, OrganizationID: 1, Role: auth.RoleUser, Team: auth.Team{ID: 5, Name: "default"}},
			Client:  auth.Client{Kind: auth.ClientMCP},
		},
		Access: auth.AccessRead,
		Agent:  "Claude Code",
	}

	got, ok := auth.CallerFromContext(auth.WithCaller(t.Context(), caller))

	require.True(t, ok)
	assert.Equal(t, caller, got)
}

func TestAContextWithNoCallerHasNone(t *testing.T) {
	_, ok := auth.CallerFromContext(t.Context())

	assert.False(t, ok)
}

func TestEachTierAllowsItsOwnToolsAndTheOnesBelowIt(t *testing.T) {
	cases := []struct {
		have, need auth.Access
		allowed    bool
	}{
		{auth.AccessRead, auth.AccessRead, true},
		{auth.AccessRead, auth.AccessChange, false},
		{auth.AccessRead, auth.AccessChangeDelete, false},
		{auth.AccessChange, auth.AccessRead, true},
		{auth.AccessChange, auth.AccessChange, true},
		{auth.AccessChange, auth.AccessChangeDelete, false},
		{auth.AccessChangeDelete, auth.AccessRead, true},
		{auth.AccessChangeDelete, auth.AccessChange, true},
		{auth.AccessChangeDelete, auth.AccessChangeDelete, true},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.allowed, tc.have.Allows(tc.need), "%q needing %q", tc.have, tc.need)
	}
}

func TestATierThatIsNotKnownReadsAndNothingMore(t *testing.T) {
	for _, have := range []auth.Access{"", "admin", "CHANGE"} {
		assert.True(t, have.Allows(auth.AccessRead), "%q", have)
		assert.False(t, have.Allows(auth.AccessChange), "%q", have)
		assert.False(t, have.Allows(auth.AccessChangeDelete), "%q", have)
	}
}

func TestLowestOfTwoTiers(t *testing.T) {
	cases := []struct {
		a, b, want auth.Access
	}{
		{auth.AccessChangeDelete, auth.AccessRead, auth.AccessRead},
		{auth.AccessRead, auth.AccessChangeDelete, auth.AccessRead},
		{auth.AccessChange, auth.AccessChangeDelete, auth.AccessChange},
		{auth.AccessChangeDelete, auth.AccessChangeDelete, auth.AccessChangeDelete},
		{auth.AccessChange, "surprise", auth.AccessRead},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, auth.Lowest(tc.a, tc.b), "%q and %q", tc.a, tc.b)
	}
}
