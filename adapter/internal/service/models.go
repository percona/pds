package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/client/cursor"
)

// ErrUnknownProvider means the Chat provider query parameter is not supported.
var ErrUnknownProvider = errors.New("unknown assistant provider")

// Model is one assistant model the UI can pick for Chat.
type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	// Default marks the model a new chat starts on.
	Default bool `json:"default,omitempty"`
}

// ListModels returns models from Anthropic or Cursor for the given provider.
// Pass nil transport in production; tests pass an httptest transport.
func (c *Connections) ListModels(ctx context.Context, provider string, transport http.RoundTripper) ([]Model, error) {
	switch strings.TrimSpace(provider) {
	case "anthropic":
		if c == nil {
			return nil, ErrAssistantUnavailable
		}

		return c.listAnthropicModels(ctx, transport)
	case "cursor":
		if c == nil {
			return nil, ErrCursorUnavailable
		}

		return c.listCursorModels(ctx, transport)
	default:
		return nil, ErrUnknownProvider
	}
}

func (c *Connections) listAnthropicModels(ctx context.Context, transport http.RoundTripper) ([]Model, error) {
	creds, err := c.Assistant(ctx)
	if err != nil {
		return nil, err
	}

	httpClient := httpClientFromTransport(transport, claude.DefaultTimeout)
	models, err := claude.ListModels(ctx, claude.Options{
		BaseURL: creds.BaseURL,
		APIKey:  creds.APIKey,
		HTTP:    httpClient,
	})
	if err != nil {
		return nil, err
	}

	return mapClaudeModels(models), nil
}

func (c *Connections) listCursorModels(ctx context.Context, transport http.RoundTripper) ([]Model, error) {
	creds, err := c.Cursor(ctx)
	if err != nil {
		return nil, err
	}

	httpClient := httpClientFromTransport(transport, cursor.DefaultTimeout)
	models, err := cursor.ListModels(ctx, cursor.Options{
		APIKey: creds.APIKey,
		HTTP:   httpClient,
	})
	if err != nil {
		return nil, err
	}

	return mapCursorModels(models), nil
}

func httpClientFromTransport(transport http.RoundTripper, timeout time.Duration) *http.Client {
	if transport == nil {
		return nil
	}

	return &http.Client{Transport: transport, Timeout: timeout}
}

func mapClaudeModels(models []claude.Model) []Model {
	out := make([]Model, 0, len(models))
	for _, model := range models {
		out = append(out, Model{ID: model.ID, DisplayName: model.DisplayName, Default: model.ID == claude.DefaultModel})
	}

	return out
}

func mapCursorModels(models []cursor.Model) []Model {
	out := make([]Model, 0, len(models))
	for _, model := range models {
		out = append(out, Model{ID: model.ID, DisplayName: model.DisplayName})
	}

	return out
}
