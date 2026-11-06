package mysql_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/ini.v1"

	"github.com/percona/pds/adapter/internal/engine/mysql"
)

// exporterReads is the password mysqld_exporter logs in with when monitoring
// hands it password in a my.cnf. PMM writes the file with the value unquoted,
// and the exporter loads it with go-ini under these options and expands
// environment variables in every value, so this mirrors both steps.
func exporterReads(t *testing.T, password string) string {
	t.Helper()
	myCnf := "[client]\nhost=127.0.0.1\nport=3306\nuser=monitor\npassword=" + password + "\n"
	cfg, err := ini.LoadSources(
		ini.LoadOptions{Loose: true, AllowBooleanKeys: true},
		[]byte("[client]\npassword = ${MYSQLD_EXPORTER_PASSWORD}\n"),
		[]byte(myCnf),
	)
	require.NoError(t, err)
	cfg.ValueMapper = os.ExpandEnv

	return cfg.Section("client").Key("password").String()
}

// The operator's default symbols include # and $. A monitor password holding
// either reaches the exporter cut short, and it fails to log in for good.
func TestTheExporterCutsAnOperatorStylePassword(t *testing.T) {
	assert.Equal(t, "Ab3d", exporterReads(t, "Ab3d#fGh1jKlMn0p"))
	assert.Equal(t, "Ab3d", exporterReads(t, "Ab3d;fGh1jKlMn0p"))
	assert.Equal(t, "Ab3d", exporterReads(t, "Ab3d$fGh1jKlMn0p"))
}

func TestSystemPasswordsCoverEveryUserTheOperatorGenerates(t *testing.T) {
	values, err := mysql.SystemPasswords()
	require.NoError(t, err)

	assert.ElementsMatch(t,
		[]string{"root", "xtrabackup", "monitor", "proxyadmin", "operator", "replication"},
		keys(values))
}

func TestSystemPasswordsReachTheExporterWhole(t *testing.T) {
	for range 200 {
		values, err := mysql.SystemPasswords()
		require.NoError(t, err)
		for user, password := range values {
			require.GreaterOrEqual(t, len(password), 20, user)
			require.Equal(t, len(password), len(exporterReads(t, password)), "%s password was cut", user)
		}
	}
}

func TestSystemPasswordsAreNotReused(t *testing.T) {
	first, err := mysql.SystemPasswords()
	require.NoError(t, err)
	second, err := mysql.SystemPasswords()
	require.NoError(t, err)

	assert.NotEqual(t, first["monitor"], second["monitor"])
	assert.NotEqual(t, first["root"], first["monitor"])
}

func TestUsersSecretIsTheOneTheProviderNames(t *testing.T) {
	assert.Equal(t, "everest-secrets-orders", mysql.UsersSecretName("orders"))
}

func keys(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}

	return out
}
