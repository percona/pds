package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/platform"
)

// Connection errors the HTTP layer maps onto the API error contract.
var (
	// ErrConnectionsUnavailable means the adapter has no Kubernetes access, so
	// it cannot store a pasted token. Local runs without a kubeconfig hit this.
	ErrConnectionsUnavailable = errors.New("environment access is not available")
	// ErrConnectionsForbidden means the ServiceAccount cannot write the Secret.
	// The chart Role is what grants this; a missing Role is this error.
	ErrConnectionsForbidden = errors.New("secrets write is forbidden")
	// ErrInvalidConnection means the draft cannot be stored as it is.
	ErrInvalidConnection = errors.New("invalid connection")
	// ErrConnectionsUnreadable means the stored credentials cannot be opened
	// with the keys this process has. The rows are there, so the operator
	// restores the key rather than pasting the credentials again.
	ErrConnectionsUnreadable = errors.New("stored credentials cannot be read")
	// ErrAssistantUnavailable means there is no Anthropic key, so Chat's Claude
	// provider answers 501. That is a supported deployment.
	ErrAssistantUnavailable = errors.New("assistant is not configured")
	// ErrCursorUnavailable means there is no Cursor key, so Chat's Cursor
	// provider answers 501. That is a supported deployment.
	ErrCursorUnavailable = errors.New("cursor is not configured")
)

// Secret keys stored in the connection Secrets. They match the env names the
// chart already mounts. A ServiceNow paste stores only the client token. The
// API key and instance URL stay in the adapter environment.
const (
	secretKeyServiceNowAPIKey      = "SN_API_KEY"
	secretKeyServiceNowClientToken = "SN_CLIENT_TOKEN"
	secretKeyServiceNowURL         = "SN_URL"
	secretKeyAnthropicAPIKey       = "ANTHROPIC_API_KEY"
	secretKeyAnthropicBaseURL      = "ANTHROPIC_BASE_URL"
	secretKeyCursorAPIKey          = "CURSOR_API_KEY"
)

// Connection kinds the DELETE route names.
const (
	ConnectionServiceNow ConnectionKind = "servicenow"
	ConnectionAssistant  ConnectionKind = "assistant"
	ConnectionCursor     ConnectionKind = "cursor"
)

// ConnectionKind is one of the optional outbound credentials.
type ConnectionKind string

// SecretStore reads and writes Kubernetes Secrets. The kube client is the
// production implementation; tests inject a fake. The service layer never
// imports client-go.
type SecretStore interface {
	GetSecret(ctx context.Context, namespace, name string) (map[string]string, error)
	PutSecret(ctx context.Context, namespace, name string, values map[string]string) error
	DeleteSecret(ctx context.Context, namespace, name string) error
	// DeleteSecretKeys removes named keys and leaves the rest, including any
	// Helm labels on the object. A missing Secret is success.
	DeleteSecretKeys(ctx context.Context, namespace, name string, keys []string) error
}

// ConnectionStatus is whether one outbound credential is present. The value
// itself never leaves the adapter.
type ConnectionStatus struct {
	Configured bool `json:"configured"`
}

// ConnectionsStatus is the GET /api/connections payload.
type ConnectionsStatus struct {
	ServiceNow ConnectionStatus `json:"servicenow"`
	Assistant  ConnectionStatus `json:"assistant"`
	Cursor     ConnectionStatus `json:"cursor"`
}

// ServiceNowInput is what the UI posts to store the ServiceNow client token.
// The API key and instance URL come from the adapter environment, so they are
// ignored here when a caller still sends them.
type ServiceNowInput struct {
	APIKey      string
	ClientToken string
	URL         string
}

// AssistantInput is what the UI posts to store the Anthropic key.
type AssistantInput struct {
	APIKey  string
	BaseURL string
}

// CursorInput is what the UI posts to store the Cursor key.
type CursorInput struct {
	APIKey string
}

// ServiceNowCredentials are the live tokens a support call uses.
type ServiceNowCredentials struct {
	URL         string
	APIKey      string
	ClientToken string
}

// AssistantCredentials are the live tokens a Claude chat call uses.
type AssistantCredentials struct {
	APIKey  string
	BaseURL string
}

// CursorCredentials are the live tokens a Chat Cursor call uses.
type CursorCredentials struct {
	APIKey string
}

