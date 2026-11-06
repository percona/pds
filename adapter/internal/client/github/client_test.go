package github_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ghclient "github.com/percona/pds/adapter/internal/client/github"
)

type fakeGitHub struct {
	mu                  sync.Mutex
	org                 string
	repos               map[string]*fakeRepo
	gitCommits          int
	contentDeletes      int
	nextKeyID           int
	createDefaultBranch string
	deployKeyPostStatus int
	skipDeployKeyInsert bool
	truncatedTrees      bool
}

type fakeRepo struct {
	files         map[string]string
	shas          map[string]string
	blobs         map[string]string
	keys          []fakeDeployKey
	commitSHA     string
	treeSHA       string
	defaultBranch string
	description   string
	refs          map[string]string
}

type fakeDeployKey struct {
	ID       int
	Title    string
	Key      string
	ReadOnly bool
}

func newFakeGitHub(org string) *fakeGitHub {
	return &fakeGitHub{org: org, repos: map[string]*fakeRepo{}, nextKeyID: 1}
}

func (f *fakeGitHub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/"+f.org+"/"):
			f.handleGet(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/"+f.org+"/repos":
			f.handleCreate(w, r)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/repos/"+f.org+"/"):
			f.handleGitWrite(w, r)
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/git/refs/"):
			f.handleGitRef(w, r)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/repos/"+f.org+"/"):
			f.handlePatchRepo(w, r)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/contents/"):
			f.handlePut(w, r)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/keys/"):
			f.handleDeleteKey(w, r)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/contents/"):
			f.handleDelete(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func (f *fakeGitHub) handleGet(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/repos/"+f.org+"/")
	name, extra, _ := strings.Cut(rest, "/")
	repo := f.repos[name]
	if extra == "keys" {
		if repo == nil {
			http.NotFound(w, r)
			return
		}
		var out []map[string]any
		for _, key := range repo.keys {
			out = append(out, map[string]any{
				"id":        key.ID,
				"title":     key.Title,
				"key":       key.Key,
				"read_only": key.ReadOnly,
			})
		}
		if out == nil {
			out = []map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	if extra == "" {
		if repo == nil {
			http.NotFound(w, r)
			return
		}
		f.writeRepo(w, name, repo)
		return
	}
	if repo == nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(extra, "git/trees/") {
		var tree []map[string]string
		for path := range repo.files {
			tree = append(tree, map[string]string{"path": path, "type": "blob"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"tree": tree, "truncated": f.truncatedTrees})
		return
	}
	if strings.HasPrefix(extra, "git/ref/") {
		branch := strings.TrimPrefix(extra, "git/ref/heads/")
		sha := repo.commitSHA
		if repo.refs != nil {
			got, ok := repo.refs[branch]
			if !ok {
				http.NotFound(w, r)
				return
			}
			sha = got
		} else if branch != "main" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ref": "refs/heads/" + branch,
			"object": map[string]string{
				"sha":  sha,
				"type": "commit",
			},
		})
		return
	}
	if strings.HasPrefix(extra, "git/commits/") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sha": repo.commitSHA,
			"tree": map[string]string{
				"sha": repo.treeSHA,
			},
		})
		return
	}
	if strings.HasPrefix(extra, "contents/") {
		path := strings.TrimPrefix(extra, "contents/")
		body, ok := repo.files[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"content":  base64.StdEncoding.EncodeToString([]byte(body)),
			"encoding": "base64",
			"sha":      repo.shas[path],
		})
		return
	}
	http.NotFound(w, r)
}

func (f *fakeGitHub) handleCreate(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(raw, &in)
	if in.Name == "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	if f.repos[in.Name] != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"already exists"}`))
		return
	}
	branch := f.createDefaultBranch
	if branch == "" {
		branch = "main"
	}
	f.repos[in.Name] = &fakeRepo{
		files:         map[string]string{"README.md": "ok"},
		shas:          map[string]string{"README.md": "sha-readme"},
		blobs:         map[string]string{},
		commitSHA:     "commit-0",
		treeSHA:       "tree-0",
		defaultBranch: branch,
		description:   in.Description,
		refs:          map[string]string{branch: "commit-0"},
	}
	w.WriteHeader(http.StatusCreated)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"name": in.Name, "description": in.Description})
}

