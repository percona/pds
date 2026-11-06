// Package servicenow is a thin hand-written client for Percona's Percona
// Connector REST API, the only path support tickets take.
//
// Four routes are called:
//
// POST /api/pellc/percona_connector/create          # open a case POST
// /api/pellc/percona_connector/tickets         # list the account's cases POST
// /api/pellc/percona_connector/ticket_details  # read one case POST
// /api/now/attachment/upload                   # attach a file (multipart)
//
// Authentication is two tokens on every request: the API key in the
// `x-sn-apikey` header and the client token in the body. Both come from one
// Kubernetes Secret, and neither is ever logged or returned to a client.
//
// Every call carries a timeout and retries the failures that are worth
// retrying: a dropped connection and a 5xx or 429 from a ServiceNow instance
// that is rolling. A 401 is not retried, because a wrong key stays wrong.
package servicenow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Routes on the Percona Connector and on the ServiceNow attachment API.
const (
	// CreatePath opens a case.
	CreatePath = "/api/pellc/percona_connector/create"
	// ListPath returns the account's cases.
	ListPath = "/api/pellc/percona_connector/tickets"
	// DetailsPath returns one case with its comments.
	DetailsPath = "/api/pellc/percona_connector/ticket_details"
	// AttachmentPath uploads a file against a record.
	AttachmentPath = "/api/now/attachment/upload"
	// CaseTable is the table a support case lives in.
	CaseTable = "sn_customerservice_case"
	// SearchPath reads that table directly. The connector list route answers
	// with a capped slice of the account that leaves out recently opened cases,
	// so the cases this control plane opened are found here instead.
	SearchPath = "/api/now/table/" + CaseTable
)

// DefaultTimeout bounds one attempt. A ticket is created from a browser
// request, so a ServiceNow that has stopped answering has to fail rather than
// hold the dialog open.
const DefaultTimeout = 15 * time.Second

// DefaultMaxAttempts is how many times one call is tried, first attempt
// included. Three covers a rolling instance without making a developer wait
// through a minute of backoff.
const DefaultMaxAttempts = 3

// DefaultRetryWait is the wait before the second attempt. It doubles after that.
const DefaultRetryWait = 500 * time.Millisecond

// Paging bounds for the ticket list.
const (
	// DefaultPageSize is the page the support list opens with.
	DefaultPageSize = 25
	// MaxPageSize caps one read, both what an API caller may ask the adapter
	// for and what is asked of ServiceNow.
	MaxPageSize = 100
)

// searchFields are the columns a ticket row is built from. The case table has
// far more than these, and anything extra is paid for on the wire and against
// the response cap.
var searchFields = []string{
	"number",
	"sys_id",
	"state",
	"priority",
	"account",
	"contact",
	"short_description",
	"description",
	"u_main_technology",
	"opened_at",
	"resolved_at",
	"sys_updated_on",
}

// maxErrorBody caps how much of an error response is read, so an HTML error
// page from a misconfigured proxy cannot fill memory.
const maxErrorBody = 8 << 10

// maxResponseBody caps a successful body. A case carries its full description,
// which runs to a few kilobytes on its own, so a page of them is comfortably
// under this. Eight megabytes leaves room for an unusually long one without
// letting a runaway response fill memory.
const maxResponseBody = 8 << 20

// MaxAttachmentBytes caps one upload. Attachments are buffered so a retry can
// resend them, which only works while they fit in memory.
const MaxAttachmentBytes = 5 << 20

// Options configures a Client.
type Options struct {
	// BaseURL is the ServiceNow instance, such as https://example.service-now.com.
	BaseURL string
	// APIKey is sent as the x-sn-apikey header on every request.
	APIKey string
	// ClientToken is sent as the client_token body field on every request.
	ClientToken string
	// HTTP overrides the transport, for tests and for a custom CA bundle.
	HTTP *http.Client
	// Timeout bounds one attempt. Zero means DefaultTimeout.
	Timeout time.Duration
	// MaxAttempts is the attempt budget for one call, first attempt included.
	// Zero means DefaultMaxAttempts.
	MaxAttempts int
	// RetryWait is the wait before the second attempt, doubling after that.
	// Zero means DefaultRetryWait.
	RetryWait time.Duration
	// Sleep replaces the wait between attempts, so a test does not sit through
	// a backoff. Nil means time.Sleep.
	Sleep func(time.Duration)
}

