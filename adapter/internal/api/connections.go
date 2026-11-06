package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/service"
)

type putServiceNowRequest struct {
	ClientToken string `json:"clientToken" validate:"required,max=400"`
}

type putAssistantRequest struct {
	APIKey  string `json:"apiKey" validate:"required,max=400"`
	BaseURL string `json:"baseUrl" validate:"omitempty,max=400"`
}

type putCursorRequest struct {
	APIKey string `json:"apiKey" validate:"required,max=400"`
}

// handleGetConnections reports which outbound credentials are stored. Values
// never appear in the body: configured true or false is all the UI needs.
func (s *Server) handleGetConnections(w http.ResponseWriter, r *http.Request) {
	if s.connections == nil {
		Respond(w, r, service.ConnectionsStatus{}, nil)

		return
	}

	status, err := s.connections.Status(r.Context())
	if err != nil {
		Respond(w, r, nil, connectionsError(err))

		return
	}

	Respond(w, r, status, nil)
}

// handlePutServiceNow stores the ServiceNow client token the UI pasted. The
// API key and instance URL come from the adapter environment.
func (s *Server) handlePutServiceNow(w http.ResponseWriter, r *http.Request) {
	if s.connections == nil {
		Respond(w, r, nil, connectionsError(service.ErrConnectionsUnavailable))

		return
	}

	var req putServiceNowRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	err := s.connections.PutServiceNow(r.Context(), service.ServiceNowInput{
		ClientToken: strings.TrimSpace(req.ClientToken),
	})
	if err != nil {
		Respond(w, r, nil, connectionsError(err))

		return
	}

	Respond(w, r, nil, nil)
}

// handlePutAssistant stores the Anthropic key the UI pasted.
func (s *Server) handlePutAssistant(w http.ResponseWriter, r *http.Request) {
	if s.connections == nil {
		Respond(w, r, nil, connectionsError(service.ErrConnectionsUnavailable))

		return
	}

	var req putAssistantRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	err := s.connections.PutAssistant(r.Context(), service.AssistantInput{
		APIKey:  strings.TrimSpace(req.APIKey),
		BaseURL: strings.TrimSpace(req.BaseURL),
	})
	if err != nil {
		Respond(w, r, nil, connectionsError(err))

		return
	}

	Respond(w, r, nil, nil)
}

// handlePutCursor stores the Cursor key the UI pasted.
func (s *Server) handlePutCursor(w http.ResponseWriter, r *http.Request) {
	if s.connections == nil {
		Respond(w, r, nil, connectionsError(service.ErrConnectionsUnavailable))

		return
	}

	var req putCursorRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	err := s.connections.PutCursor(r.Context(), service.CursorInput{
		APIKey: strings.TrimSpace(req.APIKey),
	})
	if err != nil {
		Respond(w, r, nil, connectionsError(err))

		return
	}

	Respond(w, r, nil, nil)
}

// handleDeleteConnection removes one stored credential.
func (s *Server) handleDeleteConnection(w http.ResponseWriter, r *http.Request) {
	kind := service.ConnectionKind(chi.URLParam(r, "kind"))

	if s.connections == nil {
		Respond(w, r, nil, connectionsError(service.ErrConnectionsUnavailable))

		return
	}

	if err := s.connections.Delete(r.Context(), kind); err != nil {
		Respond(w, r, nil, connectionsError(err))

		return
	}

	Respond(w, r, nil, nil)
}

func connectionsError(err error) *AppError {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, service.ErrInvalidConnection):
		return Validation("Those credentials cannot be stored as they are.").WithCause(err)

	case errors.Is(err, service.ErrKeyRefused):
		return Validation("The provider refused that key. Check it and try again.").WithCause(err)

	case errors.Is(err, service.ErrKeyUnchecked):
		return Downstream("The key could not be checked right now. Try again in a moment.", err)

	case errors.Is(err, service.ErrConnectionsForbidden):
		return Downstream("The credentials could not be stored. Contact Percona support.",
			fmt.Errorf("grant the adapter secrets write, see chart rbac: %w", err))

	case errors.Is(err, service.ErrConnectionsUnreadable):
		return Downstream("The stored credentials cannot be read with the configured encryption key. Restore the key that sealed them, or add it to the state Secret beside the current one.", err)

	case errors.Is(err, service.ErrConnectionsUnavailable):
		return Downstream("There is nowhere to store credentials yet. Contact Percona support.",
			fmt.Errorf("check the adapter database, or its access to the environment: %w", err))

	default:
		return Downstream("The credentials could not be stored. Try again in a moment.", err)
	}
}
