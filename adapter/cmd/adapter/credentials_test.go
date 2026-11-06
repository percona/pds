package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/ini.v1"
)

// fakeSecrets is a namespace of Secrets the way the agent and the operator
// share it: whoever creates a name first owns its values.
type fakeSecrets struct {
	values    map[string]map[string]string
	labels    map[string]map[string]string
	namespace string
	err       error
}

func (f *fakeSecrets) CreateSecret(_ context.Context, namespace, name string, labels, values map[string]string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.namespace = namespace
	if _, ok := f.values[name]; ok {
		return false, nil
	}
	f.values[name] = values
	f.labels[name] = labels

	return true, nil
}

// operatorStylePassword is what the PXC operator 1.20 can generate with its
// default symbols: one in five of them holds a # somewhere.
const operatorStylePassword = "Ab3d#fGh1jKlMn0p"

// operatorReconciles is what the PXC operator does once the provider creates
// the cluster: it creates the users Secret if nobody has, and fills any system
// user that has no password with one of its own.
func (f *fakeSecrets) operatorReconciles(name string) {
	secret, ok := f.values[name]
	if !ok {
		secret = map[string]string{}
		f.values[name] = secret
	}
	for _, user := range []string{"root", "xtrabackup", "monitor", "proxyadmin", "operator", "replication"} {
		if secret[user] == "" {
			secret[user] = operatorStylePassword
		}
	}
}

func (f *fakeSecrets) GetSecret(_ context.Context, _, name string) (map[string]string, error) {
	return f.values[name], nil
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{values: map[string]map[string]string{}, labels: map[string]map[string]string{}}
}

// exporterLogin is the password mysqld_exporter ends up with: monitoring writes
// it unquoted into a my.cnf, and the exporter reads that with go-ini and
// expands environment variables in it.
func exporterLogin(t *testing.T, password string) string {
	t.Helper()
	cfg, err := ini.LoadSources(ini.LoadOptions{Loose: true, AllowBooleanKeys: true},
		[]byte("[client]\npassword = ${MYSQLD_EXPORTER_PASSWORD}\n"),
		[]byte("[client]\nuser=monitor\npassword="+password+"\n"))
	require.NoError(t, err)
	cfg.ValueMapper = os.ExpandEnv

	return cfg.Section("client").Key("password").String()
}

func TestWithoutPreparedCredentialsTheExporterLoginIsCut(t *testing.T) {
	secrets := newFakeSecrets()

	secrets.operatorReconciles("everest-secrets-orders")

	monitor := secrets.values["everest-secrets-orders"]["monitor"]
	assert.Less(t, len(exporterLogin(t, monitor)), len(monitor))
}

func TestPreparedCredentialsReachTheExporterWhole(t *testing.T) {
	secrets := newFakeSecrets()

	require.NoError(t, newUsersSecrets(secrets)(context.Background(), "pds", "orders"))
	secrets.operatorReconciles("everest-secrets-orders")

	monitor := secrets.values["everest-secrets-orders"]["monitor"]
	assert.NotEqual(t, operatorStylePassword, monitor, "the operator must find every password already set")
	assert.Equal(t, len(monitor), len(exporterLogin(t, monitor)))
	assert.Equal(t, "pds", secrets.namespace)
	assert.Equal(t, "orders", secrets.labels["everest-secrets-orders"]["app.kubernetes.io/instance"])
}

// The hub hands an item out again when it has not heard back, and by then the
// database may be running on these passwords. A second run must not replace
// them.
func TestPreparedCredentialsLeaveAnExistingSecretAlone(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.values["everest-secrets-orders"] = map[string]string{"monitor": "kept-as-it-is"}

	require.NoError(t, newUsersSecrets(secrets)(context.Background(), "pds", "orders"))

	assert.Equal(t, "kept-as-it-is", secrets.values["everest-secrets-orders"]["monitor"])
}

func TestPreparedCredentialsReportAFailedCreate(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.err = errors.New("secrets is forbidden")

	err := newUsersSecrets(secrets)(context.Background(), "pds", "orders")

	require.ErrorContains(t, err, "secrets is forbidden")
}

// A users Secret already there with a password the exporter cuts, left by a
// database of the same name or written by the operator first, would be used as
// it is. Answering success would hide that the database loses its monitoring.
func TestPreparedCredentialsReportAnExistingSecretTheExporterCannotRead(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.values["everest-secrets-orders"] = map[string]string{"monitor": operatorStylePassword}

	err := newUsersSecrets(secrets)(context.Background(), "pds", "orders")

	require.ErrorContains(t, err, "monitor")
	assert.Equal(t, operatorStylePassword, secrets.values["everest-secrets-orders"]["monitor"])
}
