package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/percona/pds/adapter/internal/client/kube"
)

// WorkPersist stores the outbound queue so a hub restart does not drop work a
// spoke has not collected yet.
//
// A delete or a scale lives only in the queue between the click and the
// spoke's next beat. Push refuses the change when that write fails, so the
// UI cannot report success for work a restart would drop.
type WorkPersist interface {
	Load(ctx context.Context) (map[string][]Work, error)
	Save(ctx context.Context, environment string, items []Work) error
}

// ConfigMapWorkPersist keeps one JSON array per environment on a ConfigMap.
type ConfigMapWorkPersist struct {
	store     ConfigMapKV
	namespace string
	name      string
}

// NewConfigMapWorkPersist writes the queue to the named ConfigMap.
func NewConfigMapWorkPersist(store ConfigMapKV, namespace, name string) *ConfigMapWorkPersist {
	return &ConfigMapWorkPersist{store: store, namespace: namespace, name: name}
}

// Load returns the queue for every environment. A missing ConfigMap is an empty
// queue, which is what a hub starting for the first time has.
func (p *ConfigMapWorkPersist) Load(ctx context.Context) (map[string][]Work, error) {
	if p == nil || p.store == nil || p.name == "" {
		return map[string][]Work{}, nil
	}

	values, err := p.store.GetConfigMap(ctx, p.namespace, p.name)
	if err != nil {
		if errors.Is(err, kube.ErrConfigMapNotFound) {
			return map[string][]Work{}, nil
		}

		return nil, err
	}

	out := make(map[string][]Work, len(values))
	for environment, raw := range values {
		var items []Work
		// One unreadable environment must not cost every other environment its
		// queue. Log the skip so the missing work is visible in the hub
		// logs rather than only as an empty spoke.
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			slog.ErrorContext(ctx, "hub work queue environment skipped, persisted JSON is not valid",
				slog.String("environment", environment),
				slog.String("err", err.Error()))

			continue
		}
		if len(items) > 0 {
			out[environment] = items
		}
	}

	return out, nil
}

// Save replaces one environment's queue. An empty queue writes an empty array
// rather than removing the key, so a later read cannot mistake "drained" for
// "never seen".
func (p *ConfigMapWorkPersist) Save(ctx context.Context, environment string, items []Work) error {
	if p == nil || p.store == nil || p.name == "" || environment == "" {
		return nil
	}
	if items == nil {
		items = []Work{}
	}

	body, err := json.Marshal(items)
	if err != nil {
		return err
	}

	return p.store.PutConfigMap(ctx, p.namespace, p.name, map[string]string{environment: string(body)})
}
