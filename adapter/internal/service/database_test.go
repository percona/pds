package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/service"
)

// TestEveryCallIsUnavailableWithoutAPlugin covers the deployment the HTTP layer
// answers 501 for: an adapter with no OpenEverest connection, so nothing was
// registered.
func TestEveryCallIsUnavailableWithoutAPlugin(t *testing.T) {
	t.Parallel()

	svc := service.NewDatabase(engine.NewRegistry())
	ctx := context.Background()

	calls := map[string]func() error{
		"create": func() error {
			_, err := svc.Create(ctx, engine.CreateInput{Name: "orders", Profile: "dev"})

			return err
		},
		"list": func() error {
			_, err := svc.List(ctx)

			return err
		},
		"get": func() error {
			_, err := svc.Get(ctx, "pds.orders")

			return err
		},
		"scale":  func() error { return svc.Scale(ctx, "pds.orders", engine.ScaleInput{}) },
		"delete": func() error { return svc.Delete(ctx, "pds.orders") },
		"set backup schedule": func() error {
			_, err := svc.SetBackupSchedule(ctx, "pds.orders", nil)

			return err
		},
		"backup": func() error {
			_, err := svc.Backup(ctx, "pds.orders")

			return err
		},
		"backups": func() error {
			_, err := svc.Backups(ctx, "pds.orders")

			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, call(), service.ErrEngineUnavailable)
		})
	}
}

// TestNilRegistryIsUnavailable guards the zero value: a service built by a
// caller that forgot the registry must not panic in a handler.
func TestNilRegistryIsUnavailable(t *testing.T) {
	t.Parallel()

	_, err := service.NewDatabase(nil).List(context.Background())
	require.ErrorIs(t, err, service.ErrEngineUnavailable)
}

