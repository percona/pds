package hub_test

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

// memKV is a Secret and ConfigMap store that merges keys the way the kube
// client does, so persist tests can restart a TokenStore or Inventory against
// the same objects.
type memKV struct {
	mu      sync.Mutex
	secrets map[string]map[string]string
	cms     map[string]map[string]string
	putErr  error
}

func (m *memKV) key(namespace, name string) string { return namespace + "/" + name }

func (m *memKV) GetSecret(_ context.Context, namespace, name string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	values, ok := m.secrets[m.key(namespace, name)]
	if !ok {
		return nil, kube.ErrSecretNotFound
	}

	return copyMap(values), nil
}

func (m *memKV) PutSecret(_ context.Context, namespace, name string, values map[string]string) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.secrets == nil {
		m.secrets = map[string]map[string]string{}
	}
	existing, ok := m.secrets[m.key(namespace, name)]
	if !ok {
		existing = map[string]string{}
	}
	for k, v := range values {
		existing[k] = v
	}
	m.secrets[m.key(namespace, name)] = existing

	return nil
}

func (m *memKV) GetConfigMap(_ context.Context, namespace, name string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	values, ok := m.cms[m.key(namespace, name)]
	if !ok {
		return nil, kube.ErrConfigMapNotFound
	}

	return copyMap(values), nil
}

func (m *memKV) PutConfigMap(_ context.Context, namespace, name string, values map[string]string) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cms == nil {
		m.cms = map[string]map[string]string{}
	}
	existing, ok := m.cms[m.key(namespace, name)]
	if !ok {
		existing = map[string]string{}
	}
	for k, v := range values {
		existing[k] = v
	}
	m.cms[m.key(namespace, name)] = existing

	return nil
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}

	return out
}

func TestJoinTokenSurvivesReload(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens")
	store := hub.NewTokenStore(persist, 0)

	token, err := store.Issue(ctx, "max-testing")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(token, "jnt_"))

	restarted := hub.NewTokenStore(persist, 0)
	require.NoError(t, restarted.Load(ctx))
	name, ok := restarted.Environment(ctx, token)
	require.True(t, ok)
	assert.Equal(t, "max-testing", name)
}

func TestJoinTokenSecretNeverStoresPlaintext(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens")
	store := hub.NewTokenStore(persist, 0)

	token, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	got, err := kv.GetSecret(ctx, "everest-system", "pds-join-tokens")
	require.NoError(t, err)
	require.Len(t, got, 1)
	for hash, value := range got {
		// The value is the grant: which environment the hash buys, and until
		// when. What matters is that neither half is the token itself.
		assert.Contains(t, value, `"environment":"spoke-a"`)
		assert.Regexp(t, `^[0-9a-f]{64}$`, hash)
		assert.NotContains(t, hash, token)
		assert.NotContains(t, value, token)
		assert.NotContains(t, hash, "jnt_")
		assert.NotContains(t, value, "jnt_")
	}
}

func TestJoinTokenReloadOnMiss(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens")
	store := hub.NewTokenStore(persist, 0)
	token, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	other := hub.NewTokenStore(persist, 0)
	name, ok := other.Environment(ctx, token)
	require.True(t, ok)
	assert.Equal(t, "spoke-a", name)
}

type snapshotThenWaitPersist struct {
	hub.TokenPersist
	loadStarted chan struct{}
	release     chan struct{}
	once        sync.Once
}

func (s *snapshotThenWaitPersist) Load(ctx context.Context) (map[string]hub.Grant, error) {
	loaded, err := s.TokenPersist.Load(ctx)
	s.once.Do(func() { close(s.loadStarted) })
	<-s.release

	return loaded, err
}

func TestJoinTokenIssueSurvivesAConcurrentReload(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	inner := hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens")
	block := &snapshotThenWaitPersist{
		TokenPersist: inner,
		loadStarted:  make(chan struct{}),
		release:      make(chan struct{}),
	}
	store := hub.NewTokenStore(block, 0)
	first, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, ok := store.Environment(ctx, first)
		assert.True(t, ok)
	}()
	<-block.loadStarted
	second, err := store.Issue(ctx, "spoke-b")
	require.NoError(t, err)
	close(block.release)
	wg.Wait()

	name, ok := store.Environment(ctx, second)
	require.True(t, ok, "a token issued during a persist reload must stay valid")
	assert.Equal(t, "spoke-b", name)
}

// A replica that already authorized a token used to keep serving it from
// memory after another replica deleted the grant. Heartbeats then recreated
// the inventory row.
func TestJoinTokenRevokeIsVisibleToAnotherReplica(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens")
	now := time.Date(2026, time.September, 14, 21, 0, 0, 0, time.UTC)

	issuer := hub.NewTokenStore(persist, 0)
	issuer.SetClock(func() time.Time { return now })
	token, err := issuer.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	replica := hub.NewTokenStore(persist, 0)
	replica.SetClock(func() time.Time { return now })
	name, ok := replica.Environment(ctx, token)
	require.True(t, ok)
	assert.Equal(t, "spoke-a", name)

	require.NoError(t, issuer.RevokeEnvironment(ctx, "spoke-a"))

	_, cached := replica.Environment(ctx, token)
	assert.True(t, cached, "a revoke must not take the other replica down before the reload floor")

	now = now.Add(6 * time.Second)
	_, ok = replica.Environment(ctx, token)
	assert.False(t, ok, "a cached token must not outlive a revoke on another replica")
}

