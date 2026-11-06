package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/testutil"
)

// recordingSpy captures what the services hand the recorder, so a test can
// assert on the event rather than on the stored bytes.
type recordingSpy struct {
	mu     sync.Mutex
	events []service.ActivityEvent
}

func (r *recordingSpy) Record(_ context.Context, e service.ActivityEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, e)
}

func (r *recordingSpy) all() []service.ActivityEvent {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]service.ActivityEvent, len(r.events))
	copy(out, r.events)

	return out
}

func (r *recordingSpy) only(t *testing.T) service.ActivityEvent {
	t.Helper()

	events := r.all()
	require.Len(t, events, 1)

	return events[0]
}

func databaseWithRecorder(t *testing.T, stub *stubPlugin) (*service.Database, *recordingSpy) {
	t.Helper()

	registry := engine.NewRegistry()
	registry.Register(stub)
	spy := &recordingSpy{}

	return service.NewDatabase(registry, service.WithRecorder(spy)), spy
}

func recordedHub(t *testing.T) (*service.Database, *recordingSpy, *hub.Service) {
	t.Helper()

	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})
	h := hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
	})
	spy := &recordingSpy{}
	svc := service.NewDatabase(engine.NewRegistry(), service.WithHub(h), service.WithRecorder(spy))

	return svc, spy, h
}

func TestEveryMutationIsRecorded(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		call   func(*service.Database) error
		action string
		target string
		// outcome is empty for the operations whose result is known when the
		// request returns. Backup and restore finish later, so they are only
		// accepted at this point.
		outcome string
	}{
		{
			name: "scale",
			call: func(d *service.Database) error {
				return d.Scale(context.Background(), "pds.orders", engine.ScaleInput{})
			},
			action: service.ActionDatabaseScale,
			target: "orders",
		},
		{
			name: "backup",
			call: func(d *service.Database) error {
				_, err := d.Backup(context.Background(), "pds.orders")

				return err
			},
			action:  service.ActionDatabaseBackup,
			target:  "orders",
			outcome: service.OutcomeAccepted,
		},
		{
			name: "delete",
			call: func(d *service.Database) error {
				return d.Delete(context.Background(), "pds.orders")
			},
			action: service.ActionDatabaseDelete,
			target: "orders",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, spy := databaseWithRecorder(t, &stubPlugin{})
			require.NoError(t, tc.call(svc))

			want := tc.outcome
			if want == "" {
				want = service.OutcomeSucceeded
			}

			got := spy.only(t)
			assert.Equal(t, tc.action, got.Action)
			assert.Equal(t, tc.target, got.Target)
			assert.Equal(t, want, got.Outcome)
			assert.Empty(t, got.Error)
		})
	}
}

// A read must not land in the ring: the feed is about what changed.
func TestReadsAreNotRecorded(t *testing.T) {
	t.Parallel()

	svc, spy := databaseWithRecorder(t, &stubPlugin{list: []engine.Instance{{Name: "orders"}}})
	ctx := context.Background()

	_, err := svc.List(ctx)
	require.NoError(t, err)
	_, err = svc.Backups(ctx, "pds.orders")
	require.NoError(t, err)

	assert.Empty(t, spy.all())
}

func TestFailedMutationIsRecordedWithTheReason(t *testing.T) {
	t.Parallel()

	boom := errors.New("bucket permissions rejected the write")
	svc, spy := databaseWithRecorder(t, &stubPlugin{err: boom})

	_, err := svc.Backup(context.Background(), "pds.orders")
	require.ErrorIs(t, err, boom)

	got := spy.only(t)
	assert.Equal(t, service.ActionDatabaseBackup, got.Action)
	assert.Equal(t, service.OutcomeFailed, got.Outcome)
	assert.Contains(t, got.Error, "bucket permissions rejected the write")
}

