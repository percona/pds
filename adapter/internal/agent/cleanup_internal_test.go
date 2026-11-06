package agent

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

type cleanupCall struct{ namespace, name string }

// cleanupScript answers each cleanup call in order and repeats the last
// answer, so a test says how many passes find something and when it ends.
type cleanupScript struct {
	removed []int
	errs    []error
	calls   []cleanupCall
}

func (s *cleanupScript) run(_ context.Context, namespace, name string) (int, error) {
	i := len(s.calls)
	s.calls = append(s.calls, cleanupCall{namespace, name})

	var err error
	if len(s.errs) > 0 {
		err = s.errs[min(i, len(s.errs)-1)]
	}
	if len(s.removed) == 0 {
		return 0, err
	}

	return s.removed[min(i, len(s.removed)-1)], err
}

func cleanupOptions(script *cleanupScript) Options {
	return Options{Cleanup: script.run, cleanup: newCleanupState()}
}

func pg(name string) engine.Instance {
	return engine.Instance{Name: name, Namespace: "pds", Engine: "postgresql"}
}

func TestSweepRemovesTheSecretsOfADeletedPostgreSQLDatabase(t *testing.T) {
	script := &cleanupScript{removed: []int{3}}
	opts := cleanupOptions(script)
	ctx := context.Background()

	sweepDeleted(ctx, opts, []engine.Instance{pg("orders")})
	require.Empty(t, script.calls, "a database that is still listed keeps its Secrets")

	sweepDeleted(ctx, opts, nil)

	assert.Equal(t, []cleanupCall{{"pds", "orders"}}, script.calls)
}

// The operator recreates the Secrets a second after the finalizer removes
// them, so a pass that removed something is followed by another that must
// find nothing before the cleanup is done.
func TestSweepRunsAgainUntilAPassFindsNothing(t *testing.T) {
	script := &cleanupScript{removed: []int{3, 1, 0}}
	opts := cleanupOptions(script)
	ctx := context.Background()
	sweepDeleted(ctx, opts, []engine.Instance{pg("orders")})

	for range 6 {
		sweepDeleted(ctx, opts, nil)
	}

	assert.Len(t, script.calls, 3)
}

func TestSweepNeverTouchesARecreatedDatabase(t *testing.T) {
	script := &cleanupScript{removed: []int{1}}
	opts := cleanupOptions(script)
	ctx := context.Background()
	sweepDeleted(ctx, opts, []engine.Instance{pg("orders")})
	sweepDeleted(ctx, opts, nil)
	require.Len(t, script.calls, 1)

	sweepDeleted(ctx, opts, []engine.Instance{pg("orders")})
	sweepDeleted(ctx, opts, []engine.Instance{pg("orders")})

	assert.Len(t, script.calls, 1, "a database that exists again keeps the Secrets it was given")
}

func TestSweepIgnoresDatabasesOfOtherEngines(t *testing.T) {
	script := &cleanupScript{removed: []int{1}}
	opts := cleanupOptions(script)
	ctx := context.Background()
	mysql := engine.Instance{Name: "orders", Namespace: "pds", Engine: "mysql"}

	sweepDeleted(ctx, opts, []engine.Instance{mysql})
	sweepDeleted(ctx, opts, nil)

	assert.Empty(t, script.calls)
}

func TestSweepKeepsTheNamesOfTwoNamespacesApart(t *testing.T) {
	script := &cleanupScript{}
	opts := cleanupOptions(script)
	other := pg("orders")
	other.Namespace = "team-b"
	sweepDeleted(context.Background(), opts, []engine.Instance{pg("orders"), other})

	sweepDeleted(context.Background(), opts, []engine.Instance{other})

	assert.Equal(t, []cleanupCall{{"pds", "orders"}}, script.calls)
}

