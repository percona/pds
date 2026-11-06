package api_test

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/testutil"
)

type memStore struct {
	secrets map[string]map[string]string
	putErr  error
}

func (m *memStore) key(namespace, name string) string { return namespace + "/" + name }

func (m *memStore) GetSecret(_ context.Context, namespace, name string) (map[string]string, error) {
	values, ok := m.secrets[m.key(namespace, name)]
	if !ok {
		return nil, kube.ErrSecretNotFound
	}
	copied := make(map[string]string, len(values))
	for k, v := range values {
		copied[k] = v
	}

	return copied, nil
}

func (m *memStore) PutSecret(_ context.Context, namespace, name string, values map[string]string) error {
	if m.putErr != nil {
		return m.putErr
	}
	if m.secrets == nil {
		m.secrets = map[string]map[string]string{}
	}
	existing, ok := m.secrets[m.key(namespace, name)]
	if !ok {
		existing = map[string]string{}
	}
	for k, v := range values {
		existing[k] = v
	}
	m.secrets[m.key(namespace, name)] = existing

	return nil
}

func (m *memStore) DeleteSecret(_ context.Context, namespace, name string) error {
	delete(m.secrets, m.key(namespace, name))

	return nil
}

func (m *memStore) DeleteSecretKeys(_ context.Context, namespace, name string, keys []string) error {
	values, ok := m.secrets[m.key(namespace, name)]
	if !ok {
		return nil
	}
	for _, key := range keys {
		delete(values, key)
	}

	return nil
}

func newConnectionsServer(t *testing.T, store *memStore) *api.Server {
	t.Helper()

	return newConnectionsServerWith(t, store, testutil.AcceptingProvider())
}

func newConnectionsServerWith(t *testing.T, store *memStore, provider http.RoundTripper) *api.Server {
	t.Helper()

	conns := service.NewConnections(store, service.ConnectionsOptions{
		Namespace:            "pds",
		ServiceNowSecretName: "pds-servicenow",
		AssistantSecretName:  "pds-assistant",
		CursorSecretName:     "pds-cursor",
		EnvServiceNowAPIKey:  "env-api-key",
		EnvServiceNowURL:     "https://example.service-now.com",
	}, service.WithConnectionsTransport(provider))

	return api.NewServer(api.Deps{
		Config:      config.Config{Addr: ":8080", Env: config.EnvDev, EverestNamespace: "pds", HubOperatorToken: testHubOperatorToken},
		Logger:      log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:        auth.NewStub(),
		Connections: conns,
		Support:     service.NewSupport(nil, service.WithServiceNowSource(conns)),
	})
}

func TestGetConnectionsStartsUnconfigured(t *testing.T) {
	rec := do(t, newConnectionsServer(t, &memStore{}), http.MethodGet, "/api/connections", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	decoded := decodeBody(t, rec)
	sn := decoded["servicenow"].(map[string]any)
	asst := decoded["assistant"].(map[string]any)
	assert.Equal(t, false, sn["configured"])
	assert.Equal(t, false, asst["configured"])
	assert.NotContains(t, rec.Body.String(), "sk-")
	assert.NotContains(t, rec.Body.String(), "apiKey")
}

func TestPutServiceNowThenGetShowsConfigured(t *testing.T) {
	store := &memStore{}
	srv := newConnectionsServer(t, store)

	rec := do(t, srv, http.MethodPut, "/api/connections/servicenow", strings.NewReader(
		`{"clientToken":"client-token"}`,
	))
	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "client-token")

	got, err := store.GetSecret(t.Context(), "pds", "pds-servicenow")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"SN_CLIENT_TOKEN": "client-token"}, got)

	rec = do(t, srv, http.MethodGet, "/api/connections", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	sn := decodeBody(t, rec)["servicenow"].(map[string]any)
	assert.Equal(t, true, sn["configured"])
}

func TestPutAssistantThenGetShowsConfigured(t *testing.T) {
	srv := newConnectionsServer(t, &memStore{})

	rec := do(t, srv, http.MethodPut, "/api/connections/assistant", strings.NewReader(
		`{"apiKey":"sk-ant-test","baseUrl":"https://api.anthropic.com"}`,
	))
	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())

	rec = do(t, srv, http.MethodGet, "/api/connections", nil)
	asst := decodeBody(t, rec)["assistant"].(map[string]any)
	assert.Equal(t, true, asst["configured"])
	assert.NotContains(t, rec.Body.String(), "sk-ant-test")
}