// An adapter with no OpenEverest connection still records the attempt: a
// mutation that never reached the platform is exactly what a reader is looking
// for when they ask why nothing happened.
func TestAttemptWithoutAPluginIsRecorded(t *testing.T) {
	t.Parallel()

	spy := &recordingSpy{}
	svc := service.NewDatabase(engine.NewRegistry(), service.WithRecorder(spy))

	require.Error(t, svc.Delete(context.Background(), "pds.orders"))

	got := spy.only(t)
	assert.Equal(t, service.ActionDatabaseDelete, got.Action)
	assert.Equal(t, service.OutcomeFailed, got.Outcome)
}

func TestCreateDetailNamesTheProfileAndOverrides(t *testing.T) {
	t.Parallel()

	svc, spy, _ := recordedHub(t)
	cpu, storage := 4, 250

	_, err := svc.Create(context.Background(), engine.CreateInput{
		Name:        "orders",
		Engine:      "mysql",
		Profile:     "prod",
		Environment: "spoke-a",
		Overrides:   engine.ResourceOverrides{CPUCores: &cpu, StorageGB: &storage},
	})
	require.NoError(t, err)

	detail := spy.only(t).Detail
	assert.Contains(t, detail, "mysql")
	assert.Contains(t, detail, "prod")
	assert.Contains(t, detail, "4")
	assert.Contains(t, detail, "250")
}

func TestScaleDetailNamesOnlyWhatChanged(t *testing.T) {
	t.Parallel()

	svc, spy := databaseWithRecorder(t, &stubPlugin{})
	replicas := 3

	require.NoError(t, svc.Scale(context.Background(), "pds.orders", engine.ScaleInput{Replicas: &replicas}))

	detail := spy.only(t).Detail
	assert.Contains(t, detail, "replicas 3")
	assert.NotContains(t, detail, "cpu", "an untouched field is not part of the change")
}

// TestHubRestoreIsRecordedAgainstTheNewDatabase covers the row a developer
// reads afterwards. Recording the source made the log say the database that
// was deliberately left alone had been restored.
func TestHubRestoreIsRecordedAgainstTheNewDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	hubSvc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
	})
	// The hub only writes for an environment that has checked in.
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})
	created, err := hubSvc.Create(ctx, engine.CreateInput{
		Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.NoError(t, err)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", At: now,
		Instances: []engine.Instance{*created},
		Backups:   []engine.Backup{{ID: "orders-ok", Instance: created.Name, Status: engine.BackupSucceeded}},
	})

	spy := &recordingSpy{}
	svc := service.NewDatabase(engine.NewRegistry(), service.WithHub(hubSvc), service.WithRecorder(spy))

	_, err = svc.RestoreToNewDatabase(ctx, created.ID, "orders-ok", "orders-restore-1")
	require.NoError(t, err)

	got := spy.only(t)
	assert.Equal(t, service.ActionDatabaseRestore, got.Action)
	assert.Equal(t, "orders-restore-1", got.Target)
	assert.Equal(t, "orders-restore-1", got.ResourceName)
	// The detail behind the row is resolved from this id, so a row about the
	// copy that carried the source id opened the source database.
	assert.Equal(t, "spoke-a/pds.orders-restore-1", got.DatabaseID)
	assert.Contains(t, got.Detail, "orders-ok")
	assert.Equal(t, service.OutcomeAccepted, got.Outcome)
}

// A refused restore created nothing, so the row has to stay on the database
// the request was about rather than on an empty name.
func TestARefusedHubRestoreIsRecordedAgainstTheSource(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})
	hubSvc := hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
	})

	spy := &recordingSpy{}
	svc := service.NewDatabase(engine.NewRegistry(), service.WithHub(hubSvc), service.WithRecorder(spy))

	_, err := svc.RestoreToNewDatabase(ctx, "spoke-a/pds.orders", "orders-ok", "")
	require.Error(t, err)

	got := spy.only(t)
	assert.Equal(t, service.OutcomeFailed, got.Outcome)
	assert.Equal(t, "orders", got.Target)
	assert.Equal(t, "spoke-a/pds.orders", got.DatabaseID)
}