func TestJoinTokenUnknownAfterEmptyStore(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	store := hub.NewTokenStore(hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens"), 0)
	_, ok := store.Environment(ctx, "jnt_deadbeefdeadbeefdeadbeefdeadbeef")
	assert.False(t, ok)
}

func TestJoinTokenIssueKeepsMultiplePerEnvironment(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens")
	store := hub.NewTokenStore(persist, 0)
	first, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)
	second, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)
	assert.NotEqual(t, first, second)

	restarted := hub.NewTokenStore(persist, 0)
	require.NoError(t, restarted.Load(ctx))
	name, ok := restarted.Environment(ctx, first)
	require.True(t, ok)
	assert.Equal(t, "spoke-a", name)
	name, ok = restarted.Environment(ctx, second)
	require.True(t, ok)
	assert.Equal(t, "spoke-a", name)
}

func TestJoinTokenIssueFailsWhenPersistFails(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{putErr: kube.ErrForbidden}
	store := hub.NewTokenStore(hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens"), 0)
	_, err := store.Issue(ctx, "spoke-a")
	require.Error(t, err)
	_, ok := store.Environment(ctx, "jnt_anything")
	assert.False(t, ok)
}

func TestInventoryHeartbeatSurvivesReload(t *testing.T) {
	kv := &memKV{}
	persist := hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory")
	inv := hub.NewInventory(90 * time.Second)
	inv.SetPersist(persist)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "max-testing",
		Namespace:   "pds",
		At:          now,
		Instances: []engine.Instance{{
			ID:     "max-testing/pds.max-db",
			Name:   "max-db",
			Status: engine.Status{Phase: engine.PhaseReady},
		}},
	})

	restarted := hub.NewInventory(90 * time.Second)
	restarted.SetPersist(persist)
	require.NoError(t, restarted.Load(context.Background()))

	stale := restarted.List(now.Add(91 * time.Second))
	require.Len(t, stale, 1)
	assert.Equal(t, "max-testing", stale[0].Name)
	assert.Equal(t, hub.StatusUnreachable, stale[0].Status)
	assert.Equal(t, now, stale[0].LastSeen)

	snap, ok := restarted.Snapshot("max-testing")
	require.True(t, ok)
	require.Len(t, snap.Instances, 1)
	assert.Equal(t, "max-db", snap.Instances[0].Name)
}

func TestInventoryPersistStripsURI(t *testing.T) {
	kv := &memKV{}
	persist := hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory")
	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(persist)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          now,
		Instances: []engine.Instance{{
			Name: "orders-dev",
			Connection: &engine.Connection{
				Host: "orders-dev",
				Port: 3306,
				URI:  "mysql://root:hunter2@orders-dev:3306",
			},
		}},
	})

	stored, err := kv.GetConfigMap(context.Background(), "everest-system", hub.InventoryShardConfigMap("pds-hub-inventory", "spoke-a"))
	require.NoError(t, err)
	assert.NotContains(t, stored["spoke-a"], "hunter2")
	assert.NotContains(t, stored["spoke-a"], "mysql://root:")

	restarted := hub.NewInventory(time.Minute)
	restarted.SetPersist(persist)
	require.NoError(t, restarted.Load(context.Background()))
	snap, ok := restarted.Snapshot("spoke-a")
	require.True(t, ok)
	require.NotNil(t, snap.Instances[0].Connection)
	assert.Equal(t, "orders-dev", snap.Instances[0].Connection.Host)
	assert.Empty(t, snap.Instances[0].Connection.URI)
}

func TestInventoryKeepsThePublicURIInMemory(t *testing.T) {
	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(&memKV{}, "everest-system", "pds-hub-inventory"))
	inv.ApplyHeartbeat(publicDatabaseBeat(time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)))

	live, ok := inv.Snapshot("spoke-a")
	require.True(t, ok)
	require.NotNil(t, live.Instances[0].Connection)
	assert.Equal(t, "mysql://root:hunter2@198.51.100.20:3306", live.Instances[0].Connection.URI)
}

func TestInventoryPersistStoresNoPublicURI(t *testing.T) {
	kv := &memKV{}
	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	inv.ApplyHeartbeat(publicDatabaseBeat(time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)))

	stored, err := kv.GetConfigMap(context.Background(), "everest-system", hub.InventoryShardConfigMap("pds-hub-inventory", "spoke-a"))
	require.NoError(t, err)
	assert.NotContains(t, stored["spoke-a"], "hunter2")
}

func TestInventoryReloadHasNoPublicURIUntilTheNextBeat(t *testing.T) {
	persist := hub.NewConfigMapInventoryPersist(&memKV{}, "everest-system", "pds-hub-inventory")
	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(persist)
	inv.ApplyHeartbeat(publicDatabaseBeat(time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)))

	restarted := hub.NewInventory(time.Minute)
	restarted.SetPersist(persist)
	require.NoError(t, restarted.Load(context.Background()))
	snap, ok := restarted.Snapshot("spoke-a")
	require.True(t, ok)
	require.NotNil(t, snap.Instances[0].Connection)
	assert.Equal(t, "198.51.100.20", snap.Instances[0].Connection.Host)
	assert.Empty(t, snap.Instances[0].Connection.URI)
}

