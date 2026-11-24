package mysql

import (
	"strings"

	"github.com/percona/pds/adapter/internal/engine"
)

// systemUsers are the users the PXC operator gives a password to when the
// users Secret has none for them.
var systemUsers = []string{"root", "xtrabackup", "monitor", "proxyadmin", "operator", "replication"}

// SystemUsers returns the users the PXC operator gives a password to when the
// users Secret has none for them, as a copy the caller may change.
func SystemUsers() []string {
	return append([]string(nil), systemUsers...)
}

// exporterCuts are the characters the exporter's my.cnf reader cuts a value at
// or expands.
const exporterCuts = "#;$"

// ExporterCuts reports whether the monitoring exporter would read password
// short or changed.
func ExporterCuts(password string) bool {
	return strings.ContainsAny(password, exporterCuts)
}

// UsersSecretName is the Secret the PXC provider names as a database's users
// Secret.
func UsersSecretName(instance string) string {
	return "everest-secrets-" + instance
}

// SystemPasswords returns a fresh password for every system user. A users
// Secret holding all of them leaves the operator nothing to generate.
func SystemPasswords() (map[string]string, error) {
	out := make(map[string]string, len(systemUsers))
	for _, user := range systemUsers {
		password, err := engine.RandomPassword()
		if err != nil {
			return nil, err
		}
		out[user] = password
	}

	return out, nil
}