func TestRecordedEventCarriesTheDatabaseID(t *testing.T) {
	t.Parallel()

	svc, spy := databaseWithRecorder(t, &stubPlugin{})

	require.NoError(t, svc.Scale(context.Background(), "pds.orders", engine.ScaleInput{}))
	assert.Equal(t, "pds.orders", spy.only(t).DatabaseID)
}

func TestDatabaseServiceWorksWithoutARecorder(t *testing.T) {
	t.Parallel()

	registry := engine.NewRegistry()
	registry.Register(&stubPlugin{})

	// The recorder is optional, so nothing may panic when it is absent.
	assert.NotPanics(t, func() {
		_ = service.NewDatabase(registry).Delete(context.Background(), "pds.orders")
	})
}

func TestNameFromIDTakesTheNameHalf(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "orders", service.NameFromID("pds.orders"))
	assert.Equal(t, "orders", service.NameFromID("orders"), "a malformed id is its own name")
	assert.Empty(t, service.NameFromID(""))
}

// connectionStore is a SecretStore that accepts every write, so the test can
// watch what the recorder does rather than what Kubernetes would.
type connectionStore struct {
	deleteErr error
}

func (connectionStore) GetSecret(context.Context, string, string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (connectionStore) PutSecret(context.Context, string, string, map[string]string) error {
	return nil
}

func (s connectionStore) DeleteSecret(context.Context, string, string) error { return s.deleteErr }

func (connectionStore) DeleteSecretKeys(context.Context, string, string, []string) error {
	return nil
}

func TestConnectionChangeIsRecordedByKindOnly(t *testing.T) {
	t.Parallel()

	spy := &recordingSpy{}
	conns := service.NewConnections(connectionStore{}, service.ConnectionsOptions{},
		service.WithConnectionsRecorder(spy), service.WithConnectionsTransport(testutil.AcceptingProvider()))

	require.NoError(t, conns.PutServiceNow(context.Background(), service.ServiceNowInput{
		ClientToken: "tok-zzzz9999",
	}))

	got := spy.only(t)
	assert.Equal(t, service.ActionConnectionUpdate, got.Action)
	assert.Equal(t, "servicenow", got.Target)
	assert.Equal(t, service.OutcomeSucceeded, got.Outcome)

	// The whole point of recording the kind and nothing else.
	assert.Empty(t, got.Detail)
	for _, secret := range []string{"tok-zzzz9999"} {
		assert.NotContains(t, got.Detail, secret)
		assert.NotContains(t, got.Error, secret)
	}
}

// The end to end guard: a credential handed to the connections service must
// not be readable in the document the ring would write to the Kubernetes cluster.
func TestCredentialsNeverReachTheStoredRing(t *testing.T) {
	t.Parallel()

	store := newFakeActivityStore()
	activity := service.NewActivity(store, service.ActivityOptions{})
	conns := service.NewConnections(connectionStore{}, service.ConnectionsOptions{},
		service.WithConnectionsRecorder(activity), service.WithConnectionsTransport(testutil.AcceptingProvider()))

	ctx := context.Background()
	require.NoError(t, conns.PutAssistant(ctx, service.AssistantInput{APIKey: "sk-ant-secretvalue999"}))
	require.NoError(t, conns.PutCursor(ctx, service.CursorInput{APIKey: "sk-cursor-secretvalue888"}))

	stored := store.raw()
	require.NotEmpty(t, stored)
	assert.NotContains(t, stored, "sk-ant-secretvalue999")
	assert.NotContains(t, stored, "sk-cursor-secretvalue888")
	assert.Contains(t, stored, "assistant")
	assert.Contains(t, stored, "cursor")
}

func TestConnectionDeleteIsRecorded(t *testing.T) {
	t.Parallel()

	spy := &recordingSpy{}
	conns := service.NewConnections(connectionStore{}, service.ConnectionsOptions{},
		service.WithConnectionsRecorder(spy), service.WithConnectionsTransport(testutil.AcceptingProvider()))

	require.NoError(t, conns.Delete(context.Background(), service.ConnectionCursor))

	got := spy.only(t)
	assert.Equal(t, service.ActionConnectionDelete, got.Action)
	assert.Equal(t, "cursor", got.Target)
}

func TestFailedConnectionDeleteIsRecorded(t *testing.T) {
	t.Parallel()

	spy := &recordingSpy{}
	conns := service.NewConnections(connectionStore{deleteErr: errors.New("secrets is forbidden")},
		service.ConnectionsOptions{}, service.WithConnectionsRecorder(spy), service.WithConnectionsTransport(testutil.AcceptingProvider()))

	require.Error(t, conns.Delete(context.Background(), service.ConnectionCursor))

	got := spy.only(t)
	assert.Equal(t, service.OutcomeFailed, got.Outcome)
	assert.NotEmpty(t, got.Error)
}

// A rejected payload never reaches the Secret, so it must not claim to have
// changed a connection either.
func TestInvalidConnectionInputIsNotRecorded(t *testing.T) {
	t.Parallel()

	spy := &recordingSpy{}
	conns := service.NewConnections(connectionStore{}, service.ConnectionsOptions{},
		service.WithConnectionsRecorder(spy), service.WithConnectionsTransport(testutil.AcceptingProvider()))

	require.Error(t, conns.PutCursor(context.Background(), service.CursorInput{APIKey: "   "}))
	assert.Empty(t, spy.all())
}

// TestRecordedActionsPointAtTheObjectTheyMade is what the detail view follows.
// Without the pointer a backup row can only say a backup was asked for, never
// which backup, so the detail has nothing to read the outcome from.
func TestRecordedActionsPointAtTheObjectTheyMade(t *testing.T) {
	tests := []struct {
		name     string
		call     func(*service.Database) error
		wantKind string
		wantName string
	}{
		{
			name: "scale",
			call: func(d *service.Database) error {
				return d.Scale(context.Background(), "pds.orders", engine.ScaleInput{})
			},
			wantKind: service.ResourceInstance,
			wantName: "orders",
		},
		{
			name: "delete",
			call: func(d *service.Database) error {
				return d.Delete(context.Background(), "pds.orders")
			},
			wantKind: service.ResourceInstance,
			wantName: "orders",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			databases, spy := databaseWithRecorder(t, &stubPlugin{})

			require.NoError(t, tt.call(databases))

			event := spy.only(t)
			assert.Equal(t, tt.wantKind, event.ResourceKind)
			assert.Equal(t, tt.wantName, event.ResourceName)
		})
	}
}