func TestInventoryLoadDropsAURIAnEarlierReleaseStored(t *testing.T) {
	kv := &memKV{}
	persist := hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory")
	seed := hub.NewInventory(time.Minute)
	seed.SetPersist(persist)
	seed.ApplyHeartbeat(publicDatabaseBeat(time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)))
	shard := hub.InventoryShardConfigMap("pds-hub-inventory", "spoke-a")
	stored, err := kv.GetConfigMap(context.Background(), "everest-system", shard)
	require.NoError(t, err)
	legacy := strings.Replace(stored["spoke-a"], `"URI":""`, `"URI":"mysql://root:hunter2@198.51.100.20:3306"`, 1)
	require.Contains(t, legacy, "hunter2")
	require.NoError(t, kv.PutConfigMap(context.Background(), "everest-system", shard, map[string]string{"spoke-a": legacy}))

	restarted := hub.NewInventory(time.Minute)
	restarted.SetPersist(persist)
	require.NoError(t, restarted.Load(context.Background()))
	snap, ok := restarted.Snapshot("spoke-a")
	require.True(t, ok)
	assert.Empty(t, snap.Instances[0].Connection.URI)
}

func publicDatabaseBeat(at time.Time) hub.Heartbeat {
	return hub.Heartbeat{
		Environment:   "spoke-a",
		Namespace:     "pds",
		At:            at,
		PrivateAccess: true,
		Instances: []engine.Instance{{
			Name:     "orders-ext",
			Exposure: engine.Exposure{External: true, Address: "198.51.100.20"},
			Connection: &engine.Connection{
				Host: "198.51.100.20",
				Port: 3306,
				URI:  "mysql://root:hunter2@198.51.100.20:3306",
			},
		}},
	}
}

func TestInventoryPersistStripsTheURIOfAPrivateDatabase(t *testing.T) {
	kv := &memKV{}
	persist := hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory")
	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(persist)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		Instances: []engine.Instance{{
			Name:     "orders-priv",
			Exposure: engine.Exposure{External: true, Private: true, Address: "10.0.4.12"},
			Connection: &engine.Connection{
				Host: "10.0.4.12",
				Port: 3306,
				URI:  "mysql://root:hunter2@10.0.4.12:3306",
			},
		}},
	})

	live, ok := inv.Snapshot("spoke-a")
	require.True(t, ok)
	assert.Empty(t, live.Instances[0].Connection.URI)

	stored, err := kv.GetConfigMap(context.Background(), "everest-system", hub.InventoryShardConfigMap("pds-hub-inventory", "spoke-a"))
	require.NoError(t, err)
	assert.NotContains(t, stored["spoke-a"], "hunter2")
}

func TestInventoryPersistWritesEnvironmentsIntoFixedShards(t *testing.T) {
	kv := &memKV{}
	persist := hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory")
	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(persist)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          now,
		Instances:   []engine.Instance{{Name: "db-a"}},
	})
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-b",
		Namespace:   "pds",
		At:          now,
		Instances:   []engine.Instance{{Name: "db-b"}},
	})

	index, err := kv.GetConfigMap(context.Background(), "everest-system", "pds-hub-inventory")
	require.NoError(t, err)
	assert.Equal(t, "1", index["spoke-a"])
	assert.Equal(t, "1", index["spoke-b"])

	aName := hub.InventoryShardConfigMap("pds-hub-inventory", "spoke-a")
	bName := hub.InventoryShardConfigMap("pds-hub-inventory", "spoke-b")
	a, err := kv.GetConfigMap(context.Background(), "everest-system", aName)
	require.NoError(t, err)
	assert.Contains(t, a["spoke-a"], "db-a")
	assert.NotContains(t, a["spoke-a"], "db-b")

	b, err := kv.GetConfigMap(context.Background(), "everest-system", bName)
	require.NoError(t, err)
	assert.Contains(t, b["spoke-b"], "db-b")
	assert.NotContains(t, b["spoke-b"], "db-a")
}

// A snapshot saved before private access existed may hold a private
// database's password read as public. It carries no mark, so load drops it.
func TestInventoryLoadDropsThePasswordOfAnUnmarkedSnapshot(t *testing.T) {
	kv := &memKV{}
	ctx := context.Background()
	require.NoError(t, kv.PutConfigMap(ctx, "everest-system", "pds-hub-inventory", map[string]string{
		"spoke-a": `{"namespace":"pds","seen":true,"heartbeat":{"environment":"spoke-a","namespace":"pds",` +
			`"instances":[{"name":"orders-priv","exposure":{"external":true,"address":"internal-a1b2.elb.amazonaws.com"},` +
			`"connection":{"host":"internal-a1b2.elb.amazonaws.com","port":3306,"uri":"mysql://root:hunter2@internal-a1b2.elb.amazonaws.com:3306"}}]}}`,
	}))

	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	require.NoError(t, inv.Load(ctx))

	snap, ok := inv.Snapshot("spoke-a")
	require.True(t, ok)
	require.Len(t, snap.Instances, 1)
	require.NotNil(t, snap.Instances[0].Connection)
	assert.Equal(t, "internal-a1b2.elb.amazonaws.com", snap.Instances[0].Connection.Host)
	assert.Empty(t, snap.Instances[0].Connection.URI)
}

func TestInventoryLoadReadsALegacyCombinedConfigMap(t *testing.T) {
	kv := &memKV{}
	ctx := context.Background()
	require.NoError(t, kv.PutConfigMap(ctx, "everest-system", "pds-hub-inventory", map[string]string{
		"spoke-a": `{"namespace":"pds","seen":true,"heartbeat":{"environment":"spoke-a","namespace":"pds","instances":[{"name":"orders-dev"}]}}`,
	}))

	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	require.NoError(t, inv.Load(ctx))

	snap, ok := inv.Snapshot("spoke-a")
	require.True(t, ok)
	require.Len(t, snap.Instances, 1)
	assert.Equal(t, "orders-dev", snap.Instances[0].Name)
}

