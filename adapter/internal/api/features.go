package api

import (
	"net/http"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/features"
)

// featuresResponse is the flag set the UI seeds its own OpenFeature provider
// from. The UI ships as a static bundle behind the same origin, so this route
// is the only way a chart value reaches the browser.
type featuresResponse struct {
	Flags map[string]bool `json:"flags"`
}

// handleFeatures publishes every flag. It is never
// itself gated by a flag: a UI that cannot read this falls back to the whole
// product, and a 501 here would be indistinguishable from an adapter that
// predates flags.
//
// It sits behind the authenticator, so it answers 401 until the customer signs
// in. The UI therefore reads it after that rather than at bootstrap: reading it
// first would take the 401, fall back to every surface on, and never ask
// again, which is the wrong answer for a deployment that turned one off.
func (s *Server) handleFeatures(w http.ResponseWriter, r *http.Request) {
	Respond(w, r, featuresResponse{Flags: s.features.Resolve(r.Context())}, nil)
}

// requireFeature short circuits a surface whose flag is off. It sits ahead of
// the middleware that checks for a downstream, so an operator who turned a
// surface off reads that rather than a missing OpenEverest.
func (s *Server) requireFeature(name string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.features.Enabled(r.Context(), name) {
				Respond(w, r, nil, NotImplemented(featureOffMessage(name)))

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// featureOffMessage is the one sentence a turned off surface answers with.
func featureOffMessage(name string) string {
	return "The " + features.Label(name) + " surface is turned off."
}

// requireCaller guards the writes a signed in caller of any role may make in
// their Team: token mint, disconnect, and database mutations.
//
// It is a backstop rather than the primary guard. Customer login authenticates
// at the door, accepting either a session cookie or the operator token, so a
// request that reaches here has already been identified and this passes it
// through. What it still buys is a route mounted outside that door by mistake:
// such a route would be refused here rather than served to anybody. Routes
// reserved for an Organization admin use requireAdmin.
//
// A signed in login satisfies it on its own. Asking a customer who has
// already proved who they are for a second shared secret would only mean
// publishing that secret to the browser.
func (s *Server) requireCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.isCaller(r) {
			Respond(w, r, nil, Unauthorized("An operator token is required."))

			return
		}

		next.ServeHTTP(w, r)
	})
}

// isCaller reports whether the request is from a signed-in member or carries
// the operator token.
func (s *Server) isCaller(r *http.Request) bool {
	if _, ok := auth.TeamFromContext(r.Context()); ok {
		return true
	}

	return auth.BearerMatches(s.cfg.HubOperatorToken, r.Header.Get("Authorization"))
}
