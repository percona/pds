package mysql

import (
	"crypto/rand"
	"math/big"
	"strings"
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

// passwordAlphabet is letters and digits only. Monitoring writes the monitor
// password unquoted into the exporter's my.cnf, and the exporter cuts it at #
// or ; and expands a $ in it, all of which the operator's own symbols include.
const passwordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// passwordLength gives about 142 bits from the alphabet above, more than the
// operator's 16 to 20 characters with symbols.
const passwordLength = 24

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
		password, err := randomPassword()
		if err != nil {
			return nil, err
		}
		out[user] = password
	}

	return out, nil
}

func randomPassword() (string, error) {
	limit := big.NewInt(int64(len(passwordAlphabet)))
	b := make([]byte, passwordLength)
	for i := range b {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		b[i] = passwordAlphabet[n.Int64()]
	}

	return string(b), nil
}