// Client talks to the Percona Connector.
type Client struct {
	baseURL     *url.URL
	apiKey      string
	clientToken string
	http        *http.Client
	maxAttempts int
	retryWait   time.Duration
	sleep       func(time.Duration)
}

// New validates the options and returns a client. Both credentials are
// required: the caller builds this only when the Secret is mounted, and support
// endpoints answer 501 until then.
func New(opts Options) (*Client, error) {
	raw := strings.TrimSpace(opts.BaseURL)
	if raw == "" {
		return nil, fmt.Errorf("servicenow: base URL must not be blank")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("servicenow: base URL %q is not a URL: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("servicenow: base URL %q needs an http or https scheme", raw)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("servicenow: base URL %q has no host", raw)
	}

	apiKey := strings.TrimSpace(opts.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("servicenow: API key must not be blank")
	}

	clientToken := strings.TrimSpace(opts.ClientToken)
	if clientToken == "" {
		return nil, fmt.Errorf("servicenow: client token must not be blank")
	}

	httpClient := opts.HTTP
	if httpClient == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		httpClient = &http.Client{Timeout: timeout}
	}

	attempts := opts.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultMaxAttempts
	}

	wait := opts.RetryWait
	if wait <= 0 {
		wait = DefaultRetryWait
	}

	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	parsed.Path = strings.TrimSuffix(parsed.Path, "/")

	return &Client{
		baseURL:     parsed,
		apiKey:      apiKey,
		clientToken: clientToken,
		http:        httpClient,
		maxAttempts: attempts,
		retryWait:   wait,
		sleep:       sleep,
	}, nil
}

// PortalURL is the Percona Support Portal index for this instance, which the UI
// links to next to a ticket.
func (c *Client) PortalURL() string {
	if c == nil || c.baseURL == nil {
		return ""
	}

	return strings.TrimSuffix(c.baseURL.String(), "/") + "/percona"
}

// TicketURL returns the stable native ServiceNow link for a case record. A
// ticket without both a table and record id has no reliable per-case URL.
func TicketURL(baseURL, tableName, sysID string) string {
	table := strings.TrimSpace(tableName)
	record := strings.TrimSpace(sysID)
	if table == "" || record == "" {
		return ""
	}
	for _, r := range table {
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return ""
		}
	}

	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}

	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/" + table + ".do"
	parsed.RawQuery = url.Values{"sys_id": {record}}.Encode()
	parsed.Fragment = ""

	return parsed.String()
}

// ValidateInstanceURL reports whether raw can be used as the ServiceNow
// instance. Both tokens travel on every call, one in a header and one in the
// body, so anything but HTTPS would hand them to whatever sits on the path.
//
// This is checked where the instance is configured, on startup and on a paste,
// rather than in New: New is the transport, and a test drives it over a local
// httptest server.
func ValidateInstanceURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("servicenow: instance URL must not be blank")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("servicenow: instance URL %q is not a URL: %w", trimmed, err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("servicenow: instance URL %q needs an https scheme", trimmed)
	}
	if parsed.Host == "" {
		return fmt.Errorf("servicenow: instance URL %q has no host", trimmed)
	}

	return nil
}

// CreateTicket opens a case. ticket_type is always CASE. u_main_technology
// follows the database engine, and defaults to MySQL.
func (c *Client) CreateTicket(ctx context.Context, in CreateTicketInput) (*Ticket, error) {
	subject := strings.TrimSpace(in.ShortDescription)
	if subject == "" {
		return nil, fmt.Errorf("servicenow: ticket subject must not be blank")
	}

	technology := strings.TrimSpace(in.MainTechnology)
	if technology == "" {
		technology = MainTechnology
	}

	body := map[string]any{
		"short_description": subject,
		"description":       in.Description,
		"ticket_type":       TicketType,
		"u_main_technology": technology,
	}

	payload, err := c.postTicket(ctx, CreatePath, nil, body)
	if err != nil {
		return nil, err
	}

	if payload.sysID() == "" && payload.number() == "" {
		return nil, &APIError{
			Method:   http.MethodPost,
			Path:     CreatePath,
			Status:   http.StatusOK,
			Rejected: true,
			Message:  "the connector reported no ticket id",
		}
	}

	ticket := payload.toTicket()

	return &ticket, nil
}