// TestNilServiceIsUnavailable guards the nil service itself. An adapter with no
// OpenEverest connection hands a nil *Database to chat and the activity reader,
// which hold it inside an interface, so their own absent checks read as present
// and call straight through. Every method has to answer rather than dereference
// the receiver.
func TestNilServiceIsUnavailable(t *testing.T) {
	t.Parallel()

	var svc *service.Database
	ctx := context.Background()

	calls := map[string]func() error{
		"create": func() error {
			_, err := svc.Create(ctx, engine.CreateInput{Name: "orders", Profile: "dev"})

			return err
		},
		"list": func() error {
			_, err := svc.List(ctx)

			return err
		},
		"list environment": func() error {
			_, err := svc.ListEnvironment(ctx, "spoke-a")

			return err
		},
		"get": func() error {
			_, err := svc.Get(ctx, "pds.orders")

			return err
		},
		"lookup": func() error {
			_, err := svc.Lookup(ctx, "pds.orders")

			return err
		},
		"scale":  func() error { return svc.Scale(ctx, "pds.orders", engine.ScaleInput{}) },
		"delete": func() error { return svc.Delete(ctx, "pds.orders") },
		"set backup schedule": func() error {
			_, err := svc.SetBackupSchedule(ctx, "pds.orders", nil)

			return err
		},
		"backup": func() error {
			_, err := svc.Backup(ctx, "pds.orders")

			return err
		},
		"backups": func() error {
			_, err := svc.Backups(ctx, "pds.orders")

			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, call(), service.ErrEngineUnavailable)
		})
	}

	t.Run("hub", func(t *testing.T) {
		t.Parallel()

		got, err := svc.Hub(ctx)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("restore to a new database", func(t *testing.T) {
		t.Parallel()

		_, err := svc.RestoreToNewDatabase(ctx, "spoke-a/pds.orders", "backup-1", "orders-restore-1")
		require.ErrorIs(t, err, service.ErrEngineUnavailable)
	})
}

// TestWriteOnlyPluginIsUnavailable covers a plugin that implements the Plugin
// contract but not the read path. The HTTP layer needs both.
func TestWriteOnlyPluginIsUnavailable(t *testing.T) {
	t.Parallel()

	registry := engine.NewRegistry()
	registry.Register(writeOnlyPlugin{})

	_, err := service.NewDatabase(registry).List(context.Background())
	require.ErrorIs(t, err, service.ErrEngineUnavailable)
}

// TestCallsReachThePlugin proves the agent path is a pass through: list,
// backup, scale and delete hit the plugin and return its error as is.
func TestCallsReachThePlugin(t *testing.T) {
	t.Parallel()

	boom := errors.New("downstream is unhappy")
	stub := &stubPlugin{err: boom}

	registry := engine.NewRegistry()
	registry.Register(stub)

	svc := service.NewDatabase(registry)
	ctx := context.Background()

	_, err := svc.List(ctx)
	require.ErrorIs(t, err, boom)

	_, err = svc.Backup(ctx, "ns.orders")
	require.ErrorIs(t, err, boom)
	assert.Equal(t, "ns.orders", stub.lastID)

	require.ErrorIs(t, svc.Scale(ctx, "ns.orders", engine.ScaleInput{}), boom)
	assert.Equal(t, "ns.orders", stub.lastID)

	require.ErrorIs(t, svc.Delete(ctx, "ns.gone"), boom)
	assert.Equal(t, "ns.gone", stub.lastID)
}

// TestAgentDatabaseCallsStayOnThePlugin is the connected-environment path. Hub()
// is nil, so list, backup, scale and delete reach the engine plugin.
func TestAgentDatabaseCallsStayOnThePlugin(t *testing.T) {
	t.Parallel()

	stub := &stubPlugin{
		list:   []engine.Instance{{ID: "pds.orders", Name: "orders"}},
		backup: &engine.Backup{ID: "orders-1", Status: engine.BackupPending},
	}
	registry := engine.NewRegistry()
	registry.Register(stub)
	svc := service.NewDatabase(registry)
	ctx := context.Background()

	got, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "orders", got[0].Name)

	backup, err := svc.Backup(ctx, "pds.orders")
	require.NoError(t, err)
	assert.Equal(t, "orders-1", backup.ID)
	assert.Equal(t, "pds.orders", stub.lastID)

	require.NoError(t, svc.Scale(ctx, "pds.orders", engine.ScaleInput{}))
	assert.Equal(t, "pds.orders", stub.lastID)

	require.NoError(t, svc.Delete(ctx, "pds.orders"))
	assert.Equal(t, "pds.orders", stub.lastID)

	named, err := svc.BackupNamed(ctx, "pds.orders", "")
	require.NoError(t, err)
	assert.Equal(t, "orders-1", named.ID)

	named, err = svc.BackupNamed(ctx, "pds.orders", "orders-named")
	require.NoError(t, err)
	assert.Equal(t, "orders-1", named.ID)

	_, err = svc.NamespaceBackups(ctx, "pds")
	require.Error(t, err)

	namedPlugin := &backupNamedPlugin{stubPlugin: stub}
	registry = engine.NewRegistry()
	registry.Register(namedPlugin)
	svc = service.NewDatabase(registry)
	gotNamed, err := svc.BackupNamed(ctx, "pds.orders", "orders-named")
	require.NoError(t, err)
	assert.Equal(t, "orders-named", gotNamed.ID)
	listed, err := svc.NamespaceBackups(ctx, "pds")
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, "ns-1", listed[0].ID)
}

type backupNamedPlugin struct{ *stubPlugin }

func (p *backupNamedPlugin) BackupNamed(_ context.Context, _, backupID string) (*engine.Backup, error) {
	return &engine.Backup{ID: backupID}, nil
}

func (p *backupNamedPlugin) NamespaceBackups(context.Context, string) ([]engine.Backup, error) {
	return []engine.Backup{{ID: "ns-1"}}, nil
}

type namespaceLister struct {
	*stubPlugin
	backups []engine.Backup
}

func (p *namespaceLister) NamespaceBackups(context.Context, string) ([]engine.Backup, error) {
	return p.backups, nil
}

// Each plugin lists only the instances of its own provider, so a namespace
// holding both engines needs every plugin's list.
func TestNamespaceBackupsConcatenatesEngines(t *testing.T) {
	t.Parallel()

	older := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)
	registry := engine.NewRegistry()
	registry.Register(&namespaceLister{
		stubPlugin: &stubPlugin{engineID: "mysql"},
		backups:    []engine.Backup{{ID: "orders-1", Instance: "orders", CreatedAt: older}},
	})
	registry.Register(&namespaceLister{
		stubPlugin: &stubPlugin{engineID: "postgresql"},
		backups:    []engine.Backup{{ID: "reports-1", Instance: "reports", CreatedAt: older.Add(time.Hour)}},
	})

	got, err := service.NewDatabase(registry).NamespaceBackups(context.Background(), "pds")
	require.NoError(t, err)

	require.Len(t, got, 2)
	assert.Equal(t, "reports-1", got[0].ID, "newest first")
	assert.Equal(t, "orders-1", got[1].ID)
}

