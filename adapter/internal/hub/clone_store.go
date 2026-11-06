package hub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/percona/pds/adapter/internal/client/kube"
)

const cloneKeyPrefix = "clone."

// CloneKey is the OpenSSH identity Flux on one environment clones with.
type CloneKey struct {
	Identity string `json:"identity"`
	Pub      string `json:"pub"`
}

// CloneKeyPersist stores clone identities so a hub restart does not mint a
// new deploy key for an environment that already has one.
type CloneKeyPersist interface {
	Load(ctx context.Context) (map[string]CloneKey, error)
	Save(ctx context.Context, environment string, key CloneKey) error
	Remove(ctx context.Context, environment string) error
}

// SecretCloneKeyPersist stores clone identities in one Secret. Keys are
// clone.<environment> so they can share the join-token Secret.
type SecretCloneKeyPersist struct {
	store     SecretKV
	namespace string
	name      string
}

// NewSecretCloneKeyPersist writes identities to the named Secret.
func NewSecretCloneKeyPersist(store SecretKV, namespace, name string) *SecretCloneKeyPersist {
	return &SecretCloneKeyPersist{store: store, namespace: namespace, name: name}
}

// Load returns environment to clone key. A missing Secret is an empty map.
func (p *SecretCloneKeyPersist) Load(ctx context.Context) (map[string]CloneKey, error) {
	if p == nil || p.store == nil || p.name == "" {
		return map[string]CloneKey{}, nil
	}
	values, err := p.store.GetSecret(ctx, p.namespace, p.name)
	if err != nil {
		if errors.Is(err, kube.ErrSecretNotFound) {
			return map[string]CloneKey{}, nil
		}

		return nil, err
	}
	out := make(map[string]CloneKey, len(values))
	for key, value := range values {
		environment, ok := strings.CutPrefix(key, cloneKeyPrefix)
		if !ok || environment == "" || value == "" {
			continue
		}
		var stored CloneKey
		if err := json.Unmarshal([]byte(value), &stored); err != nil {
			continue
		}
		if stored.Identity == "" || stored.Pub == "" {
			continue
		}
		out[environment] = stored
	}

	return out, nil
}

// Save merges one environment identity onto the Secret.
func (p *SecretCloneKeyPersist) Save(ctx context.Context, environment string, key CloneKey) error {
	if p == nil || p.store == nil || p.name == "" || environment == "" {
		return nil
	}
	body, err := json.Marshal(key)
	if err != nil {
		return err
	}

	return p.store.PutSecret(ctx, p.namespace, p.name, map[string]string{cloneKeyPrefix + environment: string(body)})
}

// Remove drops one environment identity from the Secret.
func (p *SecretCloneKeyPersist) Remove(ctx context.Context, environment string) error {
	if p == nil || p.store == nil || p.name == "" || environment == "" {
		return nil
	}

	return p.store.DeleteSecretKeys(ctx, p.namespace, p.name, []string{cloneKeyPrefix + environment})
}