func (f *fakeGitHub) handlePut(w http.ResponseWriter, r *http.Request) {
	repo, path := splitContents(f.org, r.URL.Path)
	got := f.repos[repo]
	if got == nil {
		http.NotFound(w, r)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var in struct {
		Content string `json:"content"`
		SHA     string `json:"sha"`
	}
	_ = json.Unmarshal(raw, &in)
	body, err := base64.StdEncoding.DecodeString(in.Content)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	got.files[path] = string(body)
	got.shas[path] = "sha-" + path
	w.WriteHeader(http.StatusCreated)
}

func (f *fakeGitHub) handleDelete(w http.ResponseWriter, r *http.Request) {
	repo, path := splitContents(f.org, r.URL.Path)
	got := f.repos[repo]
	if got == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := got.files[path]; !ok {
		http.NotFound(w, r)
		return
	}
	delete(got.files, path)
	delete(got.shas, path)
	f.contentDeletes++
	w.WriteHeader(http.StatusOK)
}

func (f *fakeGitHub) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/repos/"+f.org+"/")
	name, extra, _ := strings.Cut(rest, "/")
	repo := f.repos[name]
	if repo == nil {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.Atoi(strings.TrimPrefix(extra, "keys/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	kept := repo.keys[:0]
	found := false
	for _, key := range repo.keys {
		if key.ID == id {
			found = true
			continue
		}
		kept = append(kept, key)
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	repo.keys = kept
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeGitHub) handleGitWrite(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/repos/"+f.org+"/")
	name, extra, _ := strings.Cut(rest, "/")
	repo := f.repos[name]
	if repo == nil {
		http.NotFound(w, r)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	switch extra {
	case "keys":
		var in struct {
			Title    string `json:"title"`
			Key      string `json:"key"`
			ReadOnly bool   `json:"read_only"`
		}
		_ = json.Unmarshal(raw, &in)
		if in.Key == "" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		id := f.nextKeyID
		f.nextKeyID++
		if !f.skipDeployKeyInsert {
			repo.keys = append(repo.keys, fakeDeployKey{
				ID:       id,
				Title:    in.Title,
				Key:      in.Key,
				ReadOnly: in.ReadOnly,
			})
		}
		status := http.StatusCreated
		if f.deployKeyPostStatus != 0 {
			status = f.deployKeyPostStatus
		}
		w.WriteHeader(status)
		if status == http.StatusCreated || status == http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":        id,
				"title":     in.Title,
				"key":       in.Key,
				"read_only": in.ReadOnly,
			})
		}
	case "git/blobs":
		var in struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		_ = json.Unmarshal(raw, &in)
		body, err := base64.StdEncoding.DecodeString(in.Content)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if repo.blobs == nil {
			repo.blobs = map[string]string{}
		}
		sha := "blob-" + strconv.Itoa(len(repo.blobs)+1)
		repo.blobs[sha] = string(body)
		w.WriteHeader(http.StatusCreated)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"sha": sha})
	case "git/trees":
		var in struct {
			Tree []struct {
				Path string  `json:"path"`
				SHA  *string `json:"sha"`
			} `json:"tree"`
		}
		_ = json.Unmarshal(raw, &in)
		for _, entry := range in.Tree {
			if entry.SHA == nil {
				delete(repo.files, entry.Path)
				delete(repo.shas, entry.Path)
				continue
			}
			if repo.blobs != nil {
				if body, ok := repo.blobs[*entry.SHA]; ok {
					repo.files[entry.Path] = body
					repo.shas[entry.Path] = *entry.SHA
				}
			}
		}
		repo.treeSHA = "tree-1"
		w.WriteHeader(http.StatusCreated)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"sha": repo.treeSHA})
	case "git/commits":
		f.gitCommits++
		repo.commitSHA = "commit-1"
		if repo.refs == nil {
			repo.refs = map[string]string{}
		}
		repo.refs["main"] = repo.commitSHA
		w.WriteHeader(http.StatusCreated)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"sha": repo.commitSHA})
	case "git/refs":
		var in struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		}
		_ = json.Unmarshal(raw, &in)
		name := strings.TrimPrefix(in.Ref, "refs/heads/")
		if name == "" || in.SHA == "" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		if repo.refs == nil {
			repo.refs = map[string]string{}
		}
		if _, exists := repo.refs[name]; exists {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		repo.refs[name] = in.SHA
		if name == "main" {
			repo.commitSHA = in.SHA
		}
		w.WriteHeader(http.StatusCreated)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ref": in.Ref,
			"object": map[string]string{
				"sha":  in.SHA,
				"type": "commit",
			},
		})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) handleGitRef(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/repos/"+f.org+"/")
	name, extra, _ := strings.Cut(rest, "/")
	repo := f.repos[name]
	if repo == nil {
		http.NotFound(w, r)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var in struct {
		SHA string `json:"sha"`
	}
	_ = json.Unmarshal(raw, &in)
	branch := strings.TrimPrefix(extra, "git/refs/heads/")
	if branch == extra || branch == "" {
		branch = "main"
	}
	if in.SHA != "" {
		repo.commitSHA = in.SHA
		if repo.refs == nil {
			repo.refs = map[string]string{}
		}
		repo.refs[branch] = in.SHA
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ref": "refs/heads/" + branch,
		"object": map[string]string{
			"sha":  repo.commitSHA,
			"type": "commit",
		},
	})
}

