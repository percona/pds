package api

import (
	"net/http"
)

// healthResponse is the liveness payload. It stays free of build or environment
// detail so an unauthenticated probe leaks nothing.
type healthResponse struct {
	Status string `json:"status"`
}

// handleHealthz reports that the process is up and serving. Liveness must not
// depend on Everest, PMM, or ServiceNow, otherwise a downstream outage restarts
// a healthy adapter.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	Respond(w, r, healthResponse{Status: "ok"}, nil)
}

// handleReadyz reports that this process can serve. Readiness must not wait on
// OpenEverest, PMM, or ServiceNow: a downstream outage would hide the UI by
// taking the pod out of the Service.
//
// The adapter's own database is the exception. A pod that cannot reach it, or
// that has not finished reconciling the schema, cannot answer for the state
// kept there, so it stays out of the Service until both hold. An adapter with
// no database configured has nothing to wait on and is ready at once.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		Respond(w, r, healthResponse{Status: "ok"}, nil)

		return
	}

	if err := s.store.Ready(r.Context()); err != nil {
		// 503 rather than the 502 the code carries by default: the platform
		// behind the adapter is fine, this pod is not ready to serve yet.
		Respond(w, r, nil, Downstream("The control plane database is not ready.", err).
			WithStatus(http.StatusServiceUnavailable))

		return
	}

	Respond(w, r, healthResponse{Status: "ok"}, nil)
}
