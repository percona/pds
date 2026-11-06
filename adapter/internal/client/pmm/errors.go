package pmm

import (
	"errors"
	"fmt"
	"net/http"
)

// Sentinels the service layer switches on. Everything else from PMM is an
// opaque downstream failure that becomes a 502.
var (
	// ErrUnauthorized means the service account token is missing, expired, or
	// scoped too narrowly. It is a deployment problem, not a request problem.
	ErrUnauthorized = errors.New("pmm: unauthorized")
	// ErrBadQuery means PMM refused the expression the adapter built, which is
	// an adapter bug rather than a cluster fault.
	ErrBadQuery = errors.New("pmm: query rejected")
	// ErrUnreachable means the call never came back with a response: the name
	// did not resolve, the connection was refused, or PMM did not answer in
	// time. PMM is not up, which is a deployment state rather than a fault in
	// the query, so a reader reports it as pending.
	ErrUnreachable = errors.New("pmm: unreachable")
	// ErrUnavailable means something in front of PMM answered for it because
	// the upstream is not serving. PMM's own nginx returns this while Grafana
	// and the metrics store boot, so it is the same state as ErrUnreachable
	// seen one hop further out.
	ErrUnavailable = errors.New("pmm: not serving yet")
)

// APIError is a failed PMM query. Message is the `error` field of the
// Prometheus error body, which describes the query fault and is meant for
// logs. The token never appears here: only method, path, status, and message.
type APIError struct {
	Method  string
	Path    string
	Status  int
	Type    string
	Message string
}

// Error implements error.
func (e *APIError) Error() string {
	base := fmt.Sprintf("pmm: %s %s: %d %s", e.Method, e.Path, e.Status, http.StatusText(e.Status))
	if e.Message == "" {
		return base
	}

	return base + ": " + e.Message
}

// Unwrap maps the status onto a sentinel so callers can use errors.Is without
// reaching for the status code.
func (e *APIError) Unwrap() error {
	switch {
	// A gateway status is not PMM refusing the query, it is PMM not being
	// there to receive it.
	case e.Status == http.StatusBadGateway ||
		e.Status == http.StatusServiceUnavailable ||
		e.Status == http.StatusGatewayTimeout:
		return ErrUnavailable
	case e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden:
		return ErrUnauthorized
	case e.Status == http.StatusBadRequest || e.Type == "bad_data":
		return ErrBadQuery
	default:
		return nil
	}
}