// nameScopedInventoryKV returns Forbidden for ConfigMaps the adapter Role
// cannot GET: anything other than the index and prefix-0..15.
type nameScopedInventoryKV struct {
	*memKV
	prefix string
	got    []string
}

func (k *nameScopedInventoryKV) allowed(name string) bool {
	if name == k.prefix {
		return true
	}
	for i := 0; i < 16; i++ {
		if name == k.prefix+"-"+strconv.Itoa(i) {
			return true
		}
	}

	return false
}

func (k *nameScopedInventoryKV) GetConfigMap(ctx context.Context, namespace, name string) (map[string]string, error) {
	k.got = append(k.got, name)
	if !k.allowed(name) {
		return nil, kube.ErrForbidden
	}

	return k.memKV.GetConfigMap(ctx, namespace, name)
}

func TestInventoryLoadReadsALegacyCombinedConfigMapUnderNameScopedRBAC(t *testing.T) {
	inner := &memKV{}
	ctx := context.Background()
	prefix := "pds-hub-inventory"
	require.NoError(t, inner.PutConfigMap(ctx, "everest-system", prefix, map[string]string{
		"spoke-a": `{"namespace":"pds","seen":true,"heartbeat":{"environment":"spoke-a","namespace":"pds","instances":[{"name":"orders-dev"}]}}`,
	}))
	require.NoError(t, inner.PutConfigMap(ctx, "everest-system", prefix+"-spoke-a", map[string]string{
		"spoke-a": `{"namespace":"pds","seen":true,"heartbeat":{"environment":"spoke-a","instances":[{"name":"from-unreadable-object"}]}}`,
	}))
	kv := &nameScopedInventoryKV{memKV: inner, prefix: prefix}

	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", prefix))
	require.NoError(t, inv.Load(ctx))

	snap, ok := inv.Snapshot("spoke-a")
	require.True(t, ok)
	require.Len(t, snap.Instances, 1)
	assert.Equal(t, "orders-dev", snap.Instances[0].Name)
	assert.NotContains(t, kv.got, prefix+"-spoke-a")
}

func TestInventorySaveMovesALegacyRowOntoItsOwnConfigMap(t *testing.T) {
	kv := &memKV{}
	ctx := context.Background()
	require.NoError(t, kv.PutConfigMap(ctx, "everest-system", "pds-hub-inventory", map[string]string{
		"spoke-a": `{"namespace":"pds","seen":true,"heartbeat":{"environment":"spoke-a","namespace":"pds"}}`,
	}))
	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})

	index, err := kv.GetConfigMap(ctx, "everest-system", "pds-hub-inventory")
	require.NoError(t, err)
	assert.Equal(t, "1", index["spoke-a"])

	stored, err := kv.GetConfigMap(ctx, "everest-system", hub.InventoryShardConfigMap("pds-hub-inventory", "spoke-a"))
	require.NoError(t, err)
	assert.Contains(t, stored["spoke-a"], `"namespace":"pds"`)
}

func TestInventoryForgetDeletesTheEnvironmentConfigMap(t *testing.T) {
	kv := &memKV{}
	ctx := context.Background()
	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})
	require.NoError(t, inv.Forget(ctx, "spoke-a"))

	shard := hub.InventoryShardConfigMap("pds-hub-inventory", "spoke-a")
	stored, err := kv.GetConfigMap(ctx, "everest-system", shard)
	if err == nil {
		_, ok := stored["spoke-a"]
		assert.False(t, ok)
	} else {
		require.ErrorIs(t, err, kube.ErrConfigMapNotFound)
	}

	index, err := kv.GetConfigMap(ctx, "everest-system", "pds-hub-inventory")
	require.NoError(t, err)
	_, ok := index["spoke-a"]
	assert.False(t, ok)
}

func TestGitRegisterAfterLoadKeepsSnapshot(t *testing.T) {
	kv := &memKV{}
	persist := hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory")
	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(persist)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          now,
		Instances:   []engine.Instance{{Name: "orders-dev"}},
	})

	restarted := hub.NewInventory(time.Minute)
	restarted.SetPersist(persist)
	restarted.Register("spoke-a", "pds")
	require.NoError(t, restarted.Load(context.Background()))
	restarted.Register("spoke-a", "pds")

	snap, ok := restarted.Snapshot("spoke-a")
	require.True(t, ok)
	require.Len(t, snap.Instances, 1)
	assert.Equal(t, "orders-dev", snap.Instances[0].Name)
}

func TestPendingBackupSurvivesInventoryReload(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.Register("spoke-a", "pds")
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
	})
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	backup, err := svc.Backup(ctx, created.ID)
	require.NoError(t, err)

	restartedInv := hub.NewInventory(time.Minute)
	restartedInv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	require.NoError(t, restartedInv.Load(ctx))
	restarted := hub.NewService(store, restartedInv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
	})
	listed, err := restarted.Backups(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, backup.ID, listed[0].ID)
	assert.Equal(t, engine.BackupPending, listed[0].Status)

	restarted.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
	})
	work := restarted.TakeWork("spoke-a")
	require.Len(t, work, 1)
	assert.Equal(t, backup.ID, work[0].BackupID)
}