func TestListConcatenatesEngines(t *testing.T) {
	t.Parallel()

	mysql := &stubPlugin{
		engineID: "mysql",
		list:     []engine.Instance{{Name: "orders", Engine: "mysql"}},
	}
	postgres := &stubPlugin{
		engineID: "postgresql",
		list:     []engine.Instance{{Name: "reporting", Engine: "postgresql"}},
	}
	registry := engine.NewRegistry()
	registry.Register(mysql)
	registry.Register(postgres)

	got, err := service.NewDatabase(registry).List(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []string{"orders", "reporting"}, []string{got[0].Name, got[1].Name})
}

// stubPlugin records what it was called with and returns the configured success
// payload when err is nil; otherwise it fails the call.
type stubPlugin struct {
	engineID string
	err      error
	lastID   string
	list     []engine.Instance
	backup   *engine.Backup
}

func (p *stubPlugin) Engine() string {
	if p.engineID != "" {
		return p.engineID
	}

	return "mysql"
}

func (p *stubPlugin) Scale(_ context.Context, id string, _ engine.ScaleInput) error {
	p.lastID = id

	return p.err
}

func (p *stubPlugin) Backup(_ context.Context, id string) (*engine.Backup, error) {
	p.lastID = id
	if p.err != nil {
		return nil, p.err
	}

	return p.backup, nil
}

func (p *stubPlugin) Delete(_ context.Context, id string) error {
	p.lastID = id

	return p.err
}

func (p *stubPlugin) List(_ context.Context) ([]engine.Instance, error) {
	if p.err != nil {
		return nil, p.err
	}

	return p.list, nil
}

func (p *stubPlugin) Backups(_ context.Context, id string) ([]engine.Backup, error) {
	p.lastID = id

	return nil, p.err
}

// writeOnlyPlugin implements engine.Plugin without engine.Reader.
type writeOnlyPlugin struct{}

func (writeOnlyPlugin) Engine() string { return "mysql" }

func (writeOnlyPlugin) Scale(context.Context, string, engine.ScaleInput) error { return nil }

func (writeOnlyPlugin) Backup(context.Context, string) (*engine.Backup, error) { return nil, nil }

func (writeOnlyPlugin) Delete(context.Context, string) error { return nil }

func TestHubReturnsTheWiredService(t *testing.T) {
	t.Parallel()

	h := hub.NewService(hub.NewMemoryStore(), hub.NewInventory(0), hub.Options{})
	svc := service.NewDatabase(engine.NewRegistry(), service.WithHub(h))

	got, err := svc.Hub(context.Background())
	require.NoError(t, err)
	assert.Same(t, h, got)
}

func TestHubForReturnsDifferentServicesPerTeam(t *testing.T) {
	t.Parallel()

	acme := hub.NewService(hub.NewMemoryStore(), hub.NewInventory(0), hub.Options{})
	globex := hub.NewService(hub.NewMemoryStore(), hub.NewInventory(0), hub.Options{})
	svc := service.NewDatabase(engine.NewRegistry(), service.WithHubFor(func(ctx context.Context) (*hub.Service, error) {
		team, _ := auth.TeamFromContext(ctx)
		if team == 1 {
			return acme, nil
		}

		return globex, nil
	}))

	gotAcme, err := svc.Hub(auth.WithIdentity(t.Context(), identityIn(1)))
	require.NoError(t, err)
	gotGlobex, err := svc.Hub(auth.WithIdentity(t.Context(), identityIn(2)))
	require.NoError(t, err)

	assert.Same(t, acme, gotAcme)
	assert.Same(t, globex, gotGlobex)
	assert.NotSame(t, gotAcme, gotGlobex)
}

func TestHubSurfacesAFactoryError(t *testing.T) {
	t.Parallel()

	boom := errors.New("the hub could not start")
	svc := service.NewDatabase(engine.NewRegistry(), service.WithHubFor(func(context.Context) (*hub.Service, error) {
		return nil, boom
	}))

	got, err := svc.Hub(t.Context())
	assert.Nil(t, got)
	require.ErrorIs(t, err, boom)
}
