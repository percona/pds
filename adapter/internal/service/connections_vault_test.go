package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
	"github.com/percona/pds/adapter/internal/testutil"
)

// vaultState stands in for the team scoped database. The store package
// covers the sealing against a live server; these cover what the vault puts
// in and takes out.
type vaultState struct {
	mu        sync.Mutex
	rows      map[string]map[string]string
	saveErr   error
	unsealErr bool
}

func newVaultState() *vaultState {
	return &vaultState{rows: map[string]map[string]string{}}
}

func (v *vaultState) Connection(_ context.Context, name string) (map[string]string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.unsealErr {
		return nil, fmt.Errorf("read connection field SN_API_KEY: %w", store.ErrUnsealable)
	}
	out := map[string]string{}
	for field, value := range v.rows[name] {
		out[field] = value
	}

	return out, nil
}

func (v *vaultState) SaveConnection(_ context.Context, name string, values map[string]string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.saveErr != nil {
		return v.saveErr
	}
	row := map[string]string{}
	for field, value := range values {
		row[field] = value
	}
	v.rows[name] = row

	return nil
}

func (v *vaultState) RemoveConnection(_ context.Context, name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.rows, name)

	return nil
}

func readyVault(state service.ConnectionState) *service.Vault {
	return service.NewVault(func(context.Context) service.ConnectionState { return state })
}

func vaultCtx(t *testing.T) context.Context {
	t.Helper()

	return auth.WithIdentity(t.Context(), auth.Identity{Subject: "acme", Scope: auth.Scope{Team: auth.Team{ID: 1}}})
}

func TestVaultRoundTripsACredential(t *testing.T) {
	vault := readyVault(newVaultState())
	ctx := vaultCtx(t)

	require.NoError(t, vault.PutSecret(ctx, "everest-system", "pds-servicenow", map[string]string{
		"SN_API_KEY":      "sn_live_abc123",
		"SN_CLIENT_TOKEN": "ct_live_xyz789",
	}))

	got, err := vault.GetSecret(ctx, "everest-system", "pds-servicenow")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"SN_API_KEY":      "sn_live_abc123",
		"SN_CLIENT_TOKEN": "ct_live_xyz789",
	}, got)
}

func TestVaultMergesTheWayTheSecretDid(t *testing.T) {
	vault := readyVault(newVaultState())
	ctx := vaultCtx(t)

	require.NoError(t, vault.PutSecret(ctx, "everest-system", "pds-servicenow", map[string]string{
		"SN_API_KEY":      "key",
		"SN_CLIENT_TOKEN": "token",
		"SN_URL":          "https://example.service-now.com",
	}))
	require.NoError(t, vault.PutSecret(ctx, "everest-system", "pds-servicenow", map[string]string{
		"SN_API_KEY":      "rotated",
		"SN_CLIENT_TOKEN": "token",
	}))

	got, err := vault.GetSecret(ctx, "everest-system", "pds-servicenow")
	require.NoError(t, err)
	assert.Equal(t, "rotated", got["SN_API_KEY"])
	assert.Equal(t, "https://example.service-now.com", got["SN_URL"],
		"a field the paste left out is kept, which is what the Secret did")
}

