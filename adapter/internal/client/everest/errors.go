package everest

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Sentinels the service layer switches on. Everything else from Everest is an
// opaque downstream failure that becomes a 502.
var (
	// ErrNotFound is a 404 from Everest.
	ErrNotFound = errors.New("everest: not found")
	// ErrConflict means an object of that name already exists.
	ErrConflict = errors.New("everest: already exists")
	// ErrUnauthorized is a refused bearer token. The token is a session that
	// expires, so this is the ordinary end of one rather than a fault.
	ErrUnauthorized = errors.New("everest: not authorized")
)

// duplicateMessage is how openeverest 1.16.2 words a duplicate create. It is
// needed because the server answers 500 rather than 409 for that case, which
// was captured from the live API on 2026-08-25 and is in
// fixtures/error-already-exists.json. Matching on the message is narrow enough
// that an unrelated 500 still reads as a downstream failure.
const duplicateMessage = "already exists"

// APIError is a non-2xx response from Everest. Message is the `message` field
// of the Everest error body, which describes the Kubernetes failure and is
// meant for logs, not for a developer in the UI.
type APIError struct {
	Method  string
	Path    string
	Status  int
	Message string
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("everest: %s %s: %d %s", e.Method, e.Path, e.Status, http.StatusText(e.Status))
	}

	return fmt.Sprintf("everest: %s %s: %d %s: %s",
		e.Method, e.Path, e.Status, http.StatusText(e.Status), e.Message)
}

// Unwrap maps the status onto a sentinel so callers can use errors.Is without
// reaching for the status code.
func (e *APIError) Unwrap() error {
	switch {
	case e.Status == http.StatusNotFound:
		return ErrNotFound
	case e.Status == http.StatusConflict:
		return ErrConflict
	case e.Status == http.StatusUnauthorized, e.Status == http.StatusForbidden:
		return ErrUnauthorized
	case strings.Contains(e.Message, duplicateMessage):
		return ErrConflict
	default:
		return nil
	}
}
