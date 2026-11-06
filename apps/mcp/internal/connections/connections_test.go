package connections_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/percona/pds/apps/mcp/internal/connections"
	"github.com/percona/pds/apps/mcp/internal/sqlrun"
)

func entry(id string) connections.Entry {
	return connections.Entry{ID: id, Target: sqlrun.Target{
		Engine: sqlrun.PostgreSQL, Host: "db.example.com", Port: 5432, User: "app", Database: "shop", TLS: sqlrun.TLSRequire,
	}}
}

func TestAConnectionIsKeptInAPrivateFileAndFoundAgain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pds-mcp")
	file := filepath.Join(dir, "connections.json")
	store := connections.New(file)

	if err := store.Put("https://pds.example.com", entry("env/ns.a")); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get("https://pds.example.com", "env/ns.a")
	if err != nil || got.Target.Host != "db.example.com" || got.Target.TLS != sqlrun.TLSRequire {
		t.Fatalf("got %+v, %v", got, err)
	}
	for path, want := range map[string]os.FileMode{file: 0o600, dir: 0o700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s: mode %v, %v, want %v", path, info.Mode().Perm(), err, want)
		}
	}
}

func TestConnectionsAreScopedByControlPlane(t *testing.T) {
	store := connections.New(filepath.Join(t.TempDir(), "connections.json"))
	if err := store.Put("https://one.example.com", entry("env/ns.a")); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get("https://two.example.com", "env/ns.a"); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("a connection of another control plane was found: %v", err)
	}
	if got, err := store.List("https://two.example.com"); err != nil || len(got) != 0 {
		t.Fatalf("listed %v, %v", got, err)
	}
	if got, err := store.List("https://one.example.com"); err != nil || len(got) != 1 || got[0].ID != "env/ns.a" {
		t.Fatalf("listed %v, %v", got, err)
	}
}

func TestAFileThatIsNotOursIsLeftAlone(t *testing.T) {
	file := filepath.Join(t.TempDir(), "connections.json")
	if err := os.WriteFile(file, []byte("not json, somebody's notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := connections.New(file)

	if err := store.Put("https://pds.example.com", entry("env/ns.a")); !errors.Is(err, connections.ErrUnreadable) {
		t.Fatalf("error %v, want unreadable", err)
	}
	if got, _ := os.ReadFile(file); !strings.Contains(string(got), "somebody's notes") {
		t.Fatal("the file was overwritten")
	}
}

func TestTheFileHoldsNoPassword(t *testing.T) {
	file := filepath.Join(t.TempDir(), "connections.json")
	if err := connections.New(file).Put("https://pds.example.com", entry("env/ns.a")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(file)
	if strings.Contains(strings.ToLower(string(raw)), "pass") {
		t.Fatalf("the file has a password field: %s", raw)
	}
}

func TestListingAFileThatCannotBeReadIsAnErrorNotAnEmptyList(t *testing.T) {
	file := filepath.Join(t.TempDir(), "connections.json")
	if err := os.WriteFile(file, []byte(`{"version": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := connections.New(file).List("https://pds.example.com"); !errors.Is(err, connections.ErrUnreadable) {
		t.Fatalf("error %v, want unreadable", err)
	}
}
