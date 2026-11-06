// Package privatefile writes the files pds-mcp keeps for one user: a file is replaced in one step,
// is readable by its owner only, and is changed under a lock so two programs never interleave.
package privatefile

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Write replaces the file at path with data, in one step, so a crash never leaves half of it. The
// new file is mode 0600 whatever the old one was. The directory has to exist.
func Write(path string, data []byte) error {
	staging, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("save %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = os.Remove(staging.Name()) }()

	// CreateTemp makes the file 0600, and the mode is set again so that it does not depend on that.
	if err := staging.Chmod(0o600); err != nil {
		_ = staging.Close()

		return fmt.Errorf("save %s: %w", filepath.Base(path), err)
	}
	if _, err := staging.Write(data); err != nil {
		_ = staging.Close()

		return fmt.Errorf("save %s: %w", filepath.Base(path), err)
	}
	if err := staging.Sync(); err != nil {
		_ = staging.Close()

		return fmt.Errorf("save %s: %w", filepath.Base(path), err)
	}
	if err := staging.Close(); err != nil {
		return fmt.Errorf("save %s: %w", filepath.Base(path), err)
	}

	if err := os.Rename(staging.Name(), path); err != nil {
		return fmt.Errorf("save %s: %w", filepath.Base(path), err)
	}

	return nil
}

// EnsureDir creates the directory that holds a file, private to the user, and tightens one that
// was already there.
func EnsureDir(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	// A directory that already existed may be looser than this program makes it.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("protect %s: %w", dir, err)
	}

	return nil
}

// Locked holds an exclusive lock on a file beside path for as long as fn runs.
func Locked(path string, fn func() error) error {
	if err := EnsureDir(path); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("lock %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = lock.Close() }()

	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	return fn()
}
