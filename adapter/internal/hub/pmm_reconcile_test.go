package hub

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// writeCountingStore records every commit the hub makes, and refuses the ones
// whose paths contain failOn.
type writeCountingStore struct {
	Store
	mu      sync.Mutex
	writes  int
	refused int
	failOn  string
}

func (w *writeCountingStore) Write(ctx context.Context, files map[string][]byte, message string) error {
	for path := range files {
		if w.failOn != "" && strings.Contains(path, w.failOn) {
			w.mu.Lock()
			w.refused++
			w.mu.Unlock()

			return fmt.Errorf("refused write of %s", path)
		}
	}
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()

	return w.Store.Write(ctx, files, message)
}

func (w *writeCountingStore) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.writes
}

// seedTreeBeforeAlertRules writes the PMM tree an environment joined before the
// alert rules has: no ConfigMap, no extra volumes, no pod annotation.
func seedTreeBeforeAlertRules(t *testing.T, store Store, environment string, in PMMSettings, chartVersion string) {
	t.Helper()
	ctx := context.Background()
	files, err := pmmFiles(environment, in, chartVersion, "", true)
	require.NoError(t, err)
	dir := pmmDir(environment)

	var release map[string]any
	require.NoError(t, yaml.Unmarshal(files[dir+"/helmrelease.yaml"], &release))
	values, _ := release["spec"].(map[string]any)["values"].(map[string]any)
	delete(values, "extraVolumes")
	delete(values, "extraVolumeMounts")
	delete(values, "podAnnotations")
	oldRelease, err := yaml.Marshal(release)
	require.NoError(t, err)
	oldIndex, err := kustomizeYAML([]string{"helmrepository.yaml", "helmrelease.yaml"})
	require.NoError(t, err)

	require.NoError(t, store.Write(ctx, map[string][]byte{
		dir + "/helmrepository.yaml": files[dir+"/helmrepository.yaml"],
		dir + "/helmrelease.yaml":    oldRelease,
		dir + "/kustomization.yaml":  oldIndex,
	}, "tree before alert rules"))
}

// reconcileHub builds a hub over store whose environments each reported the
// given beat. beat.Environment names the environment.
func reconcileHub(store Store, beats ...Heartbeat) *Service {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	inv := NewInventory(time.Minute)
	for _, beat := range beats {
		inv.Register(beat.Environment, "pds")
		beat.Namespace = "pds"
		beat.At = now
		inv.ApplyHeartbeat(beat)
	}

	return NewService(store, inv, Options{
		Namespace: "pds",
		Now:       func() time.Time { return now },
	})
}

func capable(environment string) Heartbeat {
	return Heartbeat{Environment: environment, MonitoringConfig: "pmm", AlertRules: true}
}

