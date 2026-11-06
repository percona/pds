package service_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/platform"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/testutil"
)

type fakeStore struct {
	secrets map[string]map[string]string
	getErr  error
	putErr  error
	delErr  error
}

func (f *fakeStore) key(namespace, name string) string {
	return namespace + "/" + name
}

func (f *fakeStore) GetSecret(_ context.Context, namespace, name string) (map[string]string, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	values, ok := f.secrets[f.key(namespace, name)]
	if !ok {
		return nil, kube.ErrSecretNotFound
	}
	copied := make(map[string]string, len(values))
	for k, v := range values {
		copied[k] = v
	}

	return copied, nil
}

func (f *fakeStore) PutSecret(_ context.Context, namespace, name string, values map[string]string) error {
	if f.putErr != nil {
		return f.putErr
	}
	if f.secrets == nil {
		f.secrets = map[string]map[string]string{}
	}
	existing, ok := f.secrets[f.key(namespace, name)]
	if !ok {
		existing = map[string]string{}
	}
	for k, v := range values {
		existing[k] = v
	}
	f.secrets[f.key(namespace, name)] = existing

	return nil
}

func (f *fakeStore) DeleteSecret(_ context.Context, namespace, name string) error {
	if f.delErr != nil {
		return f.delErr
	}
	delete(f.secrets, f.key(namespace, name))

	return nil
}

func (f *fakeStore) DeleteSecretKeys(_ context.Context, namespace, name string, keys []string) error {
	if f.delErr != nil {
		return f.delErr
	}
	values, ok := f.secrets[f.key(namespace, name)]
	if !ok {
		return nil
	}
	for _, key := range keys {
		delete(values, key)
	}

	return nil
}

func testConnections(store service.SecretStore) *service.Connections {
	return service.NewConnections(store, service.ConnectionsOptions{
		Namespace:            "pds",
		ServiceNowSecretName: "pds-servicenow",
		AssistantSecretName:  "pds-assistant",
		CursorSecretName:     "pds-cursor",
		EnvServiceNowURL:     "https://example.service-now.com",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))
}

func TestConnectionsStatusIsEmptyWithoutSecrets(t *testing.T) {
	got, err := testConnections(&fakeStore{}).Status(context.Background())

	require.NoError(t, err)
	assert.False(t, got.ServiceNow.Configured)
	assert.False(t, got.Assistant.Configured)
}

func TestConnectionsStatusReadsTheSecrets(t *testing.T) {
	store := &fakeStore{secrets: map[string]map[string]string{
		"pds/pds-servicenow": {
			"SN_API_KEY":      "api-key",
			"SN_CLIENT_TOKEN": "client-token",
		},
		"pds/pds-assistant": {
			"ANTHROPIC_API_KEY": "sk-ant-test",
		},
	}}

	got, err := testConnections(store).Status(context.Background())

	require.NoError(t, err)
	assert.False(t, got.ServiceNow.Configured,
		"a stored API key is not enough: Support needs SN_API_KEY in env")
	assert.True(t, got.Assistant.Configured)
}

func TestConnectionsStatusIgnoresEnvWhenTheStoreWorks(t *testing.T) {
	conns := service.NewConnections(&fakeStore{}, service.ConnectionsOptions{
		Namespace:                "pds",
		ServiceNowSecretName:     "pds-servicenow",
		AssistantSecretName:      "pds-assistant",
		EnvServiceNowAPIKey:      "env-key",
		EnvServiceNowClientToken: "env-token",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))

	got, err := conns.Status(context.Background())

	require.NoError(t, err)
	assert.False(t, got.ServiceNow.Configured, "a live store with no Secret is the source of truth")
	assert.False(t, got.Assistant.Configured)
}

func TestConnectionsStatusFallsBackToEnvForServiceNowOnly(t *testing.T) {
	conns := service.NewConnections(&fakeStore{getErr: platform.ErrUnavailable}, service.ConnectionsOptions{
		Namespace:                "pds",
		ServiceNowSecretName:     "pds-servicenow",
		AssistantSecretName:      "pds-assistant",
		EnvServiceNowURL:         "https://sn.example",
		EnvServiceNowAPIKey:      "env-key",
		EnvServiceNowClientToken: "env-token",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))

	got, err := conns.Status(context.Background())

	require.NoError(t, err)
	assert.True(t, got.ServiceNow.Configured)
	assert.False(t, got.Assistant.Configured, "only ServiceNow has an environment fallback")
}

