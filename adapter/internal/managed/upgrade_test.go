package managed_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/store"
)

const upgradeParams = `{"from":"0.1.0","version":"0.2.0"}`

func (l *lab) report(version, attempted, state string, at time.Time) {
	l.hub.mu.Lock()
	defer l.hub.mu.Unlock()
	l.hub.release = &hub.HeartbeatRelease{Version: version, Attempted: attempted, State: state, At: at}
}

func (l *lab) releaseInGit() string {
	l.hub.mu.Lock()
	defer l.hub.mu.Unlock()

	return string(l.hub.written["clusters/orders/platform/pds/helmrelease.yaml"])
}

func TestUpgradeCommitsTheNewVersionToGitAndWaitsForTheEnvironmentToRunIt(t *testing.T) {
	l := newLab(t)
	l.ready("0.1.0")
	l.report("0.2.0", "", hub.ReleaseReady, time.Now().Add(time.Hour))
	e := l.kindEngine(managed.UpgradeKind(l.deps))
	run := l.startWith(e, upgradeParams)

	l.work(e)

	got := l.run(run.ID)
	require.Equal(t, store.RunSucceeded, got.Status, got.Message)
	assert.Contains(t, l.releaseInGit(), "version: 0.2.0")
	rec := l.record()
	assert.Equal(t, "0.2.0", rec.ChartVersion)
	assert.Empty(t, rec.SkippedVersion)
	assert.Empty(t, l.cloud.calls, "a chart upgrade rides Git and Flux, so no AWS role is involved")
	assert.Empty(t, l.kube.access, "and the Kubernetes API is never opened")
}

func TestAFailedUpgradeIsRolledBackInGitAndTheVersionIsSkipped(t *testing.T) {
	l := newLab(t)
	l.ready("0.1.0")
	l.report("0.1.0", "0.2.0", hub.ReleaseRolledBack, time.Now().Add(time.Hour))
	e := l.kindEngine(managed.UpgradeKind(l.deps))
	run := l.startWith(e, upgradeParams)

	l.work(e)

	got := l.run(run.ID)
	require.Equal(t, store.RunFailed, got.Status)
	step := l.steps(run.ID)["apply-release"]
	assert.Equal(t, store.StepFailed, step.Status)
	assert.Equal(t, 1, step.Attempts, "a failed upgrade is attempted once")
	assert.Equal(t, "Upgrade failed, rolled back to 0.1.0. PDS skips this version until you retry or a newer one is published.", step.Failure)
	assert.Contains(t, l.releaseInGit(), "version: 0.1.0", "Git names the version the environment runs again")
	rec := l.record()
	assert.Equal(t, "0.1.0", rec.ChartVersion)
	assert.Equal(t, "0.2.0", rec.SkippedVersion)
}

func TestAReportFromBeforeTheCommitIsNotTakenForTheOutcome(t *testing.T) {
	l := newLab(t)
	l.ready("0.1.0")
	l.report("0.1.0", "0.2.0", hub.ReleaseRolledBack, time.Now().Add(-time.Hour))
	e := l.kindEngine(managed.UpgradeKind(l.deps))
	run := l.startWith(e, upgradeParams)

	l.work(e)

	step := l.steps(run.ID)["apply-release"]
	assert.Equal(t, store.StepFailed, step.Status)
	assert.Equal(t, "The upgrade has not finished. Retry to keep waiting.", step.Failure)
	assert.Empty(t, l.record().SkippedVersion, "an earlier attempt's rollback says nothing about this one")
}

func TestRetryCommitsAgainAndAcceptsTheEnvironmentRunningTheVersion(t *testing.T) {
	l := newLab(t)
	l.ready("0.1.0")
	e := l.kindEngine(managed.UpgradeKind(l.deps))
	run := l.startWith(e, upgradeParams)
	l.work(e)
	require.Equal(t, store.RunFailed, l.run(run.ID).Status)
	l.hub.mu.Lock()
	l.hub.written = nil
	l.hub.mu.Unlock()
	l.report("0.2.0", "", hub.ReleaseReady, time.Now().Add(time.Hour))

	require.NoError(t, e.Retry(t.Context(), l.team, l.env))
	l.work(e)

	assert.Equal(t, store.RunSucceeded, l.run(run.ID).Status)
	assert.Contains(t, l.releaseInGit(), "version: 0.2.0", "the retry commits the version again")
	assert.Equal(t, "0.2.0", l.record().ChartVersion)
}

func TestUpgradeRefusesRunParametersItCannotUse(t *testing.T) {
	l := newLab(t)
	l.ready("0.1.0")
	e := l.kindEngine(managed.UpgradeKind(l.deps))

	for _, params := range []string{"", "not json", `{"from":"0.1.0"}`, `{"version":"0.2.0"}`, `{"from":"0.1.0","version":"latest"}`} {
		run, err := e.StartWith(t.Context(), l.team, l.env, l.kind, params)
		require.NoError(t, err)
		l.work(e)

		got := l.run(run.ID)
		assert.Equal(t, store.RunFailed, got.Status, params)
		assert.Empty(t, l.hub.written, params, "nothing is committed for "+params)
		require.NoError(t, l.db.Gorm().WithContext(t.Context()).Model(&store.ProvisioningRun{}).Where("id = ?", run.ID).Update("status", store.RunCancelled).Error)
		assert.True(t, strings.Contains(l.steps(run.ID)["apply-release"].Failure, "Contact Percona support"), params)
	}
}
