// Package auth holds the pieces customer login is built from: password
// hashing, the invite and session tokens, the signed cookie, and the identity
// a request carries.
//
// The HTTP side lives with the handlers, because refusing a request means
// rendering the API's error shape. What stands here is everything that does
// not need a response writer, behind the Authenticator interface the router
// already takes.
package auth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

// Authenticator wraps handlers with session validation.
type Authenticator interface {
	Middleware(next http.Handler) http.Handler
}

// Stub accepts every request. A deployment never uses it: it is what lets a
// test drive a route without standing up a session, and what NewServer falls
// back to when no authenticator is given.
type Stub struct{}

// NewStub returns the no-op authenticator.
func NewStub() *Stub { return &Stub{} }

// Middleware passes every request through untouched.
func (s *Stub) Middleware(next http.Handler) http.Handler { return next }

// BearerToken returns the credential from an Authorization header that uses
// the Bearer scheme. The scheme is matched case-insensitively and must be
// followed by whitespace. A raw token or BearerTOKEN is rejected.
func BearerToken(authorization string) (string, bool) {
	authorization = strings.TrimSpace(authorization)
	scheme, rest, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token := strings.TrimSpace(rest)
	if token == "" {
		return "", false
	}

	return token, true
}

// BearerMatches reports whether Authorization carries Bearer <want>. An empty
// want never matches, so a missing operator token cannot be satisfied by an
// empty header.
func BearerMatches(want, authorization string) bool {
	if want == "" {
		return false
	}
	got, ok := BearerToken(authorization)
	if !ok {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// subjectKey types the context value so nothing else can collide with it.
type subjectKey struct{}

// WithSubject returns a context carrying the authenticated subject. Session
// validation calls it once the token is verified; the stub never does.
func WithSubject(ctx context.Context, subject string) context.Context {
	return context.WithValue(ctx, subjectKey{}, subject)
}

// SubjectFromContext returns the authenticated subject and reports whether one
// was attached. It answers false under the stub, which is what lets callers
// record an action now and pick up a real identity when Zitadel lands without
// touching the call site.
func SubjectFromContext(ctx context.Context) (string, bool) {
	subject, ok := ctx.Value(subjectKey{}).(string)
	if !ok || subject == "" {
		return "", false
	}

	return subject, true
}