// ListTickets returns one page of the account's cases, newest first as
// ServiceNow orders them.
func (c *Client) ListTickets(ctx context.Context, limit, offset int) ([]Ticket, error) {
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	if offset < 0 {
		offset = 0
	}

	query := url.Values{}
	query.Set("sysparm_limit", strconv.Itoa(limit))
	query.Set("sysparm_offset", strconv.Itoa(offset))

	var out envelope
	if err := c.post(ctx, ListPath, query, nil, &out); err != nil {
		return nil, err
	}

	payloads, err := decodeList(out.Result)
	if err != nil {
		return nil, fmt.Errorf("servicenow: decode %s response: %w", ListPath, err)
	}

	tickets := make([]Ticket, 0, len(payloads))
	for _, payload := range payloads {
		if payload.empty() {
			continue
		}

		tickets = append(tickets, payload.toTicket())
	}

	return tickets, nil
}

// GetTicket reads one case by its number, the identifier a developer quotes.
func (c *Client) GetTicket(ctx context.Context, number string) (*Ticket, error) {
	trimmed := strings.TrimSpace(number)
	if trimmed == "" {
		return nil, fmt.Errorf("servicenow: ticket number must not be blank")
	}

	return c.details(ctx, map[string]any{"ticket_number": trimmed})
}

// GetTicketByID reads one case by its sys_id. A create answers with the id and
// not always with the number, so this is how the number is resolved.
func (c *Client) GetTicketByID(ctx context.Context, sysID string) (*Ticket, error) {
	trimmed := strings.TrimSpace(sysID)
	if trimmed == "" {
		return nil, fmt.Errorf("servicenow: ticket id must not be blank")
	}

	return c.details(ctx, map[string]any{"ticket_id": trimmed})
}

// ReplyTicket adds a public customer comment to an existing case.
func (c *Client) ReplyTicket(ctx context.Context, in ReplyTicketInput) error {
	table := strings.TrimSpace(in.TableName)
	if table == "" {
		return fmt.Errorf("servicenow: ticket table name must not be blank")
	}

	sysID := strings.TrimSpace(in.SysID)
	if sysID == "" {
		return fmt.Errorf("servicenow: ticket id must not be blank")
	}

	body := strings.TrimSpace(in.Body)
	if body == "" {
		return fmt.Errorf("servicenow: ticket reply must not be blank")
	}

	path := "/api/now/table/" + url.PathEscape(table) + "/" + url.PathEscape(sysID)

	return c.patch(ctx, path, map[string]any{"comments": body})
}

func (c *Client) details(ctx context.Context, body map[string]any) (*Ticket, error) {
	payload, err := c.postTicket(ctx, DetailsPath, nil, body)
	if err != nil {
		return nil, err
	}

	if payload.empty() {
		return nil, &APIError{
			Method: http.MethodPost,
			Path:   DetailsPath,
			Status: http.StatusNotFound,
		}
	}

	ticket := payload.toTicket()

	return &ticket, nil
}

// UploadAttachment attaches a file to a record. The reader is buffered so a
// retry can resend the same bytes, which is why it is capped.
func (c *Client) UploadAttachment(
	ctx context.Context,
	tableName, sysID string,
	r io.Reader,
	filename string,
) error {
	table := strings.TrimSpace(tableName)
	if table == "" {
		return fmt.Errorf("servicenow: attachment table name must not be blank")
	}

	record := strings.TrimSpace(sysID)
	if record == "" {
		return fmt.Errorf("servicenow: attachment record id must not be blank")
	}

	name := strings.TrimSpace(filename)
	if name == "" {
		return fmt.Errorf("servicenow: attachment file name must not be blank")
	}
	if r == nil {
		return fmt.Errorf("servicenow: attachment content must not be nil")
	}

	content, err := io.ReadAll(io.LimitReader(r, MaxAttachmentBytes+1))
	if err != nil {
		return fmt.Errorf("servicenow: read attachment %q: %w", name, err)
	}
	if len(content) > MaxAttachmentBytes {
		return fmt.Errorf("servicenow: attachment %q is larger than %d bytes", name, MaxAttachmentBytes)
	}

	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)

	// The connector authenticates every request with both carriers, uploads
	// included, so the token goes in as a form field here.
	for field, value := range map[string]string{
		"table_name":   table,
		"table_sys_id": record,
		"client_token": c.clientToken,
	} {
		if err := form.WriteField(field, value); err != nil {
			return fmt.Errorf("servicenow: build attachment form: %w", err)
		}
	}

	file, err := form.CreateFormFile("uploadFile", name)
	if err != nil {
		return fmt.Errorf("servicenow: build attachment form: %w", err)
	}
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("servicenow: build attachment form: %w", err)
	}
	if err := form.Close(); err != nil {
		return fmt.Errorf("servicenow: build attachment form: %w", err)
	}

	body := buf.Bytes()
	contentType := form.FormDataContentType()

	return c.send(ctx, http.MethodPost, AttachmentPath, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(
			ctx, http.MethodPost, c.baseURL.String()+AttachmentPath, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}

		req.Header.Set("Content-Type", contentType)

		return req, nil
	}, nil)
}

