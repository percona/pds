// Package tokenstore keeps the one refresh token pds-mcp holds for each control plane, in a file
// only the user can read. An access token is never written.
package tokenstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/percona/pds/apps/mcp/internal/privatefile"
)

// ErrNoLogin means nothing is saved for the issuer.
var ErrNoLogin = errors.New("no login is saved")

// ErrUnreadable means the file exists and is not one this version wrote. It is left as it is, so a
// file from somewhere else is never destroyed by a sign in.
var ErrUnreadable = errors.New("the saved logins file could not be read")

const fileVersion = 1

// Login is what a sign in leaves: the refresh token, and the scopes it was granted.
type Login struct {
	RefreshToken string `json:"refreshToken"`
	Scope        string `json:"scope,omitempty"`
}

type document struct {
	Version int              `json:"version"`
	Logins  map[string]Login `json:"logins"`
}

// Store is the login file. Logins are keyed by issuer, because a refresh token is good only at the
// authorization server that issued it.
type Store struct {
	path string
}

// New returns the store for the file at path. Nothing is created until a login is saved.
func New(path string) *Store {
	return &Store{path: path}
}

// Load returns the login saved for an issuer.
func (s *Store) Load(issuer string) (Login, error) {
	doc, err := s.read()
	if err != nil {
		return Login{}, err
	}
	login, ok := doc.Logins[issuer]
	if !ok || login.RefreshToken == "" {
		return Login{}, ErrNoLogin
	}

	return login, nil
}

// Save replaces the login for an issuer.
func (s *Store) Save(issuer string, login Login) error {
	return s.locked(func() error {
		doc, err := s.read()
		if err != nil {
			return err
		}
		doc.Logins[issuer] = login

		return s.write(doc)
	})
}

// Update runs fn on the login as it is now, with every other pds-mcp process kept out, and saves
// what fn returns. A rotating refresh token has to be used once and its successor kept before
// anyone else reads the file, which is what this guarantees. When fn fails the old login stays.
func (s *Store) Update(issuer string, fn func(Login) (Login, error)) error {
	return s.locked(func() error {
		doc, err := s.read()
		if err != nil {
			return err
		}
		current, ok := doc.Logins[issuer]
		if !ok || current.RefreshToken == "" {
			return ErrNoLogin
		}
		next, err := fn(current)
		if err != nil {
			return err
		}
		doc.Logins[issuer] = next

		return s.write(doc)
	})
}

// Delete removes the login for an issuer. Removing one that is not there is not an error.
func (s *Store) Delete(issuer string) error {
	return s.locked(func() error {
		doc, err := s.read()
		if err != nil {
			return err
		}
		delete(doc.Logins, issuer)

		return s.write(doc)
	})
}

func (s *Store) read() (document, error) {
	empty := document{Version: fileVersion, Logins: map[string]Login{}}

	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return document{}, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}

	var doc document
	if err := json.Unmarshal(data, &doc); err != nil || doc.Version != fileVersion {
		return document{}, ErrUnreadable
	}
	if doc.Logins == nil {
		doc.Logins = map[string]Login{}
	}
	// A file somebody loosened is tightened, not trusted.
	if info, err := os.Stat(s.path); err == nil && info.Mode().Perm() != 0o600 {
		if err := os.Chmod(s.path, 0o600); err != nil {
			return document{}, fmt.Errorf("%w: %w", ErrUnreadable, err)
		}
	}

	return doc, nil
}

// write replaces the file in one step, so a crash never leaves half of it.
func (s *Store) write(doc document) error {
	if len(doc.Logins) == 0 {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove the saved logins: %w", err)
		}

		return nil
	}

	if err := privatefile.EnsureDir(s.path); err != nil {
		return err
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode the saved logins: %w", err)
	}

	return privatefile.Write(s.path, data)
}

func (s *Store) locked(fn func() error) error {
	return privatefile.Locked(s.path, fn)
}
