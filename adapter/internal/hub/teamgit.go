package hub

import (
	"context"
	"errors"
	"strconv"
	"sync"
)

// ErrRepoName means no unused repository name could be found for an
// environment.
var ErrRepoName = errors.New("no unused repository name was found")

// repoNameAttempts is how many names Assign tries: the plain one, then -2
// through -99.
const repoNameAttempts = 99

// RepoClaims gives each repository name one owner across every Team. The
// claim is the guard against two environments writing to one repository.
type RepoClaims interface {
	// ClaimRepo reports won when owner holds the name afterwards and fresh when
	// this call took it, as opposed to the owner already having it.
	ClaimRepo(ctx context.Context, repo, owner string) (won, fresh bool, err error)
	ReleaseRepo(ctx context.Context, repo, owner string) error
}

// repoChecker reports whether a GitHub repository already exists. GitFiles
// implementations that can answer this refuse a name somebody else already
// created; the rest take whatever the claims allow.
type repoChecker interface {
	HasRepo(ctx context.Context, repo string) (bool, error)
}

// TeamGit maps an environment name onto the GitHub repository that holds its
// desired-state tree. Paths inside the repo stay clusters/<environment>/.
//
// A new environment is named <organization>-<team>-<environment>. One that
// already has a repository on its record keeps it, whatever its Organization or
// Team is called later.
type TeamGit struct {
	files   GitFiles
	team    uint
	adopted sync.Map

	// namePrefix gives the organization-team half of a new repository's name.
	namePrefix func(ctx context.Context) (string, error)
	// recorded reads the repository stored on an environment's record.
	recorded func(environment string) string
	claims   RepoClaims
}

// NewTeamGit wraps files so writes for one Team hit that Team's repos.
func NewTeamGit(files GitFiles, team uint) *TeamGit {
	return &TeamGit{files: files, team: team}
}

// NameNewRepos makes Assign name a new environment's repository
// <organization>-<team>-<environment>. Without it a new environment is named
// after the environment alone.
func (t *TeamGit) NameNewRepos(prefix func(ctx context.Context) (string, error)) {
	t.namePrefix = prefix
}

// UseRecordedRepos makes an environment's stored repository the one it keeps,
// whatever its organization or team is called later.
func (t *TeamGit) UseRecordedRepos(recorded func(environment string) string) {
	t.recorded = recorded
}

// UseRepoClaims makes Assign take every name it hands out from the claims, so
// no two environments are ever given one repository.
func (t *TeamGit) UseRepoClaims(claims RepoClaims) {
	t.claims = claims
}

// Assign names the repository of an environment that is being connected for the
// first time. An environment that already has a repository is never given
// another name, so callers do not Assign one.
func (t *TeamGit) Assign(ctx context.Context, environment string) error {
	if t == nil || t.namePrefix == nil {
		return nil
	}
	prefix, err := t.namePrefix(ctx)
	if err != nil {
		return err
	}
	if prefix == "" {
		return nil
	}
	owner := strconv.FormatUint(uint64(t.team), 10) + "/" + environment
	base := prefix + "-" + environment
	for attempt := 1; attempt <= repoNameAttempts; attempt++ {
		name := base
		if attempt > 1 {
			name = base + "-" + strconv.Itoa(attempt)
		}
		ok, err := t.take(ctx, name, owner)
		if err != nil {
			return err
		}
		if ok {
			t.adopted.Store(environment, name)

			return nil
		}
	}

	return ErrRepoName
}

// take reports whether the environment may use the name. Another owner's claim
// refuses it. So does a repository that exists although nobody claimed it, which
// is one a release that predates claims created.
func (t *TeamGit) take(ctx context.Context, name, owner string) (bool, error) {
	fresh := true
	if t.claims != nil {
		won, wasFresh, err := t.claims.ClaimRepo(ctx, name, owner)
		if err != nil || !won {
			return false, err
		}
		fresh = wasFresh
	}
	if !fresh {
		return true, nil
	}

	exists, err := t.hasRepo(ctx, name)
	if err != nil {
		t.release(ctx, name, owner)

		return false, err
	}
	if exists {
		t.release(ctx, name, owner)

		return false, nil
	}

	return true, nil
}

