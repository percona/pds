// Package resend sends one plain text email through the Resend API.
//
// One route is called:
//
//	POST /emails   # Authorization: Bearer <api key>, JSON body
//
// The key travels only in the header. It is never part of the URL, and an
// error carries the status and the start of Resend's answer, never the request.
package resend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the Resend API.
const DefaultBaseURL = "https://api.resend.com"

// EmailsPath sends one email.
const EmailsPath = "/emails"

// DefaultTimeout bounds one send. A visitor is waiting on the form.
const DefaultTimeout = 10 * time.Second

// maxErrorBody caps how much of a refusal is kept for the log, so an HTML
// error page cannot fill memory or the log line.
const maxErrorBody = 2 << 10

// Email is the body Resend takes. Only the fields this adapter sends exist.
type Email struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	ReplyTo string   `json:"reply_to,omitempty"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
}

// APIError is a send Resend answered with something other than 2xx. Body is
// the start of its answer and is meant for logs.
type APIError struct {
	Status int
	Body   string
}

// Error implements error.
func (e *APIError) Error() string {
	msg := fmt.Sprintf("resend: POST %s: %d %s", EmailsPath, e.Status, http.StatusText(e.Status))
	if e.Body != "" {
		msg += ": " + e.Body
	}

	return msg
}

// Options configures a Client.
type Options struct {
	// BaseURL overrides DefaultBaseURL, for tests.
	BaseURL string
	// APIKey is a Resend key with sending access.
	APIKey string
	// HTTP overrides the transport.
	HTTP *http.Client
	// Timeout bounds one send. Zero means DefaultTimeout.
	Timeout time.Duration
}

// Client sends email with one API key.
type Client struct {
	emailsURL string
	apiKey    string
	http      *http.Client
}

// New validates the options and returns a client.
func New(opts Options) (*Client, error) {
	apiKey := strings.TrimSpace(opts.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("resend: API key must not be blank")
	}

	raw := strings.TrimSpace(opts.BaseURL)
	if raw == "" {
		raw = DefaultBaseURL
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("resend: base URL %q is not a URL: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("resend: base URL %q needs an http or https scheme", raw)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("resend: base URL %q has no host", raw)
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + EmailsPath

	httpClient := opts.HTTP
	if httpClient == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		httpClient = &http.Client{Timeout: timeout}
	}

	return &Client{emailsURL: parsed.String(), apiKey: apiKey, http: httpClient}, nil
}

// Send hands one email to Resend. It is tried once: a send whose answer was
// lost may still have gone out, and a retry would then deliver it twice.
func (c *Client) Send(ctx context.Context, email Email) error {
	payload, err := json.Marshal(email)
	if err != nil {
		return fmt.Errorf("resend: encode email: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.emailsURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("resend: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("resend: POST %s: %w", EmailsPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		// The answer is the new email's id, which nothing here uses. Draining
		// a little of it lets the connection be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))

		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))

	return &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
}
