// Package tailnet talks to the private network Percona reaches support
// environments through. Two OAuth clients are used, and each reaches only what
// it needs:
//
//	key client      POST   /tailnet/{t}/keys, DELETE /tailnet/{t}/keys/{id},
//	                GET    /tailnet/{t}/devices, DELETE /device/{id}
//	service client  GET and PUT /tailnet/{t}/vip-services/{name}
//
// The key client mints keys for one tag, so a key can never join a device to
// anything else. The service client creates private addresses carrying one tag.
// Neither reaches the policy, which is edited by hand. Tokens are kept in
// memory and never appear in an error.
package tailnet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the API.
const DefaultBaseURL = "https://api.tailscale.com"

// DefaultTimeout bounds one call. A customer is waiting on a window.
const DefaultTimeout = 15 * time.Second

// maxErrorBody caps how much of a refusal is kept for the log.
const maxErrorBody = 2 << 10

// servicePorts is the only port a private address serves: the Kubernetes API
// of the environment behind it.
var servicePorts = []string{"tcp:443"}

// ErrAddressPolicy means a private address is missing, or does not carry
// exactly the tag and port the hand-edited policy was written for. The hub
// refuses to open a window against it.
var ErrAddressPolicy = errors.New("the private address does not match the policy")

// APIError is a call the tailnet answered with something other than success.
// Body is the start of its answer and is meant for logs.
type APIError struct {
	Method string
	Path   string
	Status int
	Body   string
}

// Error implements error.
func (e *APIError) Error() string {
	msg := fmt.Sprintf("tailnet: %s %s: %d %s", e.Method, e.Path, e.Status, http.StatusText(e.Status))
	if e.Body != "" {
		msg += ": " + e.Body
	}

	return msg
}

// Options configures a Client.
type Options struct {
	// BaseURL overrides DefaultBaseURL, for tests.
	BaseURL string
	// Tailnet is the network's name, as the API's path takes it.
	Tailnet string
	// Domain is the network's MagicDNS domain, such as tail1234.ts.net, which a
	// private address's DNS name sits under. The hub is told it, not asked.
	Domain string
	// KeyClientID and KeyClientSecret are the OAuth client that mints keys
	// for EnvironmentTag.
	KeyClientID     string
	KeyClientSecret string
	// ServiceClientID and ServiceClientSecret are the OAuth client limited to
	// private addresses carrying ServiceTag.
	ServiceClientID     string
	ServiceClientSecret string
	// EnvironmentTag is the one tag every environment's device carries.
	EnvironmentTag string
	// ServiceTag is the one tag every private address carries.
	ServiceTag string
}

// Client calls the tailnet API with the two credentials.
type Client struct {
	base           *url.URL
	tailnet        string
	domain         string
	environmentTag string
	serviceTag     string
	http           *http.Client
	keys           *tokenSource
	services       *tokenSource
}

// New validates the options and returns a client. Nothing is called yet.
func New(opts Options) (*Client, error) {
	for name, value := range map[string]string{
		"tailnet":               opts.Tailnet,
		"domain":                opts.Domain,
		"key client id":         opts.KeyClientID,
		"key client secret":     opts.KeyClientSecret,
		"service client id":     opts.ServiceClientID,
		"service client secret": opts.ServiceClientSecret,
		"environment tag":       opts.EnvironmentTag,
		"service tag":           opts.ServiceTag,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("tailnet: %s must not be blank", name)
		}
	}
	if opts.EnvironmentTag == opts.ServiceTag {
		return nil, errors.New("tailnet: the environment tag and the service tag must differ")
	}

	domain := strings.ToLower(strings.Trim(strings.TrimSpace(opts.Domain), "."))
	if domain == "" || strings.ContainsAny(domain, "/: ") {
		return nil, fmt.Errorf("tailnet: domain %q must be a bare DNS name such as tail1234.ts.net", opts.Domain)
	}

	raw := strings.TrimSpace(opts.BaseURL)
	if raw == "" {
		raw = DefaultBaseURL
	}
	base, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("tailnet: base URL %q is not a URL: %w", raw, err)
	}
	if (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("tailnet: base URL %q needs an http or https scheme and a host", raw)
	}

	httpClient := &http.Client{Timeout: DefaultTimeout}
	client := &Client{
		base:           base,
		tailnet:        strings.TrimSpace(opts.Tailnet),
		domain:         domain,
		environmentTag: opts.EnvironmentTag,
		serviceTag:     opts.ServiceTag,
		http:           httpClient,
	}
	client.keys = &tokenSource{client: client, id: opts.KeyClientID, secret: opts.KeyClientSecret}
	client.services = &tokenSource{client: client, id: opts.ServiceClientID, secret: opts.ServiceClientSecret}

	return client, nil
}

