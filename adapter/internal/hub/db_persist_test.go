package hub_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

// fakeState stands in for the team scoped database. The store package has
// its own tests against a live server; these cover the mapping between the
// hub's types and the documents that go in the rows.
type fakeState struct {
	mu           sync.Mutex
	environments map[string]string
	tokens       map[string]string
	work         map[string]string
	cloneKeys    map[string]string
	failSave     bool
	saves        int
}

func newFakeState() *fakeState {
	return &fakeState{
		environments: map[string]string{},
		tokens:       map[string]string{},
		work:         map[string]string{},
		cloneKeys:    map[string]string{},
	}
}

func (f *fakeState) Environments(context.Context) (map[string]string, error) {
	return f.copy(f.environments), nil
}

func (f *fakeState) SaveEnvironment(_ context.Context, name, document string) error {
	return f.set(f.environments, name, document)
}

func (f *fakeState) RemoveEnvironment(_ context.Context, name string) error {
	return f.drop(f.environments, name)
}

func (f *fakeState) Tokens(context.Context) (map[string]string, error) {
	return f.copy(f.tokens), nil
}

func (f *fakeState) SaveToken(_ context.Context, hash, document string) error {
	return f.set(f.tokens, hash, document)
}

func (f *fakeState) RemoveTokens(_ context.Context, hashes []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, hash := range hashes {
		delete(f.tokens, hash)
	}

	return nil
}

func (f *fakeState) Work(context.Context) (map[string]string, error) {
	return f.copy(f.work), nil
}

func (f *fakeState) SaveWork(_ context.Context, environment, document string) error {
	return f.set(f.work, environment, document)
}

func (f *fakeState) CloneKeys(context.Context) (map[string]string, error) {
	return f.copy(f.cloneKeys), nil
}

func (f *fakeState) SaveCloneKey(_ context.Context, environment, document string) error {
	return f.set(f.cloneKeys, environment, document)
}

func (f *fakeState) RemoveCloneKey(_ context.Context, environment string) error {
	return f.drop(f.cloneKeys, environment)
}

func (f *fakeState) copy(from map[string]string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(from))
	for key, value := range from {
		out[key] = value
	}

	return out
}

func (f *fakeState) set(into map[string]string, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSave {
		return errors.New("the database refused the write")
	}
	f.saves++
	into[key] = value

	return nil
}

func (f *fakeState) drop(from map[string]string, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(from, key)

	return nil
}

func TestDBInventoryPersistRoundTripsAnEnvironment(t *testing.T) {
	state := newFakeState()
	persist := hub.NewDBInventoryPersist(state)
	ctx := t.Context()

	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(persist)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment:   "eks-prod",
		Namespace:     "pds",
		BackupStorage: "s3-backups",
		At:            time.Now(),
		Instances:     []engine.Instance{{Name: "orders", Namespace: "pds", Engine: "mysql"}},
	})

	loaded := hub.NewInventory(time.Minute)
	loaded.SetPersist(persist)
	require.NoError(t, loaded.Load(ctx))

	got, ok := loaded.Get("eks-prod", time.Now())
	require.True(t, ok)
	assert.Equal(t, "pds", got.Namespace)
	assert.Equal(t, hub.StatusConnected, got.Status)
	assert.Equal(t, "s3-backups", loaded.BackupStorage("eks-prod"))

	beat, ok := loaded.Snapshot("eks-prod")
	require.True(t, ok)
	require.Len(t, beat.Instances, 1)
	assert.Equal(t, "orders", beat.Instances[0].Name)
}

func TestDBInventoryPersistRemovesAnEnvironment(t *testing.T) {
	state := newFakeState()
	ctx := t.Context()

	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewDBInventoryPersist(state))
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "eks-prod", Namespace: "pds", At: time.Now()})
	require.NoError(t, inv.Forget(ctx, "eks-prod"))

	stored, err := state.Environments(ctx)
	require.NoError(t, err)
	assert.Empty(t, stored)
}

func TestDBInventoryPersistStoresJSON(t *testing.T) {
	state := newFakeState()
	ctx := t.Context()

	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewDBInventoryPersist(state))
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "eks-prod", Namespace: "pds", At: time.Now()})

	stored, err := state.Environments(ctx)
	require.NoError(t, err)
	require.Contains(t, stored, "eks-prod")

	var document map[string]any
	require.NoError(t, json.Unmarshal([]byte(stored["eks-prod"]), &document))
	assert.Equal(t, "pds", document["namespace"])
	assert.Equal(t, true, document["seen"])
}

func TestDBInventoryPersistStoresNoPublicURI(t *testing.T) {
	state := newFakeState()
	ctx := t.Context()

	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewDBInventoryPersist(state))
	inv.ApplyHeartbeat(publicDatabaseBeat(time.Now()))

	stored, err := state.Environments(ctx)
	require.NoError(t, err)
	require.Contains(t, stored, "spoke-a")
	assert.NotContains(t, stored["spoke-a"], "hunter2")
}

func TestDBInventoryLoadDropsAURIAnEarlierReleaseStored(t *testing.T) {
	state := newFakeState()
	ctx := t.Context()
	legacy := `{"namespace":"pds","seen":true,"heartbeat":{"environment":"spoke-a","namespace":"pds",` +
		`"at":"2026-09-08T12:00:00Z","privateAccess":true,"instances":[{"Name":"orders-ext","Namespace":"pds",` +
		`"Exposure":{"External":true,"Address":"198.51.100.20"},` +
		`"Connection":{"Host":"198.51.100.20","Port":3306,"URI":"mysql://root:hunter2@198.51.100.20:3306"}}]}}`
	require.NoError(t, state.SaveEnvironment(ctx, "spoke-a", legacy))

	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewDBInventoryPersist(state))
	require.NoError(t, inv.Load(ctx))
	snap, ok := inv.Snapshot("spoke-a")
	require.True(t, ok)
	require.NotNil(t, snap.Instances[0].Connection)
	assert.Empty(t, snap.Instances[0].Connection.URI)
}

