package github

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const defaultAPI = "https://api.github.com"

// hubRepoDescription marks repos this hub created. EnsureRepo will not
// rewrite branches on an org repo that does not carry this marker or a
// clusters/<name>/ tree from a previous hub.
const hubRepoDescription = "pds hub environment desired state"

// Client talks to the GitHub REST API as one org. The hub uses it to create
// one private repo per environment and to commit Flux desired state.
type Client struct {
	http    *http.Client
	baseURL string
	org     string
	token   string
	app     *appAuth
}

// New returns a Client for org authenticated with token.
func New(org, token string) *Client {
	return &Client{
		http:    &http.Client{Timeout: 30 * time.Second},
		baseURL: defaultAPI,
		org:     strings.TrimSpace(org),
		token:   token,
	}
}

// NewApp returns a Client for org authenticated as a GitHub App installation.
// Installation tokens are exchanged on demand and renewed before they expire.
func NewApp(org, appID, installationID string, key *rsa.PrivateKey) *Client {
	c := New(org, "")
	c.app = &appAuth{appID: appID, installationID: installationID, key: key, now: time.Now}

	return c
}

// WithClock replaces the clock the App token cache reads, for tests.
func (c *Client) WithClock(now func() time.Time) *Client {
	if c.app != nil {
		c.app.now = now
	}

	return c
}

// WithBaseURL points the client at a fake GitHub, for tests.
func (c *Client) WithBaseURL(base string) *Client {
	c.baseURL = strings.TrimRight(strings.TrimSpace(base), "/")
	return c
}

// WithHTTP replaces the HTTP client.
func (c *Client) WithHTTP(h *http.Client) *Client {
	if h != nil {
		c.http = h
	}
	return c
}

// Org is the GitHub organization repos are created in.
func (c *Client) Org() string { return c.org }

// Token is the org credential the hub uses against the GitHub API: the static
// token, or the current installation token for an App. It is not sent to
// environments.
func (c *Client) Token(ctx context.Context) (string, error) {
	if c.app == nil {
		return c.token, nil
	}

	return c.app.current(ctx, c, "")
}

// CloneURL is the HTTPS remote for this repository.
func (c *Client) CloneURL(repo string) string {
	return "https://github.com/" + c.org + "/" + repo + ".git"
}

// SSHCloneURL is the SSH remote Flux clones for this repository.
func (c *Client) SSHCloneURL(repo string) string {
	return "ssh://git@github.com/" + c.org + "/" + strings.TrimSpace(repo) + ".git"
}

// HasRepo reports whether the org already has a repository of this name.
func (c *Client) HasRepo(ctx context.Context, repo string) (bool, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return false, fmt.Errorf("environment name is empty")
	}
	status, _, err := c.do(ctx, http.MethodGet, "/repos/"+c.org+"/"+repo, nil)
	if err != nil {
		return false, err
	}
	if status == http.StatusOK {
		return true, nil
	}
	if status == http.StatusNotFound {
		return false, nil
	}

	return false, fmt.Errorf("github get repo %s/%s: HTTP %d", c.org, repo, status)
}

// EnsureRepo creates a private repo named environment if it is missing. An
// existing repo is adopted only when this hub created it or it already
// holds a clusters/<name>/ tree; otherwise EnsureRepo leaves it alone.
func (c *Client) EnsureRepo(ctx context.Context, environment string) error {
	environment = strings.TrimSpace(environment)
	if environment == "" {
		return fmt.Errorf("environment name is empty")
	}
	status, raw, err := c.do(ctx, http.MethodGet, "/repos/"+c.org+"/"+environment, nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return c.adoptExistingRepo(ctx, environment, raw)
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("github get repo %s/%s: HTTP %d", c.org, environment, status)
	}
	body, err := json.Marshal(map[string]any{
		"name":        environment,
		"description": hubRepoDescription,
		"private":     true,
		"auto_init":   true,
		"has_issues":  false,
		"has_wiki":    false,
	})
	if err != nil {
		return err
	}
	status, raw, err = c.do(ctx, http.MethodPost, "/orgs/"+c.org+"/repos", body)
	if err != nil {
		return err
	}
	if status == http.StatusCreated || status == http.StatusOK {
		return c.ensureMain(ctx, environment)
	}
	if status == http.StatusUnprocessableEntity {
		// Created between the GET and POST, or the name is taken.
		status, raw, err = c.do(ctx, http.MethodGet, "/repos/"+c.org+"/"+environment, nil)
		if err != nil {
			return err
		}
		if status == http.StatusOK {
			return c.adoptExistingRepo(ctx, environment, raw)
		}
	}

	return fmt.Errorf("github create repo %s/%s: HTTP %d: %s", c.org, environment, status, truncate(raw, 200))
}

