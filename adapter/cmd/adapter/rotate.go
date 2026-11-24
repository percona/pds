package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/mongodb"
)

// instanceReader is the OpenEverest call rotating credentials needs.
type instanceReader interface {
	GetInstance(ctx context.Context, namespace, name string) (*everest.Instance, error)
}

// secretPatcher is the Kubernetes call rotating credentials needs.
type secretPatcher interface {
	PatchSecret(ctx context.Context, namespace, name string, values map[string]string) error
}

// newRotateUsers gives a restored MongoDB database passwords of its own. The
// copy inherits its source's users Secret, so once the restore has finished
// the passwords in it are rewritten, and the operator updates the users to
// match. Before that nothing is written: the provider may still be copying the
// source's Secret over the copy's, and a rotation it undid would go unnoticed.
// A rotation that runs twice is harmless, as the second rewrite replaces the
// first.
func newRotateUsers(instances instanceReader, secrets secretPatcher) func(ctx context.Context, namespace, name string) error {
	return func(ctx context.Context, namespace, name string) error {
		instance, err := instances.GetInstance(ctx, namespace, name)
		if errors.Is(err, everest.ErrNotFound) {
			// Flux has not applied the copy yet, or it has been deleted and the
			// hub is about to drop this item.
			return engine.ErrRestoreInProgress
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if !instance.Status.DataSourceSeeded() {
			return engine.ErrRestoreInProgress
		}

		passwords, err := mongodb.RotatedPasswords()
		if err != nil {
			return fmt.Errorf("generate passwords for %s: %w", name, err)
		}
		if err := secrets.PatchSecret(ctx, namespace, mongodb.UsersSecretName(name), passwords); err != nil {
			return fmt.Errorf("rotate the passwords of %s: %w", name, err)
		}

		return nil
	}
}
