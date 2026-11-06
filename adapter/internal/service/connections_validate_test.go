package service_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/testutil"
)

const probeKey = "sk-probe-secret"

type putCase struct {
	name   string
	secret string
	key    string
	put    func(c *service.Connections) error
}

func putCases() []putCase {
	return []putCase{
		{"servicenow", "pds/pds-servicenow", "SN_CLIENT_TOKEN", func(c *service.Connections) error {
			return c.PutServiceNow(context.Background(), service.ServiceNowInput{ClientToken: probeKey})
		}},
		{"anthropic", "pds/pds-assistant", "ANTHROPIC_API_KEY", func(c *service.Connections) error {
			return c.PutAssistant(context.Background(), service.AssistantInput{APIKey: probeKey})
		}},
		{"cursor", "pds/pds-cursor", "CURSOR_API_KEY", func(c *service.Connections) error {
			return c.PutCursor(context.Background(), service.CursorInput{APIKey: probeKey})
		}},
	}
}

func newValidatingConnections(store *fakeStore, provider http.RoundTripper) *service.Connections {
	return service.NewConnections(store, service.ConnectionsOptions{
		Namespace:           "pds",
		EnvServiceNowAPIKey: "env-api-key",
		EnvServiceNowURL:    "https://sn.example",
	}, service.WithConnectionsTransport(provider))
}

func TestPutServiceNowTokenIsNotCheckedWithoutAnInstanceURL(t *testing.T) {
	store := &fakeStore{}
	provider := testutil.AcceptingProvider()
	conns := service.NewConnections(store, service.ConnectionsOptions{
		Namespace:           "pds",
		EnvServiceNowAPIKey: "env-api-key",
	}, service.WithConnectionsTransport(provider))

	require.NoError(t, conns.PutServiceNow(context.Background(), service.ServiceNowInput{ClientToken: probeKey}))

	assert.Zero(t, provider.Calls(), "no instance means nothing to ask")
	assert.Equal(t, probeKey, store.secrets["pds/pds-servicenow"]["SN_CLIENT_TOKEN"])
}

func TestPutStoresAKeyTheProviderAccepts(t *testing.T) {
	for _, tc := range putCases() {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			provider := testutil.AcceptingProvider()

			require.NoError(t, tc.put(newValidatingConnections(store, provider)))

			assert.Equal(t, 1, provider.Calls())
			assert.Equal(t, probeKey, store.secrets[tc.secret][tc.key])
		})
	}
}

func TestPutRefusesAKeyTheProviderRejects(t *testing.T) {
	for _, tc := range putCases() {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			t.Run(tc.name, func(t *testing.T) {
				store := &fakeStore{secrets: map[string]map[string]string{
					tc.secret: {tc.key: "previous"},
				}}
				provider := &testutil.ProviderStub{Status: status}

				err := tc.put(newValidatingConnections(store, provider))

				require.ErrorIs(t, err, service.ErrKeyRefused)
				assert.NotContains(t, err.Error(), probeKey)
				assert.Equal(t, "previous", store.secrets[tc.secret][tc.key])
			})
		}
	}
}

func TestPutReportsAKeyItCouldNotCheck(t *testing.T) {
	failures := map[string]*testutil.ProviderStub{
		"server error": {Status: http.StatusBadGateway},
		"unreachable":  {Err: errors.New("dial tcp: connection refused")},
	}
	for _, tc := range putCases() {
		for name, provider := range failures {
			t.Run(tc.name+" "+name, func(t *testing.T) {
				store := &fakeStore{}

				err := tc.put(newValidatingConnections(store, provider))

				require.ErrorIs(t, err, service.ErrKeyUnchecked)
				assert.NotErrorIs(t, err, service.ErrKeyRefused)
				assert.NotContains(t, err.Error(), probeKey)
				assert.Empty(t, store.secrets)
			})
		}
	}
}

func TestPutChecksTheKeyWithTheRequestContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &fakeStore{}

	err := newValidatingConnections(store, testutil.AcceptingProvider()).
		PutCursor(ctx, service.CursorInput{APIKey: probeKey})

	require.ErrorIs(t, err, service.ErrKeyUnchecked)
	assert.Empty(t, store.secrets)
}