func (c *Client) adoptExistingRepo(ctx context.Context, environment string, raw []byte) error {
	owned, err := c.hubOwnsRepo(ctx, environment, raw)
	if err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("github repo %s/%s exists and is not a hub environment repo", c.org, environment)
	}

	return c.ensureMain(ctx, environment)
}

func (c *Client) hubOwnsRepo(ctx context.Context, environment string, raw []byte) (bool, error) {
	var info struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return false, err
	}
	if strings.TrimSpace(info.Description) == hubRepoDescription {
		return true, nil
	}
	paths, err := c.ListPaths(ctx, environment, "clusters/"+environment+"/")
	if err != nil {
		return false, err
	}

	return len(paths) > 0, nil
}

// ensureMain makes sure the repo has a main branch and that GitHub uses it as
// the default. auto_init follows the org default, which is often master, and
// every later write plus the Flux GitRepository pin main.
func (c *Client) ensureMain(ctx context.Context, repo string) error {
	status, raw, err := c.do(ctx, http.MethodGet, "/repos/"+c.org+"/"+repo, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("github get repo %s/%s: HTTP %d: %s", c.org, repo, status, truncate(raw, 200))
	}
	var info struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return err
	}
	defaultBranch := strings.TrimSpace(info.DefaultBranch)
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	_, mainOK, err := c.refSHA(ctx, repo, "main")
	if err != nil {
		return err
	}
	if defaultBranch == "main" && mainOK {
		return nil
	}
	if !mainOK {
		if defaultBranch == "main" {
			return fmt.Errorf("github ref %s: main is missing", repo)
		}
		sha, ok, err := c.refSHA(ctx, repo, defaultBranch)
		if err != nil {
			return err
		}
		if !ok || sha == "" {
			return fmt.Errorf("github ref %s: %s is missing", repo, defaultBranch)
		}
		if err := c.createRef(ctx, repo, "main", sha); err != nil {
			return err
		}
	}
	if defaultBranch == "main" {
		return nil
	}

	return c.setDefaultBranch(ctx, repo, "main")
}

func (c *Client) refSHA(ctx context.Context, repo, branch string) (string, bool, error) {
	status, raw, err := c.do(ctx, http.MethodGet, "/repos/"+c.org+"/"+repo+"/git/ref/heads/"+url.PathEscape(branch), nil)
	if err != nil {
		return "", false, err
	}
	if status == http.StatusNotFound {
		return "", false, nil
	}
	if status != http.StatusOK {
		return "", false, fmt.Errorf("github ref %s/%s: HTTP %d: %s", repo, branch, status, truncate(raw, 200))
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		return "", false, err
	}
	if ref.Object.SHA == "" {
		return "", false, nil
	}

	return ref.Object.SHA, true, nil
}

func (c *Client) createRef(ctx context.Context, repo, branch, sha string) error {
	payload, err := json.Marshal(map[string]string{
		"ref": "refs/heads/" + branch,
		"sha": sha,
	})
	if err != nil {
		return err
	}
	status, raw, err := c.do(ctx, http.MethodPost, "/repos/"+c.org+"/"+repo+"/git/refs", payload)
	if err != nil {
		return err
	}
	if status == http.StatusCreated || status == http.StatusOK || status == http.StatusUnprocessableEntity {
		return nil
	}

	return fmt.Errorf("github create ref %s/%s: HTTP %d: %s", repo, branch, status, truncate(raw, 200))
}