func TestSweepRetriesAfterAFailureAndGivesUpEventually(t *testing.T) {
	script := &cleanupScript{errs: []error{errors.New("secrets is forbidden")}}
	opts := cleanupOptions(script)
	ctx := context.Background()
	sweepDeleted(ctx, opts, []engine.Instance{pg("orders")})

	sweepDeleted(ctx, opts, nil)
	sweepDeleted(ctx, opts, nil)
	assert.Len(t, script.calls, 2, "a failed pass is tried again on the next beat")

	for range maxCleanupPasses * 2 {
		sweepDeleted(ctx, opts, nil)
	}
	assert.Len(t, script.calls, maxCleanupPasses)
}

func TestSweepWithoutACleanupHookDoesNothing(t *testing.T) {
	opts := Options{cleanup: newCleanupState()}

	assert.NotPanics(t, func() {
		sweepDeleted(context.Background(), opts, []engine.Instance{pg("orders")})
		sweepDeleted(context.Background(), opts, nil)
	})
}

type scriptedList struct {
	SpokeAPI
	lists [][]engine.Instance
	errs  []error
	n     int
}

func (s *scriptedList) List(context.Context) ([]engine.Instance, error) {
	i := s.n
	s.n++
	if i < len(s.errs) && s.errs[i] != nil {
		return nil, s.errs[i]
	}

	return s.lists[i], nil
}

func (s *scriptedList) Backups(context.Context, string) ([]engine.Backup, error) { return nil, nil }

func TestBeatSweepsOnlyAfterASuccessfulList(t *testing.T) {
	hubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(hubSrv.Close)

	script := &cleanupScript{removed: []int{1}}
	dbs := &scriptedList{
		lists: [][]engine.Instance{{pg("orders")}, nil, nil, nil},
		errs:  []error{nil, errors.New("everest is starting"), nil, nil},
	}
	opts := cleanupOptions(script)
	opts.HubURL = hubSrv.URL
	opts.Environment = "spoke-a"
	opts.Namespace = "pds"
	opts.Databases = dbs
	opts.HTTP = hubSrv.Client()
	opts.Logger = slog.Default()
	opts.MonitorTimeout = time.Second
	opts.lastBackups = new([]engine.Backup)
	opts.backupsListed = new(bool)
	opts.lastUsage = &map[string]time.Time{}
	opts.secretResults = new([]hub.SecretResult)
	opts.lastAddress = new(string)
	opts.monitoring = &monitoringSeen{}
	opts.lastMonitor = &map[string]monitorReading{}
	ctx := context.Background()

	_, err := beat(ctx, opts, nil)
	require.NoError(t, err)
	_, err = beat(ctx, opts, nil)
	require.NoError(t, err)
	assert.Empty(t, script.calls, "a list that failed says nothing about which databases are gone")

	_, err = beat(ctx, opts, nil)
	require.NoError(t, err)
	assert.Equal(t, []cleanupCall{{"pds", "orders"}}, script.calls)
}

func TestSweepSkipsADatabaseThatReappearedSinceTheList(t *testing.T) {
	script := &cleanupScript{removed: []int{1}}
	opts := cleanupOptions(script)
	opts.Databases = &scriptedList{lists: [][]engine.Instance{{pg("orders")}}}
	sweepDeleted(context.Background(), opts, []engine.Instance{pg("orders")})

	sweepDeleted(context.Background(), opts, nil)

	assert.Empty(t, script.calls, "created again between the list and the delete")
}

func TestSweepWaitsWhenItCannotConfirmTheDatabaseIsGone(t *testing.T) {
	script := &cleanupScript{removed: []int{1}}
	opts := cleanupOptions(script)
	opts.Databases = &scriptedList{
		lists: [][]engine.Instance{nil, nil},
		errs:  []error{errors.New("everest is starting"), nil},
	}
	sweepDeleted(context.Background(), opts, []engine.Instance{pg("orders")})

	sweepDeleted(context.Background(), opts, nil)
	assert.Empty(t, script.calls)

	sweepDeleted(context.Background(), opts, nil)
	assert.Len(t, script.calls, 1)
}
