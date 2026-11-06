package hub

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

// ErrOutsideRoot means a path resolved outside the desired-state tree.
var ErrOutsideRoot = errors.New("git path is outside the root")

const pathEscapesParent = "path escapes from parent"

// DirStore writes YAML files into a directory a Git repo uses as a working
// tree. Flux pulls that repo, and something outside this process commits: the
// local lab runs a commit loop. A hub with a GitHub org uses GitHubStore
// instead and commits for itself.
type DirStore struct {
	root *os.Root
}

// NewDirStore stores files under root.
func NewDirStore(root string) (*DirStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("hub git dir is empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	opened, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}

	return &DirStore{root: opened}, nil
}

// confine maps a request path onto a name os.Root will accept. Callers build
// these paths from request fields, so a path that cleans its way past the
// root is a traversal rather than a typo. This does not police which environment
// directory a path names: that is the caller's rule, enforced where the name
// is validated.
func confine(p string) (string, error) {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." {
		return ".", nil
	}
	cleaned := path.Clean(p)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: %s", ErrOutsideRoot, p)
	}

	return cleaned, nil
}

func wrapRootErr(err error, p string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrOutsideRoot) {
		return err
	}
	if strings.Contains(err.Error(), pathEscapesParent) {
		return fmt.Errorf("%w: %s", ErrOutsideRoot, p)
	}

	return err
}

func (s *DirStore) rejectSymlinks(name, orig string) error {
	cur := ""
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." {
			continue
		}
		if cur == "" {
			cur = part
		} else {
			cur = path.Join(cur, part)
		}
		info, err := s.root.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}

			return wrapRootErr(err, orig)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrOutsideRoot, orig)
		}
	}

	return nil
}

// Write creates or replaces files under root.
func (s *DirStore) Write(_ context.Context, files map[string][]byte, _ string) error {
	for p, body := range files {
		name, err := confine(p)
		if err != nil {
			return err
		}
		if err := s.rejectSymlinks(name, p); err != nil {
			return err
		}
		if dir := path.Dir(name); dir != "." {
			if err := s.root.MkdirAll(dir, 0o755); err != nil {
				return wrapRootErr(err, p)
			}
		}
		if err := s.root.WriteFile(name, body, 0o644); err != nil {
			return wrapRootErr(err, p)
		}
	}

	return nil
}

// Delete removes files under root.
func (s *DirStore) Delete(_ context.Context, paths []string, _ string) error {
	for _, p := range paths {
		name, err := confine(p)
		if err != nil {
			return err
		}
		if err := s.rejectSymlinks(name, p); err != nil {
			return err
		}
		if err := s.root.Remove(name); err != nil && !os.IsNotExist(err) {
			return wrapRootErr(err, p)
		}
	}

	return nil
}

// Get reads one file.
func (s *DirStore) Get(_ context.Context, p string) ([]byte, error) {
	name, err := confine(p)
	if err != nil {
		return nil, err
	}
	if err := s.rejectSymlinks(name, p); err != nil {
		return nil, err
	}
	body, err := s.root.ReadFile(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
		}

		return nil, wrapRootErr(err, p)
	}

	return body, nil
}

// List returns file paths under prefix, relative to root, slash-separated.
func (s *DirStore) List(_ context.Context, prefix string) ([]string, error) {
	name, err := confine(prefix)
	if err != nil {
		return nil, err
	}
	if err := s.rejectSymlinks(name, prefix); err != nil {
		return nil, err
	}
	if _, err := s.root.Lstat(name); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, wrapRootErr(err, prefix)
	}

	var out []string
	err = fs.WalkDir(s.root.FS(), name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}

			return wrapRootErr(err, p)
		}
		if d.IsDir() {
			return nil
		}
		out = append(out, p)

		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Strings(out)

	return out, nil
}