func TestIssueJoinPersistsEnvironmentAndToken(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	tokens := hub.NewTokenStore(hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens"), 0)
	svc := hub.NewService(store, inv, hub.Options{
		Namespace: "pds",
		Tokens:    tokens,
	})

	token, err := svc.IssueJoin(ctx, "test-lucas")
	require.NoError(t, err)

	listed := svc.Environments()
	require.Len(t, listed, 1)
	assert.Equal(t, "test-lucas", listed[0].Name)
	assert.Equal(t, hub.StatusUnreachable, listed[0].Status)

	restartedInv := hub.NewInventory(time.Minute)
	restartedInv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	require.NoError(t, restartedInv.Load(ctx))
	got, ok := restartedInv.Get("test-lucas", time.Now())
	require.True(t, ok)
	assert.Equal(t, hub.StatusUnreachable, got.Status)

	restartedTokens := hub.NewTokenStore(hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens"), 0)
	require.NoError(t, restartedTokens.Load(ctx))
	name, ok := restartedTokens.Environment(ctx, token)
	require.True(t, ok)
	assert.Equal(t, "test-lucas", name)
}

func TestIssueJoinWrapsPersistError(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{putErr: kube.ErrForbidden}
	inv := hub.NewInventory(time.Minute)
	tokens := hub.NewTokenStore(hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens"), 0)
	store := hub.NewMemoryStore()
	svc := hub.NewService(store, inv, hub.Options{
		Namespace: "pds",
		Tokens:    tokens,
	})

	_, err := svc.IssueJoin(ctx, "spoke-a")
	require.ErrorIs(t, err, hub.ErrPersist)
	assert.Empty(t, svc.Environments(), "a failed join must not leave a listed environment")
	paths, listErr := store.List(ctx, "clusters/spoke-a/")
	require.NoError(t, listErr)
	assert.Empty(t, paths, "a failed join must not leave a desired-state tree")
}

func TestIssueJoinWrapsInventoryPersistError(t *testing.T) {
	ctx := context.Background()
	invKV := &memKV{putErr: kube.ErrForbidden}
	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(invKV, "everest-system", "pds-hub-inventory"))
	tokens := hub.NewTokenStore(hub.NewSecretTokenPersist(&memKV{}, "everest-system", "pds-join-tokens"), 0)
	store := hub.NewMemoryStore()
	svc := hub.NewService(store, inv, hub.Options{
		Namespace: "pds",
		Tokens:    tokens,
	})

	_, err := svc.IssueJoin(ctx, "spoke-a")
	require.ErrorIs(t, err, hub.ErrPersist)
	assert.Empty(t, svc.Environments(), "a failed inventory persist must not leave a listed environment")
	paths, listErr := store.List(ctx, "clusters/spoke-a/")
	require.NoError(t, listErr)
	assert.Empty(t, paths, "a failed inventory persist must not leave a desired-state tree")
}

type countingSecretKV struct {
	*memKV
	secretPuts int
	failAfter  int
	failDelete bool
}

func (c *countingSecretKV) PutSecret(ctx context.Context, namespace, name string, values map[string]string) error {
	c.secretPuts++
	if c.failAfter > 0 && c.secretPuts > c.failAfter {
		return kube.ErrForbidden
	}

	return c.memKV.PutSecret(ctx, namespace, name, values)
}

func (c *countingSecretKV) DeleteSecretKeys(ctx context.Context, namespace, name string, keys []string) error {
	if c.failDelete {
		return kube.ErrForbidden
	}

	return c.memKV.DeleteSecretKeys(ctx, namespace, name, keys)
}

func TestIssueJoinKeepsExistingDesiredStateWhenTokenPersistFails(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	kv := &memKV{}
	secrets := &countingSecretKV{memKV: kv, failAfter: 1}
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Hour)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
		Tokens:        hub.NewTokenStore(hub.NewSecretTokenPersist(secrets, "everest-system", "pds-join-tokens"), 0),
	})

	_, err := svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	require.NotNil(t, created)

	_, err = svc.IssueJoin(ctx, "spoke-a")
	require.ErrorIs(t, err, hub.ErrPersist)

	_, known := inv.Get("spoke-a", now)
	assert.True(t, known, "a token persist failure on an existing environment must not drop inventory")
	body, getErr := store.Get(ctx, "clusters/spoke-a/instances/orders.yaml")
	require.NoError(t, getErr)
	assert.Contains(t, string(body), "orders")
}

func TestADisconnectThatFailsLaterStillReportsTheSupportAccessItRemoved(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	secrets := &countingSecretKV{memKV: &memKV{}}
	support := hub.NewMemorySupportPersist()
	require.NoError(t, support.Save(ctx, "spoke-a", hub.SupportRecord{Address: "svc:pds-a"}))
	inv := hub.NewInventory(time.Hour)
	svc := hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
		Tokens:        hub.NewTokenStore(hub.NewSecretTokenPersist(secrets, "everest-system", "pds-join-tokens"), 0),
		SupportAccess: support,
	})
	_, err := svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})
	secrets.failDelete = true

	removed, err := svc.Disconnect(ctx, "spoke-a")

	require.ErrorIs(t, err, hub.ErrPersist)
	assert.True(t, removed, "the access is gone, so the caller must still be able to write the entry")
}