// ConnectionsOptions names the Secrets, and the ServiceNow settings the adapter
// reads from its environment when it has no Kubernetes access (a laptop run).
// Anthropic and Cursor keys have no environment fallback: only a key an
// Organization pastes is used.
type ConnectionsOptions struct {
	Namespace            string
	ServiceNowSecretName string
	AssistantSecretName  string
	CursorSecretName     string

	EnvServiceNowURL         string
	EnvServiceNowAPIKey      string
	EnvServiceNowClientToken string
}

// Connections stores and reads the optional outbound credentials.
type Connections struct {
	store    SecretStore
	opts     ConnectionsOptions
	recorder ActivityRecorder
	// transport carries the key check. Nil means the default transport.
	transport http.RoundTripper
}

// ConnectionsOption configures a Connections service.
type ConnectionsOption func(*Connections)

// WithConnectionsRecorder wires the activity log so a credential change is
// visible in the feed. Only the connection kind is ever recorded.
func WithConnectionsRecorder(recorder ActivityRecorder) ConnectionsOption {
	return func(c *Connections) {
		c.recorder = recorder
	}
}

// NewConnections returns the connections service. A nil store is a local run
// without Kubernetes: GET falls back to env, PUT answers unavailable.
func NewConnections(store SecretStore, opts ConnectionsOptions, options ...ConnectionsOption) *Connections {
	if strings.TrimSpace(opts.Namespace) == "" {
		opts.Namespace = "pds"
	}
	if strings.TrimSpace(opts.ServiceNowSecretName) == "" {
		opts.ServiceNowSecretName = "pds-servicenow"
	}
	if strings.TrimSpace(opts.AssistantSecretName) == "" {
		opts.AssistantSecretName = "pds-assistant"
	}
	if strings.TrimSpace(opts.CursorSecretName) == "" {
		opts.CursorSecretName = "pds-cursor"
	}

	c := &Connections{store: store, opts: opts}
	for _, option := range options {
		option(c)
	}

	return c
}

// Status reports which credentials are present. Values never appear in it.
func (c *Connections) Status(ctx context.Context) (ConnectionsStatus, error) {
	var out ConnectionsStatus
	if c == nil {
		return out, nil
	}

	sn, err := c.ServiceNow(ctx)
	if err == nil && sn.APIKey != "" && sn.ClientToken != "" {
		out.ServiceNow.Configured = true
	} else if err != nil && !isMissingCredential(err) {
		return ConnectionsStatus{}, err
	}

	asst, err := c.Assistant(ctx)
	if err == nil && asst.APIKey != "" {
		out.Assistant.Configured = true
	} else if err != nil && !isMissingCredential(err) {
		return ConnectionsStatus{}, err
	}

	cursor, err := c.Cursor(ctx)
	if err == nil && cursor.APIKey != "" {
		out.Cursor.Configured = true
	} else if err != nil && !isMissingCredential(err) {
		return ConnectionsStatus{}, err
	}

	return out, nil
}

// PutServiceNow writes the ServiceNow client token. The API key and instance
// URL stay in the adapter environment, so a paste cannot replace them.
func (c *Connections) PutServiceNow(ctx context.Context, in ServiceNowInput) error {
	if c == nil {
		return ErrConnectionsUnavailable
	}

	clientToken := strings.TrimSpace(in.ClientToken)
	if clientToken == "" {
		return fmt.Errorf("%w: a ServiceNow client token is required", ErrInvalidConnection)
	}

	if err := c.checkKey(ctx, ConnectionServiceNow, clientToken, ""); err != nil {
		return err
	}

	return c.put(ctx, ConnectionServiceNow, c.opts.ServiceNowSecretName, map[string]string{
		secretKeyServiceNowClientToken: clientToken,
	})
}

// instanceReason turns a ServiceNow URL rejection into a line a platform
// engineer can act on. The package prefix is dropped: the person reading it
// pasted a URL, not a Go import path.
func instanceReason(err error) string {
	return strings.TrimPrefix(err.Error(), "servicenow: ")
}

