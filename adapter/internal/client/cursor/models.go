package cursor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is Cursor's public API.
	DefaultBaseURL = "https://api.cursor.com"
	// DefaultTimeout bounds one ListModels call.
	DefaultTimeout = 30 * time.Second
	// CompleteTimeout bounds one Complete call including polling.
	CompleteTimeout = 90 * time.Second
	modelsPath      = "/v1/models"
)

const maxErrorBody = 8 << 10
const maxResponseBody = 2 << 20

// Options configures a Cursor client or ListModels.
type Options struct {
	BaseURL string
	APIKey  string
	Model   string
	// AgentID is a cloud agent that already holds the conversation. Complete
	// starts its runs on it rather than creating an agent.
	AgentID string
	HTTP    *http.Client
	Timeout time.Duration
	Sleep   func(time.Duration)
}

// Model is one model the Cursor API exposes.
type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// ListModels returns models from GET {baseURL}/v1/models.
func ListModels(ctx context.Context, opts Options) ([]Model, error) {
	apiKey := strings.TrimSpace(opts.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("cursor: API key must not be blank")
	}

	raw := strings.TrimSpace(opts.BaseURL)
	if raw == "" {
		raw = DefaultBaseURL
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("cursor: base URL %q is not a URL: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("cursor: base URL %q needs an http or https scheme", raw)
	}

	httpClient := opts.HTTP
	if httpClient == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		httpClient = &http.Client{Timeout: timeout}
	}

	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	endpoint := *parsed
	endpoint.Path = parsed.Path + modelsPath

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("cursor: build request: %w", err)
	}
	httpReq.SetBasicAuth(apiKey, "")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("cursor: do: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("cursor: read body: %w", err)
	}
	if len(rawBody) > maxResponseBody {
		return nil, fmt.Errorf("cursor: response too large")
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, truncate(rawBody))
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w: status %d: %s", ErrRejected, resp.StatusCode, truncate(rawBody))
	}

	var parsedBody struct {
		Items []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rawBody, &parsedBody); err != nil {
		return nil, fmt.Errorf("cursor: decode response: %w", err)
	}

	out := make([]Model, 0, len(parsedBody.Items))
	for _, item := range parsedBody.Items {
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

func truncate(raw []byte) string {
	if len(raw) > maxErrorBody {
		raw = raw[:maxErrorBody]
	}

	return strings.TrimSpace(string(raw))
}