func TestDisconnectRestoresDesiredStateWhenTokenRevokeFails(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	kv := &memKV{}
	secrets := &countingSecretKV{memKV: kv}
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Hour)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
		Tokens:        hub.NewTokenStore(hub.NewSecretTokenPersist(secrets, "everest-system", "pds-join-tokens"), 0),
	})

	token, err := svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	require.NotNil(t, created)

	secrets.failDelete = true
	require.ErrorIs(t, disconnect(ctx, svc, "spoke-a"), hub.ErrPersist)

	_, known := inv.Get("spoke-a", now)
	assert.True(t, known, "a failed disconnect must not forget an environment whose tree it could not finish removing")
	body, getErr := store.Get(ctx, "clusters/spoke-a/instances/orders.yaml")
	require.NoError(t, getErr)
	assert.Contains(t, string(body), "orders")
	environment, ok := svc.JoinEnvironment(ctx, token)
	require.True(t, ok, "a failed disconnect must not leave the join token revoked")
	assert.Equal(t, "spoke-a", environment)
}

func TestDisconnectRestoresTokensWhenQueueClearFails(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	kv := &memKV{}
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Hour)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	tokens := hub.NewTokenStore(hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens"), 0)
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
		Tokens:        tokens,
	})

	token, err := svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})
	_, err = svc.Create(ctx, engine.CreateInput{Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	require.NoError(t, svc.SetWorkPersist(ctx, saveFailPersist{}))

	require.ErrorIs(t, disconnect(ctx, svc, "spoke-a"), hub.ErrPersist)

	environment, ok := svc.JoinEnvironment(ctx, token)
	require.True(t, ok, "a failed queue clear must put the join token back")
	assert.Equal(t, "spoke-a", environment)
	body, getErr := store.Get(ctx, "clusters/spoke-a/instances/orders.yaml")
	require.NoError(t, getErr)
	assert.Contains(t, string(body), "orders")
	_, known := inv.Get("spoke-a", now)
	assert.True(t, known)
}

func TestBackupDoesNotReviveADisconnectedEnvironment(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Hour)
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
		Tokens:        hub.NewTokenStore(nil, 0),
	})

	_, err := svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})
	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	require.NoError(t, disconnect(ctx, svc, "spoke-a"))

	_, err = svc.Backup(ctx, created.ID)
	require.ErrorIs(t, err, hub.ErrUnreachable)
	assert.Empty(t, svc.Environments())
}

// TestJoinTokenExpires covers the token an operator pastes into a ticket or
// leaves in shell history. It used to authorize a heartbeat and an enrollment
// for as long as the hub kept its Secret, which is to say forever.
func TestJoinTokenExpires(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens")
	store := hub.NewTokenStore(persist, time.Hour)

	token, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	// The token is deliberately not presented here. Presenting it redeems it,
	// and a redeemed grant stops expiring on purpose: the same token is the
	// spoke's lasting beat credential, so a TTL that outlived the join would
	// take a working environment offline. What expires is an invitation nobody
	// used, which is what this test covers.
	//
	// A hub that comes back later reads the grant off the Secret and must
	// reach the same verdict, so the expiry has to be in the Secret and not
	// only in the process that minted it. The late store starts with an empty
	// map, so finding the hash at all proves it round-tripped.
	late := hub.NewTokenStore(persist, time.Hour)
	late.SetClock(func() time.Time { return time.Now().Add(2 * time.Hour) })
	_, ok := late.Environment(ctx, token)
	assert.False(t, ok, "an unredeemed token outlived its TTL")
}

// TestJoinTokenWithoutTTLDoesNotExpire keeps the previous behaviour available,
// because a lab that pins a token across runs relies on it.
func TestJoinTokenWithoutTTLDoesNotExpire(t *testing.T) {
	ctx := context.Background()
	store := hub.NewTokenStore(nil, 0)

	token, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	store.SetClock(func() time.Time { return time.Now().AddDate(1, 0, 0) })
	name, ok := store.Environment(ctx, token)
	require.True(t, ok)
	assert.Equal(t, "spoke-a", name)
}

// TestJoinTokenReadsALegacyValue covers a Secret written before grants, whose
// values were the bare environment name. Those tokens keep working.
func TestJoinTokenReadsALegacyValue(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	// sha256 of "jnt_legacy".
	hash := "8d5c1a1f34e2d1a0a32b4c8e07f38a5f2ab5b1e9b0e0ca4d0f0b1cba2d1aa9c1"
	require.NoError(t, kv.PutSecret(ctx, "everest-system", "pds-join-tokens",
		map[string]string{hash: "spoke-legacy"}))

	persist := hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens")
	loaded, err := persist.Load(ctx)
	require.NoError(t, err)
	require.Contains(t, loaded, hash)
	assert.Equal(t, "spoke-legacy", loaded[hash].Environment)
	assert.True(t, loaded[hash].Expires.IsZero(), "a legacy token must not gain an expiry it never had")
}

func (m *memKV) DeleteSecretKeys(_ context.Context, namespace, name string, keys []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	values := m.secrets[m.key(namespace, name)]
	for _, key := range keys {
		delete(values, key)
	}

	return nil
}

func (m *memKV) DeleteConfigMapKeys(_ context.Context, namespace, name string, keys []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	values := m.cms[m.key(namespace, name)]
	for _, key := range keys {
		delete(values, key)
	}

	return nil
}

func (m *memKV) DeleteConfigMap(_ context.Context, namespace, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cms, m.key(namespace, name))

	return nil
}