// PutAssistant writes the Anthropic key to the Secret.
func (c *Connections) PutAssistant(ctx context.Context, in AssistantInput) error {
	if c == nil {
		return ErrConnectionsUnavailable
	}

	apiKey := strings.TrimSpace(in.APIKey)
	if apiKey == "" {
		return fmt.Errorf("%w: an Anthropic API key is required", ErrInvalidConnection)
	}

	values := map[string]string{secretKeyAnthropicAPIKey: apiKey}
	if base := strings.TrimSpace(in.BaseURL); base != "" {
		values[secretKeyAnthropicBaseURL] = base
	}

	if err := c.checkKey(ctx, ConnectionAssistant, apiKey, values[secretKeyAnthropicBaseURL]); err != nil {
		return err
	}

	return c.put(ctx, ConnectionAssistant, c.opts.AssistantSecretName, values)
}

// PutCursor writes the Cursor key to the Secret.
func (c *Connections) PutCursor(ctx context.Context, in CursorInput) error {
	if c == nil {
		return ErrConnectionsUnavailable
	}

	apiKey := strings.TrimSpace(in.APIKey)
	if apiKey == "" {
		return fmt.Errorf("%w: a Cursor API key is required", ErrInvalidConnection)
	}

	values := map[string]string{secretKeyCursorAPIKey: apiKey}

	if err := c.checkKey(ctx, ConnectionCursor, apiKey, ""); err != nil {
		return err
	}

	return c.put(ctx, ConnectionCursor, c.opts.CursorSecretName, values)
}

// Delete removes one connection. For Support it drops only the client token
// so a Helm-provisioned API key stays in place.
func (c *Connections) Delete(ctx context.Context, kind ConnectionKind) error {
	if c == nil {
		return ErrConnectionsUnavailable
	}

	name, err := c.secretName(kind)
	if err != nil {
		return err
	}

	if kind == ConnectionServiceNow {
		err = c.clearServiceNowToken(ctx, name)
	} else {
		err = c.mapStoreError(c.store.DeleteSecret(ctx, c.opts.Namespace, name))
	}
	c.record(ctx, ActivityEvent{
		Action: ActionConnectionDelete,
		Target: string(kind),
	}, err)

	return err
}

func (c *Connections) clearServiceNowToken(ctx context.Context, name string) error {
	if c.store == nil {
		return ErrConnectionsUnavailable
	}

	return c.mapStoreError(c.store.DeleteSecretKeys(ctx, c.opts.Namespace, name, []string{
		secretKeyServiceNowClientToken,
	}))
}

// ServiceNow returns the live ServiceNow credentials. A missing Secret is
// ErrSupportUnavailable, which the support routes already map to 501.
func (c *Connections) ServiceNow(ctx context.Context) (ServiceNowCredentials, error) {
	if c == nil {
		return ServiceNowCredentials{}, ErrSupportUnavailable
	}

	values, err := c.get(ctx, c.opts.ServiceNowSecretName)
	if err != nil {
		if isMissingCredential(err) {
			return ServiceNowCredentials{}, ErrSupportUnavailable
		}

		return ServiceNowCredentials{}, err
	}

	creds := ServiceNowCredentials{
		URL:         strings.TrimSpace(c.opts.EnvServiceNowURL),
		APIKey:      strings.TrimSpace(c.opts.EnvServiceNowAPIKey),
		ClientToken: values[secretKeyServiceNowClientToken],
	}
	if creds.URL == "" || creds.APIKey == "" || creds.ClientToken == "" {
		return ServiceNowCredentials{}, ErrSupportUnavailable
	}

	return creds, nil
}

// Assistant returns the live Anthropic credentials. A missing Secret is
// ErrAssistantUnavailable, which Chat maps to 501.
func (c *Connections) Assistant(ctx context.Context) (AssistantCredentials, error) {
	if c == nil {
		return AssistantCredentials{}, ErrAssistantUnavailable
	}

	values, err := c.get(ctx, c.opts.AssistantSecretName)
	if err != nil {
		if isMissingCredential(err) {
			return AssistantCredentials{}, ErrAssistantUnavailable
		}

		return AssistantCredentials{}, err
	}

	creds := AssistantCredentials{
		APIKey:  values[secretKeyAnthropicAPIKey],
		BaseURL: strings.TrimSpace(values[secretKeyAnthropicBaseURL]),
	}
	if creds.APIKey == "" {
		return AssistantCredentials{}, ErrAssistantUnavailable
	}

	return creds, nil
}