func TestPutServiceNowRejectsABlankToken(t *testing.T) {
	bodies := []string{`{}`, `{"clientToken":""}`, `{"clientToken":"  "}`}
	for _, body := range bodies {
		rec := do(t, newConnectionsServer(t, &memStore{}), http.MethodPut, "/api/connections/servicenow",
			strings.NewReader(body))

		require.Equal(t, http.StatusBadRequest, rec.Code, "body %s: %s", body, rec.Body.String())
		assert.Equal(t, "ErrValidation", decodeBody(t, rec)["code"])
	}
}

func TestPutServiceNowIgnoresPastedAPIKeyAndURL(t *testing.T) {
	store := &memStore{}
	srv := newConnectionsServer(t, store)

	rec := do(t, srv, http.MethodPut, "/api/connections/servicenow", strings.NewReader(
		`{"apiKey":"pasted-key","clientToken":"client-token","url":"http://example.service-now.com"}`,
	))
	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())

	got, err := store.GetSecret(t.Context(), "pds", "pds-servicenow")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"SN_CLIENT_TOKEN": "client-token"}, got)
	assert.NotContains(t, got, "SN_API_KEY")
	assert.NotContains(t, got, "SN_URL")
}

func TestDeleteServiceNowClearsTheTokenAndLeavesTheHelmKey(t *testing.T) {
	store := &memStore{secrets: map[string]map[string]string{
		"pds/pds-servicenow": {
			"SN_API_KEY":      "helm-key",
			"SN_CLIENT_TOKEN": "client-token",
		},
	}}
	srv := newConnectionsServer(t, store)

	rec := do(t, srv, http.MethodDelete, "/api/connections/servicenow", nil)
	require.Equal(t, http.StatusNoContent, rec.Code)

	got, err := store.GetSecret(t.Context(), "pds", "pds-servicenow")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"SN_API_KEY": "helm-key"}, got)

	rec = do(t, srv, http.MethodGet, "/api/connections", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	sn := decodeBody(t, rec)["servicenow"].(map[string]any)
	assert.Equal(t, false, sn["configured"])
}

func TestDeleteAssistantClearsConfigured(t *testing.T) {
	store := &memStore{secrets: map[string]map[string]string{
		"pds/pds-assistant": {"ANTHROPIC_API_KEY": "sk-ant-test"},
	}}
	srv := newConnectionsServer(t, store)

	rec := do(t, srv, http.MethodDelete, "/api/connections/assistant", nil)
	require.Equal(t, http.StatusNoContent, rec.Code)

	rec = do(t, srv, http.MethodGet, "/api/connections", nil)
	asst := decodeBody(t, rec)["assistant"].(map[string]any)
	assert.Equal(t, false, asst["configured"])
}

func TestPutAssistantMapsForbiddenTo502(t *testing.T) {
	srv := newConnectionsServer(t, &memStore{putErr: kube.ErrForbidden})

	rec := do(t, srv, http.MethodPut, "/api/connections/assistant",
		strings.NewReader(`{"apiKey":"sk-ant-test"}`))

	require.Equal(t, http.StatusBadGateway, rec.Code)
	decoded := decodeBody(t, rec)
	assert.Equal(t, "ErrDownstream", decoded["code"])
	assert.Contains(t, decoded["message"], "The credentials could not be stored.")
	assert.NotContains(t, rec.Body.String(), "sk-ant-test")
}

func TestPutCursorThenGetShowsConfigured(t *testing.T) {
	srv := newConnectionsServer(t, &memStore{})

	rec := do(t, srv, http.MethodPut, "/api/connections/cursor", strings.NewReader(
		`{"apiKey":"sk"}`,
	))
	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())

	rec = do(t, srv, http.MethodGet, "/api/connections", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	decoded := decodeBody(t, rec)
	cursor := decoded["cursor"].(map[string]any)
	assert.Equal(t, true, cursor["configured"])
	assert.NotContains(t, rec.Body.String(), "sk")
	assert.NotContains(t, rec.Body.String(), "apiKey")
}

