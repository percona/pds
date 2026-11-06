package hub

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ErrNotFound means a path is not in the store.
var ErrNotFound = errors.New("git path not found")

// Store is the desired-state tree the hub commits into. Tests use MemoryStore.
type Store interface {
	Write(ctx context.Context, files map[string][]byte, message string) error
	Delete(ctx context.Context, paths []string, message string) error
	Get(ctx context.Context, path string) ([]byte, error)
	List(ctx context.Context, prefix string) ([]string, error)
}

// MemoryStore holds files in process memory.
type MemoryStore struct {
	mu    sync.Mutex
	files map[string][]byte
}

// NewMemoryStore returns an empty in-memory Git stand-in.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{files: map[string][]byte{}}
}

// Write replaces the named files.
func (s *MemoryStore) Write(_ context.Context, files map[string][]byte, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for path, body := range files {
		cp := make([]byte, len(body))
		copy(cp, body)
		s.files[path] = cp
	}

	return nil
}

// Delete removes the named files.
func (s *MemoryStore) Delete(_ context.Context, paths []string, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, path := range paths {
		delete(s.files, path)
	}

	return nil
}

// Get returns one file.
func (s *MemoryStore) Get(_ context.Context, path string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	body, ok := s.files[path]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	cp := make([]byte, len(body))
	copy(cp, body)

	return cp, nil
}

// List returns stored paths under prefix, sorted.
func (s *MemoryStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, 0, len(s.files))
	for path := range s.files {
		if prefix == "" || strings.HasPrefix(path, prefix) {
			out = append(out, path)
		}
	}
	sort.Strings(out)

	return out, nil
}
