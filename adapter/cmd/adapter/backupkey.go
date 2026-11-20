package main

import (
	"context"
	"errors"

	"github.com/percona/pds/adapter/internal/agent"
	"github.com/percona/pds/adapter/internal/client/backupkey"
	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/managed"
)

// backupSpec is the IAM user the release asked this agent to make.
func backupSpec(cfg config.AgentBackup) backupkey.Spec {
	return backupkey.Spec{
		Name: cfg.User, Path: cfg.Path, BoundaryARN: cfg.BoundaryARN, Bucket: cfg.Bucket, EnvironmentTag: cfg.Tag,
	}
}

// newBackupKey keeps this environment's backup key with the credentials the pod
// holds. It returns nil when the release asked for none.
func newBackupKey(ctx context.Context, cfg config.Config, environment *kube.Client) (func(context.Context) error, error) {
	if cfg.AgentBackup.User == "" || environment == nil {
		return nil, nil
	}
	users, err := backupkey.NewDefault(ctx, cfg.AgentBackup.Region)
	if err != nil {
		return nil, err
	}
	key := agent.BackupKey{
		Users:       users,
		Environment: kubeBackupEnvironment{client: environment, namespace: cfg.ConnectionsNamespace(), secret: cfg.AgentBackup.Secret},
		Spec:        backupSpec(cfg.AgentBackup),
	}

	return key.Reconcile, nil
}

// kubeBackupEnvironment is the agent's own namespace as the backup key sees it: the
// Secret the key is written to, and the ConfigMap the control plane asks for
// the user's removal through.
type kubeBackupEnvironment struct {
	client    *kube.Client
	namespace string
	secret    string
}

func (k kubeBackupEnvironment) Secret(ctx context.Context) (map[string]string, error) {
	values, err := k.client.GetSecret(ctx, k.namespace, k.secret)
	if errors.Is(err, kube.ErrSecretNotFound) {
		return nil, nil
	}

	return values, err
}

func (k kubeBackupEnvironment) WriteSecret(ctx context.Context, values map[string]string) error {
	return k.client.PutSecret(ctx, k.namespace, k.secret, values)
}

func (k kubeBackupEnvironment) Removal(ctx context.Context) (requested, done bool, err error) {
	values, err := k.client.GetConfigMap(ctx, k.namespace, managed.BackupUserRequest)
	if errors.Is(err, kube.ErrConfigMapNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}

	return values[managed.BackupUserRequestKey] == managed.BackupUserRemove, values[managed.BackupUserRemovedKey] == "true", nil
}

func (k kubeBackupEnvironment) MarkRemoved(ctx context.Context) error {
	return k.client.PutConfigMap(ctx, k.namespace, managed.BackupUserRequest, map[string]string{managed.BackupUserRemovedKey: "true"})
}