// TestABackupRecordsTheBackupItStarted keeps the generated backup name, which
// exists only inside the plugin, on the row that started it.
func TestABackupRecordsTheBackupItStarted(t *testing.T) {
	stub := &stubPlugin{backup: &engine.Backup{ID: "orders-20260831041203"}}
	databases, spy := databaseWithRecorder(t, stub)

	_, err := databases.Backup(context.Background(), "pds.orders")
	require.NoError(t, err)

	event := spy.only(t)
	assert.Equal(t, service.ResourceBackup, event.ResourceKind)
	assert.Equal(t, "orders-20260831041203", event.ResourceName)
}

func TestCreateRecordsExternalAccess(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in   engine.CreateInput
		want string
	}{
		"private by default": {
			in:   engine.CreateInput{Name: "orders", Profile: "dev", Environment: "spoke-a"},
			want: "engine mysql, profile dev, private access",
		},
		"private": {
			in:   engine.CreateInput{Name: "orders", Profile: "dev", Environment: "spoke-a", Access: engine.AccessPrivate},
			want: "engine mysql, profile dev, private access",
		},
		"public with an allow list": {
			in: engine.CreateInput{
				Name: "orders", Profile: "dev", Environment: "spoke-a", Access: engine.AccessPublic, SourceRanges: []string{"10.0.0.0/8"},
			},
			want: "engine mysql, profile dev, public from 10.0.0.0/8",
		},
		"ranges as the spec carries them": {
			in: engine.CreateInput{
				Name: "orders", Profile: "dev", Environment: "spoke-a", Access: engine.AccessPublic, SourceRanges: []string{"203.0.113.7"},
			},
			want: "engine mysql, profile dev, public from 203.0.113.7/32",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			svc, spy, _ := recordedHub(t)
			_, err := svc.Create(context.Background(), tc.in)
			require.NoError(t, err)

			assert.Equal(t, tc.want, spy.only(t).Detail)
		})
	}
}