func TestDBTokenPersistStoresOnlyHashes(t *testing.T) {
	state := newFakeState()
	tokens := hub.NewTokenStore(hub.NewDBTokenPersist(state), time.Hour)
	ctx := t.Context()

	token, err := tokens.Issue(ctx, "eks-prod")
	require.NoError(t, err)
	require.NotEmpty(t, token)

	stored, err := state.Tokens(ctx)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	for hash, document := range stored {
		assert.Len(t, hash, 64, "the key is the SHA-256 hex of the token")
		assert.NotContains(t, hash, token)
		assert.NotContains(t, document, token, "the plaintext token must never reach a row")
	}
}

func TestDBTokenPersistAuthorizesAfterAReload(t *testing.T) {
	state := newFakeState()
	ctx := t.Context()

	minted := hub.NewTokenStore(hub.NewDBTokenPersist(state), time.Hour)
	token, err := minted.Issue(ctx, "eks-prod")
	require.NoError(t, err)

	// A second replica that never saw the mint has to authorize the same
	// token from the row alone.
	other := hub.NewTokenStore(hub.NewDBTokenPersist(state), time.Hour)
	require.NoError(t, other.Load(ctx))

	environment, ok := other.Environment(ctx, token)
	require.True(t, ok)
	assert.Equal(t, "eks-prod", environment)
}

func TestDBTokenPersistRevokesAnEnvironment(t *testing.T) {
	state := newFakeState()
	tokens := hub.NewTokenStore(hub.NewDBTokenPersist(state), time.Hour)
	ctx := t.Context()

	token, err := tokens.Issue(ctx, "eks-prod")
	require.NoError(t, err)
	require.NoError(t, tokens.RevokeEnvironment(ctx, "eks-prod"))

	_, ok := tokens.Environment(ctx, token)
	assert.False(t, ok, "a revoked token must not authorize a heartbeat")

	stored, err := state.Tokens(ctx)
	require.NoError(t, err)
	assert.Empty(t, stored)
}

func TestDBTokenPersistKeepsEnrolmentWorking(t *testing.T) {
	state := newFakeState()
	tokens := hub.NewTokenStore(hub.NewDBTokenPersist(state), time.Hour)
	ctx := t.Context()

	join, err := tokens.Issue(ctx, "eks-prod")
	require.NoError(t, err)

	environment, agent, err := tokens.IssueAgent(ctx, join)
	require.NoError(t, err)
	assert.Equal(t, "eks-prod", environment)
	assert.True(t, strings.HasPrefix(agent, "agt_"))

	got, ok := tokens.Environment(ctx, agent)
	require.True(t, ok)
	assert.Equal(t, "eks-prod", got)

	_, ok = tokens.InvitationEnvironment(ctx, join)
	assert.False(t, ok, "the invitation is dropped once an agent credential replaces it")
}

func TestDBWorkPersistRoundTripsAQueue(t *testing.T) {
	state := newFakeState()
	persist := hub.NewDBWorkPersist(state)
	ctx := t.Context()

	require.NoError(t, persist.Save(ctx, "eks-prod", []hub.Work{
		{ID: "w1", Kind: hub.WorkDelete, Database: "orders"},
	}))

	got, err := persist.Load(ctx)
	require.NoError(t, err)
	require.Len(t, got["eks-prod"], 1)
	assert.Equal(t, "w1", got["eks-prod"][0].ID)
	assert.Equal(t, hub.WorkDelete, got["eks-prod"][0].Kind)
}

func TestDBWorkPersistWritesAnEmptyQueueRatherThanDroppingIt(t *testing.T) {
	state := newFakeState()
	persist := hub.NewDBWorkPersist(state)
	ctx := t.Context()

	require.NoError(t, persist.Save(ctx, "eks-prod", nil))

	stored, err := state.Work(ctx)
	require.NoError(t, err)
	assert.Equal(t, "[]", stored["eks-prod"])
}

func TestDBWorkPersistRefusesWorkWhenTheWriteFails(t *testing.T) {
	state := newFakeState()
	state.failSave = true
	queue := hub.NewWorkQueue()
	queue.SetPersist(hub.NewDBWorkPersist(state))

	err := queue.Push("eks-prod", hub.Work{Kind: hub.WorkDelete, Database: "orders"})

	require.Error(t, err, "the UI must not report success for work a restart would drop")
}

func TestDBCloneKeyPersistRoundTrips(t *testing.T) {
	state := newFakeState()
	persist := hub.NewDBCloneKeyPersist(state)
	ctx := t.Context()

	key := hub.CloneKey{Identity: "-----BEGIN OPENSSH PRIVATE KEY-----", Pub: "ssh-ed25519 AAAA"}
	require.NoError(t, persist.Save(ctx, "eks-prod", key))

	got, err := persist.Load(ctx)
	require.NoError(t, err)
	assert.Equal(t, key, got["eks-prod"])

	require.NoError(t, persist.Remove(ctx, "eks-prod"))
	after, err := persist.Load(ctx)
	require.NoError(t, err)
	assert.Empty(t, after)
}
