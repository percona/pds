// Package connections keeps what the data server knows about each database it can reach, except
// the password: host, port, user, database name and how the connection is protected. The password
// is in the operating system's credential store.
package connections

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/percona/pds/apps/mcp/internal/privatefile"
	"github.com/percona/pds/apps/mcp/internal/sqlrun"
)

var (
	// ErrNotFound means no connection is saved for the database.
	ErrNotFound = errors.New("no connection is saved for this database")
	// ErrUnreadable means the file exists and is not one this version wrote. It is left as it is.
	ErrUnreadable = errors.New("the saved connections file could not be read")
)

const fileVersion = 1

// Entry is one saved connection. ID is the database id the control plane gave.
type Entry struct {
	ID     string        `json:"id"`
	Target sqlrun.Target `json:"target"`
}

type document struct {
	Version int              `json:"version"`
	Entries map[string]Entry `json:"entries"`
}

// Store is the connections file. Entries are keyed by control plane and database id, because the
// same id on two control planes is two databases.
type Store struct{ path string }

// New returns the store for the file at path. Nothing is created until a connection is saved.
func New(path string) *Store { return &Store{path: path} }

func key(controlPlane, id string) string { return controlPlane + " " + id }

// Get returns the connection saved for a database.
func (s *Store) Get(controlPlane, id string) (Entry, error) {
	doc, err := s.read()
	if err != nil {
		return Entry{}, err
	}
	entry, ok := doc.Entries[key(controlPlane, id)]
	if !ok {
		return Entry{}, ErrNotFound
	}

	return entry, nil
}

// List returns the connections saved for a control plane, in id order.
func (s *Store) List(controlPlane string) ([]Entry, error) {
	doc, err := s.read()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for k, entry := range doc.Entries {
		if k == key(controlPlane, entry.ID) {
			out = append(out, entry)
		}
	}
	slices.SortFunc(out, func(a, b Entry) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}

		return 0
	})

	return out, nil
}

// Put saves or replaces the connection for a database.
func (s *Store) Put(controlPlane string, entry Entry) error {
	return privatefile.Locked(s.path, func() error {
		doc, err := s.read()
		if err != nil {
			return err
		}
		doc.Entries[key(controlPlane, entry.ID)] = entry
		data, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("encode the saved connections: %w", err)
		}

		return privatefile.Write(s.path, data)
	})
}

func (s *Store) read() (document, error) {
	empty := document{Version: fileVersion, Entries: map[string]Entry{}}

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
	if doc.Entries == nil {
		doc.Entries = map[string]Entry{}
	}

	return doc, nil
}