func (t *TeamGit) release(ctx context.Context, name, owner string) {
	if t.claims != nil {
		_ = t.claims.ReleaseRepo(ctx, name, owner)
	}
}

// Repo is the GitHub repository name this environment currently uses.
func (t *TeamGit) Repo(environment string) string {
	if t == nil {
		return environment
	}
	if name, ok := t.known(environment); ok {
		return name
	}

	return environment
}

// EnsureRepo creates or adopts the repository for environment.
func (t *TeamGit) EnsureRepo(ctx context.Context, environment string) error {
	name := t.repoFor(environment)

	return t.files.EnsureRepo(ctx, name)
}

// GetFile reads one path from the repository that holds environment.
func (t *TeamGit) GetFile(ctx context.Context, environment, path string) ([]byte, string, bool, error) {
	name := t.repoFor(environment)

	return t.files.GetFile(ctx, name, path)
}

// ListPaths lists paths in the repository that holds environment.
func (t *TeamGit) ListPaths(ctx context.Context, environment, prefix string) ([]string, error) {
	name := t.repoFor(environment)

	return t.files.ListPaths(ctx, name, prefix)
}

// PutFile writes one path in the repository that holds environment.
func (t *TeamGit) PutFile(ctx context.Context, environment, path, message string, body []byte) error {
	name := t.repoFor(environment)

	return t.files.PutFile(ctx, name, path, message, body)
}

// PutFiles writes many paths in the repository that holds environment.
func (t *TeamGit) PutFiles(ctx context.Context, environment string, files map[string][]byte, message string) error {
	name := t.repoFor(environment)

	return t.files.PutFiles(ctx, name, files, message)
}

// DeleteFiles removes paths from the repository that holds environment.
func (t *TeamGit) DeleteFiles(ctx context.Context, environment string, paths []string, message string) error {
	name := t.repoFor(environment)

	return t.files.DeleteFiles(ctx, name, paths, message)
}

// EnsureCloneKey mints the read-only identity on the repository that holds
// environment.
func (t *TeamGit) EnsureCloneKey(ctx context.Context, environment string) (identity, pub string, err error) {
	host, ok := t.files.(GitHost)
	if !ok {
		return "", "", ErrGitHost
	}
	name := t.repoFor(environment)

	return host.EnsureCloneKey(ctx, name)
}

// EnsureDeployKey adds the public key to the repository that holds environment.
func (t *TeamGit) EnsureDeployKey(ctx context.Context, environment, pub string) error {
	host, ok := t.files.(GitHost)
	if !ok {
		return ErrGitHost
	}
	name := t.repoFor(environment)

	return host.EnsureDeployKey(ctx, name, pub)
}

// RevokeCloneKey removes the deploy key from the repository that holds environment.
func (t *TeamGit) RevokeCloneKey(ctx context.Context, environment string) error {
	host, ok := t.files.(GitHost)
	if !ok {
		return ErrGitHost
	}
	name := t.repoFor(environment)

	return host.RevokeCloneKey(ctx, name)
}

// repoFor is the repository an environment's files live in, settling it on the
// environment's own name when nothing named it.
func (t *TeamGit) repoFor(environment string) string {
	if t == nil {
		return environment
	}
	if name, ok := t.known(environment); ok {
		return name
	}
	t.adopted.Store(environment, environment)

	return environment
}

// known is the repository already settled for an environment: the one just
// assigned or adopted, else the one on its record.
func (t *TeamGit) known(environment string) (string, bool) {
	if name, ok := t.adopted.Load(environment); ok {
		if repo, ok := name.(string); ok && repo != "" {
			return repo, true
		}
	}
	if t.recorded != nil {
		if repo := t.recorded(environment); repo != "" {
			return repo, true
		}
	}

	return "", false
}

func (t *TeamGit) hasRepo(ctx context.Context, repo string) (bool, error) {
	checker, ok := t.files.(repoChecker)
	if !ok {
		return false, nil
	}

	return checker.HasRepo(ctx, repo)
}

var (
	_ GitFiles = (*TeamGit)(nil)
	_ GitHost  = (*TeamGit)(nil)
)
