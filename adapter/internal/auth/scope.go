package auth

import "context"

// Roles a User holds in an Organization.
const (
	// RoleUser works in their own Team.
	RoleUser = "user"
	// RoleAdmin manages the Organization and acts in any of its Teams.
	RoleAdmin = "admin"
)

// Team is the unit of isolation a request acts in. Its id is what the stores
// read rows under.
type Team struct {
	ID   uint
	Name string
}

// Scope is what a request may reach: who is acting, the Organization they act
// in, their role there, and the Team the request acts in. Services take one,
// never a bare Team id, so a call site cannot name a Team the caller does not
// belong to.
type Scope struct {
	UserID uint
	// UserName and OrganizationName are what the console shows; no decision
	// reads them.
	UserName         string
	OrganizationID   uint
	OrganizationName string
	Role             string
	Team             Team
}

// Admin reports whether the caller is an Organization admin.
func (s Scope) Admin() bool { return s.Role == RoleAdmin }

type scopeKey struct{}

// ScopeFromContext returns the scope of the request. It answers false under
// the stub and for an identity that names no Organization.
func ScopeFromContext(ctx context.Context) (Scope, bool) {
	scope, ok := ctx.Value(scopeKey{}).(Scope)

	return scope, ok
}

// WithActingTeam returns a context that acts in another Team of the same
// Organization. The caller decides that the scope may; this only moves the
// Team the rows are read under, and leaves the role as it was.
func WithActingTeam(ctx context.Context, team Team) context.Context {
	scope, _ := ScopeFromContext(ctx)
	scope.Team = team

	ctx = context.WithValue(ctx, scopeKey{}, scope)

	return WithTeam(ctx, team.ID)
}