// TestDisconnectRemovesEverythingTheHubKept covers the row that appears the
// moment a token is minted. Every typo and every abandoned attempt was
// permanent, and its token stayed valid for as long as the Secret did.
func TestDisconnectRemovesEverythingTheHubKept(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Hour)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	tokens := hub.NewTokenStore(hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens"), 0)
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
		Tokens:        tokens,
	})

	token, err := svc.IssueJoin(ctx, "spoke-typo")
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-typo", Namespace: "pds", At: now})
	_, err = svc.Create(ctx, engine.CreateInput{Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-typo"})
	require.NoError(t, err)

	require.NoError(t, disconnect(ctx, svc, "spoke-typo"))

	// The row is gone.
	_, known := inv.Get("spoke-typo", now)
	assert.False(t, known, "the environment is still listed")

	// Desired state stays. Deleting it would let Flux prune every Instance
	// once a later join of this name restores the deploy key.
	body, err := store.Get(ctx, "clusters/spoke-typo/instances/orders.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), "orders")

	// And the token no longer buys anything.
	_, ok := svc.JoinEnvironment(ctx, token)
	assert.False(t, ok, "a disconnected environment's token still authorized a join")

	// An environment the hub never knew is a not-found, not a silent success.
	require.ErrorIs(t, disconnect(ctx, svc, "never-existed"), engine.ErrNotFound)
}

func TestReconnectAfterDisconnectKeepsDesiredState(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Hour)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
		Tokens:        hub.NewTokenStore(hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens"), 0),
	})

	_, err := svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})
	_, err = svc.Create(ctx, engine.CreateInput{Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	require.NoError(t, disconnect(ctx, svc, "spoke-a"))

	_, err = svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)

	body, err := store.Get(ctx, "clusters/spoke-a/instances/orders.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), "orders")
	index, err := store.Get(ctx, "clusters/spoke-a/instances/kustomization.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(index), "orders.yaml", "reconnecting must not publish an empty prune index")
}

// A disconnect that forgets the inventory row but leaves queued work behind
// hands the next registration of the same name a stale delete on first beat.
func TestDisconnectClearsTheWorkQueue(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Hour)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
		Tokens:        hub.NewTokenStore(nil, 0),
	})
	require.NoError(t, svc.SetWorkPersist(ctx, hub.NewConfigMapWorkPersist(kv, "everest-system", "pds-hub-work")))

	_, err := svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          now,
		Instances: []engine.Instance{{
			ID: "spoke-a/pds.orders-dev", Name: "orders-dev",
			Namespace: "pds", Environment: "spoke-a",
			Status: engine.Status{Phase: engine.PhaseReady},
		}},
	})
	_, err = svc.Backup(ctx, "spoke-a/pds.orders-dev")
	require.NoError(t, err)
	require.Len(t, svc.TakeWork("spoke-a"), 1)
	require.NoError(t, disconnect(ctx, svc, "spoke-a"))

	assert.Empty(t, svc.TakeWork("spoke-a"))

	restarted := hub.NewWorkQueue()
	restarted.SetPersist(hub.NewConfigMapWorkPersist(kv, "everest-system", "pds-hub-work"))
	require.NoError(t, restarted.Load(ctx))
	assert.Empty(t, restarted.Take("spoke-a"))
}

// Authorization used to finish before ApplyHeartbeat, so Disconnect could
// revoke the token and forget the row in between. A stale "still allowed"
// callback after that must not recreate the environment.
func TestAcceptHeartbeatDoesNotReviveAfterDisconnect(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv := hub.NewInventory(time.Hour)
	svc := hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
		Namespace: "pds",
		Now:       func() time.Time { return now },
		Tokens:    hub.NewTokenStore(nil, 0),
	})
	token, err := svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)
	_, ok, err := svc.AcceptHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds"}, func() bool {
		name, allowed := svc.JoinEnvironment(ctx, token)
		return allowed && name == "spoke-a"
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, disconnect(ctx, svc, "spoke-a"))

	_, ok, err = svc.AcceptHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds"}, func() bool {
		return true
	})
	require.NoError(t, err)
	assert.False(t, ok, "a disconnected environment must not be recreated by a stale authorize")
	assert.Empty(t, svc.Environments())
}

func TestAcceptHeartbeatDoesNotTakeWhenAckPersistFails(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	kv := &memKV{}
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Hour)
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
		Tokens:        hub.NewTokenStore(nil, 0),
	})
	_, err := svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})
	require.NoError(t, svc.SetWorkPersist(ctx, hub.NewConfigMapWorkPersist(kv, "everest-system", "pds-hub-work")))

	created, err := svc.Create(ctx, engine.CreateInput{Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)
	releaseCredentials(t, svc)
	backup, err := svc.Backup(ctx, created.ID)
	require.NoError(t, err)

	work, ok, err := svc.AcceptHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds"}, func() bool {
		return true
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, work, 1)
	assert.Equal(t, backup.ID, work[0].BackupID)

	kv.putErr = kube.ErrForbidden
	acked, ok, err := svc.AcceptHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		Results:     []hub.WorkResult{{ID: work[0].ID}},
	}, func() bool { return true })
	require.ErrorIs(t, err, hub.ErrPersist)
	assert.True(t, ok)
	assert.Empty(t, acked, "a failed ack must not hand the next batch")

	kv.putErr = nil
	acked, ok, err = svc.AcceptHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		Results:     []hub.WorkResult{{ID: work[0].ID}},
		Backups: []engine.Backup{{
			ID:       backup.ID,
			Status:   engine.BackupSucceeded,
			Instance: "orders",
		}},
	}, func() bool { return true })
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, acked)
}

