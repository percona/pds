package hub

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"strconv"

	"github.com/percona/pds/adapter/internal/client/kube"
)

// ConfigMapKV reads and writes one Kubernetes ConfigMap. The kube client is
// the production implementation; tests inject a map.
type ConfigMapKV interface {
	GetConfigMap(ctx context.Context, namespace, name string) (map[string]string, error)
	PutConfigMap(ctx context.Context, namespace, name string, values map[string]string) error
	DeleteConfigMapKeys(ctx context.Context, namespace, name string, keys []string) error
}

// InventoryConfigMaps is ConfigMapKV plus whole-object delete for tests and
// the kube client. Snapshot rows are keys on shard ConfigMaps.
type InventoryConfigMaps interface {
	ConfigMapKV
	DeleteConfigMap(ctx context.Context, namespace, name string) error
}

// InventoryPersist stores the hub environment list and last heartbeat snapshot.
type InventoryPersist interface {
	Load(ctx context.Context) (map[string]persistedEnvironment, error)
	Save(ctx context.Context, name string, rec persistedEnvironment) error
	Remove(ctx context.Context, name string) error
}

type persistedEnvironment struct {
	Namespace      string          `json:"namespace,omitempty"`
	BackupStorage  string          `json:"backupStorage,omitempty"`
	Repo           string          `json:"repo,omitempty"`
	Seen           bool            `json:"seen"`
	Heartbeat      Heartbeat       `json:"heartbeat"`
	PendingBackups []PendingBackup `json:"pendingBackups,omitempty"`
}

// environmentIndexMarker is stored under an environment name on the prefix ConfigMap
// after that snapshot has moved to its own object. It is not valid JSON for
// persistedEnvironment, so Load cannot mistake it for a legacy combined row.
const environmentIndexMarker = "1"

// inventoryShards is how many snapshot ConfigMaps the hub uses. The names
// are prefix-0 through prefix-15, which Helm can put in resourceNames.
const inventoryShards = 16

// InventoryShardConfigMap is the ConfigMap that holds this environment's snapshot.
func InventoryShardConfigMap(prefix, environment string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(environment))

	return prefix + "-" + strconv.FormatUint(uint64(h.Sum32()%inventoryShards), 10)
}

// ConfigMapInventoryPersist stores one JSON document per environment.
//
// Kubernetes ConfigMaps are capped at 1 MiB. A single object holding every
// environment's heartbeat, instances, and backups will hit that ceiling, after
// which saves fail and a restart reloads a stale fleet. The named object is
// only an index of environment names; snapshots live in a fixed set of shard
// ConfigMaps so Role resourceNames can name them at install.
type ConfigMapInventoryPersist struct {
	store     InventoryConfigMaps
	namespace string
	name      string
}

// NewConfigMapInventoryPersist writes inventory rows using name as the index
// ConfigMap and <name>-<0..15> for snapshots.
func NewConfigMapInventoryPersist(store InventoryConfigMaps, namespace, name string) *ConfigMapInventoryPersist {
	return &ConfigMapInventoryPersist{store: store, namespace: namespace, name: name}
}

func (p *ConfigMapInventoryPersist) shardObject(environment string) string {
	return InventoryShardConfigMap(p.name, environment)
}

// Load returns each environment key. A missing index ConfigMap is an empty map.
// Rows that still hold JSON on the index (the previous combined object) load
// as they did before the split, so a hub upgrade does not drop the fleet.
func (p *ConfigMapInventoryPersist) Load(ctx context.Context) (map[string]persistedEnvironment, error) {
	if p == nil || p.store == nil || p.name == "" {
		return map[string]persistedEnvironment{}, nil
	}
	values, err := p.store.GetConfigMap(ctx, p.namespace, p.name)
	if err != nil {
		if errors.Is(err, kube.ErrConfigMapNotFound) {
			return map[string]persistedEnvironment{}, nil
		}

		return nil, err
	}
	out := make(map[string]persistedEnvironment, len(values))
	for name, body := range values {
		if !ValidEnvironmentName(name) {
			continue
		}
		rec, err := p.readEnvironment(ctx, name, body)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			continue
		}
		out[name] = *rec
	}

	return out, nil
}

func (p *ConfigMapInventoryPersist) readEnvironment(ctx context.Context, name, indexBody string) (*persistedEnvironment, error) {
	if rec, err := p.decodeFromObject(ctx, p.shardObject(name), name); err != nil {
		return nil, err
	} else if rec != nil {
		return rec, nil
	}
	// The chart Role can GET the index and prefix-0..15 only. A missing shard
	// is NotFound. Probing <prefix>-<environment> is Forbidden and would abort
	// Load before this combined-row decode, so that probe is gone.
	rec, ok := decodePersistedEnvironment(indexBody)
	if !ok {
		return nil, nil
	}
	rec.Heartbeat = persistableHeartbeat(rec.Heartbeat)

	return &rec, nil
}

func (p *ConfigMapInventoryPersist) decodeFromObject(ctx context.Context, object, environment string) (*persistedEnvironment, error) {
	values, err := p.store.GetConfigMap(ctx, p.namespace, object)
	if err != nil {
		if errors.Is(err, kube.ErrConfigMapNotFound) {
			return nil, nil
		}

		return nil, err
	}
	body := values[environment]
	if body == "" {
		body = values["cluster"]
	}
	rec, ok := decodePersistedEnvironment(body)
	if !ok {
		return nil, nil
	}
	rec.Heartbeat = persistableHeartbeat(rec.Heartbeat)

	return &rec, nil
}

func decodePersistedEnvironment(body string) (persistedEnvironment, bool) {
	var rec persistedEnvironment
	if err := json.Unmarshal([]byte(body), &rec); err != nil {
		return persistedEnvironment{}, false
	}

	return rec, true
}

// Save writes one environment snapshot to its shard ConfigMap, then records the
// name on the index. The shard is written first so a failed index update
// retries on the next beat instead of loading a marker with no body.
func (p *ConfigMapInventoryPersist) Save(ctx context.Context, name string, rec persistedEnvironment) error {
	if p == nil || p.store == nil || p.name == "" {
		return nil
	}
	rec.Heartbeat = persistableHeartbeat(rec.Heartbeat)
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := p.store.PutConfigMap(ctx, p.namespace, p.shardObject(name), map[string]string{
		name: string(body),
	}); err != nil {
		return err
	}

	return p.store.PutConfigMap(ctx, p.namespace, p.name, map[string]string{name: environmentIndexMarker})
}

// Remove drops the environment snapshot from its shard and its index row.
func (p *ConfigMapInventoryPersist) Remove(ctx context.Context, name string) error {
	if p == nil || p.store == nil || p.name == "" || name == "" {
		return nil
	}
	if err := p.store.DeleteConfigMapKeys(ctx, p.namespace, p.shardObject(name), []string{name}); err != nil {
		return err
	}

	return p.store.DeleteConfigMapKeys(ctx, p.namespace, p.name, []string{name})
}
