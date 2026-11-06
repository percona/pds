package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const modelsPath = "/v1/models"

// Model is one model the assistant API exposes.
type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// ListModels returns models from GET {baseURL}/v1/models.
func ListModels(ctx context.Context, opts Options) ([]Model, error) {
	client, err := New(opts)
	if err != nil {
		return nil, err
	}

	endpoint := *client.baseURL
	endpoint.Path = strings.TrimSuffix(client.baseURL.Path, "/") + modelsPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("claude: build request: %w", err)
	}
	httpReq.Header.Set("x-api-key", client.apiKey)
	httpReq.Header.Set("anthropic-version", APIVersion)

	resp, err := client.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("claude: do: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("claude: read body: %w", err)
	}
	if len(raw) > maxResponseBody {
		return nil, fmt.Errorf("claude: response too large")
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, truncate(raw))
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w: status %d: %s", ErrRejected, resp.StatusCode, truncate(raw))
	}

	var parsed struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("claude: decode response: %w", err)
	}

	out := make([]Model, 0, len(parsed.Data))
	for _, item := range parsed.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		displayName := strings.TrimSpace(item.DisplayName)
		if displayName == "" {
			displayName = id
		}
		out = append(out, Model{ID: id, DisplayName: displayName})
	}

	return out, nil
}