// MintKey makes a single-use key that expires after ttl and joins one
// ephemeral device to the environment tag. The secret is returned once and the
// caller keeps only the id.
func (c *Client) MintKey(ctx context.Context, ttl time.Duration) (id, secret string, err error) {
	body := map[string]any{
		"capabilities": map[string]any{"devices": map[string]any{"create": map[string]any{
			"reusable":      false,
			"ephemeral":     true,
			"preauthorized": true,
			"tags":          []string{c.environmentTag},
		}}},
		"expirySeconds": int(ttl.Seconds()),
		"description":   "pds support access",
	}
	var out struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := c.do(ctx, c.keys, http.MethodPost, c.tailnetPath("keys"), body, &out); err != nil {
		return "", "", err
	}
	if out.ID == "" || out.Key == "" {
		return "", "", errors.New("tailnet: the key answer has no id or key")
	}

	return out.ID, out.Key, nil
}

// RevokeKey revokes a key by id. A key that is already gone is not an error.
func (c *Client) RevokeKey(ctx context.Context, id string) error {
	err := c.do(ctx, c.keys, http.MethodDelete, c.tailnetPath("keys", id), nil, nil)

	return ignoreNotFound(err)
}

// Device is a node on the tailnet.
type Device struct {
	ID       string
	Hostname string
}

// ListDevices returns every device carrying the environment tag, which is every
// device a support window has joined.
func (c *Client) ListDevices(ctx context.Context) ([]Device, error) {
	var out struct {
		Devices []struct {
			ID       string   `json:"id"`
			Hostname string   `json:"hostname"`
			Tags     []string `json:"tags"`
		} `json:"devices"`
	}
	if err := c.do(ctx, c.keys, http.MethodGet, c.tailnetPath("devices"), nil, &out); err != nil {
		return nil, err
	}
	var devices []Device
	for _, device := range out.Devices {
		if slices.Contains(device.Tags, c.environmentTag) {
			devices = append(devices, Device{ID: device.ID, Hostname: device.Hostname})
		}
	}

	return devices, nil
}

// DeleteDevice deletes one device, which ends its access at once. A device that
// is already gone is not an error.
func (c *Client) DeleteDevice(ctx context.Context, id string) error {
	err := c.do(ctx, c.keys, http.MethodDelete, "/api/v2/device/"+url.PathEscape(id), nil, nil)

	return ignoreNotFound(err)
}

// DeleteDevices deletes every device whose hostname is the given name, which is
// how a window's device is found: the agent joins it under the window id.
// Deleting a device ends its access at once. No device is not an error.
func (c *Client) DeleteDevices(ctx context.Context, hostname string) error {
	devices, err := c.ListDevices(ctx)
	if err != nil {
		return err
	}
	for _, device := range devices {
		if device.Hostname != hostname {
			continue
		}
		if err := c.DeleteDevice(ctx, device.ID); err != nil {
			return err
		}
	}

	return nil
}

// CreateAddress creates a private address with the service tag and the one
// port the policy allows.
func (c *Client) CreateAddress(ctx context.Context, name string) error {
	body := map[string]any{"name": name, "tags": []string{c.serviceTag}, "ports": servicePorts}

	return c.do(ctx, c.services, http.MethodPut, c.tailnetPath("vip-services", name), body, nil)
}

// AddressHost returns the DNS name a private address is served under, which is
// also what the proxy behind it names the Secret holding its certificate.
func (c *Client) AddressHost(name string) string {
	return strings.TrimPrefix(name, "svc:") + "." + c.domain
}

// ListAddresses returns the name of every private address carrying the service
// tag.
func (c *Client) ListAddresses(ctx context.Context) ([]string, error) {
	var out struct {
		Services []struct {
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		} `json:"vipServices"`
	}
	if err := c.do(ctx, c.services, http.MethodGet, c.tailnetPath("vip-services"), nil, &out); err != nil {
		return nil, err
	}
	var names []string
	for _, svc := range out.Services {
		if slices.Contains(svc.Tags, c.serviceTag) {
			names = append(names, svc.Name)
		}
	}

	return names, nil
}

