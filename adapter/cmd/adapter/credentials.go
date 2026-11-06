package main

import (
	"context"
	"fmt"

	"github.com/percona/pds/adapter/internal/engine/mysql"
)

// secretCreator is the Kubernetes calls preparing credentials needs.
type secretCreator interface {
	CreateSecret(ctx context.Context, namespace, name string, labels, values map[string]string) (bool, error)
	GetSecret(ctx context.Context, namespace, name string) (map[string]string, error)
}

// newUsersSecrets writes a new MySQL database's users Secret before the
// operator can generate one, with a password for every system user so the
// operator fills in none of its own. A Secret that already exists is kept: the
// hub may ask twice, and the database may already run on the first answer. An
// existing Secret whose password the exporter cannot read is reported, so the
// hub logs that the database will lose its monitoring.
func newUsersSecrets(secrets secretCreator) func(ctx context.Context, namespace, name string) error {
	return func(ctx context.Context, namespace, name string) error {
		values, err := mysql.SystemPasswords()
		if err != nil {
			return fmt.Errorf("generate passwords for %s: %w", name, err)
		}
		labels := map[string]string{
			"app.kubernetes.io/instance":   name,
			"app.kubernetes.io/managed-by": mysql.ManagedBy,
		}
		secretName := mysql.UsersSecretName(name)
		created, err := secrets.CreateSecret(ctx, namespace, secretName, labels, values)
		if err != nil {
			return fmt.Errorf("create the users secret for %s: %w", name, err)
		}
		if created {
			return nil
		}
		existing, err := secrets.GetSecret(ctx, namespace, secretName)
		if err != nil {
			return fmt.Errorf("read the existing users secret for %s: %w", name, err)
		}
		for _, user := range mysql.SystemUsers() {
			if mysql.ExporterCuts(existing[user]) {
				return fmt.Errorf("users secret %s already exists, and its %s password has a character the monitoring exporter cuts at", secretName, user)
			}
		}

		return nil
	}
}
