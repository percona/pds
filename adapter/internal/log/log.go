// Package log builds the adapter logger and strips credentials from records.
//
// Production writes JSON, development writes text, and both run through a
// redaction handler so an API key, token, password, or connection string never
// reaches a log sink, even on error paths.
package log

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

// Redacted replaces any value that looks like a credential.
const Redacted = "[REDACTED]"

// Format selects the log encoding.
type Format string

const (
	// FormatJSON is used in production.
	FormatJSON Format = "json"
	// FormatText is used in development.
	FormatText Format = "text"
)

// FormatFor returns the encoding for the environment.
func FormatFor(production bool) Format {
	if production {
		return FormatJSON
	}

	return FormatText
}

// New builds a logger that writes redacted records to w.
func New(w io.Writer, format Format, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}

	var base slog.Handler
	if format == FormatText {
		base = slog.NewTextHandler(w, opts)
	} else {
		base = slog.NewJSONHandler(w, opts)
	}

	return slog.New(NewRedactingHandler(base))
}

// NewRedactingHandler wraps next so every attribute and message is scrubbed.
func NewRedactingHandler(next slog.Handler) slog.Handler {
	return &redactingHandler{next: next}
}

type redactingHandler struct {
	next slog.Handler
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, rec slog.Record) error {
	clean := slog.NewRecord(rec.Time, rec.Level, redactText(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(redactAttr(a))

		return true
	})

	return h.next.Handle(ctx, clean)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		clean = append(clean, redactAttr(a))
	}

	return &redactingHandler{next: h.next.WithAttrs(clean)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: h.next.WithGroup(name)}
}

// redactAttr scrubs one attribute, walking into groups.
func redactAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()

	if a.Value.Kind() == slog.KindGroup {
		members := a.Value.Group()
		clean := make([]slog.Attr, 0, len(members))
		for _, m := range members {
			clean = append(clean, redactAttr(m))
		}

		return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}
	}

	if isSensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}

	if a.Value.Kind() == slog.KindString && looksLikeCredential(a.Value.String()) {
		return slog.String(a.Key, Redacted)
	}

	return a
}

// sensitiveParts match anywhere in a normalized attribute key.
var sensitiveParts = []string{
	"password",
	"passwd",
	"pwd",
	"secret",
	"token",
	"apikey",
	"authorization",
	"credential",
	"privatekey",
	"accesskey",
	"connectionstring",
	"connectionuri",
	"connuri",
	"databaseurl",
	"dburl",
}

// sensitiveExact match a normalized key in full, for names too short to use as
// substrings.
var sensitiveExact = map[string]struct{}{
	"dsn":  {},
	"auth": {},
}

// nonAlphanumeric strips separators so api_key, api-key, and apiKey normalize
// to the same token.
var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

func isSensitiveKey(key string) bool {
	normalized := nonAlphanumeric.ReplaceAllString(strings.ToLower(key), "")
	if _, ok := sensitiveExact[normalized]; ok {
		return true
	}

	for _, part := range sensitiveParts {
		if strings.Contains(normalized, part) {
			return true
		}
	}

	return false
}

// credentialURL matches a URL that embeds a user and password, which is how
// connection strings leak.
//
// The password class excludes only whitespace and the at sign that ends it. It
// used to exclude the slash as well, on the reasoning that a slash ends the
// authority, and that let a password containing one through untouched. A
// password is arbitrary bytes, so the pattern reads to the last at sign before
// whitespace and lets the replacement decide.
var credentialURL = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.\-]*://[^/\s:@]+:[^\s]+@`)

// credentialAssignment matches key=value or key: value pairs inside free text,
// which is how panics and downstream error strings leak.
var credentialAssignment = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api[_\-]?key|authorization)\b\s*[:=]\s*[^\s,;)"']+`)

func looksLikeCredential(v string) bool {
	return credentialURL.MatchString(v) || credentialAssignment.MatchString(v)
}

// redactText scrubs credentials from a free-form string such as a log message
// or a recovered panic value.
func redactText(v string) string {
	v = credentialURL.ReplaceAllString(v, Redacted+"@")

	return credentialAssignment.ReplaceAllStringFunc(v, func(match string) string {
		sep := strings.IndexAny(match, ":=")
		if sep < 0 {
			return Redacted
		}

		return match[:sep+1] + Redacted
	})
}

// RedactText exposes message scrubbing for callers that build strings before
// logging them, such as the panic recoverer.
func RedactText(v string) string { return redactText(v) }

type contextKey struct{}

// NewContext returns a context carrying logger, so handlers and services log
// with the request ID already attached.
func NewContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, logger)
}

// FromContext returns the request logger, falling back to the default logger.
func FromContext(ctx context.Context) *slog.Logger {
	logger, _ := LoggerFromContext(ctx)

	return logger
}

// LoggerFromContext returns the request logger and reports whether one was
// attached, so callers can decide whether to add request scoped attributes
// themselves.
func LoggerFromContext(ctx context.Context) (*slog.Logger, bool) {
	if logger, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && logger != nil {
		return logger, true
	}

	return slog.Default(), false
}
