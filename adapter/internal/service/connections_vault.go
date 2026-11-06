package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/percona/pds/adapter/internal/platform"
	"github.com/percona/pds/adapter/internal/store"
)

// ConnectionState is the team scoped database the outbound credentials live
// in. Values are sealed on the way in and opened on the way out, so a database
// dump on its own does not yield a usable key.
//
// These cannot be hashed the way join tokens are: the adapter replays the real
// ServiceNow, Anthropic and Cursor keys downstream, so it has to read them back.
type ConnectionState interface {
	Connection(ctx context.Context, name string) (map[string]string, error)
	SaveConnection(ctx context.Context, name string, values map[string]string) error
	RemoveConnection(ctx context.Context, name string) error
}

// Vault stores the outbound credentials in the database rather than in one
// Kubernetes Secret per integration, so two Organizations never share an object.
//
// It is a SecretStore, which is the seam the connections service already had,
// so nothing above it changed: the namespace argument is accepted and ignored,
// because the Organization is what separates one deployment's rows from
// another's.
type Vault struct {
	state func(context.Context) ConnectionState
}

// NewVault stores credentials through state.
//
// state is a function because the database connects in the background: the
// adapter serves throughout, reporting not ready, and the vault reports the
// same until there is somewhere to store a paste. It takes a context so a
// request opens that Team's Organization rather than a process pin.
func NewVault(state func(context.Context) ConnectionState) *Vault {
	return &Vault{state: state}
}

// GetSecret returns one credential's fields.
func (v *Vault) GetSecret(ctx context.Context, _, name string) (map[string]string, error) {
	state, err := v.ready(ctx, name)
	if err != nil {
		return nil, err
	}

	values, err := state.Connection(ctx, name)
	if err != nil {
		return nil, readError(name, err)
	}

	return values, nil
}

// readError names the failure a caller can act on. A row that will not open is
// a missing key, not a missing credential, and the two need different answers.
func readError(name string, err error) error {
	if errors.Is(err, store.ErrUnsealable) {
		return fmt.Errorf("%w: %s", ErrConnectionsUnreadable, name)
	}

	return fmt.Errorf("read the stored credential: %w", err)
}

// PutSecret merges fields onto one credential.
//
// Merging is what the Kubernetes Secret did, so a paste that leaves a field
// out keeps it rather than clearing it. Moving where the credential lives must
// not change what the connections panel does with it.
func (v *Vault) PutSecret(ctx context.Context, _, name string, values map[string]string) error {
	state, err := v.ready(ctx, name)
	if err != nil {
		return err
	}

	current, err := state.Connection(ctx, name)
	if err != nil {
		return readError(name, err)
	}

	merged := make(map[string]string, len(current)+len(values))
	for field, value := range current {
		merged[field] = value
	}
	for field, value := range values {
		merged[field] = value
	}

	if err := state.SaveConnection(ctx, name, merged); err != nil {
		return fmt.Errorf("store the credential: %w", err)
	}

	return nil
}

// DeleteSecretKeys removes named fields from one credential and leaves the
// rest. PutSecret only merges, so clearing the ServiceNow token needs a way
// to drop one field without recreating the row.
func (v *Vault) DeleteSecretKeys(ctx context.Context, _, name string, keys []string) error {
	state, err := v.ready(ctx, name)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}

	current, err := state.Connection(ctx, name)
	if err != nil {
		return readError(name, err)
	}

	removed := false
	for _, key := range keys {
		if _, ok := current[key]; ok {
			delete(current, key)
			removed = true
		}
	}
	if !removed {
		return nil
	}
	if len(current) == 0 {
		if err := state.RemoveConnection(ctx, name); err != nil {
			return fmt.Errorf("remove the stored credential: %w", err)
		}

		return nil
	}
	if err := state.SaveConnection(ctx, name, current); err != nil {
		return fmt.Errorf("store the credential: %w", err)
	}

	return nil
}

// DeleteSecret drops one credential.
func (v *Vault) DeleteSecret(ctx context.Context, _, name string) error {
	state, err := v.ready(ctx, name)
	if err != nil {
		return err
	}

	if err := state.RemoveConnection(ctx, name); err != nil {
		return fmt.Errorf("remove the stored credential: %w", err)
	}

	return nil
}

func (v *Vault) ready(ctx context.Context, name string) (ConnectionState, error) {
	if v == nil || v.state == nil {
		return nil, fmt.Errorf("%w: no credential store is configured", platform.ErrUnavailable)
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("a credential needs a name")
	}

	state := v.state(ctx)
	if state == nil {
		return nil, fmt.Errorf("%w: the adapter database is not connected yet", platform.ErrUnavailable)
	}

	return state, nil
}