func (f *fakeGitHub) handlePatchRepo(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/repos/"+f.org+"/")
	name, extra, _ := strings.Cut(rest, "/")
	if extra != "" {
		http.NotFound(w, r)
		return
	}
	repo := f.repos[name]
	if repo == nil {
		http.NotFound(w, r)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var in struct {
		DefaultBranch string `json:"default_branch"`
	}
	_ = json.Unmarshal(raw, &in)
	if in.DefaultBranch != "" {
		repo.defaultBranch = in.DefaultBranch
	}
	f.writeRepo(w, name, repo)
}

func (f *fakeGitHub) writeRepo(w http.ResponseWriter, name string, repo *fakeRepo) {
	branch := repo.defaultBranch
	if branch == "" {
		branch = "main"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"name":           name,
		"default_branch": branch,
		"description":    repo.description,
	})
}

func splitContents(org, urlPath string) (repo, path string) {
	rest := strings.TrimPrefix(urlPath, "/repos/"+org+"/")
	name, extra, _ := strings.Cut(rest, "/")
	return name, strings.TrimPrefix(extra, "contents/")
}

func TestEnsureRepoCreatesThenIsIdempotent(t *testing.T) {
	fake := newFakeGitHub("percona-cloud")
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := ghclient.New("percona-cloud", "token").WithBaseURL(srv.URL)

	require.NoError(t, c.EnsureRepo(context.Background(), "test-lucas"))
	require.NoError(t, c.EnsureRepo(context.Background(), "test-lucas"))
	assert.Equal(t, "https://github.com/percona-cloud/test-lucas.git", c.CloneURL("test-lucas"))
	assert.Equal(t, "ssh://git@github.com/percona-cloud/test-lucas.git", c.SSHCloneURL("test-lucas"))
}

func TestPutGetListDeleteFile(t *testing.T) {
	fake := newFakeGitHub("percona-cloud")
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := ghclient.New("percona-cloud", "token").WithBaseURL(srv.URL)
	ctx := context.Background()
	require.NoError(t, c.EnsureRepo(ctx, "spoke-a"))

	require.NoError(t, c.PutFile(ctx, "spoke-a", "clusters/spoke-a/instances/kustomization.yaml", "seed", []byte("kind: Kustomization\n")))
	body, sha, missing, err := c.GetFile(ctx, "spoke-a", "clusters/spoke-a/instances/kustomization.yaml")
	require.NoError(t, err)
	assert.False(t, missing)
	assert.NotEmpty(t, sha)
	assert.Contains(t, string(body), "kind: Kustomization")

	paths, err := c.ListPaths(ctx, "spoke-a", "clusters/spoke-a/instances/")
	require.NoError(t, err)
	assert.Equal(t, []string{"clusters/spoke-a/instances/kustomization.yaml"}, paths)

	require.NoError(t, c.DeleteFile(ctx, "spoke-a", "clusters/spoke-a/instances/kustomization.yaml", "delete"))
	_, _, missing, err = c.GetFile(ctx, "spoke-a", "clusters/spoke-a/instances/kustomization.yaml")
	require.NoError(t, err)
	assert.True(t, missing)
}

func TestListPathsRefusesATruncatedTree(t *testing.T) {
	fake := newFakeGitHub("percona-cloud")
	fake.truncatedTrees = true
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := ghclient.New("percona-cloud", "token").WithBaseURL(srv.URL)
	ctx := context.Background()
	require.NoError(t, c.EnsureRepo(ctx, "spoke-a"))
	require.NoError(t, c.PutFile(ctx, "spoke-a", "clusters/spoke-a/instances/a.yaml", "a", []byte("a: 1\n")))
	require.NoError(t, c.PutFile(ctx, "spoke-a", "clusters/spoke-a/instances/b.yaml", "b", []byte("b: 1\n")))

	_, err := c.ListPaths(ctx, "spoke-a", "clusters/spoke-a/instances/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncated")
}

