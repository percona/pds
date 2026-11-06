package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/agent"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

type stubDatabases struct {
	backupNamed []string
}

func (s *stubDatabases) List(context.Context) ([]engine.Instance, error) { return nil, nil }

func (s *stubDatabases) Backups(context.Context, string) ([]engine.Backup, error) { return nil, nil }

func (s *stubDatabases) Backup(context.Context, string) (*engine.Backup, error) {
	return nil, nil
}

func (s *stubDatabases) BackupNamed(_ context.Context, id, backupID string) (*engine.Backup, error) {
	s.backupNamed = append(s.backupNamed, id+"|"+backupID)

	return &engine.Backup{ID: backupID, Status: engine.BackupPending}, nil
}

func (s *stubDatabases) Delete(context.Context, string) error { return nil }

func (s *stubDatabases) Scale(context.Context, string, engine.ScaleInput) error { return nil }

func TestBeatAppliesBackupWorkWithBackupID(t *testing.T) {
	dbs := &stubDatabases{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"work":[{"kind":"backup","database":"pds.orders-dev","backupId":"orders-dev-20260908120000"}]}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := agent.Run(ctx, agent.Options{
		HubURL:      srv.URL,
		Token:       "secret",
		Environment: "spoke-a",
		Namespace:   "pds",
		Interval:    time.Hour,
		Databases:   dbs,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"pds.orders-dev|orders-dev-20260908120000"}, dbs.backupNamed)
}

func TestBeatPostsJSON(t *testing.T) {
	var gotAuth string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		assert.Equal(t, "/api/internal/environments/spoke-a/heartbeat", r.URL.Path)
		var err error
		body, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"work":[]}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := agent.Run(ctx, agent.Options{
		HubURL:      srv.URL,
		Token:       "secret",
		Environment: "spoke-a",
		Namespace:   "pds",
		Interval:    time.Hour,
	})
	require.NoError(t, err)
	assert.Equal(t, "Bearer secret", gotAuth)

	var beat hub.Heartbeat
	require.NoError(t, json.Unmarshal(body, &beat))
	assert.Equal(t, "spoke-a", beat.Environment)
	assert.True(t, beat.PrivateAccess, "the hub keeps a public password only from an agent that tells private apart")
	assert.True(t, beat.AlertRules, "the hub writes the alert rules only for an agent whose join gave Flux the ConfigMap grant")
}

// listStub returns one instance carrying a real connection string, so a test
// can assert on what actually leaves the environment.
type listStub struct {
	stubDatabases
	instances []engine.Instance
}

func (s *listStub) List(context.Context) ([]engine.Instance, error) { return s.instances, nil }

// TestBeatSendsNoCredential is the wiring test behind stripCredentials. The
// helper had a unit test, but nothing put an instance with a URI through
// beat(), so removing the call from the payload kept the suite green while
// every spoke posted its passwords to the hub on each interval.
func TestBeatSendsNoCredential(t *testing.T) {
	const password = "s3cr3t-do-not-send"

	dbs := &listStub{instances: []engine.Instance{{
		ID:        "pds.orders-dev",
		Name:      "orders-dev",
		Namespace: "pds",
		Status:    engine.Status{Phase: engine.PhaseReady},
		Connection: &engine.Connection{
			Host:     "orders-dev-haproxy",
			Port:     3306,
			Username: "root",
			URI:      "mysql://root:" + password + "@orders-dev-haproxy:3306",
		},
	}}}

	bodies := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		select {
		case bodies <- string(raw):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"work":[]}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		require.NoError(t, agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Namespace:   "pds",
			Databases:   dbs,
			Interval:    time.Hour,
		}))
	}()

	var body string
	select {
	case body = <-bodies:
	case <-time.After(5 * time.Second):
		t.Fatal("the agent never posted a heartbeat")
	}
	cancel()

	assert.NotContains(t, body, password)
	assert.NotContains(t, body, "mysql://")

	// The parts that are not secret still travel, because the hub renders them.
	assert.Contains(t, body, "orders-dev-haproxy")
	assert.Contains(t, body, "root")
}

