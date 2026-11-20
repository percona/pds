package api_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

func (h *managedHTTP) entries(t *testing.T) []service.ActivityEvent {
	t.Helper()

	page, err := h.activity.List(t.Context(), service.ActivityQuery{Limit: 100})
	require.NoError(t, err)

	return page.Entries
}

func (h *managedHTTP) only(t *testing.T) service.ActivityEvent {
	t.Helper()

	entries := h.entries(t)
	require.Len(t, entries, 1, "%+v", entries)

	return entries[0]
}

func TestCreatingAManagedEnvironmentIsRecordedAsAccepted(t *testing.T) {
	h := newManagedHTTP(t)

	h.create(t, "orders")

	got := h.only(t)
	assert.Equal(t, service.ActionEnvironmentCreate, got.Action)
	assert.Equal(t, "orders", got.Target)
	assert.Equal(t, "orders", got.Environment)
	assert.Equal(t, service.OutcomeAccepted, got.Outcome)
	assert.Equal(t, uint(3), got.UserID)
	assert.Equal(t, uint(7), got.TeamID)
}

func TestARefusedCreateIsRecordedAsFailedWithTheSentenceThePersonRead(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")

	rec := h.do(t, 0, http.MethodPost, "/api/cloud-connections/"+h.connection+"/environments", `{"name":"orders","region":"eu-west-1"}`)
	require.Equal(t, http.StatusConflict, rec.Code)

	got := h.entries(t)[0]
	assert.Equal(t, service.OutcomeFailed, got.Outcome)
	assert.Equal(t, "An environment with that name already exists in your team. Choose another name.", got.Error)
}

func TestEachRunAPersonStartsIsRecordedWithItsKind(t *testing.T) {
	for _, tc := range []struct{ body, action, detail string }{
		{`{"kind":"upgrade_pds"}`, service.ActionEnvironmentUpgrade, "Upgrade PDS"},
		{`{"kind":"upgrade_kubernetes","kubernetesVersion":"1.34"}`, service.ActionEnvironmentUpgrade, "Upgrade Kubernetes to 1.34"},
		{`{"kind":"upgrade_kubernetes"}`, service.ActionEnvironmentUpgrade, "Update the environment's template"},
		{`{"kind":"rebuild"}`, service.ActionEnvironmentRebuild, ""},
	} {
		h := newManagedHTTP(t)
		h.ready(t, "orders")
		before := len(h.entries(t))

		rec := h.do(t, 0, http.MethodPost, "/api/environments/orders/runs", tc.body)
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

		entries := h.entries(t)
		require.Len(t, entries, before+1)
		assert.Equal(t, tc.action, entries[0].Action, tc.body)
		assert.Equal(t, tc.detail, entries[0].Detail, tc.body)
		assert.Equal(t, service.OutcomeAccepted, entries[0].Outcome)
		assert.Equal(t, "orders", entries[0].Environment)
	}
}

func TestARunTheLifecycleRefusesIsRecordedAsFailed(t *testing.T) {
	h := newManagedHTTP(t)
	h.ready(t, "orders")
	h.life.errs["upgrade"] = managed.ErrNothingToUpgrade
	before := len(h.entries(t))

	h.do(t, 0, http.MethodPost, "/api/environments/orders/runs", `{"kind":"upgrade_pds"}`)

	entries := h.entries(t)
	require.Len(t, entries, before+1)
	assert.Equal(t, service.OutcomeFailed, entries[0].Outcome)
	assert.Equal(t, "This environment is up to date, so there is nothing to upgrade.", entries[0].Error)
}

func TestARetryIsRecorded(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")
	h.failRun(1, 0, store.StepRefusal{}, "Raise the quota, then retry.")

	rec := h.do(t, 0, http.MethodPost, "/api/environments/orders/runs/1/retry", "")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	got := h.entries(t)[0]
	assert.Equal(t, service.ActionEnvironmentRetry, got.Action)
	assert.Equal(t, service.OutcomeAccepted, got.Outcome)
}

func TestDeletingAndRemovingFromPDSOnlyAreRecordedApart(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")
	h.managed.runs[0].Status = store.RunSucceeded

	require.Equal(t, http.StatusAccepted, h.do(t, 0, http.MethodDelete, "/api/environments/orders", `{"confirm":"orders"}`).Code)
	deleted := h.entries(t)[0]
	assert.Equal(t, service.ActionEnvironmentDelete, deleted.Action)
	assert.Equal(t, service.OutcomeAccepted, deleted.Outcome)
	assert.Empty(t, deleted.Detail)

	h.cloudDB.Connections[h.connection].State = store.CloudUnreachable
	require.Equal(t, http.StatusNoContent, h.do(t, 0, http.MethodDelete, "/api/environments/orders", `{"confirm":"orders","scope":"pds_only"}`).Code)
	removed := h.entries(t)[0]
	assert.Equal(t, service.ActionEnvironmentDelete, removed.Action)
	assert.Equal(t, service.OutcomeSucceeded, removed.Outcome)
	assert.Equal(t, "Removed from PDS only. Everything in AWS stays.", removed.Detail)
}

func TestADeferralAndAMaintenanceWindowAreRecorded(t *testing.T) {
	h := newManagedHTTP(t)
	h.ready(t, "orders")
	before := len(h.entries(t))

	require.Equal(t, http.StatusOK, h.do(t, 0, http.MethodPost, "/api/environments/orders/upgrades/defer", `{"days":7}`).Code)
	require.Equal(t, http.StatusOK, h.do(t, 0, http.MethodPut, "/api/environments/orders/maintenance-window", `{"day":5,"hour":22,"timeZone":"Europe/Berlin"}`).Code)

	entries := h.entries(t)
	require.Len(t, entries, before+2)
	assert.Equal(t, service.ActionEnvironmentWindow, entries[0].Action)
	assert.Equal(t, "Fridays at 22:00 Europe/Berlin", entries[0].Detail)
	assert.Equal(t, service.OutcomeSucceeded, entries[0].Outcome)
	assert.Equal(t, service.ActionEnvironmentDefer, entries[1].Action)
	assert.Equal(t, "Deferred for 7 days", entries[1].Detail)
}

func TestRemovingAnAWSAccountIsRecordedByItsName(t *testing.T) {
	h := newManagedHTTP(t)

	rec := h.do(t, 0, http.MethodDelete, "/api/cloud-connections/"+h.connection, `{"confirm":"acme"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	got := h.only(t)
	assert.Equal(t, service.ActionCloudConnectionRemove, got.Action)
	assert.Equal(t, "acme", got.Target)
	assert.Equal(t, service.OutcomeSucceeded, got.Outcome)
	assert.Empty(t, got.Environment)
}

func TestAnotherTeamsProbeOfAnEnvironmentRecordsNothingInItsLog(t *testing.T) {
	h := newManagedHTTP(t)
	h.ready(t, "orders")
	before := len(h.entries(t))

	assert.Equal(t, http.StatusNotFound, h.do(t, 8, http.MethodPost, "/api/environments/orders/runs", `{"kind":"rebuild"}`).Code)

	assert.Len(t, h.entries(t), before)
}
