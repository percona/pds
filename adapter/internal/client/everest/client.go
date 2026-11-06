// Package everest is a thin hand-written client for the OpenEverest HTTP API.
// It is hand-written rather than generated, and covers only the routes the
// adapter calls.
//
// The v2 API is cluster and namespace scoped under `/v1`:
//
//	GET    /v1/clusters/{cluster}/namespaces/{namespace}/instances
//	GET    /v1/clusters/{cluster}/namespaces/{namespace}/instances/{name}
//	PUT    /v1/clusters/{cluster}/namespaces/{namespace}/instances/{name}
//	DELETE /v1/clusters/{cluster}/namespaces/{namespace}/instances/{name}
//	GET    /v1/clusters/{cluster}/namespaces/{namespace}/instances/{name}/connection
//	GET    /v1/clusters/{cluster}/namespaces/{namespace}/instances/{name}/backups
//	GET    /v1/clusters/{cluster}/namespaces/{namespace}/backups
//	POST   /v1/clusters/{cluster}/namespaces/{namespace}/backups
//
// Authentication is `Authorization: Bearer <token>`; the token comes from a
// Kubernetes Secret and is never logged.
package everest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIPrefix is the server prefix every route sits under.
const APIPrefix = "/v1"

// DefaultTimeout bounds one Everest call. It is generous because the API
// server can be slow under load, and short enough that a hung downstream does
// not hold a browser request open indefinitely.
const DefaultTimeout = 30 * time.Second

// maxErrorBody caps how much of an error response is read before giving up, so
// an HTML error page from a misconfigured ingress cannot fill memory.
const maxErrorBody = 8 << 10

// Options configures a Client.
type Options struct {
	// BaseURL is the Everest service URL, without the /v1 prefix.
	BaseURL string
	// Cluster is the Kubernetes cluster name Everest scopes instances under.
	// Empty means DefaultCluster.
	Cluster string
	// Token is the JWT sent as a bearer token. Empty means unauthenticated,
	// which only works against a development Everest. TokenSource wins over it.
	Token string
	// TokenSource supplies the bearer token for each call. Everest issues a
	// session that expires, so a client that reads a token once stops working
	// partway through its own uptime.
	//
	// refresh is true when the token it last gave was refused, which is the
	// source's cue to replace it rather than hand back the same one.
	TokenSource func(ctx context.Context, refresh bool) (string, error)
	// HTTP overrides the transport, for tests and for a custom CA bundle.
	HTTP *http.Client
	// Timeout bounds one call. Zero means DefaultTimeout.
	Timeout time.Duration
}

// Client talks to the OpenEverest HTTP API.
type Client struct {
	baseURL *url.URL
	cluster string
	token   string
	source  func(ctx context.Context, refresh bool) (string, error)
	http    *http.Client
}