func TestPutServiceNowStoresOnlyTheClientToken(t *testing.T) {
	store := &fakeStore{}
	conns := service.NewConnections(store, service.ConnectionsOptions{
		Namespace:            "pds",
		ServiceNowSecretName: "pds-servicenow",
		EnvServiceNowAPIKey:  "env-key",
		EnvServiceNowURL:     "https://example.service-now.com",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))

	err := conns.PutServiceNow(context.Background(), service.ServiceNowInput{
		ClientToken: "client-token",
		APIKey:      "pasted-key-must-not-be-stored",
		URL:         "https://stored.example.service-now.com",
	})
	require.NoError(t, err)

	got, err := store.GetSecret(context.Background(), "pds", "pds-servicenow")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"SN_CLIENT_TOKEN": "client-token"}, got)

	creds, err := conns.ServiceNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "env-key", creds.APIKey)
	assert.Equal(t, "client-token", creds.ClientToken)
	assert.Equal(t, "https://example.service-now.com", creds.URL)
}

func TestPutServiceNowKeepsAHelmAPIKeyBesideTheToken(t *testing.T) {
	store := &fakeStore{secrets: map[string]map[string]string{
		"pds/pds-servicenow": {
			"SN_API_KEY": "helm-key",
		},
	}}
	conns := service.NewConnections(store, service.ConnectionsOptions{
		Namespace:            "pds",
		ServiceNowSecretName: "pds-servicenow",
		EnvServiceNowAPIKey:  "env-key",
		EnvServiceNowURL:     "https://example.service-now.com",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))

	require.NoError(t, conns.PutServiceNow(context.Background(), service.ServiceNowInput{
		ClientToken: "client-token",
	}))

	got, err := store.GetSecret(context.Background(), "pds", "pds-servicenow")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"SN_API_KEY":      "helm-key",
		"SN_CLIENT_TOKEN": "client-token",
	}, got)
}

func TestPutServiceNowRejectsABlankToken(t *testing.T) {
	err := testConnections(&fakeStore{}).PutServiceNow(context.Background(), service.ServiceNowInput{
		APIKey: "api-key",
	})

	require.ErrorIs(t, err, service.ErrInvalidConnection)
}

func TestServiceNowStatusIgnoresAStoredAPIKeyWhenEnvIsEmpty(t *testing.T) {
	store := &fakeStore{secrets: map[string]map[string]string{
		"pds/pds-servicenow": {
			"SN_API_KEY":      "stored-key",
			"SN_CLIENT_TOKEN": "client-token",
		},
	}}

	got, err := testConnections(store).Status(context.Background())

	require.NoError(t, err)
	assert.False(t, got.ServiceNow.Configured)
}

func TestServiceNowStatusNeedsEnvKeyAndStoredToken(t *testing.T) {
	store := &fakeStore{secrets: map[string]map[string]string{
		"pds/pds-servicenow": {
			"SN_CLIENT_TOKEN": "client-token",
		},
	}}
	conns := service.NewConnections(store, service.ConnectionsOptions{
		Namespace:            "pds",
		ServiceNowSecretName: "pds-servicenow",
		EnvServiceNowAPIKey:  "env-key",
		EnvServiceNowURL:     "https://example.service-now.com",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))

	got, err := conns.Status(context.Background())

	require.NoError(t, err)
	assert.True(t, got.ServiceNow.Configured)
}

func TestDeleteServiceNowLeavesEnvCredentials(t *testing.T) {
	store := &fakeStore{secrets: map[string]map[string]string{
		"pds/pds-servicenow": {
			"SN_API_KEY":      "helm-key",
			"SN_CLIENT_TOKEN": "client-token",
		},
	}}
	conns := service.NewConnections(store, service.ConnectionsOptions{
		Namespace:            "pds",
		ServiceNowSecretName: "pds-servicenow",
		EnvServiceNowAPIKey:  "env-key",
		EnvServiceNowURL:     "https://example.service-now.com",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))

	require.NoError(t, conns.Delete(context.Background(), service.ConnectionServiceNow))

	kept, err := store.GetSecret(context.Background(), "pds", "pds-servicenow")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"SN_API_KEY": "helm-key"}, kept)

	got, err := conns.Status(context.Background())
	require.NoError(t, err)
	assert.False(t, got.ServiceNow.Configured, "clearing the token is enough to disconnect support")

	require.NoError(t, conns.PutServiceNow(context.Background(), service.ServiceNowInput{
		ClientToken: "new-token",
	}))

	creds, err := conns.ServiceNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "env-key", creds.APIKey)
	assert.Equal(t, "new-token", creds.ClientToken)
	assert.Equal(t, "https://example.service-now.com", creds.URL)
}