// The feed says what the schedule became, in the words the Backups tab uses,
// so a reader can tell an edit from turning it off.
func TestBackupScheduleDetail(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   *engine.ScheduleInput
		want string
	}{
		{"default", nil, "Daily at 02:00 UTC, keeps 7 copies"},
		{"off", &engine.ScheduleInput{Enabled: false}, "Turned off"},
		{"hourly", &engine.ScheduleInput{Enabled: true, Period: engine.PeriodHourly, Minute: 5, RetentionCopies: 1}, "Hourly at 5 minutes past, keeps 1 copy"},
		{"daily", &engine.ScheduleInput{Enabled: true, Period: engine.PeriodDaily, Minute: 30, Hour: 14, RetentionCopies: 7}, "Daily at 14:30 UTC, keeps 7 copies"},
		{"weekly", &engine.ScheduleInput{Enabled: true, Period: engine.PeriodWeekly, Hour: 3, Weekday: 0, RetentionCopies: 4}, "Weekly on Sunday at 03:00 UTC, keeps 4 copies"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, spy, h := recordedHub(t)
			_, err := h.Create(context.Background(), engine.CreateInput{
				Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
			})
			require.NoError(t, err)
			_, err = svc.SetBackupSchedule(context.Background(), "spoke-a/pds.orders", tc.in)
			require.NoError(t, err)

			assert.Equal(t, tc.want, spy.only(t).Detail)
		})
	}
}

// A repeated default request keeps a schedule that is already running, so the
// feed names that schedule rather than the daily default nobody got.
func TestBackupScheduleDetailNamesTheScheduleLeft(t *testing.T) {
	t.Parallel()

	svc, _, h := recordedHub(t)
	ctx := context.Background()
	_, err := h.Create(ctx, engine.CreateInput{
		Name: "orders", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.NoError(t, err)
	_, err = svc.SetBackupSchedule(ctx, "spoke-a/pds.orders", &engine.ScheduleInput{
		Enabled: true, Period: engine.PeriodWeekly, Hour: 3, Weekday: 1, RetentionCopies: 4,
	})
	require.NoError(t, err)

	spy := &recordingSpy{}
	svc = service.NewDatabase(engine.NewRegistry(), service.WithHub(h), service.WithRecorder(spy))
	_, err = svc.SetBackupSchedule(ctx, "spoke-a/pds.orders", nil)
	require.NoError(t, err)

	assert.Equal(t, "Weekly on Monday at 03:00 UTC, keeps 4 copies", spy.only(t).Detail)
}

func TestBackupScheduleDetailOfAFailedWriteNamesTheRequest(t *testing.T) {
	t.Parallel()

	svc, spy, _ := recordedHub(t)

	_, err := svc.SetBackupSchedule(context.Background(), "spoke-a/pds.orders", nil)
	require.Error(t, err)

	assert.Equal(t, "Daily at 02:00 UTC, keeps 7 copies", spy.only(t).Detail)
}
