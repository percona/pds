package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

func TestBackupObservedInstanceEnqueuesWork(t *testing.T) {
	svc, store, inv := newHub(t)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	id := "spoke-a/pds.dev-testing"
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          now,
		Instances: []engine.Instance{{
			ID: id, Name: "dev-testing", Namespace: "pds", Environment: "spoke-a",
			Status: engine.Status{Phase: engine.PhaseReady},
		}},
	})

	backup, err := svc.Backup(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, engine.BackupPending, backup.Status)

	_, err = store.Get(context.Background(), "clusters/spoke-a/backups/"+backup.ID+".yaml")
	require.ErrorIs(t, err, hub.ErrNotFound)

	work := svc.TakeWork("spoke-a")
	require.Len(t, work, 1)
	assert.Equal(t, hub.WorkBackup, work[0].Kind)
	assert.Equal(t, "pds.dev-testing", work[0].Database)
	assert.Equal(t, backup.ID, work[0].BackupID)
}

func TestScaleObservedInstanceEnqueuesWork(t *testing.T) {
	svc, _, inv := newHub(t)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	id := "spoke-a/pds.dev-testing"
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{{
			ID: id, Name: "dev-testing", Namespace: "pds", Environment: "spoke-a",
		}},
	})
	cpu := 2

	require.NoError(t, svc.Scale(context.Background(), id, engine.ScaleInput{CPUCores: &cpu}))
	work := svc.TakeWork("spoke-a")
	require.Len(t, work, 1)
	assert.Equal(t, hub.WorkScale, work[0].Kind)
	require.NotNil(t, work[0].CPUCores)
	assert.Equal(t, 2, *work[0].CPUCores)
}

func TestIssueJoinSeedsEnvironmentMetadataAndTheInstancesDir(t *testing.T) {
	svc, store, _ := newHub(t)
	token, err := svc.IssueJoin(context.Background(), "test-lucas")
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	body, err := store.Get(context.Background(), "clusters/test-lucas/cluster.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), "name: test-lucas")
	assert.Contains(t, string(body), "namespace: pds")

	body, err = store.Get(context.Background(), "clusters/test-lucas/instances/kustomization.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), "kind: Kustomization")

	// Backups and restores travel the work queue, so a spoke that reconciles
	// a Git tree for them is reconciling something that is always empty.
	for _, path := range []string{
		"clusters/test-lucas/backups/kustomization.yaml",
		"clusters/test-lucas/restores/kustomization.yaml",
	} {
		_, getErr := store.Get(context.Background(), path)
		require.Error(t, getErr, path)
	}
}

func TestIssueJoinRejectsBadName(t *testing.T) {
	svc, _, _ := newHub(t)
	_, err := svc.IssueJoin(context.Background(), "EKS")
	require.ErrorIs(t, err, hub.ErrInvalidEnvironment)
}

func TestIssueJoinRejectsReservedName(t *testing.T) {
	svc, _, _ := newHub(t)
	for _, name := range []string{"connect", "new"} {
		_, err := svc.IssueJoin(context.Background(), name)
		require.ErrorIs(t, err, hub.ErrInvalidEnvironment, name)
	}
}

func TestIssueJoinRegistersUnreachableEnvironment(t *testing.T) {
	svc, _, inv := newHub(t)
	_, err := svc.IssueJoin(context.Background(), "test-lucas")
	require.NoError(t, err)

	got, ok := inv.Get("test-lucas", time.Now())
	require.True(t, ok)
	assert.Equal(t, hub.StatusUnreachable, got.Status)
	assert.Equal(t, "pds", got.Namespace)
}

type joinHost struct {
	names []string
	err   error
}

func (h *joinHost) EnsureRepo(_ context.Context, environment string) error {
	h.names = append(h.names, environment)
	return h.err
}

func (h *joinHost) EnsureCloneKey(_ context.Context, _ string) (string, string, error) {
	return "-----BEGIN OPENSSH PRIVATE KEY-----\ntest\n-----END OPENSSH PRIVATE KEY-----\n",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItesthub pds-flux\n",
		h.err
}

func (h *joinHost) EnsureDeployKey(context.Context, string, string) error {
	return h.err
}

func (h *joinHost) RevokeCloneKey(context.Context, string) error {
	return h.err
}

func TestIssueJoinEnsuresGitHubRepo(t *testing.T) {
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	host := &joinHost{}
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		GitHost:       host,
	})

	_, err := svc.IssueJoin(context.Background(), "test-lucas")
	require.NoError(t, err)
	assert.Equal(t, []string{"test-lucas"}, host.names)
}

