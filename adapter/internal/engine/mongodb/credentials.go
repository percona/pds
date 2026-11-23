package mongodb

import (
	"fmt"

	"github.com/percona/pds/adapter/internal/engine"
)

// passwordKeys are the keys of a database's users Secret that hold a system
// user's password. The operator reads them again when they change and updates
// the users, so rewriting them rotates every password.
var passwordKeys = []string{
	"MONGODB_BACKUP_PASSWORD",
	"MONGODB_CLUSTER_ADMIN_PASSWORD",
	"MONGODB_CLUSTER_MONITOR_PASSWORD",
	"MONGODB_DATABASE_ADMIN_PASSWORD",
	"MONGODB_USER_ADMIN_PASSWORD",
}

// UsersSecretName is the Secret the MongoDB provider names as a database's
// users Secret.
func UsersSecretName(instance string) string {
	return "everest-secrets-" + instance
}

// RotatedPasswords returns a fresh password for every system user. A database
// restored from a backup inherits its source's users Secret, and rewriting
// these keys is what gives the copy passwords of its own.
func RotatedPasswords() (map[string]string, error) {
	out := make(map[string]string, len(passwordKeys))
	for _, key := range passwordKeys {
		password, err := engine.RandomPassword()
		if err != nil {
			return nil, fmt.Errorf("generate a password: %w", err)
		}
		out[key] = password
	}

	return out, nil
}
