// Package pmm is a thin hand-written client for the PMM 3 metrics API.
//
// PMM 3 stores metrics in VictoriaMetrics and serves the Prometheus querying
// API through the nginx in front of PMM Server. Only the range query is used,
// because a chart needs a series and not a single reading:
//
//	GET /prometheus/api/v1/query_range?query=...&start=...&end=...&step=...
//
// The response is the standard Prometheus matrix envelope, so a fixture
// recorded from PMM is also a valid Prometheus fixture.
//
// Authentication is a Grafana service account token sent as
// `Authorization: Bearer <token>`. PMM 3 replaced the PMM 2 API keys with
// service accounts; the token comes from a Kubernetes Secret and is never
// logged.
package pmm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// APIPrefix is the nginx location that proxies VictoriaMetrics inside PMM.
const APIPrefix = "/prometheus"

// QueryRangePath is the Prometheus range query route under the prefix.
const QueryRangePath = APIPrefix + "/api/v1/query_range"

// DefaultTimeout bounds one PMM call. Charts are a read on a browser request,
// so this is short: a slow PMM should show an error, not hold the tab open.
const DefaultTimeout = 15 * time.Second

// maxErrorBody caps how much of an error response is read, so an HTML error
// page from a misconfigured ingress cannot fill memory.
const maxErrorBody = 8 << 10

// Options configures a Client.
type Options struct {
	// BaseURL is the PMM Server URL, without the /prometheus prefix.
	BaseURL string
	// Token is the Grafana service account token. Empty means unauthenticated,
	// which only works against a PMM with anonymous access.
	Token string
	// HTTP overrides the transport, for tests and for a custom CA bundle.
	HTTP *http.Client
	// Timeout bounds one call. Zero means DefaultTimeout.
	Timeout time.Duration
}

// Client queries the PMM metrics API.
type Client struct {
	baseURL *url.URL
	token   string
	http    *http.Client
}

// New validates the options and returns a client.
func New(opts Options) (*Client, error) {
	raw := strings.TrimSpace(opts.BaseURL)
	if raw == "" {
		return nil, fmt.Errorf("pmm: base URL must not be blank")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("pmm: base URL %q is not a URL: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("pmm: base URL %q needs an http or https scheme", raw)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("pmm: base URL %q has no host", raw)
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

	return &Client{baseURL: parsed, token: opts.Token, http: httpClient}, nil
}

// QueryRange evaluates one PromQL expression over a window and returns every
// series it matched. An expression that matches nothing is not an error: it is
// how PMM reports that it has no target for a database yet.
func (c *Client) QueryRange(ctx context.Context, q RangeQuery) ([]Series, error) {
	if strings.TrimSpace(q.Query) == "" {
		return nil, fmt.Errorf("pmm: query must not be blank")
	}
	if q.Step <= 0 {
		return nil, fmt.Errorf("pmm: step must be positive, got %s", q.Step)
	}
	if !q.End.After(q.Start) {
		return nil, fmt.Errorf("pmm: end %s must be after start %s", q.End, q.Start)
	}

	params := url.Values{}
	params.Set("query", q.Query)
	params.Set("start", strconv.FormatInt(q.Start.Unix(), 10))
	params.Set("end", strconv.FormatInt(q.End.Unix(), 10))
	params.Set("step", strconv.FormatInt(int64(q.Step.Seconds()), 10))

	target := c.baseURL.String() + QueryRangePath + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("pmm: build query_range request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, transportError(QueryRangePath, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, newAPIError(resp, QueryRangePath)
	}

	var decoded rangeResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("pmm: decode query_range response: %w", err)
	}

	// VictoriaMetrics can answer 200 with a failed status, so the body decides
	// as well as the code.
	if decoded.Status != "" && decoded.Status != "success" {
		return nil, &APIError{
			Method:  http.MethodGet,
			Path:    QueryRangePath,
			Status:  resp.StatusCode,
			Type:    decoded.ErrorType,
			Message: decoded.Error,
		}
	}

	series := make([]Series, 0, len(decoded.Data.Result))
	for _, raw := range decoded.Data.Result {
		series = append(series, raw.toSeries())
	}

	return series, nil
}

// maxResponseBody caps a successful body. A 24 hour window at a 15 minute step
// is a few kilobytes per series, so a megabyte is generous and a runaway query
// cannot exhaust the adapter.
const maxResponseBody = 1 << 20

// newAPIError reads the Prometheus error body, which is
// `{"status":"error","errorType":"...","error":"..."}`, and falls back to the
// status alone when the body is a different shape, such as the nginx JSON that
// answers an unauthenticated request or the HTML nginx serves for a route it
// does not proxy.
func newAPIError(resp *http.Response, path string) *APIError {
	apiErr := &APIError{Method: http.MethodGet, Path: path, Status: resp.StatusCode}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil || len(raw) == 0 {
		return apiErr
	}

	var decoded struct {
		ErrorType string `json:"errorType"`
		Error     string `json:"error"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return apiErr
	}

	apiErr.Type = decoded.ErrorType
	apiErr.Message = decoded.Error
	if apiErr.Message == "" {
		apiErr.Message = decoded.Message
	}

	return apiErr
}

// transportError describes a call that never came back with a response.
//
// An explicit cancellation is the caller hanging up, and that error says nothing
// about PMM. A deadline exceeded is treated as unreachable by policy: the
// transport layer cannot reliably tell whether the deadline came from the caller
// or PMM, and the pending state is the safe answer when the upstream never
// answered in time. Any other transport error means the name did not resolve, the
// connection was refused, or PMM did not answer, which is a PMM that is not up
// rather than one refusing the query, so it carries ErrUnreachable for a reader
// to report as pending.
//
// The URL carries no credential, but it does carry the query, and a transport
// error already names the host. Wrap without the URL so the message stays one
// line.
func transportError(path string, err error) error {
	inner := unwrapURLError(err)
	if errors.Is(inner, context.Canceled) {
		return fmt.Errorf("pmm: GET %s: %w", path, inner)
	}

	return fmt.Errorf("pmm: GET %s: %w: %w", path, ErrUnreachable, inner)
}

// unwrapURLError strips the *url.Error wrapper so the full request URL, which
// includes the query expression, stays out of the message while errors.Is on
// context.Canceled still works.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}

	return err
}
