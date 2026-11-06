package envfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/percona/pds/apps/mcp/internal/envfile"
)

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(raw)
}

func TestANewFileIsPrivateAndHoldsWhatWasAsked(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")

	report, err := envfile.Write(path, []envfile.Pair{{Key: "DB_HOST", Value: "db.example.com"}, {Key: "DB_PORT", Value: "5432"}}, false)
	if err != nil {
		t.Fatal(err)
	}

	if got := read(t, path); got != "DB_HOST=db.example.com\nDB_PORT=5432\n" {
		t.Fatalf("file %q", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	if !report.Created || !reflect.DeepEqual(report.Written, []string{"DB_HOST", "DB_PORT"}) || len(report.Skipped) != 0 {
		t.Fatalf("report %+v", report)
	}
}

func TestAnExistingKeyIsNotOverwrittenUnlessAskedAndIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("# mine\nDB_HOST=old.example.com\nOTHER=1\nexport DB_USER=olduser\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pairs := []envfile.Pair{{Key: "DB_HOST", Value: "new.example.com"}, {Key: "DB_USER", Value: "u"}, {Key: "DB_NAME", Value: "shop"}}

	report, err := envfile.Write(path, pairs, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "# mine\nDB_HOST=old.example.com\nOTHER=1\nexport DB_USER=olduser\nDB_NAME=shop\n"
	if got := read(t, path); got != want {
		t.Fatalf("file %q, want %q", got, want)
	}
	if !reflect.DeepEqual(report.Skipped, []string{"DB_HOST", "DB_USER"}) || !reflect.DeepEqual(report.Written, []string{"DB_NAME"}) {
		t.Fatalf("report %+v", report)
	}

	report, err = envfile.Write(path, pairs[:1], true)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); !strings.Contains(got, "DB_HOST=new.example.com\n") || !strings.Contains(got, "OTHER=1\n") || strings.Contains(got, "old.example") {
		t.Fatalf("file %q", got)
	}
	if !reflect.DeepEqual(report.Overwritten, []string{"DB_HOST"}) {
		t.Fatalf("report %+v", report)
	}
}

func TestAFileOthersCouldReadIsMadePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := envfile.Write(path, []envfile.Pair{{Key: "DB_PASSWORD", Value: "x"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 || !report.MadePrivate {
		t.Fatalf("mode %v, report %+v", info.Mode().Perm(), report)
	}
}

func TestASymlinkIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".env")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if _, err := envfile.Write(link, []envfile.Pair{{Key: "A", Value: "b"}}, false); !errors.Is(err, envfile.ErrSymlink) {
		t.Fatalf("error %v, want a symlink refusal", err)
	}
	if read(t, target) != "keep\n" {
		t.Fatal("the target was changed")
	}
}

func TestValuesAreQuotedSoTheyReadBackAsWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	_, err := envfile.Write(path, []envfile.Pair{
		{Key: "PLAIN", Value: "abc-123_.:/@"}, {Key: "SPACES", Value: "a b # c"},
		{Key: "QUOTE", Value: "it's fine"}, {Key: "EMPTY", Value: ""},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "PLAIN=abc-123_.:/@\nSPACES='a b # c'\nQUOTE=\"it's fine\"\nEMPTY=\n"
	if got := read(t, path); got != want {
		t.Fatalf("file %q, want %q", got, want)
	}
}

func TestAValueWithALineBreakIsRefusedAndNothingIsWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	for _, pair := range []envfile.Pair{{Key: "A", Value: "x\nB=y"}, {Key: "A", Value: "x\r"}, {Key: "A B", Value: "x"}, {Key: "1A", Value: "x"},
		{Key: "A", Value: "it's $HOME"}, {Key: "A", Value: "it's \"x\""}, {Key: "A", Value: "it's \\x"}, {Key: "A", Value: "it's `x`"}} {
		if _, err := envfile.Write(path, []envfile.Pair{pair}, false); !errors.Is(err, envfile.ErrBadPair) {
			t.Fatalf("%q: error %v, want a bad pair", pair.Key, err)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a file was created")
	}
}

func TestOnlyAnEnvFileInsideTheProjectCanBeNamed(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, ok := range []string{"", ".env", ".env.local", "sub/.env", "sub/.env.production"} {
		if _, err := envfile.Resolve(root, ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"../.env", "/etc/passwd", ".bashrc", "env", "sub/../../.env", "linked/.env", ".env/x", "sub/.envrc/../x", ".envrc", ".environment", ".env2"} {
		if _, err := envfile.Resolve(root, bad); !errors.Is(err, envfile.ErrBadPath) {
			t.Errorf("%q: error %v, want a refused path", bad, err)
		}
	}
	got, _ := envfile.Resolve(root, "")
	if want, _ := filepath.EvalSymlinks(root); got != filepath.Join(want, ".env") {
		t.Fatalf("default %q", got)
	}
}

func TestAProjectDirectoryThatIsTheRootOrTheHomeIsRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, dir := range []string{home, "/"} {
		if _, err := envfile.Resolve(dir, ""); !errors.Is(err, envfile.ErrBadPath) {
			t.Errorf("%q: error %v, want a refused path", dir, err)
		}
	}
}
