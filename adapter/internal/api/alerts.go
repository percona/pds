package api

import (
	"net/http"

	"github.com/percona/pds/adapter/internal/service"
)

// handleListAlerts answers with every environment's state and firing alerts
// for the signed-in team. The page and the sidebar count read this one route.
func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	report, err := s.databases.Alerts(r.Context())
	if err != nil {
		Respond(w, r, nil, databaseError(err, ""))

		return
	}

	Respond(w, r, report, nil)
}

// handleDatabaseAlerts answers with one database's firing alerts and the state
// of the environment it runs in.
func (s *Server) handleDatabaseAlerts(w http.ResponseWriter, r *http.Request) {
	id := databaseID(r)

	alerts, err := s.databases.DatabaseAlerts(r.Context(), id)
	if err != nil {
		Respond(w, r, nil, databaseError(err, service.NameFromID(id)))

		return
	}

	Respond(w, r, alerts, nil)
}
