package service

import (
	"context"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/store"
)

// AccessLimits reads what an Organization lets agents and the Chat do. The
// Directory satisfies it.
type AccessLimits interface {
	AI(ctx context.Context, organizationID uint) (store.AISettings, error)
}

// CallerAccess is the tier a caller's tools are served at: the lowest of the
// Organization's limit and what the person's role allows. Both roles may
// change and delete in the Teams they act in, so today the role only caps a
// role that is not known at read.
func CallerAccess(limit auth.Access, role string) auth.Access {
	return auth.Lowest(limit, roleAccess(role))
}

func roleAccess(role string) auth.Access {
	switch role {
	case auth.RoleAdmin, auth.RoleUser:
		return auth.AccessChangeDelete
	default:
		return auth.AccessRead
	}
}
