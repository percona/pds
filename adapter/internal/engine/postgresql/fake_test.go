package postgresql_test

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine/postgresql"
)

type fakeEverest struct {
	mu          sync.Mutex
	instances   map[string]*everest.Instance
	backups     map[string][]everest.Backup
	connections map[string]*everest.Connection
	calls       []string

	failWith       error
	failUpdateWith error
	// vanishedInstance answers 404 on its per-instance backup list, as an
	// instance deleted after it was listed does.
	vanishedInstance string
	createdPhase     string
}

func newFakeEverest() *fakeEverest {
	return &fakeEverest{
		instances:    make(map[string]*everest.Instance),
		backups:      make(map[string][]everest.Backup),
		connections:  make(map[string]*everest.Connection),
		createdPhase: everest.PhaseProvisioning,
	}
}

var _ postgresql.EverestClient = (*fakeEverest)(nil)

func key(namespace, name string) string { return namespace + "/" + name }

func clone(in *everest.Instance) *everest.Instance {
	body, err := json.Marshal(in)
	if err != nil {
		panic("fake everest: marshal instance: " + err.Error())
	}

	var out everest.Instance
	if err := json.Unmarshal(body, &out); err != nil {
		panic("fake everest: unmarshal instance: " + err.Error())
	}

	return &out
}

func (f *fakeEverest) record(call string) error {
	f.calls = append(f.calls, call)

	return f.failWith
}

func (f *fakeEverest) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]string, len(f.calls))
	copy(out, f.calls)

	return out
}

func (f *fakeEverest) resetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = nil
}

func (f *fakeEverest) stored(namespace, name string) *everest.Instance {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.instances[key(namespace, name)]
}

func (f *fakeEverest) CreateInstance(_ context.Context, namespace string, in *everest.Instance) (*everest.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.record("CreateInstance " + key(namespace, in.Metadata.Name)); err != nil {
		return nil, err
	}

	k := key(namespace, in.Metadata.Name)
	if _, exists := f.instances[k]; exists {
		return nil, &everest.APIError{
			Status: 500,
			Message: "instance with name '" + in.Metadata.Name +
				"' already exists in namespace '" + namespace + "'",
		}
	}

	stored := clone(in)
	created := time.Date(2026, time.August, 25, 13, 22, 1, 0, time.UTC)
	stored.Metadata.Namespace = namespace
	stored.Metadata.CreationTimestamp = &created
	stored.Status.Phase = f.createdPhase
	f.instances[k] = stored

	return clone(stored), nil
}

func (f *fakeEverest) ListInstances(_ context.Context, namespace string) ([]everest.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.record("ListInstances " + namespace); err != nil {
		return nil, err
	}

	out := make([]everest.Instance, 0, len(f.instances))
	prefix := namespace + "/"
	for k, in := range f.instances {
		if len(k) < len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		out = append(out, *clone(in))
	}

	return out, nil
}

func (f *fakeEverest) GetInstance(_ context.Context, namespace, name string) (*everest.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.record("GetInstance " + key(namespace, name)); err != nil {
		return nil, err
	}

	in, ok := f.instances[key(namespace, name)]
	if !ok {
		return nil, &everest.APIError{Status: 404, Message: "not found"}
	}

	return clone(in), nil
}

func (f *fakeEverest) UpdateInstance(_ context.Context, namespace, name string, in *everest.Instance) (*everest.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.record("UpdateInstance " + key(namespace, name)); err != nil {
		return nil, err
	}
	if f.failUpdateWith != nil {
		return nil, f.failUpdateWith
	}

	if _, ok := f.instances[key(namespace, name)]; !ok {
		return nil, &everest.APIError{Status: 404, Message: "not found"}
	}

	stored := clone(in)
	f.instances[key(namespace, name)] = stored

	return clone(stored), nil
}

func (f *fakeEverest) DeleteInstance(_ context.Context, namespace, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.record("DeleteInstance " + key(namespace, name)); err != nil {
		return err
	}

	k := key(namespace, name)
	if _, ok := f.instances[k]; !ok {
		return &everest.APIError{Status: 404, Message: "not found"}
	}
	delete(f.instances, k)

	return nil
}

