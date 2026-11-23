package engine

import (
	"crypto/rand"
	"math/big"
)

// passwordAlphabet is letters and digits only. Monitoring writes the monitor
// password unquoted into the exporter's my.cnf, and the exporter cuts it at #
// or ; and expands a $ in it, all of which the operators' own symbols include.
// A connection string needs no escaping for it either.
const passwordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// passwordLength gives about 142 bits from the alphabet above, more than the
// operators' 16 to 20 characters with symbols.
const passwordLength = 24

// RandomPassword returns a password PDS writes into a users Secret in place of
// one the operator would generate.
func RandomPassword() (string, error) {
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