func TestPutCursorRejectsABlankKey(t *testing.T) {
	rec := do(t, newConnectionsServer(t, &memStore{}), http.MethodPut, "/api/connections/cursor",
		strings.NewReader(`{"apiKey":""}`))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "ErrValidation", decodeBody(t, rec)["code"])
}

func TestDeleteCursorClearsConfigured(t *testing.T) {
	store := &memStore{secrets: map[string]map[string]string{
		"pds/pds-cursor": {"CURSOR_API_KEY": "sk-cursor"},
	}}
	srv := newConnectionsServer(t, store)

	rec := do(t, srv, http.MethodDelete, "/api/connections/cursor", nil)
	require.Equal(t, http.StatusNoContent, rec.Code)

	rec = do(t, srv, http.MethodGet, "/api/connections", nil)
	cursor := decodeBody(t, rec)["cursor"].(map[string]any)
	assert.Equal(t, false, cursor["configured"])
}

func TestRemovedProviderKeyRoutesAreGone(t *testing.T) {
	srv := newConnectionsServer(t, &memStore{})

	for _, provider := range []string{"openai", "gemini", "openrouter"} {
		t.Run(provider, func(t *testing.T) {
			rec := do(t, srv, http.MethodPut, "/api/connections/"+provider, strings.NewReader(`{"apiKey":"k"}`))
			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, "the path exists for DELETE only")

			rec = do(t, srv, http.MethodDelete, "/api/connections/"+provider, nil)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Equal(t, "ErrValidation", decodeBody(t, rec)["code"])
		})
	}
}

func TestConnectionsStatusNamesOnlyTheKeptConnections(t *testing.T) {
	rec := do(t, newConnectionsServer(t, &memStore{}), http.MethodGet, "/api/connections", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	decoded := decodeBody(t, rec)
	assert.ElementsMatch(t, []string{"servicenow", "assistant", "cursor"}, slices.Collect(maps.Keys(decoded)))
}

func TestPutServiceNowEnablesSupportWithoutRestart(t *testing.T) {
	srv := newConnectionsServer(t, &memStore{})

	rec := do(t, srv, http.MethodGet, "/api/support/tickets", nil)
	require.Equal(t, http.StatusNotImplemented, rec.Code)

	rec = do(t, srv, http.MethodPut, "/api/connections/servicenow", strings.NewReader(
		`{"clientToken":"client-token"}`,
	))
	require.Equal(t, http.StatusNoContent, rec.Code)

	// Credentials are live, but there is no real Connector behind them, so the
	// list is a 502 rather than a 501: support is configured.
	rec = do(t, srv, http.MethodGet, "/api/support/tickets", nil)
	require.NotEqual(t, http.StatusNotImplemented, rec.Code, "body: %s", rec.Body.String())
}

func TestPutRejectedKeyAnswers400AndKeepsTheStoredKey(t *testing.T) {
	store := &memStore{secrets: map[string]map[string]string{
		"pds/pds-assistant": {"ANTHROPIC_API_KEY": "previous"},
	}}
	srv := newConnectionsServerWith(t, store, &testutil.ProviderStub{Status: http.StatusUnauthorized})

	rec := do(t, srv, http.MethodPut, "/api/connections/assistant", strings.NewReader(
		`{"apiKey":"sk-ant-mistyped"}`,
	))

	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, api.CodeValidation, body["code"])
	assert.Equal(t, "The provider refused that key. Check it and try again.", body["message"])
	assert.NotContains(t, rec.Body.String(), "sk-ant-mistyped")
	assert.Equal(t, "previous", store.secrets["pds/pds-assistant"]["ANTHROPIC_API_KEY"])
}

func TestPutUncheckableKeyAnswers502AndStoresNothing(t *testing.T) {
	store := &memStore{}
	srv := newConnectionsServerWith(t, store, &testutil.ProviderStub{Status: http.StatusServiceUnavailable})

	rec := do(t, srv, http.MethodPut, "/api/connections/cursor", strings.NewReader(
		`{"apiKey":"cursor-key"}`,
	))

	require.Equal(t, http.StatusBadGateway, rec.Code, "body: %s", rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, api.CodeDownstream, body["code"])
	assert.Equal(t, "The key could not be checked right now. Try again in a moment.", body["message"])
	assert.NotContains(t, rec.Body.String(), "cursor-key")
	assert.Empty(t, store.secrets)
}