func TestVaultWithNothingStoredIsEmptyRatherThanAnError(t *testing.T) {
	vault := readyVault(newVaultState())

	got, err := vault.GetSecret(vaultCtx(t), "everest-system", "pds-cursor")

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestVaultReportsUnavailableBeforeTheDatabaseIsUp(t *testing.T) {
	vault := service.NewVault(func(context.Context) service.ConnectionState { return nil })
	ctx := vaultCtx(t)

	_, err := vault.GetSecret(ctx, "everest-system", "pds-servicenow")
	require.Error(t, err)

	err = vault.PutSecret(ctx, "everest-system", "pds-servicenow", map[string]string{"SN_API_KEY": "key"})
	require.Error(t, err, "a paste must not report success while there is nowhere to store it")
}

func TestVaultSurfacesAFailedWrite(t *testing.T) {
	state := newVaultState()
	state.saveErr = errors.New("the database refused the write")
	vault := readyVault(state)

	err := vault.PutSecret(vaultCtx(t), "everest-system", "pds-cursor", map[string]string{
		"CURSOR_API_KEY": "sk-live",
	})

	require.Error(t, err)
}

func TestConnectionsThroughTheVaultReportConfigured(t *testing.T) {
	vault := readyVault(newVaultState())
	conns := service.NewConnections(vault, service.ConnectionsOptions{
		Namespace:           "everest-system",
		EnvServiceNowURL:    "https://sn.example",
		EnvServiceNowAPIKey: "from-env",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))
	ctx := vaultCtx(t)

	require.NoError(t, conns.PutServiceNow(ctx, service.ServiceNowInput{
		ClientToken: "ct_live_xyz789",
	}))

	status, err := conns.Status(ctx)
	require.NoError(t, err)
	assert.True(t, status.ServiceNow.Configured)

	creds, err := conns.ServiceNow(ctx)
	require.NoError(t, err)
	assert.Equal(t, "from-env", creds.APIKey)
	assert.Equal(t, "ct_live_xyz789", creds.ClientToken)
}

func TestConnectionsClearServiceNowTokenThroughTheVault(t *testing.T) {
	vault := readyVault(newVaultState())
	conns := service.NewConnections(vault, service.ConnectionsOptions{
		Namespace:            "everest-system",
		ServiceNowSecretName: "pds-servicenow",
		EnvServiceNowAPIKey:  "from-env",
		EnvServiceNowURL:     "https://example.service-now.com",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))
	ctx := vaultCtx(t)
	require.NoError(t, vault.PutSecret(ctx, "everest-system", "pds-servicenow",
		map[string]string{"SN_API_KEY": "stored-key"}))

	require.NoError(t, conns.PutServiceNow(ctx, service.ServiceNowInput{
		ClientToken: "ct_live_xyz789",
	}))
	status, err := conns.Status(ctx)
	require.NoError(t, err)
	assert.True(t, status.ServiceNow.Configured)

	require.NoError(t, conns.Delete(ctx, service.ConnectionServiceNow))

	got, err := vault.GetSecret(ctx, "everest-system", "pds-servicenow")
	require.NoError(t, err)
	assert.Equal(t, "stored-key", got["SN_API_KEY"])
	assert.NotContains(t, got, "SN_CLIENT_TOKEN")

	status, err = conns.Status(ctx)
	require.NoError(t, err)
	assert.False(t, status.ServiceNow.Configured)

	require.NoError(t, conns.PutServiceNow(ctx, service.ServiceNowInput{
		ClientToken: "ct_live_rotated",
	}))
	creds, err := conns.ServiceNow(ctx)
	require.NoError(t, err)
	assert.Equal(t, "from-env", creds.APIKey)
	assert.Equal(t, "ct_live_rotated", creds.ClientToken)
}

func TestVaultReportsACredentialItCannotOpen(t *testing.T) {
	state := newVaultState()
	state.unsealErr = true
	vault := readyVault(state)

	_, err := vault.GetSecret(t.Context(), "everest-system", "pds-servicenow")

	require.ErrorIs(t, err, service.ErrConnectionsUnreadable)
}

func TestConnectionsDoNotReadAsAbsentWhenTheKeyIsWrong(t *testing.T) {
	state := newVaultState()
	state.unsealErr = true
	vault := readyVault(state)
	conns := service.NewConnections(vault, service.ConnectionsOptions{
		Namespace:           "everest-system",
		EnvServiceNowAPIKey: "from-env",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))

	_, err := conns.Status(t.Context())

	require.ErrorIs(t, err, service.ErrConnectionsUnreadable,
		"a credential that is stored but unreadable must not read as one that was never set")
}

func TestConnectionsDoNotFallBackToEnvWhenTheKeyIsWrong(t *testing.T) {
	state := newVaultState()
	state.unsealErr = true
	vault := readyVault(state)
	conns := service.NewConnections(vault, service.ConnectionsOptions{
		Namespace:                "everest-system",
		EnvServiceNowAPIKey:      "from-env",
		EnvServiceNowClientToken: "from-env",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))

	_, err := conns.ServiceNow(t.Context())

	require.ErrorIs(t, err, service.ErrConnectionsUnreadable)
}

func teamCtx(t *testing.T, team uint) context.Context {
	t.Helper()

	return auth.WithIdentity(t.Context(), identityIn(team))
}

// teamVault is one Vault over several team states, resolved from the request
// identity. A request with no team reads the zero state, which is how it is
// supposed to see nothing rather than a pin.
func teamVault(states map[uint]service.ConnectionState) *service.Vault {
	return service.NewVault(func(ctx context.Context) service.ConnectionState {
		team, _ := auth.TeamFromContext(ctx)

		return states[team]
	})
}

func TestVaultKeepsTeamsApart(t *testing.T) {
	states := map[uint]service.ConnectionState{1: newVaultState(), 2: newVaultState(), 0: newVaultState()}
	vault := teamVault(states)
	acme := teamCtx(t, 1)
	globex := teamCtx(t, 2)

	require.NoError(t, vault.PutSecret(acme, "everest-system", "pds-servicenow", map[string]string{
		"SN_API_KEY": "acme-key",
	}))

	got, err := vault.GetSecret(globex, "everest-system", "pds-servicenow")
	require.NoError(t, err)
	assert.Empty(t, got, "team B must not see team A's stored connection")

	got, err = vault.GetSecret(acme, "everest-system", "pds-servicenow")
	require.NoError(t, err)
	assert.Equal(t, "acme-key", got["SN_API_KEY"])
}

func TestVaultReadsNothingWithoutATeam(t *testing.T) {
	pinned := newVaultState()
	require.NoError(t, pinned.SaveConnection(context.Background(), "pds-cursor", map[string]string{
		"CURSOR_API_KEY": "pinned-key",
	}))

	vault := teamVault(map[uint]service.ConnectionState{1: pinned, 0: newVaultState()})

	got, err := vault.GetSecret(context.Background(), "everest-system", "pds-cursor")
	require.NoError(t, err)
	assert.Empty(t, got, "a request with no team must not read a pinned team's rows")
}
