package hub_test

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/hub"
)

type memFiles struct {
	mu            sync.Mutex
	repos         map[string]map[string][]byte
	ensured       []string
	writeBatches  int
	deleteBatches int
}

func (m *memFiles) EnsureRepo(_ context.Context, environment string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.repos == nil {
		m.repos = map[string]map[string][]byte{}
	}
	if _, ok := m.repos[environment]; !ok {
		m.repos[environment] = map[string][]byte{}
	}
	m.ensured = append(m.ensured, environment)

	return nil
}

func (m *memFiles) GetFile(_ context.Context, repo, path string) ([]byte, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	files, ok := m.repos[repo]
	if !ok {
		return nil, "", true, nil
	}
	body, ok := files[path]
	if !ok {
		return nil, "", true, nil
	}
	cp := make([]byte, len(body))
	copy(cp, body)

	return cp, "sha", false, nil
}

func (m *memFiles) ListPaths(_ context.Context, repo, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	files, ok := m.repos[repo]
	if !ok {
		return nil, nil
	}
	var paths []string
	for path := range files {
		if prefix == "" || strings.HasPrefix(path, prefix) {
			paths = append(paths, path)
		}
	}

	return paths, nil
}

func (m *memFiles) PutFile(_ context.Context, repo, path, _ string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.repos == nil || m.repos[repo] == nil {
		return hub.ErrGitHost
	}
	cp := make([]byte, len(body))
	copy(cp, body)
	m.repos[repo][path] = cp

	return nil
}

func (m *memFiles) PutFiles(_ context.Context, repo string, files map[string][]byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeBatches++
	if m.repos == nil || m.repos[repo] == nil {
		return hub.ErrGitHost
	}
	for path, body := range files {
		cp := make([]byte, len(body))
		copy(cp, body)
		m.repos[repo][path] = cp
	}

	return nil
}

func (m *memFiles) DeleteFiles(_ context.Context, repo string, paths []string, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteBatches++
	if files, ok := m.repos[repo]; ok {
		for _, path := range paths {
			delete(files, path)
		}
	}

	return nil
}

func (m *memFiles) HasRepo(_ context.Context, repo string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.repos[repo]

	return ok, nil
}

func TestGitHubStoreWriteGetListDelete(t *testing.T) {
	host := &memFiles{}
	store := hub.NewGitHubStore(host)
	ctx := context.Background()
	path := "clusters/spoke-a/instances/kustomization.yaml"

	require.NoError(t, store.Write(ctx, map[string][]byte{path: []byte("kind: Kustomization\n")}, "seed"))
	assert.Equal(t, []string{"spoke-a"}, host.ensured)
	assert.Equal(t, 1, host.writeBatches)

	body, err := store.Get(ctx, path)
	require.NoError(t, err)
	assert.Equal(t, "kind: Kustomization\n", string(body))

	paths, err := store.List(ctx, "clusters/spoke-a/instances/")
	require.NoError(t, err)
	assert.Equal(t, []string{path}, paths)

	require.NoError(t, store.Delete(ctx, []string{path}, "delete"))
	_, err = store.Get(ctx, path)
	require.ErrorIs(t, err, hub.ErrNotFound)
	assert.Equal(t, 1, host.deleteBatches)
}

func TestGitHubStoreDeleteRemovesAnEnvironmentTreeInOneCommit(t *testing.T) {
	host := &memFiles{}
	store := hub.NewGitHubStore(host)
	ctx := context.Background()
	files := map[string][]byte{
		"clusters/spoke-a/instances/a.yaml":             []byte("a: 1\n"),
		"clusters/spoke-a/instances/b.yaml":             []byte("b: 1\n"),
		"clusters/spoke-a/instances/kustomization.yaml": []byte("kind: Kustomization\n"),
	}
	require.NoError(t, store.Write(ctx, files, "seed"))

	var paths []string
	for path := range files {
		paths = append(paths, path)
	}
	require.NoError(t, store.Delete(ctx, paths, "disconnect spoke-a"))
	assert.Equal(t, 1, host.deleteBatches)

	listed, err := store.List(ctx, "clusters/spoke-a/")
	require.NoError(t, err)
	assert.Empty(t, listed)
}