// TestBeatReportsWhatItDidWithTheWork covers the acknowledgement the hub
// needs. The hub holds an item until a beat reports on it, so an agent that
// stayed silent would be handed the same delete over and over.
func TestBeatReportsWhatItDidWithTheWork(t *testing.T) {
	dbs := &stubDatabases{}
	bodies := make(chan string, 4)
	replies := []string{
		`{"work":[{"id":"wrk_1","kind":"backup","database":"pds.orders-dev","backupId":"orders-dev-1"}]}`,
		`{"work":[]}`,
	}
	var sent int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		select {
		case bodies <- string(raw):
		default:
		}
		body := `{"work":[]}`
		if sent < len(replies) {
			body = replies[sent]
		}
		sent++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		require.NoError(t, agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Namespace:   "pds",
			Databases:   dbs,
			Interval:    20 * time.Millisecond,
		}))
	}()

	// The first beat carries no results and receives the work.
	first := waitForBody(t, bodies)
	assert.NotContains(t, first, `"results"`)

	// The next one reports on it by id.
	var second string
	for range 10 {
		second = waitForBody(t, bodies)
		if strings.Contains(second, "wrk_1") {
			break
		}
	}
	cancel()

	assert.Contains(t, second, `"id":"wrk_1"`, "the agent never acknowledged the work it ran")
	assert.Equal(t, []string{"pds.orders-dev|orders-dev-1"}, dbs.backupNamed)
}

func waitForBody(t *testing.T, bodies chan string) string {
	t.Helper()

	select {
	case body := <-bodies:
		return body
	case <-time.After(5 * time.Second):
		t.Fatal("the agent never posted a heartbeat")

		return ""
	}
}

// TestBeatKeepsTheHubViewWhenListingFails covers the transient Everest error.
// The hub replaces its whole snapshot of a spoke on each beat, so posting an
// empty instance list read as every database on that environment disappearing.
func TestBeatKeepsTheHubViewWhenListingFails(t *testing.T) {
	dbs := &flakyList{instances: []engine.Instance{{
		ID:        "pds.orders-dev",
		Name:      "orders-dev",
		Namespace: "pds",
		Status:    engine.Status{Phase: engine.PhaseReady},
	}}}

	bodies := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		select {
		case bodies <- string(raw):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"work":[]}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		require.NoError(t, agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Namespace:   "pds",
			Databases:   dbs,
			Interval:    20 * time.Millisecond,
		}))
	}()

	// The first beat lists successfully and carries the database.
	first := waitForBody(t, bodies)
	require.Contains(t, first, "orders-dev")

	// From here listing fails. Nothing further may go out claiming the
	// environment is empty.
	dbs.fail.Store(true)
	second := waitForBody(t, bodies)
	require.Contains(t, second, `"omitInstances":true`)
	assert.NotContains(t, second, "orders-dev",
		"a failed list must not replace the hub snapshot with an empty one")
	cancel()
}

func TestBeatOmitsInstancesWhenTheFirstListFails(t *testing.T) {
	dbs := &flakyList{}
	dbs.fail.Store(true)

	bodies := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		select {
		case bodies <- string(raw):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"work":[]}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		require.NoError(t, agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Namespace:   "pds",
			Databases:   dbs,
			Interval:    time.Hour,
		}))
	}()

	body := waitForBody(t, bodies)
	cancel()
	require.Contains(t, body, `"omitInstances":true`)
	assert.NotContains(t, body, `"instances":[{`)
}