func (c *Client) setDefaultBranch(ctx context.Context, repo, branch string) error {
	payload, err := json.Marshal(map[string]string{"default_branch": branch})
	if err != nil {
		return err
	}
	status, raw, err := c.do(ctx, http.MethodPatch, "/repos/"+c.org+"/"+repo, payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("github default branch %s: HTTP %d: %s", repo, status, truncate(raw, 200))
	}

	return nil
}

// GetFile returns the contents of path on main. missing is true when GitHub
// has no such file.
func (c *Client) GetFile(ctx context.Context, repo, path string) (body []byte, sha string, missing bool, err error) {
	status, raw, err := c.do(ctx, http.MethodGet, "/repos/"+c.org+"/"+repo+"/contents/"+encodePath(path)+"?ref=main", nil)
	if err != nil {
		return nil, "", false, err
	}
	if status == http.StatusNotFound {
		return nil, "", true, nil
	}
	if status != http.StatusOK {
		return nil, "", false, fmt.Errorf("github get %s/%s: HTTP %d", repo, path, status)
	}
	var out struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, "", false, err
	}
	data := []byte(out.Content)
	if out.Encoding == "base64" {
		data, err = base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
		if err != nil {
			return nil, "", false, err
		}
	}

	return data, out.SHA, false, nil
}

// ListPaths returns file paths under prefix on main. A truncated GitHub tree
// is an error: treating it as complete lets rewriteKustomize commit an index
// that omits existing instances, and Flux then prunes them.
func (c *Client) ListPaths(ctx context.Context, repo, prefix string) ([]string, error) {
	status, raw, err := c.do(ctx, http.MethodGet, "/repos/"+c.org+"/"+repo+"/git/trees/main?recursive=1", nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound || status == http.StatusConflict {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("github list %s: HTTP %d", repo, status)
	}
	var out struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out.Truncated {
		return nil, fmt.Errorf("github list %s: tree response is truncated", repo)
	}
	prefix = strings.TrimLeft(prefix, "/")
	var paths []string
	for _, node := range out.Tree {
		if node.Type != "blob" {
			continue
		}
		if prefix == "" || node.Path == prefix || strings.HasPrefix(node.Path, strings.TrimSuffix(prefix, "/")+"/") {
			paths = append(paths, node.Path)
		}
	}

	return paths, nil
}

// PutFile creates or replaces path on main.
func (c *Client) PutFile(ctx context.Context, repo, path, message string, body []byte) error {
	_, sha, missing, err := c.GetFile(ctx, repo, path)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(body),
		"branch":  "main",
	}
	if !missing && sha != "" {
		payload["sha"] = sha
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	status, resp, err := c.do(ctx, http.MethodPut, "/repos/"+c.org+"/"+repo+"/contents/"+encodePath(path), raw)
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusCreated {
		return nil
	}

	return fmt.Errorf("github put %s/%s: HTTP %d: %s", repo, path, status, truncate(resp, 200))
}

// DeleteFile removes path on main. Missing files are a no-op.
func (c *Client) DeleteFile(ctx context.Context, repo, path, message string) error {
	_, sha, missing, err := c.GetFile(ctx, repo, path)
	if err != nil {
		return err
	}
	if missing || sha == "" {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"message": message,
		"sha":     sha,
		"branch":  "main",
	})
	if err != nil {
		return err
	}
	status, resp, err := c.do(ctx, http.MethodDelete, "/repos/"+c.org+"/"+repo+"/contents/"+encodePath(path), payload)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}

	return fmt.Errorf("github delete %s/%s: HTTP %d: %s", repo, path, status, truncate(resp, 200))
}