// postTicket posts one connector call whose result is a single record.
func (c *Client) postTicket(
	ctx context.Context,
	path string,
	query url.Values,
	fields map[string]any,
) (ticketPayload, error) {
	var out envelope
	if err := c.post(ctx, path, query, fields, &out); err != nil {
		return ticketPayload{}, err
	}

	var payload ticketPayload
	if len(out.Result) > 0 {
		if err := json.Unmarshal(out.Result, &payload); err != nil {
			return ticketPayload{}, fmt.Errorf("servicenow: decode %s response: %w", path, err)
		}
	}

	payload = payload.resolve()

	if payload.rejected() {
		return ticketPayload{}, &APIError{
			Method:   http.MethodPost,
			Path:     path,
			Status:   http.StatusOK,
			Rejected: true,
			Message:  payload.ErrorMessage,
		}
	}

	return payload, nil
}

// SearchTickets returns the cases whose description contains any of match,
// newest first. The instance does the filtering, so this is one request
// regardless of how large the account is or how many markers are passed.
func (c *Client) SearchTickets(ctx context.Context, match []string, limit, offset int) ([]Ticket, error) {
	conditions := make([]string, 0, len(match))
	for _, one := range match {
		if needle := strings.TrimSpace(one); needle != "" {
			conditions = append(conditions, "descriptionLIKE"+needle)
		}
	}
	if len(conditions) == 0 {
		return nil, fmt.Errorf("servicenow: search needs something to match")
	}

	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	if offset < 0 {
		offset = 0
	}

	query := url.Values{}
	// ^OR joins the conditions and the ordering applies to all of them, so one
	// page stays in one request however many markers there are.
	query.Set("sysparm_query", strings.Join(conditions, "^OR")+"^ORDERBYDESCsys_created_on")
	query.Set("sysparm_limit", strconv.Itoa(limit))
	query.Set("sysparm_offset", strconv.Itoa(offset))
	// Reference fields such as the account and the technology are record ids
	// without this, which is not something to show anyone.
	query.Set("sysparm_display_value", "true")
	// The case table has far more columns than a ticket row shows, and the
	// instance sends all of them unless asked otherwise.
	query.Set("sysparm_fields", strings.Join(searchFields, ","))

	var out envelope
	if err := c.get(ctx, SearchPath, query, &out); err != nil {
		return nil, err
	}

	payloads, err := decodeList(out.Result)
	if err != nil {
		return nil, fmt.Errorf("servicenow: decode %s response: %w", SearchPath, err)
	}

	tickets := make([]Ticket, 0, len(payloads))
	for _, payload := range payloads {
		if payload.empty() {
			continue
		}

		ticket := payload.toTicket()
		// The table answers about itself, so it does not repeat the table name
		// on every row, and a reply needs it to find the record.
		if ticket.TableName == "" {
			ticket.TableName = CaseTable
		}

		tickets = append(tickets, ticket)
	}

	return tickets, nil
}

// get reads one Table API route. The key alone authenticates it: there is no
// body to carry the client token on.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	target := c.baseURL.String() + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	return c.send(ctx, http.MethodGet, path, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}

		req.Header.Set("Accept", "application/json")

		return req, nil
	}, out)
}

// post sends one JSON request with the client token merged into the body.
func (c *Client) post(
	ctx context.Context,
	path string,
	query url.Values,
	fields map[string]any,
	out any,
) error {
	body := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		body[key] = value
	}
	body["client_token"] = c.clientToken

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("servicenow: encode %s request: %w", path, err)
	}

	target := c.baseURL.String() + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	return c.send(ctx, http.MethodPost, path, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(encoded))
		if err != nil {
			return nil, err
		}

		req.Header.Set("Content-Type", "application/json")

		return req, nil
	}, out)
}

