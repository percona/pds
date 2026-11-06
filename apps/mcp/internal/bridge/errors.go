package bridge

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/percona/pds/apps/mcp/internal/messages"
	"github.com/percona/pds/apps/mcp/internal/oauth"
	"github.com/percona/pds/apps/mcp/internal/tokenstore"
)

// insufficientScopeError is a 403 with error="insufficient_scope": the sign in does not allow what
// was asked. The bridge never starts a browser to widen it; it names the scope and the command.
type insufficientScopeError struct{ scope string }

func (e *insufficientScopeError) Error() string {
	return "the sign in does not allow " + e.scope
}

// statusError is a refusal that is not worth retrying: a status the control plane answered with,
// and nothing from its body.
type statusError struct{ status int }

func (e *statusError) Error() string {
	return "the control plane answered with a refusal"
}

// describe is what a person or an agent is told for a failure. Nothing from the error itself is
// repeated, so a token or an address inside it cannot reach them.
func describe(err error) string {
	var scope *insufficientScopeError
	var status *statusError

	switch {
	case errors.As(err, &scope):
		return messages.NeedsPermission(scope.scope)
	case errors.Is(err, tokenstore.ErrNoLogin):
		return messages.NotSignedIn
	case errors.Is(err, tokenstore.ErrUnreadable):
		return messages.LoginUnreadable
	case errors.Is(err, oauth.ErrSessionEnded):
		return messages.SessionEnded
	case errors.Is(err, oauth.ErrNotServed):
		return messages.NotServed
	case errors.As(err, &status) && (status.status == 401 || status.status == 403):
		return messages.SessionEnded
	case errors.As(err, &status):
		return messages.Refused
	case errors.Is(err, context.Canceled):
		return messages.Unreachable
	default:
		return messages.Unreachable
	}
}

// rejectedCode is the code the SDK gives its own "rejected by transport" marker, which is not an
// answer from the control plane.
const rejectedCode = -32005

// serverError finds the JSON-RPC error the control plane itself answered with inside what a write
// returned, so it can be passed to the agent as it came.
func serverError(err error) *jsonrpc.Error {
	var wire *jsonrpc.Error
	if errors.As(err, &wire) && wire.Code != rejectedCode {
		return wire
	}

	switch u := err.(type) {
	case interface{ Unwrap() []error }:
		for _, inner := range u.Unwrap() {
			if found := serverError(inner); found != nil {
				return found
			}
		}
	case interface{ Unwrap() error }:
		if inner := u.Unwrap(); inner != nil {
			return serverError(inner)
		}
	}

	return nil
}
