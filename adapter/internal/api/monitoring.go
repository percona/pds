package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/service"
)

// monitoringAccess says where an environment's monitoring login lives and how to
// reach it. It carries no password: the hub never holds one for an environment it
// does not run, so the person reads the Secret with that environment's kubeconfig,
// the same way they read a private database's.
type monitoringAccess struct {
	Username           string `json:"username"`
	PasswordCommand    string `json:"passwordCommand"`
	PortForwardCommand string `json:"portForwardCommand"`
	// Address is where the environment says monitoring can be opened, or empty
	// while the cloud has not assigned one or monitoring is cluster only.
	Address string `json:"address"`
	// URL is the address to sign in at: the environment's own address when it has
	// one, the local end of the port-forward otherwise.
	URL string `json:"url"`
	// TunnelURL is the local end of the port-forward, for someone with access
	// to the environment. It does not depend on the load balancer.
	TunnelURL string `json:"tunnelUrl"`
}

type monitoringResponse struct {
	Settings hub.PMMSettings  `json:"settings"`
	Access   monitoringAccess `json:"access"`
}

// monitoringLocalPort is the local port the port-forward command binds.
const monitoringLocalPort = 8443

// monitoringNamespace is where an environment's monitoring and its admin Secret
// run: the namespace the platform release is installed in, which the chart
// fixes. It is not the configured database namespace.
const monitoringNamespace = "everest-system"

func (s *Server) monitoringAccess(hubSvc *hub.Service, environment string) monitoringAccess {
	ns := monitoringNamespace
	address := hubSvc.MonitoringAddress(environment)
	tunnel := fmt.Sprintf("https://localhost:%d", monitoringLocalPort)
	url := tunnel
	if address != "" {
		url = "https://" + address
	}

	return monitoringAccess{
		Address:   address,
		URL:       url,
		TunnelURL: tunnel,
		Username:  "admin",
		PasswordCommand: fmt.Sprintf(
			"kubectl get secret pmm-secret -n %s -o jsonpath='{.data.PMM_ADMIN_PASSWORD}' | base64 --decode; echo", ns),
		PortForwardCommand: fmt.Sprintf(
			"kubectl port-forward -n %s svc/monitoring-service %d:443", ns, monitoringLocalPort),
	}
}

func monitoringError(err error, environment string) *AppError {
	if errors.Is(err, hub.ErrInvalidPMMSettings) {
		return Validation(strings.TrimPrefix(err.Error(), hub.ErrInvalidPMMSettings.Error()+": ")).WithCause(err)
	}

	return databaseError(err, environment)
}

// handleGetMonitoring answers with an environment's monitoring settings and how to
// reach its login.
func (s *Server) handleGetMonitoring(w http.ResponseWriter, r *http.Request) {
	hubSvc, err := s.hub(r)
	if err != nil {
		Respond(w, r, nil, hubUnavailable(err))

		return
	}
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	settings, err := hubSvc.PMMSettings(r.Context(), name)
	if err != nil {
		Respond(w, r, nil, monitoringError(err, name))

		return
	}

	Respond(w, r, monitoringResponse{Settings: settings, Access: s.monitoringAccess(hubSvc, name)}, nil)
}

// handlePutMonitoring replaces an environment's monitoring settings. The hub writes
// them to Git and the environment applies them, so the answer is the stored
// settings, not a promise that the environment has caught up.
func (s *Server) handlePutMonitoring(w http.ResponseWriter, r *http.Request) {
	hubSvc, err := s.hub(r)
	if err != nil {
		Respond(w, r, nil, hubUnavailable(err))

		return
	}
	var in hub.PMMSettings
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody))
	// Unknown fields are refused, so a request cannot pretend to set something
	// the hub does not let anyone choose, such as the namespace.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		Respond(w, r, nil, Validation("That request body is not valid monitoring settings.").WithCause(err))

		return
	}

	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if err := hubSvc.SetPMMSettings(r.Context(), name, in); err != nil {
		Respond(w, r, nil, monitoringError(err, name))

		return
	}
	stored, err := hubSvc.PMMSettings(r.Context(), name)
	if err != nil {
		Respond(w, r, nil, monitoringError(err, name))

		return
	}

	Respond(w, r, monitoringResponse{Settings: stored, Access: s.monitoringAccess(hubSvc, name)}, nil)
}

// monitoringPasswordTimeout bounds how long a copy waits for the environment. The
// agent answers on its next beat, and beats every few seconds.
const monitoringPasswordTimeout = 20 * time.Second

// monitoringPasswordResponse carries the one value the monitoring panel
// copies. Like the database credential, it is asked for on click, so a page
// view never moves a password.
type monitoringPasswordResponse struct {
	Password string `json:"password"`
}

func monitoringPasswordError(err error, environment string) *AppError {
	switch {
	case errors.Is(err, hub.ErrSecretTimeout):
		return Downstream("The environment did not answer. Try again in a moment.", err).
			WithStatus(http.StatusGatewayTimeout)
	case errors.Is(err, hub.ErrSecretUnavailable):
		return Downstream("The monitoring password could not be read in that environment. Contact Percona support.", err)
	default:
		return monitoringError(err, environment)
	}
}

// handleGetMonitoringPassword reads an environment's monitoring admin password
// through that environment's agent and returns it. The hub holds the value for
// this one request: it is not stored, not logged, and not cached.
func (s *Server) handleGetMonitoringPassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	hubSvc, err := s.hub(r)
	if err != nil {
		Respond(w, r, nil, hubUnavailable(err))

		return
	}
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	ctx, cancel := context.WithTimeout(r.Context(), monitoringPasswordTimeout)
	defer cancel()

	password, err := hubSvc.ReadSecret(ctx, name, hub.SecretPMMAdminPassword)
	// The read is recorded whether or not it worked, and never with the value.
	event := service.ActivityEvent{Action: service.ActionMonitoringPassword, Target: name}
	if err != nil {
		event.Outcome = service.OutcomeFailed
		event.Error = "the password could not be read"
	} else {
		event.Outcome = service.OutcomeSucceeded
	}
	s.activity.Record(r.Context(), event)
	if err != nil {
		Respond(w, r, nil, monitoringPasswordError(err, name))

		return
	}

	Respond(w, r, monitoringPasswordResponse{Password: password}, nil)
}