func TestAcceptHeartbeatDoesNotAckWhenInventoryPersistFails(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	kv := &memKV{}
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Hour)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
		Tokens:        hub.NewTokenStore(nil, 0),
	})
	_, err := svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)

	kv.putErr = kube.ErrForbidden
	_, ok, err := svc.AcceptHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds"}, func() bool {
		return true
	})
	require.ErrorIs(t, err, hub.ErrPersist)
	assert.True(t, ok)
}

type countingInventoryKV struct {
	*memKV
	inventoryPuts int
}

func (c *countingInventoryKV) PutConfigMap(ctx context.Context, namespace, name string, values map[string]string) error {
	if strings.Contains(name, "hub-inventory") {
		c.inventoryPuts++
	}

	return c.memKV.PutConfigMap(ctx, namespace, name, values)
}

func TestAcceptHeartbeatPacesInventoryPersist(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	kv := &countingInventoryKV{memKV: &memKV{}}
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Hour)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
		Tokens:        hub.NewTokenStore(nil, 0),
	})
	_, err := svc.IssueJoin(ctx, "spoke-a")
	require.NoError(t, err)
	afterJoin := kv.inventoryPuts

	_, ok, err := svc.AcceptHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds",
	}, func() bool { return true })
	require.NoError(t, err)
	require.True(t, ok)
	afterFirst := kv.inventoryPuts
	assert.Greater(t, afterFirst, afterJoin)

	_, ok, err = svc.AcceptHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds",
	}, func() bool { return true })
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, afterFirst, kv.inventoryPuts, "a lastSeen-only beat must not rewrite the ConfigMap")

	inv.AddPendingBackup("spoke-a", "orders", engine.Backup{ID: "b1", Status: engine.BackupPending})
	assert.Greater(t, kv.inventoryPuts, afterFirst, "pending-operation updates persist immediately")
}

type gatedGitStore struct {
	inner *hub.MemoryStore
	ready *atomic.Bool
}

func (g *gatedGitStore) Write(ctx context.Context, files map[string][]byte, message string) error {
	return g.inner.Write(ctx, files, message)
}

func (g *gatedGitStore) Delete(ctx context.Context, paths []string, message string) error {
	return g.inner.Delete(ctx, paths, message)
}

func (g *gatedGitStore) Get(ctx context.Context, path string) ([]byte, error) {
	return g.inner.Get(ctx, path)
}

func (g *gatedGitStore) List(ctx context.Context, prefix string) ([]string, error) {
	if !g.ready.Load() {
		return nil, hub.ErrGitHost
	}

	return g.inner.List(ctx, prefix)
}

type readyGitHost struct {
	ready *atomic.Bool
}

func (h *readyGitHost) EnsureRepo(context.Context, string) error {
	h.ready.Store(true)

	return nil
}

func (h *readyGitHost) EnsureCloneKey(context.Context, string) (string, string, error) {
	return "", "", nil
}

func (h *readyGitHost) EnsureDeployKey(context.Context, string, string) error { return nil }

func (h *readyGitHost) RevokeCloneKey(context.Context, string) error { return nil }

func TestIssueJoinRollsBackTreeWhenRepoDidNotExistYet(t *testing.T) {
	ctx := context.Background()
	ready := &atomic.Bool{}
	inner := hub.NewMemoryStore()
	store := &gatedGitStore{inner: inner, ready: ready}
	kv := &memKV{putErr: kube.ErrForbidden}
	svc := hub.NewService(store, hub.NewInventory(time.Minute), hub.Options{
		Namespace: "pds",
		GitHost:   &readyGitHost{ready: ready},
		Tokens:    hub.NewTokenStore(hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens"), 0),
	})

	_, err := svc.IssueJoin(ctx, "spoke-a")
	require.ErrorIs(t, err, hub.ErrPersist)
	assert.True(t, ready.Load(), "the GitHub repo must exist before the hub decides whether a tree already did")
	paths, listErr := store.List(ctx, "clusters/spoke-a/")
	require.NoError(t, listErr)
	assert.Empty(t, paths, "a failed join must still roll back the tree it just created")
}

func TestIssueJoinFailsWhenListingAfterEnsureRepoFails(t *testing.T) {
	ctx := context.Background()
	inner := hub.NewMemoryStore()
	store := &gatedGitStore{inner: inner, ready: &atomic.Bool{}}
	svc := hub.NewService(store, hub.NewInventory(time.Minute), hub.Options{
		Namespace: "pds",
		GitHost:   &joinHost{},
	})

	_, err := svc.IssueJoin(ctx, "spoke-a")
	require.ErrorIs(t, err, hub.ErrGitHost)
	paths, listErr := inner.List(ctx, "clusters/spoke-a/")
	require.NoError(t, listErr)
	assert.Empty(t, paths, "a listing failure must not leave a desired-state tree")
}

func TestTheRepoOnARecordSurvivesARestartAndABeat(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	prefix := "acme-platform"
	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	svc, _ := newTeamGitHub(t, &prefix, inv)
	_, err := svc.IssueJoin(ctx, "prod")
	require.NoError(t, err)
	svc.ApplyHeartbeat(hub.Heartbeat{Environment: "prod", Namespace: "pds", At: time.Now()})

	restarted := hub.NewInventory(time.Minute)
	restarted.SetPersist(hub.NewConfigMapInventoryPersist(kv, "everest-system", "pds-hub-inventory"))
	require.NoError(t, restarted.Load(ctx))

	assert.Equal(t, "acme-platform-prod", restarted.Repo("prod"))
}
