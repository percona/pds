// Package vault keeps database passwords in the operating system's credential store: the Keychain
// on macOS and the Secret Service on Linux. There is no file fallback: a computer with no
// credential store gets an error, and the password is not written anywhere else.
package vault

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// service is the name every entry is filed under in the credential store.
const service = "pds-mcp"

var (
	// ErrNotFound means nothing is stored for the account.
	ErrNotFound = errors.New("no password is stored for this database")
	// ErrUnavailable means this computer has no credential store the program can use. The cause is
	// dropped on purpose: it can name a bus address or a path, and nothing it says helps an agent.
	ErrUnavailable = errors.New("no credential store is available")
)

// Vault is where passwords live. It is the seam the tests fake.
type Vault interface {
	Set(account, password string) error
	Get(account string) (string, error)
	Delete(account string) error
}

// Account names one database on one control plane, so the same database id on two control planes
// never shares an entry.
func Account(controlPlane, databaseID string) string {
	return controlPlane + " " + databaseID
}

type system struct{}

// OS returns the operating system's credential store.
func OS() Vault { return system{} }

func (system) Set(account, password string) error {
	if err := keyring.Set(service, account, password); err != nil {
		return ErrUnavailable
	}

	return nil
}

func (system) Get(account string) (string, error) {
	password, err := keyring.Get(service, account)
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return "", ErrNotFound
	case err != nil:
		return "", ErrUnavailable
	}

	return password, nil
}

func (system) Delete(account string) error {
	err := keyring.Delete(service, account)
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return nil
	case err != nil:
		return ErrUnavailable
	}

	return nil
}