func TestPutAssistantWritesTheSecret(t *testing.T) {
	store := &fakeStore{}

	err := testConnections(store).PutAssistant(context.Background(), service.AssistantInput{
		APIKey:  "sk-ant-test",
		BaseURL: "https://api.anthropic.com",
	})
	require.NoError(t, err)

	got, err := store.GetSecret(context.Background(), "pds", "pds-assistant")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-test", got["ANTHROPIC_API_KEY"])
	assert.Equal(t, "https://api.anthropic.com", got["ANTHROPIC_BASE_URL"])
}

func TestPutAssistantRejectsABlankKey(t *testing.T) {
	err := testConnections(&fakeStore{}).PutAssistant(context.Background(), service.AssistantInput{})

	require.ErrorIs(t, err, service.ErrInvalidConnection)
}

func TestDeleteClearsTheSecret(t *testing.T) {
	store := &fakeStore{secrets: map[string]map[string]string{
		"pds/pds-assistant": {"ANTHROPIC_API_KEY": "sk-ant-test"},
	}}

	err := testConnections(store).Delete(context.Background(), service.ConnectionAssistant)
	require.NoError(t, err)

	_, err = store.GetSecret(context.Background(), "pds", "pds-assistant")
	require.ErrorIs(t, err, kube.ErrSecretNotFound)
}

func TestPutMapsForbiddenToConnectionsForbidden(t *testing.T) {
	err := testConnections(&fakeStore{putErr: fmt.Errorf("create secret: %w", kube.ErrForbidden)}).PutAssistant(
		context.Background(), service.AssistantInput{APIKey: "sk-ant-test"},
	)

	require.ErrorIs(t, err, service.ErrConnectionsForbidden)
}

func TestPutMapsUnavailableToConnectionsUnavailable(t *testing.T) {
	err := testConnections(&fakeStore{putErr: platform.ErrUnavailable}).PutAssistant(
		context.Background(), service.AssistantInput{APIKey: "sk-ant-test"},
	)

	require.ErrorIs(t, err, service.ErrConnectionsUnavailable)
}

func TestServiceNowCredentialsReadTheSecret(t *testing.T) {
	store := &fakeStore{secrets: map[string]map[string]string{
		"pds/pds-servicenow": {
			"SN_API_KEY":      "stored-key",
			"SN_CLIENT_TOKEN": "client-token",
			"SN_URL":          "https://stored.example.service-now.com",
		},
	}}
	conns := service.NewConnections(store, service.ConnectionsOptions{
		Namespace:            "pds",
		ServiceNowSecretName: "pds-servicenow",
		EnvServiceNowAPIKey:  "env-key",
		EnvServiceNowURL:     "https://example.service-now.com",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))

	got, err := conns.ServiceNow(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "https://example.service-now.com", got.URL)
	assert.Equal(t, "env-key", got.APIKey)
	assert.Equal(t, "client-token", got.ClientToken)
}

func TestServiceNowCredentialsPreferEnvAPIKeyOverAStoredKey(t *testing.T) {
	store := &fakeStore{secrets: map[string]map[string]string{
		"pds/pds-servicenow": {
			"SN_API_KEY":      "stored-key",
			"SN_CLIENT_TOKEN": "client-token",
		},
	}}
	conns := service.NewConnections(store, service.ConnectionsOptions{
		Namespace:            "pds",
		ServiceNowSecretName: "pds-servicenow",
		EnvServiceNowAPIKey:  "env-key",
		EnvServiceNowURL:     "https://example.service-now.com",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))

	got, err := conns.ServiceNow(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "env-key", got.APIKey)
	assert.Equal(t, "https://example.service-now.com", got.URL)
	assert.Equal(t, "client-token", got.ClientToken)
}

func TestServiceNowCredentialsAreUnavailableWhenEnvHasNoURL(t *testing.T) {
	store := &fakeStore{secrets: map[string]map[string]string{
		"pds/pds-servicenow": {
			"SN_CLIENT_TOKEN": "client-token",
			"SN_URL":          "https://stale.service-now.com",
		},
	}}
	conns := service.NewConnections(store, service.ConnectionsOptions{
		Namespace:            "pds",
		ServiceNowSecretName: "pds-servicenow",
		EnvServiceNowAPIKey:  "env-key",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))

	_, err := conns.ServiceNow(context.Background())

	require.ErrorIs(t, err, service.ErrSupportUnavailable,
		"without an SN_URL there is no instance to send cases to")
}

