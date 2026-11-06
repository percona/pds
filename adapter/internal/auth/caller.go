package auth

import "context"

// Access is the tier of tools a caller may use. A tier includes the ones
// below it.
type Access string

const (
	// AccessRead may read and nothing else.
	AccessRead Access = "read"
	// AccessChange may also create, scale, back up, restore and open tickets.
	AccessChange Access = "change"
	// AccessChangeDelete may also delete a database.
	AccessChangeDelete Access = "change_delete"
)

// Allows reports whether a caller at this tier may use a tool that needs the
// given one. A tier that is not known may read and nothing more, so a caller
// built wrong cannot change anything.
func (a Access) Allows(needed Access) bool { return a.rank() >= needed.rank() }

// Valid reports whether this is one of the three tiers.
func (a Access) Valid() bool {
	switch a {
	case AccessRead, AccessChange, AccessChangeDelete:
		return true
	default:
		return false
	}
}

// Lowest is the tier that allows the least of the two. A tier that is not known
// counts as read.
func Lowest(a, b Access) Access {
	if a.rank() <= b.rank() {
		return a.known()
	}

	return b.known()
}

// known is the tier itself, or read for one that is not known.
func (a Access) known() Access {
	switch a {
	case AccessChange, AccessChangeDelete:
		return a
	default:
		return AccessRead
	}
}

func (a Access) rank() int {
	switch a {
	case AccessChange:
		return 1
	case AccessChangeDelete:
		return 2
	default:
		return 0
	}
}

// Caller is who an in-process request acts for: the Chat or an MCP client
// the control plane has already authenticated. Identity is the signed in
// person, their Organization, the Team the request acts in and how they signed
// in. Agent names the program that is calling, such as "Claude Code".
type Caller struct {
	Identity Identity
	Access   Access
	Agent    string
}

type callerKey struct{}

// WithCaller returns a context that carries the caller. The router's
// authentication accepts it in place of a session, and only a request that
// was built in this process can hold it: a context value cannot arrive in an
// HTTP request, whatever its headers say.
func WithCaller(ctx context.Context, caller Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, caller)
}

// CallerFromContext returns the caller an in-process request acts for.
func CallerFromContext(ctx context.Context) (Caller, bool) {
	caller, ok := ctx.Value(callerKey{}).(Caller)

	return caller, ok
}