func TestDeleteFilesRemovesManyPathsInOneCommit(t *testing.T) {
	fake := newFakeGitHub("percona-cloud")
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := ghclient.New("percona-cloud", "token").WithBaseURL(srv.URL)
	ctx := context.Background()
	require.NoError(t, c.EnsureRepo(ctx, "spoke-a"))

	require.NoError(t, c.PutFile(ctx, "spoke-a", "clusters/spoke-a/instances/a.yaml", "a", []byte("a: 1\n")))
	require.NoError(t, c.PutFile(ctx, "spoke-a", "clusters/spoke-a/instances/b.yaml", "b", []byte("b: 1\n")))
	require.NoError(t, c.PutFile(ctx, "spoke-a", "clusters/spoke-a/instances/kustomization.yaml", "k", []byte("kind: Kustomization\n")))

	require.NoError(t, c.DeleteFiles(ctx, "spoke-a", []string{
		"clusters/spoke-a/instances/a.yaml",
		"clusters/spoke-a/instances/b.yaml",
		"clusters/spoke-a/instances/kustomization.yaml",
	}, "disconnect spoke-a"))

	assert.Equal(t, 1, fake.gitCommits)
	assert.Equal(t, 0, fake.contentDeletes)

	paths, err := c.ListPaths(ctx, "spoke-a", "clusters/spoke-a/instances/")
	require.NoError(t, err)
	assert.Empty(t, paths)
}

func TestPutFilesWritesManyPathsInOneCommit(t *testing.T) {
	fake := newFakeGitHub("percona-cloud")
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := ghclient.New("percona-cloud", "token").WithBaseURL(srv.URL)
	ctx := context.Background()
	require.NoError(t, c.EnsureRepo(ctx, "spoke-a"))

	require.NoError(t, c.PutFiles(ctx, "spoke-a", map[string][]byte{
		"clusters/spoke-a/instances/a.yaml":             []byte("a: 1\n"),
		"clusters/spoke-a/instances/b.yaml":             []byte("b: 1\n"),
		"clusters/spoke-a/instances/kustomization.yaml": []byte("kind: Kustomization\n"),
	}, "seed"))

	assert.Equal(t, 1, fake.gitCommits)

	paths, err := c.ListPaths(ctx, "spoke-a", "clusters/spoke-a/instances/")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"clusters/spoke-a/instances/a.yaml",
		"clusters/spoke-a/instances/b.yaml",
		"clusters/spoke-a/instances/kustomization.yaml",
	}, paths)
}

func TestEnsureCloneKeyPostsReadOnlyDeployKey(t *testing.T) {
	fake := newFakeGitHub("percona-cloud")
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := ghclient.New("percona-cloud", "token").WithBaseURL(srv.URL)
	ctx := context.Background()

	identity, pub, err := c.EnsureCloneKey(ctx, "spoke-a")
	require.NoError(t, err)
	assert.Contains(t, identity, "BEGIN OPENSSH PRIVATE KEY")
	assert.True(t, strings.HasPrefix(pub, "ssh-ed25519 "))

	fake.mu.Lock()
	repo := fake.repos["spoke-a"]
	require.NotNil(t, repo)
	require.Len(t, repo.keys, 1)
	assert.Equal(t, "pds-flux", repo.keys[0].Title)
	assert.True(t, repo.keys[0].ReadOnly)
	assert.Equal(t, strings.TrimSpace(pub), repo.keys[0].Key)
	firstID := repo.keys[0].ID
	fake.mu.Unlock()

	_, pub2, err := c.EnsureCloneKey(ctx, "spoke-a")
	require.NoError(t, err)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, repo.keys, 2)
	assert.Equal(t, firstID, repo.keys[0].ID)
	assert.Equal(t, strings.TrimSpace(pub2), repo.keys[1].Key)
}

func TestEnsureCloneKeyRejectsUnverified422(t *testing.T) {
	fake := newFakeGitHub("percona-cloud")
	fake.deployKeyPostStatus = http.StatusUnprocessableEntity
	fake.skipDeployKeyInsert = true
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := ghclient.New("percona-cloud", "token").WithBaseURL(srv.URL)

	_, _, err := c.EnsureCloneKey(context.Background(), "spoke-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 422")

	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.Empty(t, fake.repos["spoke-a"].keys)
}

func TestEnsureCloneKeyAccepts422WhenTheKeyExists(t *testing.T) {
	fake := newFakeGitHub("percona-cloud")
	fake.deployKeyPostStatus = http.StatusUnprocessableEntity
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := ghclient.New("percona-cloud", "token").WithBaseURL(srv.URL)

	identity, pub, err := c.EnsureCloneKey(context.Background(), "spoke-a")
	require.NoError(t, err)
	assert.Contains(t, identity, "BEGIN OPENSSH PRIVATE KEY")

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.repos["spoke-a"].keys, 1)
	assert.Equal(t, strings.TrimSpace(pub), fake.repos["spoke-a"].keys[0].Key)
}