// patch sends one Table API update. The Percona ServiceNow integration expects
// both authentication carriers on this route, as it does for attachments.
func (c *Client) patch(ctx context.Context, path string, fields map[string]any) error {
	body := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		body[key] = value
	}
	body["client_token"] = c.clientToken

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("servicenow: encode %s request: %w", path, err)
	}

	return c.send(ctx, http.MethodPatch, path, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPatch,
			c.baseURL.String()+path,
			bytes.NewReader(encoded),
		)
		if err != nil {
			return nil, err
		}

		req.Header.Set("Content-Type", "application/json")

		return req, nil
	}, nil)
}

// send runs one call with retries. build produces a fresh request per attempt,
// because a request body can only be read once.
//
// The context is attached to every request, so canceling a browser request
// cancels the ServiceNow call and stops the retry loop.
func (c *Client) send(
	ctx context.Context,
	method string,
	path string,
	build func() (*http.Request, error),
	out any,
) error {
	var last error

	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			if last != nil {
				return last
			}

			return fmt.Errorf("servicenow: %s %s: %w", method, path, err)
		}

		if attempt > 1 {
			c.sleep(c.backoff(attempt))

			if err := ctx.Err(); err != nil {
				return last
			}
		}

		done, err := c.attempt(ctx, method, path, build, out)
		if done {
			return err
		}

		last = err
	}

	return last
}

// attempt performs one request. It reports whether the call is settled: a
// success, or a failure that another attempt cannot improve on.
func (c *Client) attempt(
	ctx context.Context,
	method string,
	path string,
	build func() (*http.Request, error),
	out any,
) (bool, error) {
	req, err := build()
	if err != nil {
		return true, fmt.Errorf("servicenow: build %s %s request: %w", method, path, err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-sn-apikey", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		// The URL carries no credential, but the wrapper repeats the full
		// target on every line, so it is unwrapped for a shorter message.
		failed := fmt.Errorf("servicenow: %s %s: %w", method, path, unwrapURLError(err))

		if ctx.Err() != nil || errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return true, failed
		}

		return false, failed
	}
	// The rest of the body is drained so the connection can be reused. An
	// oversized one is the exception: draining it would spend the time and
	// bandwidth the cap exists to save.
	oversized := false
	defer func() {
		if !oversized {
			_, _ = io.Copy(io.Discard, resp.Body)
		}
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := newAPIError(method, path, resp)

		return !retryableStatus(resp.StatusCode), apiErr
	}

	if out == nil {
		return true, nil
	}

	// One byte past the cap tells a body that is merely large apart from one
	// that was cut off. Without it an oversized page decodes as malformed json.
	limited := io.LimitReader(resp.Body, maxResponseBody+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return true, fmt.Errorf("servicenow: read %s response: %w", path, err)
	}
	if len(raw) > maxResponseBody {
		oversized = true

		return true, fmt.Errorf("servicenow: read %s response: %w", path, ErrResponseTooLarge)
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return true, fmt.Errorf("servicenow: decode %s response: %w", path, err)
	}

	return true, nil
}

// backoff is the wait before an attempt, doubling each time.
func (c *Client) backoff(attempt int) time.Duration {
	return c.retryWait << (attempt - 2)
}

// retryableStatus reports the statuses a second attempt can fix: a ServiceNow
// instance that is rolling, a proxy with no upstream, and a rate limit.
func retryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// decodeList reads a list result, which is an array or an object wrapping one.
func decodeList(raw json.RawMessage) ([]ticketPayload, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	var array []ticketPayload
	if err := json.Unmarshal(raw, &array); err == nil {
		return array, nil
	}

	var wrapped listPayload
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}

	return wrapped.tickets(), nil
}

// newAPIError reads the ServiceNow error body, which is
// `{"error":{"message":"...","detail":"..."}}`, and falls back to the status
// alone when the body is a different shape.
func newAPIError(method, path string, resp *http.Response) *APIError {
	apiErr := &APIError{Method: method, Path: path, Status: resp.StatusCode}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil || len(raw) == 0 {
		return apiErr
	}

	var decoded struct {
		Error struct {
			Message string `json:"message"`
			Detail  string `json:"detail"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return apiErr
	}

	switch {
	case decoded.Error.Message != "":
		apiErr.Message = decoded.Error.Message
		if decoded.Error.Detail != "" {
			apiErr.Message += ": " + decoded.Error.Detail
		}
	default:
		apiErr.Message = decoded.Message
	}

	return apiErr
}

// unwrapURLError strips the *url.Error wrapper so the message stays one line
// while errors.Is on context.Canceled still works.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}

	return err
}
