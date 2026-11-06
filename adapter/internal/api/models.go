package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/client/cursor"
	"github.com/percona/pds/adapter/internal/service"
)

type modelsResponse struct {
	Models []service.Model `json:"models"`
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	if provider == "" {
		Respond(w, r, nil, Validation("A provider query parameter is required."))

		return
	}

	list, err := s.connections.ListModels(r.Context(), provider, nil)
	if err != nil {
		Respond(w, r, nil, modelsError(err))

		return
	}

	if list == nil {
		list = make([]service.Model, 0)
	}

	Respond(w, r, modelsResponse{Models: list}, nil)
}

func modelsError(err error) *AppError {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, service.ErrUnknownProvider):
		return Validation("Provider must be anthropic or cursor.").WithCause(err)

	case errors.Is(err, service.ErrAssistantUnavailable):
		return NotImplemented("Chat needs an Anthropic key, which is not configured.").
			WithCause(err)

	case errors.Is(err, service.ErrCursorUnavailable):
		return NotImplemented("Chat needs a Cursor key, which is not configured.").
			WithCause(err)

	case errors.Is(err, claude.ErrUnauthorized), errors.Is(err, cursor.ErrUnauthorized):
		return Downstream("The assistant could not authenticate. Check the key in Connections.", err)

	default:
		return Downstream("The assistant did not answer. Try again in a moment.", err)
	}
}
