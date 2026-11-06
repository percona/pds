package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/percona/pds/adapter/internal/log"
)

// requestIDHeader is read from the ingress when present so a request can be
// followed across the proxy, the adapter, and the operator events.
const requestIDHeader = "X-Request-Id"

// RequestID puts a request ID on the context and echoes it on the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = newRequestID()
		}

		w.Header().Set(requestIDHeader, id)
		ctx := context.WithValue(r.Context(), middleware.RequestIDKey, id)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestLogger writes one structured record per request and puts a logger
// carrying the request ID on the context for handlers and services.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			reqLogger := logger.With(slog.String("request_id", middleware.GetReqID(r.Context())))
			ctx := log.NewContext(r.Context(), reqLogger)

			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(wrapped, r.WithContext(ctx))

			status := wrapped.Status()
			if status == 0 {
				status = http.StatusOK
			}

			reqLogger.LogAttrs(ctx, levelForStatus(status), "request completed",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.Int("bytes", wrapped.BytesWritten()),
			)
		})
	}
}

// Recoverer turns a panic into a structured 500 and logs the stack. The panic
// value goes through message redaction in case it carries a credential.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				if err, ok := recovered.(error); ok && err == http.ErrAbortHandler {
					panic(recovered)
				}

				logger.LogAttrs(r.Context(), slog.LevelError, "recovered from panic",
					slog.String("request_id", middleware.GetReqID(r.Context())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("panic", log.RedactText(fmt.Sprint(recovered))),
					slog.String("stack", string(debug.Stack())),
				)

				Respond(w, r, nil, Internal("Something went wrong on our side. Try again.", nil))
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// levelForStatus keeps 501 at warn: an endpoint that is not wired yet, such as
// support without a ServiceNow secret, is a configuration state, not a failure.
func levelForStatus(status int) slog.Level {
	switch {
	case status == http.StatusNotImplemented:
		return slog.LevelWarn
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// newRequestID returns a short random hex ID. Randomness failure falls back to
// the clock, because a request must never fail over its own trace ID.
func newRequestID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}

	return hex.EncodeToString(buf[:])
}