// New validates the options and returns a client.
func New(opts Options) (*Client, error) {
	raw := strings.TrimSpace(opts.BaseURL)
	if raw == "" {
		return nil, fmt.Errorf("everest: base URL must not be blank")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("everest: base URL %q is not a URL: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("everest: base URL %q needs an http or https scheme", raw)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("everest: base URL %q has no host", raw)
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

	cluster := strings.TrimSpace(opts.Cluster)
	if cluster == "" {
		cluster = DefaultCluster
	}

	return &Client{
		baseURL: parsed,
		cluster: cluster,
		token:   opts.Token,
		source:  opts.TokenSource,
		http:    httpClient,
	}, nil
}

// bearer returns the token for one attempt. refresh asks the source to replace
// what it last gave, which is what happens after Everest refuses one.
func (c *Client) bearer(ctx context.Context, refresh bool) (string, error) {
	if c.source == nil {
		return c.token, nil
	}

	token, err := c.source(ctx, refresh)
	if err != nil {
		return "", fmt.Errorf("everest: read token: %w", err)
	}

	return token, nil
}

// Session trades an account password for a bearer token. The route is
// POST /v1/auth/token with the password grant. It is unauthenticated, so it
// works on a client that has no token yet, which is how a token source gets
// its first one.
//
// It sends the request itself rather than going through do, so it never asks
// the token source for a bearer. A source that mints through Session would
// otherwise call into itself, and because it holds a lock while minting that
// is a deadlock rather than merely a loop.
//
// The token it returns is a secret with the same reach as the account, so it
// belongs in memory and in an Authorization header, never in a log line.
func (c *Client) Session(ctx context.Context, username, password string) (string, error) {
	if err := requireAll("username", username, "password", password); err != nil {
		return "", err
	}

	encoded, err := json.Marshal(map[string]string{
		"grant_type": "password",
		"username":   strings.TrimSpace(username),
		"password":   strings.TrimSpace(password),
	})
	if err != nil {
		return "", fmt.Errorf("everest: encode %s request: %w", AuthTokenPath, err)
	}

	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.attempt(ctx, http.MethodPost, AuthTokenPath, encoded, "", true, &out); err != nil {
		return "", err
	}

	if strings.TrimSpace(out.AccessToken) == "" {
		return "", fmt.Errorf("everest: %s returned no token", AuthTokenPath)
	}

	return out.AccessToken, nil
}

// ListInstances returns every instance in a namespace.
func (c *Client) ListInstances(ctx context.Context, namespace string) ([]Instance, error) {
	if err := requireAll("namespace", namespace); err != nil {
		return nil, err
	}

	var out InstanceList
	if err := c.do(ctx, http.MethodGet, c.instancesPath(namespace), nil, &out); err != nil {
		return nil, err
	}

	return out.Items, nil
}

// GetInstance returns one instance.
func (c *Client) GetInstance(ctx context.Context, namespace, name string) (*Instance, error) {
	if err := requireAll("namespace", namespace, "instance name", name); err != nil {
		return nil, err
	}

	var out Instance
	if err := c.do(ctx, http.MethodGet, c.instancePath(namespace, name), nil, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

// UpdateInstance replaces the spec of an existing instance.
func (c *Client) UpdateInstance(
	ctx context.Context,
	namespace, name string,
	in *Instance,
) (*Instance, error) {
	if err := requireAll("namespace", namespace, "instance name", name); err != nil {
		return nil, err
	}
	if in == nil {
		return nil, fmt.Errorf("everest: instance must not be nil")
	}

	body := *in
	body.APIVersion = APIVersion
	body.Kind = KindInstance
	// The API validates the whole body against the CRD schema, and the status
	// read back drops fields the schema requires, such as a condition's empty
	// message. Status belongs to the operator, so the update sends none.
	body.Status = InstanceStatus{}

	var out Instance
	if err := c.do(ctx, http.MethodPut, c.instancePath(namespace, name), body, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

// DeleteInstance removes an instance. The operator's finalizers take its
// backups and restores with it.
func (c *Client) DeleteInstance(ctx context.Context, namespace, name string) error {
	if err := requireAll("namespace", namespace, "instance name", name); err != nil {
		return err
	}

	return c.do(ctx, http.MethodDelete, c.instancePath(namespace, name), nil, nil)
}

// GetInstanceConnection returns the connection details the provider published.
// It answers ErrNotFound until the instance exists and that Secret has been
// created.
func (c *Client) GetInstanceConnection(ctx context.Context, namespace, name string) (*Connection, error) {
	if err := requireAll("namespace", namespace, "instance name", name); err != nil {
		return nil, err
	}

	var out Connection
	if err := c.do(ctx, http.MethodGet, c.instancePath(namespace, name)+"/connection", nil, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

// ListInstanceBackups returns every backup of one instance.
func (c *Client) ListInstanceBackups(ctx context.Context, namespace, name string) ([]Backup, error) {
	if err := requireAll("namespace", namespace, "instance name", name); err != nil {
		return nil, err
	}

	var out BackupList
	if err := c.do(ctx, http.MethodGet, c.instancePath(namespace, name)+"/backups", nil, &out); err != nil {
		return nil, err
	}

	return out.Items, nil
}

// CreateBackup starts an on-demand backup. Backups are a namespace-scoped
// collection that names their instance.
func (c *Client) CreateBackup(ctx context.Context, namespace string, in *Backup) (*Backup, error) {
	if err := requireAll("namespace", namespace); err != nil {
		return nil, err
	}
	if in == nil {
		return nil, fmt.Errorf("everest: backup must not be nil")
	}

	body := *in
	body.APIVersion = BackupAPIVersion
	body.Kind = KindBackup

	var out Backup
	if err := c.do(ctx, http.MethodPost, c.backupsPath(namespace), body, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

func (c *Client) namespacePath(namespace string) string {
	return APIPrefix + "/clusters/" + url.PathEscape(c.cluster) + "/namespaces/" + url.PathEscape(namespace)
}

func (c *Client) instancesPath(namespace string) string {
	return c.namespacePath(namespace) + "/instances"
}

func (c *Client) instancePath(namespace, name string) string {
	return c.instancesPath(namespace) + "/" + url.PathEscape(name)
}

func (c *Client) backupsPath(namespace string) string {
	return c.namespacePath(namespace) + "/backups"
}

// do performs one request. The context is attached to the request so canceling
// a browser request cancels the Everest call. A nil out means the response
// body is discarded, which is what a 204 delete returns.
//
// A refused token is retried once. Everest issues a session that expires, so a
// 401 is usually the session ending rather than the credentials being wrong,
// and asking the source again is what carries the call. The retry only happens
// when the second token differs from the first, so a source that keeps handing
// back the same refused token cannot spin.
func (c *Client) do(ctx context.Context, method, path string, payload, out any) error {
	var encoded []byte
	if payload != nil {
		marshalled, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("everest: encode %s %s request: %w", method, path, err)
		}
		encoded = marshalled
	}

	token, err := c.bearer(ctx, false)
	if err != nil {
		return err
	}

	err = c.attempt(ctx, method, path, encoded, token, payload != nil, out)
	if !errors.Is(err, ErrUnauthorized) || c.source == nil {
		return err
	}

	refreshed, refreshErr := c.bearer(ctx, true)
	if refreshErr != nil {
		return refreshErr
	}
	if refreshed == token {
		return err
	}

	return c.attempt(ctx, method, path, encoded, refreshed, payload != nil, out)
}

// attempt sends the request once. The body is rebuilt from the encoded bytes
// each time, because a reader can only be read through once.
func (c *Client) attempt(
	ctx context.Context,
	method, path string,
	encoded []byte,
	token string,
	hasPayload bool,
	out any,
) error {
	var body io.Reader
	if encoded != nil {
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL.String()+path, body)
	if err != nil {
		return fmt.Errorf("everest: build %s %s request: %w", method, path, err)
	}

	req.Header.Set("Accept", "application/json")
	if hasPayload {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("everest: %s %s: %w", method, path, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newAPIError(method, path, resp)
	}

	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("everest: decode %s %s response: %w", method, path, err)
	}

	return nil
}

// newAPIError reads the Everest error body, which is `{"message": "..."}`, and
// falls back to the status alone when the body is not that shape.
func newAPIError(method, path string, resp *http.Response) error {
	apiErr := &APIError{Method: method, Path: path, Status: resp.StatusCode}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil || len(raw) == 0 {
		return apiErr
	}

	var decoded struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &decoded); err == nil {
		apiErr.Message = decoded.Message
	}

	return apiErr
}

// requireAll rejects blank path segments before a request is built, so a typo
// cannot turn a get into a list of the whole namespace. Arguments are label and
// value pairs.
func requireAll(pairs ...string) error {
	for i := 0; i+1 < len(pairs); i += 2 {
		if strings.TrimSpace(pairs[i+1]) == "" {
			return fmt.Errorf("everest: %s must not be blank", pairs[i])
		}
	}

	return nil
}
