package postgresql_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/percona/pds/adapter/internal/engine/mysql"
	"github.com/percona/pds/adapter/internal/engine/postgresql"
)

func TestLeftoverSecretsNameThePasswordAndTLSSecretsOfADatabase(t *testing.T) {
	got := postgresql.LeftoverSecrets("orders")

	assert.Subset(t, got, []string{
		"orders-pguser-postgres",
		"orders-pguser-monitor",
		"orders-replication-cert",
	})
}

func TestLeftoverSecretsAreAllNamedForTheDatabase(t *testing.T) {
	for _, name := range postgresql.LeftoverSecrets("orders") {
		assert.True(t, strings.HasPrefix(name, "orders-"), name)
	}
}

func TestLeftoverSecretsNeverNameAnotherEnginesSecret(t *testing.T) {
	assert.NotContains(t, postgresql.LeftoverSecrets("orders"), mysql.UsersSecretName("orders"))
}
