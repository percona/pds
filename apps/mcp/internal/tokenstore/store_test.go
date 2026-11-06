package tokenstore_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/percona/pds/apps/mcp/internal/tokenstore"
)

func newStore(t *testing.T) (*tokenstore.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cfg", "pds-mcp", "credentials.json")

	return tokenstore.New(path), path
}

func TestASavedLoginIsReadBackByIssuer(t *testing.T) {
	store, _ := newStore(t)
	if err := store.Save("https://a.example", tokenstore.Login{RefreshToken: "rt-a", Scope: "pds:read"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("https://b.example", tokenstore.Login{RefreshToken: "rt-b"}); err != nil {
		t.Fatal(err)
	}

	a, err := store.Load("https://a.example")
	if err != nil || a.RefreshToken != "rt-a" || a.Scope != "pds:read" {
		t.Fatalf("Load(a) = %+v, %v", a, err)
	}
	b, err := store.Load("https://b.example")
	if err != nil || b.RefreshToken != "rt-b" {
		t.Fatalf("Load(b) = %+v, %v", b, err)
	}
	if _, err := store.Load("https://c.example"); !errors.Is(err, tokenstore.ErrNoLogin) {
		t.Errorf("Load(c) error = %v, want ErrNoLogin", err)
	}
}

func TestTheFileIsPrivateToTheUser(t *testing.T) {
	store, path := newStore(t)
	if err := store.Save("https://a.example", tokenstore.Login{RefreshToken: "rt"}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %v, want 0700", dir.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a staging file %q was left behind", e.Name())
		}
	}
}

func TestAFileThatWasLooseIsTightenedWhenRead(t *testing.T) {
	store, path := newStore(t)
	if err := store.Save("https://a.example", tokenstore.Login{RefreshToken: "rt"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Load("https://a.example"); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestDeleteRemovesOnlyThatIssuer(t *testing.T) {
	store, path := newStore(t)
	_ = store.Save("https://a.example", tokenstore.Login{RefreshToken: "rt-a"})
	_ = store.Save("https://b.example", tokenstore.Login{RefreshToken: "rt-b"})

	if err := store.Delete("https://a.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("https://a.example"); !errors.Is(err, tokenstore.ErrNoLogin) {
		t.Errorf("a is still there: %v", err)
	}
	if _, err := store.Load("https://b.example"); err != nil {
		t.Errorf("b was lost: %v", err)
	}
	if err := store.Delete("https://b.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the empty file was kept: %v", err)
	}
	if err := store.Delete("https://b.example"); err != nil {
		t.Errorf("deleting what is not there failed: %v", err)
	}
}

func TestAFileThatCannotBeReadIsLeftAloneAndRefusedToAWriter(t *testing.T) {
	store, path := newStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Load("https://a.example"); !errors.Is(err, tokenstore.ErrUnreadable) {
		t.Errorf("Load error = %v, want ErrUnreadable", err)
	}
	if err := store.Save("https://a.example", tokenstore.Login{RefreshToken: "rt"}); !errors.Is(err, tokenstore.ErrUnreadable) {
		t.Errorf("Save error = %v, want ErrUnreadable", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "not json" {
		t.Errorf("the file was overwritten: %q", got)
	}
}

func TestUpdatesRunOneAtATime(t *testing.T) {
	store, _ := newStore(t)
	_ = store.Save("https://a.example", tokenstore.Login{RefreshToken: "0"})

	var wg sync.WaitGroup
	var mu sync.Mutex
	running, overlap := 0, false
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := store.Update("https://a.example", func(old tokenstore.Login) (tokenstore.Login, error) {
				mu.Lock()
				running++
				overlap = overlap || running > 1
				mu.Unlock()
				mu.Lock()
				running--
				mu.Unlock()

				return tokenstore.Login{RefreshToken: old.RefreshToken + "x"}, nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	got, _ := store.Load("https://a.example")
	if got.RefreshToken != "0xxxxxxxx" || overlap {
		t.Errorf("token = %q, overlap = %v: updates were lost or ran together", got.RefreshToken, overlap)
	}
}

func TestAFailedUpdateKeepsTheOldLogin(t *testing.T) {
	store, _ := newStore(t)
	_ = store.Save("https://a.example", tokenstore.Login{RefreshToken: "old"})

	boom := errors.New("boom")
	err := store.Update("https://a.example", func(tokenstore.Login) (tokenstore.Login, error) { return tokenstore.Login{}, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := store.Load("https://a.example"); got.RefreshToken != "old" {
		t.Errorf("token = %q, want old", got.RefreshToken)
	}
}

func TestUpdateWithoutALoginReportsNoLogin(t *testing.T) {
	store, _ := newStore(t)
	err := store.Update("https://a.example", func(tokenstore.Login) (tokenstore.Login, error) { return tokenstore.Login{}, nil })
	if !errors.Is(err, tokenstore.ErrNoLogin) {
		t.Errorf("err = %v, want ErrNoLogin", err)
	}
}
