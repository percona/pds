package vault_test

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/percona/pds/apps/mcp/internal/vault"
)

func TestAccountsAreScopedByControlPlaneAndDatabase(t *testing.T) {
	seen := map[string]string{}
	for _, c := range []struct{ origin, id string }{
		{"https://pds.example.com", "env/ns.a"}, {"https://pds.example.com", "env/ns.b"},
		{"https://other.example.com", "env/ns.a"}, {"https://pds.example.com:8443", "env/ns.a"},
	} {
		account := vault.Account(c.origin, c.id)
		if previous, taken := seen[account]; taken {
			t.Fatalf("%s and %s share the account %q", previous, c.origin+" "+c.id, account)
		}
		seen[account] = c.origin + " " + c.id
	}
}

func TestTheOperatingSystemVaultKeepsAPasswordUntilItIsDeleted(t *testing.T) {
	keyring.MockInit()
	v := vault.OS()
	account := vault.Account("https://pds.example.com", "env/ns.a")

	if _, err := v.Get(account); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("a password that was never stored: %v", err)
	}
	if err := v.Set(account, "sentinel-secret"); err != nil {
		t.Fatal(err)
	}
	if got, err := v.Get(account); err != nil || got != "sentinel-secret" {
		t.Fatalf("got %q, %v", got, err)
	}
	if err := v.Delete(account); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(account); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestAMissingCredentialStoreIsReportedAndKeepsNothing(t *testing.T) {
	keyring.MockInitWithError(errors.New("dbus: no session bus sentinel-detail"))
	v := vault.OS()

	err := v.Set(vault.Account("https://pds.example.com", "env/ns.a"), "sentinel-secret")
	if !errors.Is(err, vault.ErrUnavailable) {
		t.Fatalf("error %v, want unavailable", err)
	}
	if err.Error() != vault.ErrUnavailable.Error() {
		t.Fatalf("the error says more than it should: %q", err)
	}
}