func TestBeatKeepsBackupCacheWhenInstanceListFails(t *testing.T) {
	dbs := &flakyBackups{
		flakyList: flakyList{instances: []engine.Instance{{
			ID:        "pds.orders-dev",
			Name:      "orders-dev",
			Namespace: "pds",
			Status:    engine.Status{Phase: engine.PhaseReady},
		}}},
		backups: []engine.Backup{{
			ID:       "orders-dev-1",
			Status:   engine.BackupSucceeded,
			Instance: "pds.orders-dev",
		}},
	}

	bodies := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		select {
		case bodies <- string(raw):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"work":[]}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		require.NoError(t, agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Namespace:   "pds",
			Databases:   dbs,
			Interval:    20 * time.Millisecond,
		}))
	}()

	first := waitForBody(t, bodies)
	require.Contains(t, first, "orders-dev-1")

	dbs.fail.Store(true)
	second := waitForBody(t, bodies)
	require.Contains(t, second, `"omitInstances":true`)
	require.Contains(t, second, "orders-dev-1")

	dbs.fail.Store(false)
	dbs.failBackups.Store(true)
	third := waitForBody(t, bodies)
	require.Contains(t, third, "orders-dev-1")
	require.Contains(t, third, `"omitBackups":true`)
	cancel()
}

func TestBeatKeepsTheHubViewWhenBackupListingFails(t *testing.T) {
	dbs := &flakyBackups{
		flakyList: flakyList{instances: []engine.Instance{{
			ID:        "pds.orders-dev",
			Name:      "orders-dev",
			Namespace: "pds",
			Status:    engine.Status{Phase: engine.PhaseReady},
		}}},
		backups: []engine.Backup{{
			ID:       "orders-dev-1",
			Status:   engine.BackupSucceeded,
			Instance: "pds.orders-dev",
		}},
	}

	bodies := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		select {
		case bodies <- string(raw):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"work":[]}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		require.NoError(t, agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Namespace:   "pds",
			Databases:   dbs,
			Interval:    20 * time.Millisecond,
		}))
	}()

	first := waitForBody(t, bodies)
	require.Contains(t, first, "orders-dev")
	require.Contains(t, first, "orders-dev-1")

	dbs.failBackups.Store(true)
	second := waitForBody(t, bodies)
	require.Contains(t, second, "orders-dev")
	require.Contains(t, second, "orders-dev-1")
	require.Contains(t, second, `"omitBackups":true`)
	cancel()
}

func TestBeatReusesAnEmptyBackupCacheWhenListingFails(t *testing.T) {
	dbs := &flakyBackups{
		flakyList: flakyList{instances: []engine.Instance{{
			ID:        "pds.orders-dev",
			Name:      "orders-dev",
			Namespace: "pds",
			Status:    engine.Status{Phase: engine.PhaseReady},
		}}},
	}

	bodies := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		select {
		case bodies <- string(raw):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"work":[]}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		require.NoError(t, agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Namespace:   "pds",
			Databases:   dbs,
			Interval:    20 * time.Millisecond,
		}))
	}()

	first := waitForBody(t, bodies)
	require.Contains(t, first, "orders-dev")
	require.NotContains(t, first, "orders-dev-1")

	dbs.failBackups.Store(true)
	second := waitForBody(t, bodies)
	require.Contains(t, second, "orders-dev")
	require.NotContains(t, second, `"backups":[`)
	cancel()
}

func TestBeatOmitsBackupsWhenListingHasNeverSucceeded(t *testing.T) {
	dbs := &flakyBackups{
		flakyList: flakyList{instances: []engine.Instance{{
			ID:        "pds.orders-dev",
			Name:      "orders-dev",
			Namespace: "pds",
			Status:    engine.Status{Phase: engine.PhaseReady},
		}}},
		backups: []engine.Backup{{
			ID:       "orders-dev-1",
			Status:   engine.BackupSucceeded,
			Instance: "pds.orders-dev",
		}},
	}
	dbs.failBackups.Store(true)

	bodies := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		select {
		case bodies <- string(raw):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"work":[]}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		require.NoError(t, agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Namespace:   "pds",
			Databases:   dbs,
			Interval:    20 * time.Millisecond,
		}))
	}()

	// A backup list that has never succeeded must not keep the environment from
	// registering. The beat goes out with the backups omitted, which is what
	// the hub reads as "keep what you have" rather than "there are none".
	select {
	case body := <-bodies:
		require.Contains(t, body, `"omitBackups":true`,
			"beat carried backups it never managed to list")
		require.NotContains(t, body, "orders-dev-1",
			"beat carried a backup from a list that failed")
	case <-time.After(2 * time.Second):
		t.Fatal("no beat was posted, so the environment could never register")
	}
	cancel()
}

