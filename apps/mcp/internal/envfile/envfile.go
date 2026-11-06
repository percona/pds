// Package envfile writes database settings into a .env file without taking anything the person did
// not ask for: it never overwrites a key unless told to, says what it skipped, creates the file
// private to the user and never follows a symbolic link.
package envfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/percona/pds/apps/mcp/internal/privatefile"
)

var (
	// ErrSymlink means the file is a symbolic link, which is never written through.
	ErrSymlink = errors.New("the file is a symbolic link")
	// ErrBadPair means a name or a value cannot be written safely.
	ErrBadPair = errors.New("the name or value cannot be written to an env file")
	// ErrBadPath means the path is not an env file inside the project.
	ErrBadPath = errors.New("the path is not an env file inside the project")
)

// Pair is one setting.
type Pair struct{ Key, Value string }

// Report says what a write did.
type Report struct {
	Created bool
	// Written are keys added, Overwritten keys that had a value and were replaced, Skipped keys that
	// had a value and were left alone.
	Written, Overwritten, Skipped []string
	// MadePrivate is set when the file existed readable by others and was changed to owner only.
	MadePrivate bool
}

var (
	keyPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	plainPattern  = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]*$`)
	assignPattern = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`)
)

// Resolve turns the path a caller named into the file to write. An empty path is .env in the
// project directory. The file's name has to be .env or begin with .env., and it has to be inside the directory
// once symbolic links are resolved, so a request cannot reach a shell profile or an SSH key.
func Resolve(projectDir, requested string) (string, error) {
	root, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		return "", ErrBadPath
	}
	if requested == "" {
		requested = ".env"
	}
	if base := filepath.Base(requested); filepath.IsAbs(requested) || (base != ".env" && !strings.HasPrefix(base, ".env.")) {
		return "", ErrBadPath
	}
	// A server started in the root or the home directory has no project to write into.
	if home, err := os.UserHomeDir(); root == string(filepath.Separator) || (err == nil && sameDir(root, home)) {
		return "", ErrBadPath
	}
	for _, part := range strings.Split(filepath.ToSlash(requested), "/") {
		if part == ".." {
			return "", ErrBadPath
		}
	}

	file := filepath.Join(root, filepath.Clean(requested))
	dir, err := filepath.EvalSymlinks(filepath.Dir(file))
	if err != nil {
		return "", ErrBadPath
	}
	if rel, err := filepath.Rel(root, dir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrBadPath
	}
	if info, err := os.Stat(file); err == nil && info.IsDir() {
		return "", ErrBadPath
	}

	return filepath.Join(dir, filepath.Base(file)), nil
}

func sameDir(a, b string) bool {
	resolved, err := filepath.EvalSymlinks(b)

	return err == nil && a == resolved
}

// Write adds the pairs to the file, creating it private to the user when it is not there.
func Write(path string, pairs []Pair, overwrite bool) (Report, error) {
	for _, p := range pairs {
		if !keyPattern.MatchString(p.Key) || strings.ContainsAny(p.Value, "\r\n\x00") {
			return Report{}, ErrBadPair
		}
	}

	var report Report
	var lines []string
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		report.Created = true
	case err != nil:
		return Report{}, fmt.Errorf("read the env file: %w", err)
	case info.Mode()&os.ModeSymlink != 0:
		return Report{}, ErrSymlink
	default:
		raw, err := os.ReadFile(path)
		if err != nil {
			return Report{}, fmt.Errorf("read the env file: %w", err)
		}
		lines = strings.SplitAfter(string(raw), "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		report.MadePrivate = info.Mode().Perm()&0o077 != 0
	}

	for _, p := range pairs {
		quoted, err := quote(p.Value)
		if err != nil {
			return Report{}, err
		}
		at := -1
		for i, line := range lines {
			if m := assignPattern.FindStringSubmatch(line); m != nil && m[1] == p.Key {
				at = i
			}
		}
		line := p.Key + "=" + quoted + "\n"
		switch {
		case at < 0:
			if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
				lines[n-1] += "\n"
			}
			lines = append(lines, line)
			report.Written = append(report.Written, p.Key)
		case overwrite:
			lines[at] = line
			report.Overwritten = append(report.Overwritten, p.Key)
		default:
			report.Skipped = append(report.Skipped, p.Key)
		}
	}

	if len(report.Written)+len(report.Overwritten) == 0 && !report.MadePrivate {
		return report, nil
	}
	if err := privatefile.Write(path, []byte(strings.Join(lines, ""))); err != nil {
		return Report{}, err
	}

	return report, nil
}

// quote writes a value so that the common .env readers return it as it was given: plain when it
// has nothing special, single quoted when it can be, and double quoted when it holds an apostrophe
// and nothing else those readers treat specially. They disagree about the escapes in a double
// quoted value, so a value that needs one is refused instead of risking a different one read back.
func quote(value string) (string, error) {
	switch {
	case plainPattern.MatchString(value):
		return value, nil
	case !strings.Contains(value, "'"):
		return "'" + value + "'", nil
	case strings.ContainsAny(value, "\\\"$`"):
		return "", ErrBadPair
	default:
		return `"` + value + `"`, nil
	}
}
