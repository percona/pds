package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/percona/pds/adapter/internal/log"
)

const contentTypeJSON = "application/json"

// Respond writes the success payload or the structured error. It is the only
// place the adapter turns a handler result into bytes.
//
// A nil result with a nil error means the work succeeded and there is nothing
// to return, which is a 204. Any error is rendered as {"code","message"}; the
// cause is logged with the request ID and never serialized.
func Respond(w http.ResponseWriter, r *http.Request, result any, err error) {
	RespondWithStatus(w, r, http.StatusOK, result, err)
}

// RespondWithStatus is Respond with an explicit success status, for a create
// that answers 201 or an operation the operator finishes later and answers 202.
// An error still picks its own status from its code.
func RespondWithStatus(w http.ResponseWriter, r *http.Request, status int, result any, err error) {
	ctx := r.Context()

	if err != nil {
		// The browser left. Writing a 502 here races the abort and the UI
		// shows "The database platform did not answer" for a navigation.
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return
		}

		respondError(ctx, w, r, err)

		return
	}

	if result == nil {
		w.WriteHeader(http.StatusNoContent)

		return
	}

	writeJSON(ctx, w, status, result)
}

func respondError(ctx context.Context, w http.ResponseWriter, r *http.Request, err error) {
	appErr := logError(ctx, r, err)
	writeJSON(ctx, w, appErr.Status, appErr)
}

// logError records a failed request the way every handler does and returns the
// AppError to render, for a handler that renders it somewhere other than a
// JSON body.
func logError(ctx context.Context, r *http.Request, err error) *AppError {
	appErr := asAppError(err)

	logger, scoped := log.LoggerFromContext(ctx)

	attrs := []slog.Attr{
		slog.String("code", appErr.Code),
		slog.Int("status", appErr.Status),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
	}
	// The request logger already carries the ID. Outside the router, such as a
	// direct Respond call, add it here so the record is still traceable.
	if id := middleware.GetReqID(ctx); id != "" && !scoped {
		attrs = append(attrs, slog.String("request_id", id))
	}
	if appErr.Cause != nil {
		attrs = append(attrs, slog.String("cause", appErr.Cause.Error()))
	}

	logger.LogAttrs(ctx, levelForStatus(appErr.Status), "request failed", attrs...)

	return appErr
}

// writeJSON marshals before writing the status so a marshal failure cannot
// leave a half written response with a success code.
func writeJSON(ctx context.Context, w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		log.FromContext(ctx).ErrorContext(ctx, "could not encode response", slog.String("cause", err.Error()))

		w.Header().Set("Content-Type", contentTypeJSON)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"` + CodeInternal + `","message":"Something went wrong on our side."}`))

		return
	}

	body = withLegacyNames(body)

	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// asAppError finds the AppError anywhere in the chain, or wraps an unexpected
// error so no internal detail reaches the client.
func asAppError(err error) *AppError {
	var appErr *AppError
	if errors.As(err, &appErr) {
		if appErr.Status == 0 {
			appErr.Status = statusForCode(appErr.Code)
		}

		return appErr
	}

	return &AppError{
		Code:    CodeInternal,
		Message: "Something went wrong on our side. Try again.",
		Status:  http.StatusInternalServerError,
		Cause:   err,
	}
}
