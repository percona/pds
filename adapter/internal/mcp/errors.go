package mcp

import (
	"errors"
	"net/http"
)

// The sentences a tool answers with when the API's own does not apply. Every
// one is read by a customer through an agent, so none names a part of the
// platform that a customer does not run.
const (
	msgInvalidInput            = "That request was not accepted. Check the inputs and try again."
	msgInvalidArguments        = "The input is not valid. Check it against the tool's input schema."
	msgInvalidDatabaseID       = "That is not a database id. Copy an id exactly as list_databases returns it."
	msgInvalidEnvironmentName  = "That is not an environment name. Copy a name exactly as list_environments returns it."
	msgNotFound                = "That was not found. Check the id and try again."
	msgNotFoundHint            = "Call list_databases for the current ids."
	msgEnvironmentNotFoundHint = "Call list_environments for the current names."
	msgActivityNotFoundHint    = "Call list_activity for the current ids."
	msgBackupNotFoundHint      = "Call list_backups for the backups of that database, or list_databases for its id."
	msgTicketNotFoundHint      = "Call list_support_tickets for the current numbers."
	msgConflict                = "That conflicts with the current state. Check it and try again."
	msgNotAvailable            = "That is not available on this control plane."
	msgInternal                = "Something went wrong on the control plane. Try again."
	msgUnavailable             = "The control plane could not complete that request. Try again in a moment."
	msgRefused                 = "The control plane refused that request."
	msgTimedOut                = "The control plane took too long to answer. Try again in a moment."
	msgUnreadable              = "The control plane sent an answer this tool could not read. Try again in a moment."

	msgChangesNotAllowed = "Changes are not allowed in this session. Tell the user, and do not try again."
	msgDeleteNotAllowed  = "Deleting is not allowed in this session. Tell the user, and do not try again."

	// A change that got no readable answer may or may not have been made, so
	// these never offer it again.
	msgChangeTimedOut    = "The control plane took too long to answer, so the change may or may not have been made. Do not repeat it. Check its state first."
	msgChangeUnreachable = "The control plane did not answer, so the change may or may not have been made. Do not repeat it. Check its state first."
	msgChangeUnreadable  = "The control plane sent an answer this tool could not read, so the change may or may not have been made. Do not repeat it. Check its state first."
)

// Error kinds. An agent decides what to do next from the kind alone, so the
// set is small and fixed.
const (
	kindInvalidInput = "invalid_input"
	kindNotFound     = "not_found"
	kindConflict     = "conflict"
	kindNotAvailable = "not_available"
	kindUnavailable  = "unavailable"
	kindTimeout      = "timeout"
	// kindNotAllowed is a tool above the caller's access tier.
	kindNotAllowed = "not_allowed"
	// kindConfirmationRequired is a change that needs the user's word first.
	kindConfirmationRequired = "confirmation_required"
)

// toolError is what a failed call answers with. It is sent as the result's
// structured content and as its text, with isError set. RequestID is empty
// when no request was made. Outcome is "unknown" when a change was sent and
// the answer did not say whether it was made.
type toolError struct {
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Hint      string `json:"hint,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

// Error implements error. The text is safe to show an agent.
func (e *toolError) Error() string { return e.Message }

// refuse is a call the tool turned away before any request was made.
func refuse(message string) *toolError {
	return &toolError{Kind: kindInvalidInput, Message: message}
}

// unknownOutcome is the failure of a change that was sent and not answered:
// the agent is told not to repeat it, and which call finds out what happened.
func unknownOutcome(kind, message, check, requestID string) *toolError {
	return &toolError{Kind: kind, Message: message, Hint: check, Outcome: outcomeUnknown, RequestID: requestID}
}

// asToolError returns the error as the result the agent reads. Anything that
// is not already one is a bug in a tool, and says only that something went wrong.
func asToolError(err error) (*toolError, bool) {
	var failure *toolError
	if errors.As(err, &failure) {
		return failure, true
	}

	return &toolError{Kind: kindUnavailable, Message: msgInternal, Retryable: true}, false
}

// answered is the failure an API status maps to. The API words its 400, 403,
// 404, 409 and 501 messages for customers, and its copy test keeps them clean,
// so they pass through. A 500 or 502 says nothing about what went wrong that a
// customer could use, so those, and every status this does not name, get this
// package's own sentences.
func answered(status int, message, requestID string) *toolError {
	failure := &toolError{RequestID: requestID}
	switch status {
	case http.StatusBadRequest:
		failure.Kind, failure.Message = kindInvalidInput, orDefault(message, msgInvalidInput)
	case http.StatusNotFound:
		failure.Kind, failure.Message, failure.Hint = kindNotFound, orDefault(message, msgNotFound), msgNotFoundHint
	case http.StatusConflict:
		failure.Kind, failure.Message = kindConflict, orDefault(message, msgConflict)
	case http.StatusForbidden, http.StatusNotImplemented:
		failure.Kind, failure.Message = kindNotAvailable, orDefault(message, msgNotAvailable)
	case http.StatusInternalServerError:
		failure.Kind, failure.Message, failure.Retryable = kindUnavailable, msgInternal, true
	default:
		failure.Kind, failure.Message = kindUnavailable, msgRefused
		if status >= http.StatusInternalServerError {
			failure.Message, failure.Retryable = msgUnavailable, true
		}
	}

	return failure
}

func orDefault(message, fallback string) string {
	if message == "" {
		return fallback
	}

	return message
}

// hintedAs replaces the hint of a not found failure, whose default names the
// tool that lists databases, with the one that lists what this tool was given.
func hintedAs(err error, hint string) error {
	var failure *toolError
	if errors.As(err, &failure) && failure.Kind == kindNotFound {
		failure.Hint = hint
	}

	return err
}