func TestGitHubStoreWritePutsAnEnvironmentTreeInOneCommit(t *testing.T) {
	host := &memFiles{}
	store := hub.NewGitHubStore(host)
	ctx := context.Background()
	files := map[string][]byte{
		"clusters/spoke-a/instances/a.yaml":             []byte("a: 1\n"),
		"clusters/spoke-a/instances/b.yaml":             []byte("b: 1\n"),
		"clusters/spoke-a/instances/kustomization.yaml": []byte("kind: Kustomization\n"),
	}
	require.NoError(t, store.Write(ctx, files, "seed"))
	assert.Equal(t, 1, host.writeBatches)

	listed, err := store.List(ctx, "clusters/spoke-a/")
	require.NoError(t, err)
	assert.Len(t, listed, 3)
}

func TestGitHubStoreRejectsPathsOutsideEnvironments(t *testing.T) {
	store := hub.NewGitHubStore(&memFiles{})
	err := store.Write(context.Background(), map[string][]byte{"README.md": []byte("no")}, "bad")
	require.ErrorIs(t, err, hub.ErrGitHost)
}

func TestTeamGitWritesToTheRepoAssignedToEachTeam(t *testing.T) {
	host := &memFiles{}
	ctx := context.Background()
	path := "clusters/prod/instances/kustomization.yaml"
	acmeFiles := newPrefixedTeamGit(host, 1, "acme-platform")
	globexFiles := newPrefixedTeamGit(host, 2, "globex-core")
	require.NoError(t, acmeFiles.Assign(ctx, "prod"))
	require.NoError(t, globexFiles.Assign(ctx, "prod"))
	acme := hub.NewGitHubStore(acmeFiles)
	globex := hub.NewGitHubStore(globexFiles)

	require.NoError(t, acme.Write(ctx, map[string][]byte{path: []byte("acme\n")}, "seed"))
	require.NoError(t, globex.Write(ctx, map[string][]byte{path: []byte("globex\n")}, "seed"))

	assert.ElementsMatch(t, []string{"acme-platform-prod", "globex-core-prod"}, host.ensured)
	assert.Equal(t, "acme\n", string(host.repos["acme-platform-prod"][path]))
	assert.Equal(t, "globex\n", string(host.repos["globex-core-prod"][path]))
	_, ok := host.repos["prod"]
	assert.False(t, ok, "neither team should write the unprefixed name")

	body, err := acme.Get(ctx, path)
	require.NoError(t, err)
	assert.Equal(t, "acme\n", string(body))
	body, err = globex.Get(ctx, path)
	require.NoError(t, err)
	assert.Equal(t, "globex\n", string(body))
}

func TestTeamGitNamesARepoAfterTheEnvironmentWhenNothingNamesItBetter(t *testing.T) {
	host := &memFiles{}
	ctx := context.Background()
	path := "clusters/prod/instances/kustomization.yaml"
	store := hub.NewGitHubStore(hub.NewTeamGit(host, 1))

	require.NoError(t, store.Write(ctx, map[string][]byte{path: []byte("acme\n")}, "seed"))

	assert.Equal(t, []string{"prod"}, host.ensured)
	assert.Equal(t, "acme\n", string(host.repos["prod"][path]))
}

func newPrefixedTeamGit(host hub.GitFiles, team uint, prefix string) *hub.TeamGit {
	files := hub.NewTeamGit(host, team)
	files.NameNewRepos(func(context.Context) (string, error) { return prefix, nil })

	return files
}

func TestTeamGitNamesANewEnvironmentAfterItsOrganizationAndTeam(t *testing.T) {
	host := &memFiles{}
	ctx := context.Background()
	files := newPrefixedTeamGit(host, 1, "acme-platform")

	require.NoError(t, files.Assign(ctx, "prod"))
	require.NoError(t, hub.NewGitHubStore(files).Write(ctx,
		map[string][]byte{"clusters/prod/instances/kustomization.yaml": []byte("new\n")}, "seed"))

	assert.Equal(t, "acme-platform-prod", files.Repo("prod"))
	assert.Equal(t, []string{"acme-platform-prod"}, host.ensured)
}