// DeleteAddress deletes a private address. One that is already gone is not an
// error.
func (c *Client) DeleteAddress(ctx context.Context, name string) error {
	err := c.do(ctx, c.services, http.MethodDelete, c.tailnetPath("vip-services", name), nil, nil)

	return ignoreNotFound(err)
}

// CheckAddress reads a private address and returns ErrAddressPolicy unless it
// carries exactly the service tag and the one port. The policy is written by
// hand against those two things, so anything else means it was edited wrongly
// and must fail closed.
func (c *Client) CheckAddress(ctx context.Context, name string) error {
	var out struct {
		Tags  []string `json:"tags"`
		Ports []string `json:"ports"`
	}
	err := c.do(ctx, c.services, http.MethodGet, c.tailnetPath("vip-services", name), nil, &out)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return fmt.Errorf("%w: it does not exist", ErrAddressPolicy)
	}
	if err != nil {
		return err
	}
	if !sameSet(out.Tags, []string{c.serviceTag}) || !sameSet(out.Ports, servicePorts) {
		return fmt.Errorf("%w: tags %v, ports %v", ErrAddressPolicy, out.Tags, out.Ports)
	}

	return nil
}

// AddressApproved reports whether the device a window joined under hostname is
// approved to host the private address, by the policy or by a person, which is
// what lets the network route the address to it. The network does not say
// whether the device advertises the address, so this is permission, not proof
// that anyone can connect. A device or an address that does not exist serves
// nothing, and a network that cannot answer is an error, never a no.
func (c *Client) AddressApproved(ctx context.Context, name, hostname string) (bool, error) {
	devices, err := c.ListDevices(ctx)
	if err != nil {
		return false, err
	}
	i := slices.IndexFunc(devices, func(d Device) bool { return d.Hostname == hostname })
	if i < 0 {
		return false, nil
	}

	var out struct {
		Approved     bool `json:"approved"`
		AutoApproved bool `json:"autoApproved"`
	}
	err = c.do(ctx, c.services, http.MethodGet, c.tailnetPath("vip-services", name, "device", devices[i].ID, "approved"), nil, &out)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return out.Approved || out.AutoApproved, nil
}

// sameSet reports whether two lists hold the same values, whatever their order.
func sameSet(a, b []string) bool {
	a, b = slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b))

	return slices.Equal(a, b)
}

func (c *Client) tailnetPath(parts ...string) string {
	path := "/api/v2/tailnet/" + url.PathEscape(c.tailnet)
	for _, part := range parts {
		path += "/" + url.PathEscape(part)
	}

	return path
}

func ignoreNotFound(err error) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil
	}

	return err
}

// do sends one authenticated call and decodes a JSON answer into out when out
// is not nil.
func (c *Client) do(ctx context.Context, source *tokenSource, method, path string, in, out any) error {
	token, err := source.token(ctx)
	if err != nil {
		return err
	}

	var body io.Reader
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("tailnet: encode %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.JoinPath(path).String(), body)
	if err != nil {
		return fmt.Errorf("tailnet: build %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tailnet: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return refusal(method, path, resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))

		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("tailnet: decode %s %s: %w", method, path, err)
	}

	return nil
}

func refusal(method, path string, resp *http.Response) *APIError {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))

	return &APIError{Method: method, Path: path, Status: resp.StatusCode, Body: strings.TrimSpace(string(snippet))}
}

// tokenSource trades one OAuth client's credentials for a bearer token and
// keeps it until shortly before it expires.
type tokenSource struct {
	client *Client
	id     string
	secret string

	mu      sync.Mutex
	value   string
	expires time.Time
}

func (t *tokenSource) token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.value != "" && time.Now().Before(t.expires) {
		return t.value, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {t.id}, "client_secret": {t.secret}}
	const path = "/api/v2/oauth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.client.base.JoinPath(path).String(),
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("tailnet: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := t.client.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("tailnet: POST %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The refusal is the status alone: the body of a token call can echo
		// the credentials it was sent.
		return "", &APIError{Method: http.MethodPost, Path: path, Status: resp.StatusCode}
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.AccessToken == "" {
		return "", errors.New("tailnet: the token answer has no access token")
	}
	t.value = out.AccessToken
	// Renew a minute early so a call never leaves with a token about to lapse.
	t.expires = time.Now().Add(time.Duration(out.ExpiresIn)*time.Second - time.Minute)

	return t.value, nil
}
