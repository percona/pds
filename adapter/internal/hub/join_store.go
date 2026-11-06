package hub

import (
	"context"
	"errors"

	"github.com/percona/pds/adapter/internal/client/kube"
)

// SecretKV reads and writes one Kubernetes Secret. The kube client is the
// production implementation; tests inject a map.
type SecretKV interface {
	GetSecret(ctx context.Context, namespace, name string) (map[string]string, error)
	PutSecret(ctx context.Context, namespace, name string, values map[string]string) error
	DeleteSecretKeys(ctx context.Context, namespace, name string, keys []string) error
}

// SecretTokenPersist stores join-token hashes in one Secret. Keys are SHA-256
// hex of the token; values are environment names. Plaintext never lands here.
type SecretTokenPersist struct {
	store     SecretKV
	namespace string
	name      string
}

// NewSecretTokenPersist writes hashes to the named Secret.
func NewSecretTokenPersist(store SecretKV, namespace, name string) *SecretTokenPersist {
	return &SecretTokenPersist{store: store, namespace: namespace, name: name}
}

// Load returns hash to grant. A missing Secret is an empty map.
func (p *SecretTokenPersist) Load(ctx context.Context) (map[string]Grant, error) {
	if p == nil || p.store == nil || p.name == "" {
		return map[string]Grant{}, nil
	}
	values, err := p.store.GetSecret(ctx, p.namespace, p.name)
	if err != nil {
		if errors.Is(err, kube.ErrSecretNotFound) {
			return map[string]Grant{}, nil
		}

		return nil, err
	}
	out := make(map[string]Grant, len(values))
	for hash, value := range values {
		if hash == "" {
			continue
		}
		grant, ok := decodeGrant(value)
		if !ok {
			continue
		}
		out[hash] = grant
	}

	return out, nil
}

// Save merges one hash to grant key onto the Secret.
func (p *SecretTokenPersist) Save(ctx context.Context, hash string, grant Grant) error {
	if p == nil || p.store == nil || p.name == "" {
		return nil
	}
	value, err := encodeGrant(grant)
	if err != nil {
		return err
	}

	return p.store.PutSecret(ctx, p.namespace, p.name, map[string]string{hash: value})
}

// Remove drops hashes from the Secret and leaves the rest.
func (p *SecretTokenPersist) Remove(ctx context.Context, hashes []string) error {
	if p == nil || p.store == nil || p.name == "" || len(hashes) == 0 {
		return nil
	}

	return p.store.DeleteSecretKeys(ctx, p.namespace, p.name, hashes)
}