// Cursor returns the live Cursor credentials. A missing Secret is
// ErrCursorUnavailable, which Chat maps to 501.
func (c *Connections) Cursor(ctx context.Context) (CursorCredentials, error) {
	if c == nil {
		return CursorCredentials{}, ErrCursorUnavailable
	}

	values, err := c.get(ctx, c.opts.CursorSecretName)
	if err != nil {
		if isMissingCredential(err) {
			return CursorCredentials{}, ErrCursorUnavailable
		}

		return CursorCredentials{}, err
	}

	creds := CursorCredentials{APIKey: values[secretKeyCursorAPIKey]}
	if creds.APIKey == "" {
		return CursorCredentials{}, ErrCursorUnavailable
	}

	return creds, nil
}

func (c *Connections) get(ctx context.Context, name string) (map[string]string, error) {
	if c.store == nil {
		return c.envFallback(name), nil
	}

	values, err := c.store.GetSecret(ctx, c.opts.Namespace, name)
	if err == nil {
		return values, nil
	}
	// A credential that is stored but unreadable must not fall back to env or
	// read as absent. Doing either sends the connections panel to a paste form over
	// a credential that is still there, and the paste would be sealed with the
	// same key that cannot open the rest.
	if errors.Is(err, ErrConnectionsUnreadable) {
		return nil, err
	}
	if errors.Is(err, platform.ErrUnavailable) {
		return c.envFallback(name), nil
	}
	if errors.Is(err, kube.ErrSecretNotFound) {
		return map[string]string{}, nil
	}

	return nil, c.mapStoreError(err)
}

// put writes one credential Secret and records that it changed. values holds
// the credential itself and never reaches the event: the kind is the whole
// story a reader needs, and it is the only part that is safe to keep.
func (c *Connections) put(ctx context.Context, kind ConnectionKind, name string, values map[string]string) error {
	err := c.putSecret(ctx, name, values)
	c.record(ctx, ActivityEvent{
		Action: ActionConnectionUpdate,
		Target: string(kind),
	}, err)

	return err
}

func (c *Connections) putSecret(ctx context.Context, name string, values map[string]string) error {
	if c.store == nil {
		return ErrConnectionsUnavailable
	}

	return c.mapStoreError(c.store.PutSecret(ctx, c.opts.Namespace, name, values))
}

// record hands one connection change to the activity log, if one was wired.
func (c *Connections) record(ctx context.Context, e ActivityEvent, err error) {
	if c == nil || c.recorder == nil {
		return
	}
	if err != nil {
		e.Outcome = OutcomeFailed
		e.Error = err.Error()
	} else {
		e.Outcome = OutcomeSucceeded
	}

	c.recorder.Record(ctx, e)
}

func (c *Connections) envFallback(name string) map[string]string {
	switch name {
	case c.opts.ServiceNowSecretName:
		return map[string]string{
			secretKeyServiceNowAPIKey:      c.opts.EnvServiceNowAPIKey,
			secretKeyServiceNowClientToken: c.opts.EnvServiceNowClientToken,
			secretKeyServiceNowURL:         c.opts.EnvServiceNowURL,
		}
	default:
		return map[string]string{}
	}
}

func (c *Connections) secretName(kind ConnectionKind) (string, error) {
	switch kind {
	case ConnectionServiceNow:
		return c.opts.ServiceNowSecretName, nil
	case ConnectionAssistant:
		return c.opts.AssistantSecretName, nil
	case ConnectionCursor:
		return c.opts.CursorSecretName, nil
	default:
		return "", fmt.Errorf("%w: unknown connection %q", ErrInvalidConnection, kind)
	}
}

func isMissingCredential(err error) bool {
	return errors.Is(err, ErrSupportUnavailable) ||
		errors.Is(err, ErrAssistantUnavailable) ||
		errors.Is(err, ErrCursorUnavailable)
}

func (c *Connections) mapStoreError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrConnectionsUnreadable):
		return err
	case errors.Is(err, platform.ErrUnavailable):
		return fmt.Errorf("%w: %v", ErrConnectionsUnavailable, err)
	case errors.Is(err, kube.ErrForbidden):
		return fmt.Errorf("%w: %v", ErrConnectionsForbidden, err)
	default:
		return fmt.Errorf("%w: %v", ErrConnectionsUnavailable, err)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}

	return ""
}