func (f *fakeEverest) GetInstanceConnection(_ context.Context, namespace, name string) (*everest.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.record("GetInstanceConnection " + key(namespace, name)); err != nil {
		return nil, err
	}

	conn, ok := f.connections[key(namespace, name)]
	if !ok {
		return nil, &everest.APIError{Status: 404, Message: "not found"}
	}

	return conn, nil
}

func (f *fakeEverest) ListInstanceBackups(_ context.Context, namespace, name string) ([]everest.Backup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.record("ListInstanceBackups " + key(namespace, name)); err != nil {
		return nil, err
	}

	if _, ok := f.instances[key(namespace, name)]; !ok || name == f.vanishedInstance {
		return nil, &everest.APIError{Status: 404, Message: "not found"}
	}

	return f.backups[key(namespace, name)], nil
}

func (f *fakeEverest) CreateBackup(_ context.Context, namespace string, in *everest.Backup) (*everest.Backup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.record("CreateBackup " + key(namespace, in.Metadata.Name)); err != nil {
		return nil, err
	}

	k := key(namespace, in.Spec.InstanceName())
	// The API server refuses a name that is already taken, and the plugin
	// leans on that: a repeated import or a re-sent backup is meant to come
	// back as a conflict it can treat as success. A fake that appends
	// instead makes both look like they created a second object.
	for _, existing := range f.backups[k] {
		if existing.Metadata.Name == in.Metadata.Name {
			return nil, everest.ErrConflict
		}
	}
	stored := *in
	created := time.Date(2026, time.August, 25, 13, 22, 1, 0, time.UTC)
	stored.Metadata.CreationTimestamp = &created
	stored.Status.State = everest.BackupStatePending
	f.backups[k] = append(f.backups[k], stored)

	out := stored

	return &out, nil
}

func (f *fakeEverest) seedBackup(namespace, cluster, name, state string, size string, created time.Time) {
	f.seedBackupNamed(namespace, cluster, name, state, size, created, "")
}

func (f *fakeEverest) seedScheduledBackup(namespace, cluster, name, state string, size string, created time.Time) {
	f.seedBackupNamed(namespace, cluster, name, state, size, created, "daily")
}

func (f *fakeEverest) seedBackupNamed(namespace, cluster, name, state, size string, created time.Time, schedule string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	backup := everest.Backup{}
	backup.Metadata.Name = name
	backup.Metadata.Namespace = namespace
	backup.Metadata.CreationTimestamp = &created
	backup.Spec.Origin = everest.InstanceBackupOrigin(cluster)
	backup.Spec.StorageRef = everest.ObjectRef{Name: "backups"}
	backup.Spec.ScheduleName = schedule
	backup.Status.State = state
	backup.Status.StartedAt = &created
	if state == everest.BackupStateSucceeded {
		backup.Status.CompletedAt = &created
	}
	if size != "" {
		backup.Status.Size = &size
	}

	f.backups[key(namespace, cluster)] = append(f.backups[key(namespace, cluster)], backup)
}

func (f *fakeEverest) seedCredentials(namespace, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.connections[key(namespace, name)] = &everest.Connection{
		Username: "postgres",
		Password: "s3cr3t",
		Host:     name + "-pgbouncer." + namespace,
		Port:     5432,
	}
}

func (f *fakeEverest) seedStatus(namespace, name, hostname string, port int32, phase string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	stored, ok := f.instances[key(namespace, name)]
	if !ok {
		return
	}
	stored.Status.Phase = phase
	if hostname != "" {
		stored.Status.ConnectionSecretRef = &everest.ObjectRef{Name: name + "-conn"}
		f.connections[key(namespace, name)] = &everest.Connection{
			Username: "postgres",
			Password: "s3cr3t",
			Host:     hostname,
			Port:     everest.Port(port),
		}
	}
}
