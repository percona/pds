package api

import (
	"fmt"
	"net/http"
)

// Error codes the API returns. These five are the contract the UI switches on.
// CodeInternal is the fallback for an unexpected failure, such as a recovered
// panic, and carries no detail.
const (
	CodeValidation     = "ErrValidation"
	CodeNotFound       = "ErrNotFound"
	CodeConflict       = "ErrConflict"
	CodeDownstream     = "ErrDownstream"
	CodeNotImplemented = "ErrNotImplemented"
	CodeInternal       = "ErrInternal"
	// CodeForbidden answers a signed in caller whose role the route refuses.
	// It is 403, where another Team's object is 404.
	CodeForbidden = "ErrForbidden"
)

// AppError is the structured error handlers return and Respond renders.
// Status and Cause are transport and log detail, never part of the body.
type AppError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
	Cause   error  `json:"-"`
}

// Error implements error. The result is for logs, not for clients.
func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}

	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *AppError) Unwrap() error { return e.Cause }

// WithCause attaches the underlying failure for logging.
func (e *AppError) WithCause(cause error) *AppError {
	e.Cause = cause

	return e
}

// WithStatus overrides the status for the rare case where the code alone does
// not describe the transport result, such as a wrong method on a real route.
func (e *AppError) WithStatus(status int) *AppError {
	e.Status = status

	return e
}

// Validation reports input the adapter refuses.
func Validation(message string) *AppError {
	return newAppError(CodeValidation, message, nil)
}

// NotFound reports a resource that does not exist.
func NotFound(message string) *AppError {
	return newAppError(CodeNotFound, message, nil)
}

// Conflict reports a request that clashes with current state.
func Conflict(message string) *AppError {
	return newAppError(CodeConflict, message, nil)
}

// Downstream reports a failed call to Everest, PMM, or ServiceNow. The cause is
// logged with the request ID and never returned to the client.
func Downstream(message string, cause error) *AppError {
	return newAppError(CodeDownstream, message, cause)
}

// NotImplemented reports an endpoint that is not wired yet, for example support
// tickets without a ServiceNow secret.
func NotImplemented(message string) *AppError {
	return newAppError(CodeNotImplemented, message, nil)
}

// Unauthorized reports a missing or wrong agent token.
func Unauthorized(message string) *AppError {
	return newAppError(CodeValidation, message, nil).WithStatus(http.StatusUnauthorized)
}

// Forbidden reports a route the caller's role does not allow.
func Forbidden(message string) *AppError {
	return newAppError(CodeForbidden, message, nil)
}

// Internal reports an unexpected failure inside the adapter.
func Internal(message string, cause error) *AppError {
	return newAppError(CodeInternal, message, cause)
}

func newAppError(code, message string, cause error) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
		Status:  statusForCode(code),
		Cause:   cause,
	}
}

// statusForCode maps an error code to its HTTP status. Downstream failures are
// 502: the adapter is healthy, the platform behind it is not.
func statusForCode(code string) int {
	switch code {
	case CodeValidation:
		return http.StatusBadRequest
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict:
		return http.StatusConflict
	case CodeDownstream:
		return http.StatusBadGateway
	case CodeNotImplemented:
		return http.StatusNotImplemented
	case CodeForbidden:
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}