func TestTeamGitKeepsTheRepoStoredOnTheRecord(t *testing.T) {
	host := &memFiles{}
	ctx := context.Background()
	files := newPrefixedTeamGit(host, 1, "renamed-team")
	files.UseRecordedRepos(func(environment string) string {
		if environment == "prod" {
			return "acme-platform-prod"
		}

		return ""
	})

	require.NoError(t, hub.NewGitHubStore(files).Write(ctx,
		map[string][]byte{"clusters/prod/instances/kustomization.yaml": []byte("x\n")}, "seed"))

	assert.Equal(t, []string{"acme-platform-prod"}, host.ensured)
}

// memClaims is the claim table: one owner per repository name, across teams.
type memClaims struct {
	mu     sync.Mutex
	owners map[string]string
}

func (c *memClaims) ClaimRepo(_ context.Context, repo, owner string) (won, fresh bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owners == nil {
		c.owners = map[string]string{}
	}
	if current, ok := c.owners[repo]; ok {
		return current == owner, false, nil
	}
	c.owners[repo] = owner

	return true, true, nil
}

func (c *memClaims) ReleaseRepo(_ context.Context, repo, owner string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owners[repo] == owner {
		delete(c.owners, repo)
	}

	return nil
}

func claimingTeamGit(host hub.GitFiles, claims hub.RepoClaims, team uint, prefix string) *hub.TeamGit {
	files := hub.NewTeamGit(host, team)
	files.NameNewRepos(func(context.Context) (string, error) { return prefix, nil })
	files.UseRepoClaims(claims)

	return files
}

func TestTwoOwnersNeverGetTheSameRepoName(t *testing.T) {
	// Team web with environment api-x, and team web-api with environment x,
	// both read acme-web-api-x.
	host := &memFiles{}
	claims := &memClaims{}
	ctx := context.Background()
	first := claimingTeamGit(host, claims, 1, "acme-web")
	second := claimingTeamGit(host, claims, 2, "acme-web-api")

	require.NoError(t, first.Assign(ctx, "api-x"))
	require.NoError(t, second.Assign(ctx, "x"))

	assert.Equal(t, "acme-web-api-x", first.Repo("api-x"))
	assert.Equal(t, "acme-web-api-x-2", second.Repo("x"))
}

func TestAssignAgainReusesTheNameItClaimed(t *testing.T) {
	host := &memFiles{}
	claims := &memClaims{}
	ctx := context.Background()
	files := claimingTeamGit(host, claims, 1, "acme-web")
	require.NoError(t, files.Assign(ctx, "prod"))

	retry := claimingTeamGit(host, claims, 1, "acme-web")
	require.NoError(t, retry.Assign(ctx, "prod"))

	assert.Equal(t, "acme-web-prod", retry.Repo("prod"))
}

func TestAssignSkipsARepoThatAlreadyExistsOnGitHub(t *testing.T) {
	host := &memFiles{repos: map[string]map[string][]byte{"acme-web-prod": {}}}
	claims := &memClaims{}
	files := claimingTeamGit(host, claims, 1, "acme-web")

	require.NoError(t, files.Assign(context.Background(), "prod"))

	assert.Equal(t, "acme-web-prod-2", files.Repo("prod"), "a repo this hub did not claim is somebody else's")
	_, held := claims.owners["acme-web-prod"]
	assert.False(t, held, "the claim on a name that turned out taken is given back")
}

func TestAssignRacingOwnersAllGetDistinctNames(t *testing.T) {
	host := &memFiles{}
	claims := &memClaims{}
	const n = 8
	names := make(chan string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			files := claimingTeamGit(host, claims, uint(i+1), "acme-web")
			assert.NoError(t, files.Assign(context.Background(), "prod"))
			names <- files.Repo("prod")
		}()
	}
	wg.Wait()
	close(names)

	seen := map[string]bool{}
	for name := range names {
		assert.False(t, seen[name], "%s was handed out twice", name)
		seen[name] = true
	}
	assert.Len(t, seen, n)
}

func TestAssignGivesUpWhenEveryNameIsTaken(t *testing.T) {
	host := &memFiles{}
	claims := &memClaims{owners: map[string]string{"acme-web-prod": "other/prod"}}
	for i := 2; i <= 99; i++ {
		claims.owners["acme-web-prod-"+strconv.Itoa(i)] = "other/prod"
	}
	files := claimingTeamGit(host, claims, 1, "acme-web")

	err := files.Assign(context.Background(), "prod")

	require.ErrorIs(t, err, hub.ErrRepoName)
}
