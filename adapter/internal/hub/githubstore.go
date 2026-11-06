package hub

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// GitHost creates the GitHub repository a spoke Flux will clone and the
// read-only deploy key Flux authenticates with. The org token stays on the hub.
type GitHost interface {
	EnsureRepo(ctx context.Context, environment string) error
	EnsureCloneKey(ctx context.Context, environment string) (identity, pub string, err error)
	EnsureDeployKey(ctx context.Context, environment, pub string) error
	RevokeCloneKey(ctx context.Context, environment string) error
}

// GitFiles is the GitHub API the hub store needs. Writes and deletes of a
// environment tree go through the Git data API so they land in one commit.
type GitFiles interface {
	EnsureRepo(ctx context.Context, environment string) error
	GetFile(ctx context.Context, repo, path string) (body []byte, sha string, missing bool, err error)
	ListPaths(ctx context.Context, repo, prefix string) ([]string, error)
	PutFile(ctx context.Context, repo, path, message string, body []byte) error
	PutFiles(ctx context.Context, repo string, files map[string][]byte, message string) error
	DeleteFiles(ctx context.Context, repo string, paths []string, message string) error
}

// GitHubStore is a Store backed by one private GitHub repo per environment.
// Paths stay clusters/<name>/... inside that repo so Flux kustomizations
// keep the same path.
type GitHubStore struct {
	mu    sync.Mutex
	files GitFiles
}

// NewGitHubStore writes desired state through files.
func NewGitHubStore(files GitFiles) *GitHubStore {
	return &GitHubStore{files: files}
}

// Write creates or replaces files, grouping by environment so each repo is
// created before the first commit into it.
func (s *GitHubStore) Write(ctx context.Context, files map[string][]byte, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	byEnvironment := map[string][]string{}
	for path := range files {
		environment, err := environmentOf(path)
		if err != nil {
			return err
		}
		byEnvironment[environment] = append(byEnvironment[environment], path)
	}
	environments := make([]string, 0, len(byEnvironment))
	for environment := range byEnvironment {
		environments = append(environments, environment)
	}
	sort.Strings(environments)
	for _, environment := range environments {
		if err := s.files.EnsureRepo(ctx, environment); err != nil {
			return fmt.Errorf("%w: %w", ErrGitHost, err)
		}
		paths := byEnvironment[environment]
		sort.Strings(paths)
		payload := make(map[string][]byte, len(paths))
		for _, path := range paths {
			payload[path] = files[path]
		}
		if err := s.files.PutFiles(ctx, environment, payload, message); err != nil {
			return fmt.Errorf("%w: %w", ErrGitHost, err)
		}
	}

	return nil
}

// Delete removes files. Missing files are a no-op. Paths for one environment
// become one Git commit so Flux never observes a half-deleted tree.
func (s *GitHubStore) Delete(ctx context.Context, paths []string, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	byEnvironment := map[string][]string{}
	for _, path := range paths {
		environment, err := environmentOf(path)
		if err != nil {
			return err
		}
		byEnvironment[environment] = append(byEnvironment[environment], path)
	}
	environments := make([]string, 0, len(byEnvironment))
	for environment := range byEnvironment {
		environments = append(environments, environment)
	}
	sort.Strings(environments)
	for _, environment := range environments {
		environmentPaths := byEnvironment[environment]
		sort.Strings(environmentPaths)
		if err := s.files.DeleteFiles(ctx, environment, environmentPaths, message); err != nil {
			return fmt.Errorf("%w: %w", ErrGitHost, err)
		}
	}

	return nil
}

// Get returns one file.
func (s *GitHubStore) Get(ctx context.Context, path string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	environment, err := environmentOf(path)
	if err != nil {
		return nil, err
	}
	body, _, missing, err := s.files.GetFile(ctx, environment, path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGitHost, err)
	}
	if missing {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	}

	return body, nil
}

// List returns stored paths under prefix, sorted.
func (s *GitHubStore) List(ctx context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	environment, err := environmentOf(prefix)
	if err != nil {
		return nil, err
	}
	paths, err := s.files.ListPaths(ctx, environment, prefix)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGitHost, err)
	}
	sort.Strings(paths)

	return paths, nil
}

var _ Store = (*GitHubStore)(nil)

func environmentOf(path string) (string, error) {
	path = strings.TrimPrefix(path, "/")
	if path == "" || strings.Contains(path, "..") {
		return "", fmt.Errorf("%w: path %q is not under clusters/", ErrGitHost, path)
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] != "clusters" || parts[1] == "" {
		return "", fmt.Errorf("%w: path %q is not under clusters/", ErrGitHost, path)
	}

	return parts[1], nil
}
