package servicenow

import (
	"errors"
	"fmt"
	"net/http"
)

// Sentinels the service layer switches on. Everything else from ServiceNow is
// an opaque downstream failure that becomes a 502.
var (
	// ErrNotFound is a ticket number the account cannot see.
	ErrNotFound = errors.New("servicenow: ticket not found")
	// ErrResponseTooLarge is a body past the cap the client reads. Decoding it
	// would report malformed json for what is really a size problem.
	ErrResponseTooLarge = errors.New("servicenow: response too large")
	// ErrUnauthorized is a rejected API key or client token. It is a
	// configuration fault, not a transient one, so it is never retried.
	ErrUnauthorized = errors.New("servicenow: not authorized")
	// ErrRejected is a 200 that says the connector refused the request.
	ErrRejected = errors.New("servicenow: request rejected")
)

// Forbidden reports whether err is the instance refusing a request it did
// authenticate. That is a permission on the route, which is granted separately
// from the key itself, so it is not the same fault as a rejected key.
func Forbidden(err error) bool {
	var apiErr *APIError

	return errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden
}

// APIError is a failed ServiceNow call. Message comes from the connector and is
// meant for logs: it names ServiceNow internals a developer in the UI has no use
// for. Neither the API key nor the client token is ever part of it, because
// neither is part of the URL and the body is not echoed back.
type APIError struct {
	Method string
	Path   string
	Status int
	// Message is the reason ServiceNow gave, if it gave one.
	Message string
	// Rejected marks a 2xx whose body said the request was refused.
	Rejected bool
}

// Error implements error.
func (e *APIError) Error() string {
	prefix := fmt.Sprintf("servicenow: %s %s: %d %s",
		e.Method, e.Path, e.Status, http.StatusText(e.Status))

	if e.Rejected {
		prefix += ": rejected"
	}

	if e.Message == "" {
		return prefix
	}

	return prefix + ": " + e.Message
}

// Unwrap maps the failure onto a sentinel so callers use errors.Is rather than
// reading status codes.
func (e *APIError) Unwrap() error {
	switch {
	case e.Rejected:
		return ErrRejected
	case e.Status == http.StatusUnauthorized, e.Status == http.StatusForbidden:
		return ErrUnauthorized
	case e.Status == http.StatusNotFound:
		return ErrNotFound
	default:
		return nil
	}
}