func TestIssueJoinGitHostError(t *testing.T) {
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	svc := hub.NewService(store, inv, hub.Options{
		Namespace: "pds",
		GitHost:   &joinHost{err: hub.ErrGitHost},
	})

	_, err := svc.IssueJoin(context.Background(), "test-lucas")
	require.ErrorIs(t, err, hub.ErrGitHost)
}

type cloneHost struct {
	minted  int
	ensured int
	revoked int
	err     error
}

func (h *cloneHost) EnsureRepo(context.Context, string) error { return h.err }

func (h *cloneHost) EnsureCloneKey(context.Context, string) (string, string, error) {
	h.minted++
	return "-----BEGIN OPENSSH PRIVATE KEY-----\ntest\n-----END OPENSSH PRIVATE KEY-----\n",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItesthub pds-flux\n",
		h.err
}

func (h *cloneHost) EnsureDeployKey(context.Context, string, string) error {
	h.ensured++
	return h.err
}

func (h *cloneHost) RevokeCloneKey(context.Context, string) error {
	h.revoked++
	return h.err
}

func TestCloneIdentityReusesAStoredKey(t *testing.T) {
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	host := &cloneHost{}
	svc := hub.NewService(store, inv, hub.Options{
		Namespace: "pds",
		GitHost:   host,
	})

	firstID, firstPub, err := svc.CloneIdentity(context.Background(), "spoke-a")
	require.NoError(t, err)
	secondID, secondPub, err := svc.CloneIdentity(context.Background(), "spoke-a")
	require.NoError(t, err)

	assert.Equal(t, firstID, secondID)
	assert.Equal(t, firstPub, secondPub)
	assert.Equal(t, 1, host.minted)
	assert.Equal(t, 1, host.ensured)
}

func TestCloneIdentityLoadsAPersistedKey(t *testing.T) {
	kv := &memKV{}
	persist := hub.NewSecretCloneKeyPersist(kv, "everest-system", "pds-join-tokens")
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	first := hub.NewService(store, inv, hub.Options{
		Namespace: "pds",
		GitHost:   &cloneHost{},
		CloneKeys: persist,
	})
	identity, pub, err := first.CloneIdentity(context.Background(), "spoke-a")
	require.NoError(t, err)

	host := &cloneHost{}
	second := hub.NewService(store, inv, hub.Options{
		Namespace: "pds",
		GitHost:   host,
		CloneKeys: persist,
	})
	require.NoError(t, second.LoadCloneKeys(context.Background()))
	gotID, gotPub, err := second.CloneIdentity(context.Background(), "spoke-a")
	require.NoError(t, err)
	assert.Equal(t, identity, gotID)
	assert.Equal(t, pub, gotPub)
	assert.Equal(t, 0, host.minted)
	assert.Equal(t, 1, host.ensured)
}