func TestServiceNowCredentialsAreUnavailableWithoutASecret(t *testing.T) {
	_, err := testConnections(&fakeStore{}).ServiceNow(context.Background())

	require.ErrorIs(t, err, service.ErrSupportUnavailable)
}

func TestAssistantCredentialsReadTheSecret(t *testing.T) {
	store := &fakeStore{secrets: map[string]map[string]string{
		"pds/pds-assistant": {
			"ANTHROPIC_API_KEY":  "sk-ant-test",
			"ANTHROPIC_BASE_URL": "https://api.anthropic.com",
		},
	}}

	got, err := testConnections(store).Assistant(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "sk-ant-test", got.APIKey)
	assert.Equal(t, "https://api.anthropic.com", got.BaseURL)
}

func TestAssistantCredentialsAreUnavailableWithoutASecret(t *testing.T) {
	_, err := testConnections(&fakeStore{}).Assistant(context.Background())

	require.ErrorIs(t, err, service.ErrAssistantUnavailable)
}

func TestServiceNowCredentialsFallBackToEnvWithoutEnvironmentAccess(t *testing.T) {
	conns := service.NewConnections(&fakeStore{getErr: platform.ErrUnavailable}, service.ConnectionsOptions{
		Namespace:                "pds",
		ServiceNowSecretName:     "pds-servicenow",
		AssistantSecretName:      "pds-assistant",
		EnvServiceNowURL:         "https://example.service-now.com",
		EnvServiceNowAPIKey:      "env-key",
		EnvServiceNowClientToken: "env-token",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))

	got, err := conns.ServiceNow(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "env-key", got.APIKey)
	assert.Equal(t, "env-token", got.ClientToken)
	assert.Equal(t, "https://example.service-now.com", got.URL)
}

func TestUnknownConnectionKindIsInvalid(t *testing.T) {
	err := testConnections(&fakeStore{}).Delete(context.Background(), "pagerduty")

	require.ErrorIs(t, err, service.ErrInvalidConnection)
}

func TestNilConnectionsIsEmpty(t *testing.T) {
	var conns *service.Connections

	got, err := conns.Status(context.Background())
	require.NoError(t, err)
	assert.False(t, got.ServiceNow.Configured)
	assert.False(t, got.Assistant.Configured)

	_, err = conns.ServiceNow(context.Background())
	require.ErrorIs(t, err, service.ErrSupportUnavailable)
}

func TestConnectionsStatusIncludesCursor(t *testing.T) {
	store := &fakeStore{secrets: map[string]map[string]string{
		"pds/pds-cursor": {"CURSOR_API_KEY": "sk-cursor"},
	}}
	c := testConnections(store)

	got, err := c.Status(context.Background())
	require.NoError(t, err)
	assert.True(t, got.Cursor.Configured)
	assert.False(t, got.Assistant.Configured)
}

func TestPutCursorStoresTheKeyAndNeverReturnsIt(t *testing.T) {
	store := &fakeStore{}
	c := testConnections(store)

	require.NoError(t, c.PutCursor(context.Background(), service.CursorInput{APIKey: " sk-cursor "}))
	got, err := c.Cursor(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "sk-cursor", got.APIKey)

	status, err := c.Status(context.Background())
	require.NoError(t, err)
	assert.True(t, status.Cursor.Configured)
}

func TestPutCursorRejectsABlankKey(t *testing.T) {
	err := testConnections(&fakeStore{}).PutCursor(context.Background(), service.CursorInput{})
	require.ErrorIs(t, err, service.ErrInvalidConnection)
}

func TestCursorIsUnavailableWithoutAStore(t *testing.T) {
	c := service.NewConnections(nil, service.ConnectionsOptions{
		CursorSecretName: "pds-cursor",
	})
	_, err := c.Cursor(context.Background())
	require.ErrorIs(t, err, service.ErrCursorUnavailable)
}

func TestAssistantIsUnavailableWhenTheStoreIsUnreachable(t *testing.T) {
	c := service.NewConnections(&fakeStore{getErr: platform.ErrUnavailable}, service.ConnectionsOptions{
		AssistantSecretName: "pds-assistant",
	})
	_, err := c.Assistant(context.Background())
	require.ErrorIs(t, err, service.ErrAssistantUnavailable)
}

func TestCursorUnavailableWithoutKey(t *testing.T) {
	_, err := testConnections(&fakeStore{}).Cursor(context.Background())
	require.ErrorIs(t, err, service.ErrCursorUnavailable)
}
