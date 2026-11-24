package mongodb_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine/mongodb"
)

func TestUsersSecretIsTheOneTheProviderNames(t *testing.T) {
	assert.Equal(t, "everest-secrets-orders", mongodb.UsersSecretName("orders"))
}

func TestRotatedPasswordsCoverEverySystemUserAndNothingElse(t *testing.T) {
	got, err := mongodb.RotatedPasswords()
	require.NoError(t, err)

	keys := make([]string, 0, len(got))
	for key := range got {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{
		"MONGODB_BACKUP_PASSWORD",
		"MONGODB_CLUSTER_ADMIN_PASSWORD",
		"MONGODB_CLUSTER_MONITOR_PASSWORD",
		"MONGODB_DATABASE_ADMIN_PASSWORD",
		"MONGODB_USER_ADMIN_PASSWORD",
	}, keys, "the user names stay as the source's Secret has them")
	for _, password := range got {
		assert.Regexp(t, `^[A-Za-z0-9]{24}$`, password)
	}
}

func TestRotatedPasswordsAreNotReused(t *testing.T) {
	first, err := mongodb.RotatedPasswords()
	require.NoError(t, err)
	second, err := mongodb.RotatedPasswords()
	require.NoError(t, err)

	assert.NotEqual(t, first["MONGODB_DATABASE_ADMIN_PASSWORD"], second["MONGODB_DATABASE_ADMIN_PASSWORD"])
	assert.NotEqual(t, first["MONGODB_DATABASE_ADMIN_PASSWORD"], first["MONGODB_USER_ADMIN_PASSWORD"])
}