func TestDisconnectRevokesTheCloneKey(t *testing.T) {
	kv := &memKV{}
	persist := hub.NewSecretCloneKeyPersist(kv, "everest-system", "pds-join-tokens")
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	host := &cloneHost{}
	svc := hub.NewService(store, inv, hub.Options{
		Namespace: "pds",
		GitHost:   host,
		CloneKeys: persist,
	})
	ctx := context.Background()

	_, err := svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)
	_, _, err = svc.CloneIdentity(ctx, "spoke-a")
	require.NoError(t, err)
	assert.Equal(t, 1, host.minted)

	require.NoError(t, disconnect(ctx, svc, "spoke-a"))
	assert.Equal(t, 1, host.revoked)

	loaded, err := persist.Load(ctx)
	require.NoError(t, err)
	assert.NotContains(t, loaded, "spoke-a")

	_, _, err = svc.CloneIdentity(ctx, "spoke-a")
	require.NoError(t, err)
	assert.Equal(t, 2, host.minted)
}

func TestCloneIdentityRetriesPersistWithoutMintingAnotherKey(t *testing.T) {
	ctx := context.Background()
	inner := hub.NewSecretCloneKeyPersist(&memKV{}, "everest-system", "pds-join-tokens")
	persist := &failFirstCloneSave{inner: inner}
	host := &cloneHost{}
	svc := hub.NewService(hub.NewMemoryStore(), hub.NewInventory(time.Minute), hub.Options{
		Namespace: "pds",
		GitHost:   host,
		CloneKeys: persist,
	})

	_, _, err := svc.CloneIdentity(ctx, "spoke-a")
	require.ErrorIs(t, err, hub.ErrPersist)
	assert.Equal(t, 1, host.minted)

	identity, pub, err := svc.CloneIdentity(ctx, "spoke-a")
	require.NoError(t, err)
	assert.Equal(t, 1, host.minted, "a failed save must retry persist instead of minting another deploy key")
	assert.NotEmpty(t, identity)
	assert.NotEmpty(t, pub)

	loaded, err := inner.Load(ctx)
	require.NoError(t, err)
	require.Contains(t, loaded, "spoke-a")
	assert.Equal(t, identity, loaded["spoke-a"].Identity)
}

type failFirstCloneSave struct {
	inner hub.CloneKeyPersist
	calls int
}

func (f *failFirstCloneSave) Load(ctx context.Context) (map[string]hub.CloneKey, error) {
	return f.inner.Load(ctx)
}

func (f *failFirstCloneSave) Save(ctx context.Context, environment string, key hub.CloneKey) error {
	f.calls++
	if f.calls == 1 {
		return kube.ErrForbidden
	}

	return f.inner.Save(ctx, environment, key)
}

func (f *failFirstCloneSave) Remove(ctx context.Context, environment string) error {
	return f.inner.Remove(ctx, environment)
}

func TestEnrollRejectsARevokedToken(t *testing.T) {
	ctx := context.Background()
	host := &cloneHost{}
	svc := hub.NewService(hub.NewMemoryStore(), hub.NewInventory(time.Minute), hub.Options{
		Namespace: "pds",
		GitHost:   host,
	})
	token, err := svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)
	require.NoError(t, disconnect(ctx, svc, "spoke-a"))

	_, _, _, _, err = svc.Enroll(ctx, token, true)
	require.ErrorIs(t, err, hub.ErrJoinToken)
	assert.Equal(t, 0, host.minted, "a revoked token must not mint a deploy key")
}