// DeleteFiles removes paths on main in one commit. Missing paths are skipped.
// An empty or uninitialized repo is a no-op.
func (c *Client) DeleteFiles(ctx context.Context, repo string, paths []string, message string) error {
	paths = uniquePaths(paths)
	if len(paths) == 0 {
		return nil
	}
	status, raw, err := c.do(ctx, http.MethodGet, "/repos/"+c.org+"/"+repo+"/git/ref/heads/main", nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound || status == http.StatusConflict {
		return nil
	}
	if status != http.StatusOK {
		return fmt.Errorf("github ref %s: HTTP %d: %s", repo, status, truncate(raw, 200))
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		return err
	}
	if ref.Object.SHA == "" {
		return nil
	}

	status, raw, err = c.do(ctx, http.MethodGet, "/repos/"+c.org+"/"+repo+"/git/commits/"+ref.Object.SHA, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("github commit %s: HTTP %d: %s", repo, status, truncate(raw, 200))
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(raw, &commit); err != nil {
		return err
	}
	if commit.Tree.SHA == "" {
		return nil
	}

	existing, err := c.ListPaths(ctx, repo, "")
	if err != nil {
		return err
	}
	present := map[string]struct{}{}
	for _, path := range existing {
		present[path] = struct{}{}
	}
	type treeEntry struct {
		Path string  `json:"path"`
		Mode string  `json:"mode"`
		Type string  `json:"type"`
		SHA  *string `json:"sha"`
	}
	var tree []treeEntry
	for _, path := range paths {
		if _, ok := present[path]; !ok {
			continue
		}
		tree = append(tree, treeEntry{Path: path, Mode: "100644", Type: "blob"})
	}
	if len(tree) == 0 {
		return nil
	}

	payload, err := json.Marshal(map[string]any{
		"base_tree": commit.Tree.SHA,
		"tree":      tree,
	})
	if err != nil {
		return err
	}
	status, raw, err = c.do(ctx, http.MethodPost, "/repos/"+c.org+"/"+repo+"/git/trees", payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("github tree %s: HTTP %d: %s", repo, status, truncate(raw, 200))
	}
	var newTree struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(raw, &newTree); err != nil {
		return err
	}

	if message == "" {
		message = "delete"
	}
	payload, err = json.Marshal(map[string]any{
		"message": message,
		"tree":    newTree.SHA,
		"parents": []string{ref.Object.SHA},
	})
	if err != nil {
		return err
	}
	status, raw, err = c.do(ctx, http.MethodPost, "/repos/"+c.org+"/"+repo+"/git/commits", payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("github commit %s: HTTP %d: %s", repo, status, truncate(raw, 200))
	}
	var newCommit struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(raw, &newCommit); err != nil {
		return err
	}

	payload, err = json.Marshal(map[string]any{"sha": newCommit.SHA})
	if err != nil {
		return err
	}
	status, raw, err = c.do(ctx, http.MethodPatch, "/repos/"+c.org+"/"+repo+"/git/refs/heads/main", payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("github update ref %s: HTTP %d: %s", repo, status, truncate(raw, 200))
	}

	return nil
}

// PutFiles creates or replaces paths on main in one commit. An empty map is a
// no-op. The repo must already have a main branch, which EnsureRepo creates.
func (c *Client) PutFiles(ctx context.Context, repo string, files map[string][]byte, message string) error {
	paths := make([]string, 0, len(files))
	byPath := map[string][]byte{}
	for path, body := range files {
		path = strings.TrimPrefix(path, "/")
		if path == "" {
			continue
		}
		if _, ok := byPath[path]; ok {
			continue
		}
		byPath[path] = body
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil
	}

	status, raw, err := c.do(ctx, http.MethodGet, "/repos/"+c.org+"/"+repo+"/git/ref/heads/main", nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound || status == http.StatusConflict {
		return fmt.Errorf("github ref %s: main is missing", repo)
	}
	if status != http.StatusOK {
		return fmt.Errorf("github ref %s: HTTP %d: %s", repo, status, truncate(raw, 200))
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		return err
	}
	if ref.Object.SHA == "" {
		return fmt.Errorf("github ref %s: main is missing", repo)
	}

	status, raw, err = c.do(ctx, http.MethodGet, "/repos/"+c.org+"/"+repo+"/git/commits/"+ref.Object.SHA, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("github commit %s: HTTP %d: %s", repo, status, truncate(raw, 200))
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(raw, &commit); err != nil {
		return err
	}
	if commit.Tree.SHA == "" {
		return fmt.Errorf("github commit %s: tree is missing", repo)
	}

	type treeEntry struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	var tree []treeEntry
	for _, path := range paths {
		payload, err := json.Marshal(map[string]any{
			"content":  base64.StdEncoding.EncodeToString(byPath[path]),
			"encoding": "base64",
		})
		if err != nil {
			return err
		}
		status, raw, err = c.do(ctx, http.MethodPost, "/repos/"+c.org+"/"+repo+"/git/blobs", payload)
		if err != nil {
			return err
		}
		if status != http.StatusOK && status != http.StatusCreated {
			return fmt.Errorf("github blob %s/%s: HTTP %d: %s", repo, path, status, truncate(raw, 200))
		}
		var blob struct {
			SHA string `json:"sha"`
		}
		if err := json.Unmarshal(raw, &blob); err != nil {
			return err
		}
		if blob.SHA == "" {
			return fmt.Errorf("github blob %s/%s: sha is missing", repo, path)
		}
		tree = append(tree, treeEntry{Path: path, Mode: "100644", Type: "blob", SHA: blob.SHA})
	}

	payload, err := json.Marshal(map[string]any{
		"base_tree": commit.Tree.SHA,
		"tree":      tree,
	})
	if err != nil {
		return err
	}
	status, raw, err = c.do(ctx, http.MethodPost, "/repos/"+c.org+"/"+repo+"/git/trees", payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("github tree %s: HTTP %d: %s", repo, status, truncate(raw, 200))
	}
	var newTree struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(raw, &newTree); err != nil {
		return err
	}

	if message == "" {
		message = "update"
	}
	payload, err = json.Marshal(map[string]any{
		"message": message,
		"tree":    newTree.SHA,
		"parents": []string{ref.Object.SHA},
	})
	if err != nil {
		return err
	}
	status, raw, err = c.do(ctx, http.MethodPost, "/repos/"+c.org+"/"+repo+"/git/commits", payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("github commit %s: HTTP %d: %s", repo, status, truncate(raw, 200))
	}
	var newCommit struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(raw, &newCommit); err != nil {
		return err
	}

	payload, err = json.Marshal(map[string]any{"sha": newCommit.SHA})
	if err != nil {
		return err
	}
	status, raw, err = c.do(ctx, http.MethodPatch, "/repos/"+c.org+"/"+repo+"/git/refs/heads/main", payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("github update ref %s: HTTP %d: %s", repo, status, truncate(raw, 200))
	}

	return nil
}

func uniquePaths(paths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimPrefix(path, "/")
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	sort.Strings(out)

	return out
}

func encodePath(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}

	return strings.Join(parts, "/")
}

// do sends one request. An App's cached token can be revoked before it
// expires, so a 401 drops it and the request is sent once more with a fresh one.
func (c *Client) do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	token, err := c.Token(ctx)
	if err != nil {
		return 0, nil, err
	}
	status, raw, err := c.send(ctx, method, path, body, token)
	if err != nil || status != http.StatusUnauthorized || c.app == nil {
		return status, raw, err
	}
	token, err = c.app.current(ctx, c, token)
	if err != nil {
		return 0, nil, err
	}

	return c.send(ctx, method, path, body, token)
}

func (c *Client) send(ctx context.Context, method, path string, body []byte, token string) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return res.StatusCode, nil, err
	}

	return res.StatusCode, raw, nil
}

func truncate(raw []byte, n int) string {
	s := strings.TrimSpace(string(raw))
	if len(s) <= n {
		return s
	}

	return s[:n]
}
