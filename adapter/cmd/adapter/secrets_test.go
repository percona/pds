package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/hub"
)

type fakeSecretGetter struct {
	values    map[string]string
	err       error
	namespace string
	name      string
}

func (f *fakeSecretGetter) GetSecret(_ context.Context, namespace, name string) (map[string]string, error) {
	f.namespace, f.name = namespace, name

	return f.values, f.err
}

func TestSecretReaderReadsTheMonitoringPassword(t *testing.T) {
	getter := &fakeSecretGetter{values: map[string]string{"PMM_ADMIN_PASSWORD": "pw"}}

	got, err := newSecretReader(getter, "everest-system").Read(context.Background(), hub.SecretPMMAdminPassword)

	require.NoError(t, err)
	assert.Equal(t, "pw", got)
	assert.Equal(t, "everest-system", getter.namespace)
	assert.Equal(t, "pmm-secret", getter.name)
}

func TestSecretReaderUsesThePlatformNamespaceWhenNoneIsGiven(t *testing.T) {
	getter := &fakeSecretGetter{values: map[string]string{"PMM_ADMIN_PASSWORD": "pw"}}

	_, err := newSecretReader(getter, "").Read(context.Background(), hub.SecretPMMAdminPassword)

	require.NoError(t, err)
	assert.Equal(t, "everest-system", getter.namespace)
}

func TestSecretReaderRefusesAnythingItWasNotAskedToRead(t *testing.T) {
	getter := &fakeSecretGetter{values: map[string]string{"PMM_ADMIN_PASSWORD": "pw"}}

	_, err := newSecretReader(getter, "everest-system").Read(context.Background(), "database-password")

	require.Error(t, err)
	assert.Empty(t, getter.name, "an unknown kind must not reach the environment")
}

func TestSecretReaderReportsAMissingPassword(t *testing.T) {
	for name, getter := range map[string]*fakeSecretGetter{
		"no secret": {err: errors.New("not found")},
		"no key":    {values: map[string]string{"OTHER": "x"}},
		"empty":     {values: map[string]string{"PMM_ADMIN_PASSWORD": ""}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := newSecretReader(getter, "everest-system").Read(context.Background(), hub.SecretPMMAdminPassword)

			require.Error(t, err)
			assert.Empty(t, got)
		})
	}
}

type fakeAddresser struct {
	address   string
	err       error
	namespace string
	name      string
}

func (f *fakeAddresser) ServiceAddress(_ context.Context, namespace, name string) (string, error) {
	f.namespace, f.name = namespace, name

	return f.address, f.err
}

func TestMonitoringAddressReadsTheMonitoringService(t *testing.T) {
	services := &fakeAddresser{address: "pmm.elb.example.com"}

	got, err := monitoringAddress(services, "pds-system")(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "pmm.elb.example.com", got)
	assert.Equal(t, "pds-system", services.namespace)
	assert.Equal(t, "monitoring-service", services.name)
}

func TestMonitoringAddressUsesThePlatformNamespaceWhenNoneIsGiven(t *testing.T) {
	services := &fakeAddresser{}

	_, err := monitoringAddress(services, "")(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "everest-system", services.namespace)
}

func TestMonitoringAddressPassesTheReadFailureOn(t *testing.T) {
	services := &fakeAddresser{err: errors.New("forbidden")}

	_, err := monitoringAddress(services, "")(context.Background())

	require.Error(t, err)
}