func TestEnrollHoldsTheHubLockAcrossClone(t *testing.T) {
	ctx := context.Background()
	host := &blockingCloneHost{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	svc := hub.NewService(hub.NewMemoryStore(), hub.NewInventory(time.Minute), hub.Options{
		Namespace: "pds",
		GitHost:   host,
	})
	token, err := svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)

	enrollErr := make(chan error, 1)
	go func() {
		_, _, _, _, err := svc.Enroll(ctx, token, true)
		enrollErr <- err
	}()
	<-host.entered

	disconnected := make(chan error, 1)
	go func() {
		disconnected <- disconnect(ctx, svc, "spoke-a")
	}()
	select {
	case err := <-disconnected:
		t.Fatalf("disconnect returned while enroll held the hub lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(host.release)
	require.NoError(t, <-enrollErr)
	require.NoError(t, <-disconnected)
	assert.Equal(t, 1, host.minted)
	assert.Equal(t, 1, host.revoked)
}

type blockingCloneHost struct {
	entered chan struct{}
	release chan struct{}
	minted  int
	revoked int
}

func (h *blockingCloneHost) EnsureRepo(context.Context, string) error { return nil }

func (h *blockingCloneHost) EnsureCloneKey(context.Context, string) (string, string, error) {
	close(h.entered)
	<-h.release
	h.minted++
	return "-----BEGIN OPENSSH PRIVATE KEY-----\ntest\n-----END OPENSSH PRIVATE KEY-----\n",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItesthub pds-flux\n",
		nil
}

func (h *blockingCloneHost) EnsureDeployKey(context.Context, string, string) error {
	return nil
}

func (h *blockingCloneHost) RevokeCloneKey(context.Context, string) error {
	h.revoked++
	return nil
}

func newTeamGitHub(t *testing.T, prefix *string, inv *hub.Inventory) (*hub.Service, *memFiles) {
	t.Helper()
	host := &memFiles{}
	files := hub.NewTeamGit(host, 1)
	files.NameNewRepos(func(context.Context) (string, error) { return *prefix, nil })
	files.UseRecordedRepos(inv.Repo)
	svc := hub.NewService(hub.NewGitHubStore(files), inv, hub.Options{
		Namespace: "pds",
		GitHost:   files,
	})

	return svc, host
}

func TestIssueJoinStoresTheNewRepoNameOnTheRecord(t *testing.T) {
	prefix := "acme-platform"
	inv := hub.NewInventory(time.Minute)
	svc, host := newTeamGitHub(t, &prefix, inv)

	_, err := svc.IssueJoin(context.Background(), "prod")
	require.NoError(t, err)

	assert.Equal(t, "acme-platform-prod", inv.Repo("prod"))
	assert.Equal(t, "acme-platform-prod", svc.GitRepo("prod"))
	assert.Contains(t, host.ensured, "acme-platform-prod")
}

func TestIssueJoinAgainKeepsTheRepoAfterATeamRename(t *testing.T) {
	prefix := "acme-platform"
	inv := hub.NewInventory(time.Minute)
	svc, host := newTeamGitHub(t, &prefix, inv)
	_, err := svc.IssueJoin(context.Background(), "prod")
	require.NoError(t, err)

	prefix = "acme-renamed"
	_, err = svc.IssueJoin(context.Background(), "prod")
	require.NoError(t, err)

	assert.Equal(t, "acme-platform-prod", inv.Repo("prod"))
	assert.NotContains(t, host.ensured, "acme-renamed-prod")
}

func TestIssueJoinAgainRecordsTheEnvironmentNameForARecordWithoutARepo(t *testing.T) {
	prefix := "acme-platform"
	inv := hub.NewInventory(time.Minute)
	inv.Register("prod", "pds")
	svc, host := newTeamGitHub(t, &prefix, inv)

	_, err := svc.IssueJoin(context.Background(), "prod")
	require.NoError(t, err)

	assert.Equal(t, "prod", inv.Repo("prod"), "an environment with no recorded repo is named after itself")
	assert.NotContains(t, host.ensured, "acme-platform-prod")
}

func TestIssueJoinGivesTwoTeamsWithTheSameNamesDifferentRepos(t *testing.T) {
	claims := &memClaims{}
	host := &memFiles{}
	join := func(team uint) string {
		inv := hub.NewInventory(time.Minute)
		files := claimingTeamGit(host, claims, team, "acme-web")
		files.UseRecordedRepos(inv.Repo)
		svc := hub.NewService(hub.NewGitHubStore(files), inv, hub.Options{Namespace: "pds", GitHost: files})
		_, err := svc.IssueJoin(context.Background(), "prod")
		require.NoError(t, err)

		return inv.Repo("prod")
	}

	first := join(1)
	second := join(2)

	assert.Equal(t, "acme-web-prod", first)
	assert.Equal(t, "acme-web-prod-2", second)
}