func TestBeatUsesANamespaceBackupList(t *testing.T) {
	dbs := &namespaceBackups{
		instances: []engine.Instance{{
			ID:        "pds.orders-dev",
			Name:      "orders-dev",
			Namespace: "pds",
			Status:    engine.Status{Phase: engine.PhaseReady},
		}},
		backups: []engine.Backup{{
			ID:       "orders-dev-1",
			Status:   engine.BackupSucceeded,
			Instance: "orders-dev",
		}},
	}

	bodies := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		select {
		case bodies <- string(raw):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"work":[]}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		require.NoError(t, agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Namespace:   "pds",
			Databases:   dbs,
			Interval:    time.Hour,
		}))
	}()

	body := waitForBody(t, bodies)
	cancel()
	assert.Equal(t, 1, dbs.listCalls)
	assert.Equal(t, 0, dbs.perInstance)
	require.Contains(t, body, "orders-dev-1")
}

type flakyList struct {
	stubDatabases
	instances []engine.Instance
	fail      atomic.Bool
}

func (s *flakyList) List(context.Context) ([]engine.Instance, error) {
	if s.fail.Load() {
		return nil, errors.New("everest is not answering")
	}

	return s.instances, nil
}

type flakyBackups struct {
	flakyList
	backups     []engine.Backup
	failBackups atomic.Bool
}

func (s *flakyBackups) Backups(context.Context, string) ([]engine.Backup, error) {
	if s.failBackups.Load() {
		return nil, errors.New("everest backups is not answering")
	}

	return s.backups, nil
}

type namespaceBackups struct {
	instances   []engine.Instance
	backups     []engine.Backup
	listCalls   int
	perInstance int
}

func (s *namespaceBackups) List(context.Context) ([]engine.Instance, error) {
	return s.instances, nil
}

func (s *namespaceBackups) Backups(context.Context, string) ([]engine.Backup, error) {
	s.perInstance++
	return nil, errors.New("per-instance backups should not run")
}

func (s *namespaceBackups) NamespaceBackups(context.Context, string) ([]engine.Backup, error) {
	s.listCalls++
	return s.backups, nil
}

func (s *namespaceBackups) Backup(context.Context, string) (*engine.Backup, error) {
	return nil, nil
}

func (s *namespaceBackups) BackupNamed(context.Context, string, string) (*engine.Backup, error) {
	return nil, nil
}

func (s *namespaceBackups) Delete(context.Context, string) error { return nil }

func (s *namespaceBackups) Scale(context.Context, string, engine.ScaleInput) error { return nil }

func waitReady(t *testing.T, ready *atomic.Bool, want bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if ready.Load() == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("ready stayed %v", !want)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestRunStaysUnreadyUntilTheHubAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ready := &atomic.Bool{}
	go func() {
		require.NoError(t, agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Interval:    20 * time.Millisecond,
			Ready:       ready,
		}))
	}()

	time.Sleep(80 * time.Millisecond)
	assert.False(t, ready.Load(), "a hub that has never answered must not report ready")
}

func TestRunBecomesReadyAfterTheFirstHeartbeat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"work":[]}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ready := &atomic.Bool{}
	go func() {
		require.NoError(t, agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Interval:    20 * time.Millisecond,
			Ready:       ready,
		}))
	}()

	waitReady(t, ready, true)
}

func TestRunAppliesTheFailureThresholdOnlyAfterJoining(t *testing.T) {
	var beats atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := beats.Add(1)
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"work":[]}`))

			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ready := &atomic.Bool{}
	go func() {
		require.NoError(t, agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Interval:    20 * time.Millisecond,
			Ready:       ready,
		}))
	}()

	waitReady(t, ready, true)
	waitReady(t, ready, false)
}