func TestEnsureDeployKeyIsIdempotent(t *testing.T) {
	fake := newFakeGitHub("percona-cloud")
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := ghclient.New("percona-cloud", "token").WithBaseURL(srv.URL)
	ctx := context.Background()

	_, pub, err := c.EnsureCloneKey(ctx, "spoke-a")
	require.NoError(t, err)
	require.NoError(t, c.EnsureDeployKey(ctx, "spoke-a", pub))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.repos["spoke-a"].keys, 1)
}

func TestEnsureRepoSelectsMainWhenOrgDefaultIsMaster(t *testing.T) {
	fake := newFakeGitHub("percona-cloud")
	fake.createDefaultBranch = "master"
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := ghclient.New("percona-cloud", "token").WithBaseURL(srv.URL)
	ctx := context.Background()

	require.NoError(t, c.EnsureRepo(ctx, "spoke-a"))
	require.NoError(t, c.PutFiles(ctx, "spoke-a", map[string][]byte{
		"clusters/spoke-a/instances/kustomization.yaml": []byte("kind: Kustomization\n"),
	}, "seed"))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	repo := fake.repos["spoke-a"]
	require.NotNil(t, repo)
	assert.Equal(t, "main", repo.defaultBranch)
	assert.Contains(t, repo.refs, "main")
	assert.Contains(t, repo.files, "clusters/spoke-a/instances/kustomization.yaml")
}

func TestEnsureRepoRejectsAnUnrelatedExistingRepo(t *testing.T) {
	fake := newFakeGitHub("percona-cloud")
	fake.repos["other"] = &fakeRepo{
		files:         map[string]string{"README.md": "ok"},
		shas:          map[string]string{"README.md": "sha-readme"},
		blobs:         map[string]string{},
		commitSHA:     "commit-0",
		treeSHA:       "tree-0",
		defaultBranch: "master",
		refs:          map[string]string{"master": "commit-0"},
	}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := ghclient.New("percona-cloud", "token").WithBaseURL(srv.URL)

	err := c.EnsureRepo(context.Background(), "other")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a hub environment repo")

	fake.mu.Lock()
	defer fake.mu.Unlock()
	repo := fake.repos["other"]
	require.NotNil(t, repo)
	assert.Equal(t, "master", repo.defaultBranch)
	assert.NotContains(t, repo.refs, "main")
}

func TestEnsureRepoAdoptsAnExistingHubTree(t *testing.T) {
	fake := newFakeGitHub("percona-cloud")
	fake.repos["spoke-a"] = &fakeRepo{
		files: map[string]string{
			"README.md": "ok",
			"clusters/spoke-a/instances/kustomization.yaml": "kind: Kustomization\n",
		},
		shas: map[string]string{
			"README.md": "sha-readme",
			"clusters/spoke-a/instances/kustomization.yaml": "sha-k",
		},
		blobs:         map[string]string{},
		commitSHA:     "commit-0",
		treeSHA:       "tree-0",
		defaultBranch: "master",
		refs:          map[string]string{"master": "commit-0"},
	}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := ghclient.New("percona-cloud", "token").WithBaseURL(srv.URL)

	require.NoError(t, c.EnsureRepo(context.Background(), "spoke-a"))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	repo := fake.repos["spoke-a"]
	require.NotNil(t, repo)
	assert.Equal(t, "main", repo.defaultBranch)
	assert.Contains(t, repo.refs, "main")
}

func TestRevokeCloneKeyDeletesTitledDeployKeys(t *testing.T) {
	fake := newFakeGitHub("percona-cloud")
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	c := ghclient.New("percona-cloud", "token").WithBaseURL(srv.URL)
	ctx := context.Background()

	_, _, err := c.EnsureCloneKey(ctx, "spoke-a")
	require.NoError(t, err)
	_, _, err = c.EnsureCloneKey(ctx, "spoke-a")
	require.NoError(t, err)

	fake.mu.Lock()
	fake.repos["spoke-a"].keys = append(fake.repos["spoke-a"].keys, fakeDeployKey{
		ID:    99,
		Title: "other",
		Key:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIother",
	}, fakeDeployKey{
		ID:    100,
		Title: "basecamp-flux", // allow-legacy-name
		Key:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIlegacy",
	})
	fake.mu.Unlock()

	require.NoError(t, c.RevokeCloneKey(ctx, "spoke-a"))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.repos["spoke-a"].keys, 1)
	assert.Equal(t, "other", fake.repos["spoke-a"].keys[0].Title)
}