func TestReconcileGivesAnEnvironmentWithoutRulesTheRules(t *testing.T) {
	ctx := context.Background()
	store := &writeCountingStore{Store: NewMemoryStore()}
	seedTreeBeforeAlertRules(t, store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	store.writes = 0
	svc := reconcileHub(store, capable("spoke-a"))

	report := svc.ReconcileAlertRules(ctx)

	assert.Equal(t, []string{"spoke-a"}, report.Updated)
	assert.Empty(t, report.Failed)
	assert.Equal(t, 1, store.count())

	want, err := pmmFiles("spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion, "", true)
	require.NoError(t, err)
	for path, body := range want {
		got, err := store.Get(ctx, path)
		require.NoError(t, err, path)
		assert.Equal(t, string(body), string(got), path)
	}
}

func TestReconcileKeepsWhatAnOperatorChoseAndTheChartVersion(t *testing.T) {
	ctx := context.Background()
	store := &writeCountingStore{Store: NewMemoryStore()}
	chosen := PMMSettings{
		Access:        PMMAccessPrivate,
		SourceRanges:  []string{"10.0.0.0/8"},
		CPUMillicores: 2000,
		MemoryMi:      4096,
		StorageGi:     50,
	}
	seedTreeBeforeAlertRules(t, store, "spoke-a", chosen, "1.8.0")
	svc := reconcileHub(store, capable("spoke-a"))

	report := svc.ReconcileAlertRules(ctx)

	require.Equal(t, []string{"spoke-a"}, report.Updated)
	got, err := svc.PMMSettings(ctx, "spoke-a")
	require.NoError(t, err)
	assert.Equal(t, chosen, got)
	body, err := store.Get(ctx, pmmDir("spoke-a")+"/helmrelease.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), "version: 1.8.0")
}

func TestReconcileLeavesACurrentEnvironmentAlone(t *testing.T) {
	ctx := context.Background()
	store := &writeCountingStore{Store: NewMemoryStore()}
	seedTreeBeforeAlertRules(t, store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	svc := reconcileHub(store, capable("spoke-a"))
	require.Equal(t, []string{"spoke-a"}, svc.ReconcileAlertRules(ctx).Updated)
	after := store.count()

	again := reconcileHub(store, capable("spoke-a")).ReconcileAlertRules(ctx)

	assert.Empty(t, again.Updated)
	assert.Equal(t, []string{"spoke-a"}, again.Current)
	assert.Equal(t, after, store.count())
}

func TestReconcileSkipsAnEnvironmentWithMonitoringOff(t *testing.T) {
	ctx := context.Background()
	store := &writeCountingStore{Store: NewMemoryStore()}
	seedTreeBeforeAlertRules(t, store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	store.writes = 0
	off := capable("spoke-a")
	off.MonitoringConfig = ""
	svc := reconcileHub(store, off)

	report := svc.ReconcileAlertRules(ctx)

	assert.Equal(t, []string{"spoke-a"}, report.Skipped)
	assert.Empty(t, report.Updated)
	assert.Zero(t, store.count())
}

// The Role that lets Flux apply the ConfigMap is installed by the join command.
// An environment whose agent does not say it ran this release's command would
// have its whole platform tree refused, and PMM would mount a ConfigMap that
// was never created.
func TestReconcileSkipsAnEnvironmentThatHasNotRunTheCurrentJoin(t *testing.T) {
	ctx := context.Background()
	store := &writeCountingStore{Store: NewMemoryStore()}
	seedTreeBeforeAlertRules(t, store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	store.writes = 0
	old := capable("spoke-a")
	old.AlertRules = false
	svc := reconcileHub(store, old)

	report := svc.ReconcileAlertRules(ctx)

	assert.Equal(t, []string{"spoke-a"}, report.NeedsJoin)
	assert.Empty(t, report.Updated)
	assert.Zero(t, store.count())
}

func TestReconcileOneEnvironmentFailingDoesNotStopTheNext(t *testing.T) {
	ctx := context.Background()
	store := &writeCountingStore{Store: NewMemoryStore()}
	seedTreeBeforeAlertRules(t, store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	seedTreeBeforeAlertRules(t, store, "spoke-b", DefaultPMMSettings(), DefaultPMMChartVersion)
	store.failOn = "spoke-a"
	svc := reconcileHub(store, capable("spoke-a"), capable("spoke-b"))

	report := svc.ReconcileAlertRules(ctx)

	assert.Equal(t, []string{"spoke-a"}, report.Failed)
	assert.Equal(t, []string{"spoke-b"}, report.Updated)
}

func TestReconcileSkipsAnEnvironmentWithoutAPMMDefinition(t *testing.T) {
	store := &writeCountingStore{Store: NewMemoryStore()}
	svc := reconcileHub(store, capable("spoke-a"))

	report := svc.ReconcileAlertRules(context.Background())

	assert.Equal(t, []string{"spoke-a"}, report.Skipped)
	assert.Zero(t, store.count())
}

func TestReconcileRefusesToRewriteADefinitionItCannotRead(t *testing.T) {
	ctx := context.Background()
	store := &writeCountingStore{Store: NewMemoryStore()}
	require.NoError(t, store.Write(ctx, map[string][]byte{
		pmmDir("spoke-a") + "/helmrelease.yaml": []byte("spec: [not, a, mapping"),
	}, "hand edit"))
	store.writes = 0
	svc := reconcileHub(store, capable("spoke-a"))

	report := svc.ReconcileAlertRules(ctx)

	assert.Equal(t, []string{"spoke-a"}, report.Failed)
	assert.Zero(t, store.count())
}

// An environment that joins again after the hub started has no startup pass to
// wait for: its first beat from the current agent brings the rules.
func TestAFirstBeatFromACapableAgentBringsTheRules(t *testing.T) {
	ctx := context.Background()
	store := &writeCountingStore{Store: NewMemoryStore()}
	seedTreeBeforeAlertRules(t, store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	svc := reconcileHub(store, Heartbeat{Environment: "spoke-a"})
	store.writes = 0

	_, known, err := svc.AcceptHeartbeat(capable("spoke-a"), nil)
	require.NoError(t, err)
	require.True(t, known)
	svc.rulesWork.Wait()
	assert.Equal(t, 1, store.count())

	_, _, err = svc.AcceptHeartbeat(capable("spoke-a"), nil)
	require.NoError(t, err)
	svc.rulesWork.Wait()
	assert.Equal(t, 1, store.count(), "a later beat must not write again")
	_, err = store.Get(ctx, pmmDir("spoke-a")+"/alert-rules.yaml")
	require.NoError(t, err)
}

func TestABeatStillSucceedsWhenTheRulesCannotBeWritten(t *testing.T) {
	store := &writeCountingStore{Store: NewMemoryStore()}
	seedTreeBeforeAlertRules(t, store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	store.failOn = "spoke-a"
	svc := reconcileHub(store, Heartbeat{Environment: "spoke-a"})

	_, known, err := svc.AcceptHeartbeat(capable("spoke-a"), nil)

	require.NoError(t, err)
	assert.True(t, known)
	svc.rulesWork.Wait()
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, 1, store.refused, "the beat must have tried to write the rules")
}

func TestAFailedReconcileIsNotRetriedOnEveryBeat(t *testing.T) {
	store := &writeCountingStore{Store: NewMemoryStore()}
	seedTreeBeforeAlertRules(t, store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	store.failOn = "spoke-a"
	svc := reconcileHub(store, Heartbeat{Environment: "spoke-a"})

	for range 3 {
		_, _, err := svc.AcceptHeartbeat(capable("spoke-a"), nil)
		require.NoError(t, err)
		svc.rulesWork.Wait()
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, 1, store.refused)
}

// blockingStore holds every read until release is closed, as a slow Git does,
// and counts the reads that started, and the ones of an environment's PMM
// definition.
type blockingStore struct {
	Store
	release  chan struct{}
	mu       sync.Mutex
	reads    int
	releases int
}

func (b *blockingStore) Get(ctx context.Context, path string) ([]byte, error) {
	b.mu.Lock()
	b.reads++
	if strings.HasSuffix(path, "/helmrelease.yaml") {
		b.releases++
	}
	b.mu.Unlock()
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return b.Store.Get(ctx, path)
}

func (b *blockingStore) runs() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.releases
}

func (b *blockingStore) started() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.reads
}

func TestABeatReplyDoesNotWaitOnGitAndRunsOneReconcileAtATime(t *testing.T) {
	store := &blockingStore{Store: NewMemoryStore(), release: make(chan struct{})}
	seedTreeBeforeAlertRules(t, store.Store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	svc := reconcileHub(store, Heartbeat{Environment: "spoke-a"})

	replied := make(chan struct{})
	go func() {
		defer close(replied)
		_, known, err := svc.AcceptHeartbeat(capable("spoke-a"), nil)
		assert.NoError(t, err)
		assert.True(t, known)
	}()
	select {
	case <-replied:
	case <-time.After(5 * time.Second):
		close(store.release)
		t.Fatal("the beat reply waited on Git")
	}

	assert.Eventually(t, func() bool { return store.started() > 0 }, 5*time.Second, 5*time.Millisecond)
	svc.reconcileAlertRulesOnBeat("spoke-a")
	svc.reconcileAlertRulesOnBeat("spoke-a")

	// A second run would queue on the environment's lock and read every file
	// again once the first lets go. One run reads the definition once, or twice
	// when its comparison pass reaches that file before the one it writes, and
	// a run that finds the tree current reads it twice, so more than two reads
	// means a second run.
	close(store.release)
	svc.rulesWork.Wait()
	runs := store.runs()
	_, err := store.Get(context.Background(), pmmDir("spoke-a")+"/alert-rules.yaml")
	require.NoError(t, err)
	assert.LessOrEqual(t, runs, 2, "a run in flight must not start a second one")
}

func TestReconcileStopsWhenItsContextIsCancelled(t *testing.T) {
	store := &blockingStore{Store: NewMemoryStore(), release: make(chan struct{})}
	defer close(store.release)
	seedTreeBeforeAlertRules(t, store.Store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	seedTreeBeforeAlertRules(t, store.Store, "spoke-b", DefaultPMMSettings(), DefaultPMMChartVersion)
	svc := reconcileHub(store, capable("spoke-a"), capable("spoke-b"))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan AlertRulesReport)
	go func() { done <- svc.ReconcileAlertRules(ctx) }()
	assert.Eventually(t, func() bool { return store.started() > 0 }, 5*time.Second, 5*time.Millisecond)
	cancel()

	select {
	case report := <-done:
		assert.Len(t, report.Failed, 1, "the second environment is not attempted")
	case <-time.After(5 * time.Second):
		t.Fatal("the startup pass ignored its cancelled context")
	}
}

func settingsFiles(t *testing.T, store Store, environment string) (release, index []byte, rules bool) {
	t.Helper()
	ctx := context.Background()
	release, err := store.Get(ctx, pmmDir(environment)+"/helmrelease.yaml")
	require.NoError(t, err)
	index, err = store.Get(ctx, pmmDir(environment)+"/kustomization.yaml")
	require.NoError(t, err)
	_, err = store.Get(ctx, pmmDir(environment)+"/alert-rules.yaml")

	return release, index, err == nil
}

// Flux rejects a ConfigMap its Role cannot apply and drops the whole apply,
// the user's edit with it, so an environment whose agent never reported the
// mark must not get the rules from a settings change.
func TestAChangeToSettingsLeavesTheRulesOutForAnAgentWithoutTheMark(t *testing.T) {
	store := NewMemoryStore()
	seedTreeBeforeAlertRules(t, store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	svc := reconcileHub(store, Heartbeat{Environment: "spoke-a", MonitoringConfig: "pmm"})
	in := DefaultPMMSettings()
	in.StorageGi = 40

	require.NoError(t, svc.SetPMMSettings(context.Background(), "spoke-a", in))

	release, index, rules := settingsFiles(t, store, "spoke-a")
	assert.False(t, rules, "no ConfigMap file")
	assert.NotContains(t, string(index), "alert-rules.yaml")
	assert.NotContains(t, string(release), "pds-alert-rules")
	assert.NotContains(t, string(release), "pds.percona.com/alert-rules")
	got, err := parsePMMSettings(release)
	require.NoError(t, err)
	assert.Equal(t, 40, got.StorageGi)
}

func TestAChangeToSettingsKeepsTheRulesForAnAgentWithTheMark(t *testing.T) {
	store := NewMemoryStore()
	seedTreeBeforeAlertRules(t, store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	svc := reconcileHub(store, capable("spoke-a"))

	require.NoError(t, svc.SetPMMSettings(context.Background(), "spoke-a", DefaultPMMSettings()))

	release, index, rules := settingsFiles(t, store, "spoke-a")
	assert.True(t, rules)
	assert.Contains(t, string(index), "alert-rules.yaml")
	assert.Contains(t, string(release), "pds-alert-rules")
}

// The mark is read after the environment's lock is taken. A beat that records
// it while the settings call waits must be seen, or the call writes a tree
// without the rules over the one the beat's reconcile just wrote.
func TestASettingsCallWaitingOnTheLockSeesAMarkThatArrivedMeanwhile(t *testing.T) {
	store := NewMemoryStore()
	seedTreeBeforeAlertRules(t, store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	svc := reconcileHub(store, Heartbeat{Environment: "spoke-a", MonitoringConfig: "pmm"})
	unlock := svc.spokes.lock("spoke-a")

	done := make(chan error, 1)
	go func() { done <- svc.SetPMMSettings(context.Background(), "spoke-a", DefaultPMMSettings()) }()
	require.Eventually(t, func() bool {
		svc.spokes.mu.Lock()
		defer svc.spokes.mu.Unlock()

		return svc.spokes.held["spoke-a"].refs == 2
	}, 5*time.Second, time.Millisecond, "the call must be waiting on the lock")
	svc.inv.ApplyHeartbeat(Heartbeat{Environment: "spoke-a", Namespace: "pds", At: svc.now(), MonitoringConfig: "pmm", AlertRules: true})
	unlock()

	require.NoError(t, <-done)
	_, _, rules := settingsFiles(t, store, "spoke-a")
	assert.True(t, rules)
}

// done is only true while the tree holds the rules. A settings change rendered
// without them, or an agent that comes back from an older join command, must
// let the next beat with the mark reconcile again.
func TestRulesAreReconciledAgainAfterATreeWithoutThemWasWritten(t *testing.T) {
	ctx := context.Background()
	store := &writeCountingStore{Store: NewMemoryStore()}
	seedTreeBeforeAlertRules(t, store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	svc := reconcileHub(store, Heartbeat{Environment: "spoke-a"})
	_, _, err := svc.AcceptHeartbeat(capable("spoke-a"), nil)
	require.NoError(t, err)
	svc.rulesWork.Wait()
	_, _, rules := settingsFiles(t, store, "spoke-a")
	require.True(t, rules)

	old := Heartbeat{Environment: "spoke-a", MonitoringConfig: "pmm"}
	_, _, err = svc.AcceptHeartbeat(old, nil)
	require.NoError(t, err)
	in := DefaultPMMSettings()
	in.StorageGi = 40
	require.NoError(t, svc.SetPMMSettings(ctx, "spoke-a", in))
	release, index, _ := settingsFiles(t, store, "spoke-a")
	require.NotContains(t, string(index), "alert-rules.yaml", "the settings change was rendered without the rules")
	require.NotContains(t, string(release), "pds-alert-rules")

	_, _, err = svc.AcceptHeartbeat(capable("spoke-a"), nil)
	require.NoError(t, err)
	svc.rulesWork.Wait()

	release, index, _ = settingsFiles(t, store, "spoke-a")
	assert.Contains(t, string(index), "alert-rules.yaml", "the returning agent gets the rules back")
	assert.Contains(t, string(release), "pds-alert-rules")
	got, err := parsePMMSettings(release)
	require.NoError(t, err)
	assert.Equal(t, 40, got.StorageGi, "and keeps the settings change")
}

func TestABeatWithoutTheMarkMakesTheNextOneWithItReconcileAgain(t *testing.T) {
	store := &writeCountingStore{Store: NewMemoryStore()}
	seedTreeBeforeAlertRules(t, store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	svc := reconcileHub(store, Heartbeat{Environment: "spoke-a"})
	_, _, err := svc.AcceptHeartbeat(capable("spoke-a"), nil)
	require.NoError(t, err)
	svc.rulesWork.Wait()
	require.Equal(t, 1, store.count()-1)

	// The agent was reinstalled from an older join command and the tree was
	// rendered without the rules.
	require.NoError(t, store.Write(context.Background(), mustPMMFiles(t, "spoke-a", false), "rules removed"))
	before := store.count()
	_, _, err = svc.AcceptHeartbeat(Heartbeat{Environment: "spoke-a", MonitoringConfig: "pmm"}, nil)
	require.NoError(t, err)
	_, _, err = svc.AcceptHeartbeat(capable("spoke-a"), nil)
	require.NoError(t, err)
	svc.rulesWork.Wait()

	assert.Equal(t, before+1, store.count())
}

func mustPMMFiles(t *testing.T, environment string, withRules bool) map[string][]byte {
	t.Helper()
	files, err := pmmFiles(environment, DefaultPMMSettings(), DefaultPMMChartVersion, "", withRules)
	require.NoError(t, err)

	return files
}

// The background run belongs to the process: once its context ends, a run
// waiting on Git stops rather than holding the environment's lock.
func TestABeatReconcileStopsWithTheProcess(t *testing.T) {
	store := &blockingStore{Store: NewMemoryStore(), release: make(chan struct{})}
	defer close(store.release)
	seedTreeBeforeAlertRules(t, store.Store, "spoke-a", DefaultPMMSettings(), DefaultPMMChartVersion)
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	inv := NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	svc := NewService(store, inv, Options{Namespace: "pds", Now: func() time.Time { return now }, Context: ctx})
	svc.inv.ApplyHeartbeat(Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})

	_, _, err := svc.AcceptHeartbeat(capable("spoke-a"), nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return store.started() > 0 }, 5*time.Second, 5*time.Millisecond)
	cancel()

	finished := make(chan struct{})
	go func() { svc.rulesWork.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the beat reconcile ignored the process's context")
	}
}
