package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/service"
)

// stubDetailDatabases answers the two reads a detail makes.
type stubDetailDatabases struct {
	instance   *engine.Instance
	backups    []engine.Backup
	lookupErr  error
	backupsErr error
	lookedUpID string
}

func (s *stubDetailDatabases) Lookup(_ context.Context, id string) (*engine.Instance, error) {
	s.lookedUpID = id
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}

	return s.instance, nil
}

func (s *stubDetailDatabases) Backups(_ context.Context, _ string) ([]engine.Backup, error) {
	if s.backupsErr != nil {
		return nil, s.backupsErr
	}

	return s.backups, nil
}

func recordedEvent(t *testing.T, e service.ActivityEvent) (*service.Activity, string) {
	t.Helper()

	ring := service.NewActivity(nil, service.ActivityOptions{})
	ring.Record(context.Background(), e)

	page, err := ring.List(context.Background(), service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)

	return ring, page.Entries[0].ID
}

func TestDetailOpensWithTheRecordedRequest(t *testing.T) {
	ring, id := recordedEvent(t, service.ActivityEvent{
		Action:       service.ActionDatabaseCreate,
		DatabaseID:   "pds.orders",
		Target:       "orders",
		Detail:       "engine mysql, profile dev",
		ResourceKind: service.ResourceInstance,
		ResourceName: "orders",
	})

	details := service.NewActivityDetails(ring, nil)
	got, err := details.Detail(context.Background(), id)
	require.NoError(t, err)

	assert.Equal(t, id, got.Event.ID)
	require.NotEmpty(t, got.Timeline)
	assert.Equal(t, service.TimelineRequest, got.Timeline[0].Source)
	assert.Equal(t, "Requested", got.Timeline[0].Reason)
	assert.Equal(t, "engine mysql, profile dev", got.Timeline[0].Message)
}

func TestDetailOfAnUnknownIDIsNotFound(t *testing.T) {
	ring := service.NewActivity(nil, service.ActivityOptions{})
	details := service.NewActivityDetails(ring, nil)

	_, err := details.Detail(context.Background(), "00000000000000000099")

	require.Error(t, err)
	assert.True(t, errors.Is(err, service.ErrActivityEventNotFound), "got %v", err)
}

// TestDetailReportsABackupThatFailedAfterTheRequestSucceeded is the case the
// feed alone cannot show: the row says succeeded because the request was
// accepted, and the backup died four minutes later.
func TestDetailReportsABackupThatFailedAfterTheRequestSucceeded(t *testing.T) {
	at := time.Date(2026, time.August, 31, 4, 12, 3, 0, time.UTC)
	ring, id := recordedEvent(t, service.ActivityEvent{
		At:           at,
		Action:       service.ActionDatabaseBackup,
		DatabaseID:   "spoke-a/pds.orders",
		Target:       "orders",
		ResourceKind: service.ResourceBackup,
		ResourceName: "orders-20260831041203",
	})

	databases := &stubDetailDatabases{backups: []engine.Backup{
		{ID: "orders-other", Status: engine.BackupSucceeded},
		{
			ID:         "orders-20260831041203",
			Status:     engine.BackupFailed,
			Message:    "exit code 137, agent terminated",
			CreatedAt:  at.Add(time.Second),
			FinishedAt: at.Add(2 * time.Minute),
		},
	}}
	got, err := service.NewActivityDetails(ring, databases).
		Detail(context.Background(), id)
	require.NoError(t, err)

	require.NotNil(t, got.Resource)
	assert.Equal(t, service.ResourceBackup, got.Resource.Kind)
	assert.Equal(t, "pds", got.Resource.Namespace)
	assert.Equal(t, engine.BackupFailed, got.Resource.State)
	assert.Equal(t, "exit code 137, agent terminated", got.Resource.Message)

	require.Len(t, got.Timeline, 3)
	assert.Equal(t, service.TimelineRequest, got.Timeline[0].Source)
	assert.Equal(t, service.TimelineResource, got.Timeline[1].Source)
	assert.Equal(t, "Failed", got.Timeline[2].Reason)
	assert.Equal(t, service.TimelineWarning, got.Timeline[2].Type)
}

func TestDetailFollowsTheDatabaseForAProvision(t *testing.T) {
	ring, id := recordedEvent(t, service.ActivityEvent{
		Action:       service.ActionDatabaseCreate,
		DatabaseID:   "pds.orders",
		Target:       "orders",
		ResourceKind: service.ResourceInstance,
		ResourceName: "orders",
	})

	databases := &stubDetailDatabases{instance: &engine.Instance{
		Name:      "orders",
		Namespace: "pds",
		Status:    engine.Status{Phase: engine.PhaseProvisioning, Message: "waiting for volumes"},
	}}
	got, err := service.NewActivityDetails(ring, databases).
		Detail(context.Background(), id)
	require.NoError(t, err)

	require.NotNil(t, got.Resource)
	assert.Equal(t, engine.PhaseProvisioning, got.Resource.State)
	assert.Equal(t, "waiting for volumes", got.Resource.Message)
	assert.Equal(t, "orders", got.Resource.Name)
}

// TestDetailOfAFailedRequestStopsAtTheRequest keeps the timeline honest: a
// request that never reached the platform created nothing to follow.
func TestDetailOfAFailedRequestStopsAtTheRequest(t *testing.T) {
	ring, id := recordedEvent(t, service.ActivityEvent{
		Action:     service.ActionDatabaseCreate,
		DatabaseID: "pds.orders",
		Target:     "orders",
		Outcome:    service.OutcomeFailed,
		Error:      "storage class does not exist",
	})

	got, err := service.NewActivityDetails(ring, &stubDetailDatabases{}).
		Detail(context.Background(), id)
	require.NoError(t, err)

	require.Len(t, got.Timeline, 1)
	assert.Equal(t, service.TimelineWarning, got.Timeline[0].Type)
	assert.Equal(t, "storage class does not exist", got.Timeline[0].Message)
	assert.Nil(t, got.Resource)
}

func TestDetailNotesASourceItCouldNotRead(t *testing.T) {
	ring, id := recordedEvent(t, service.ActivityEvent{
		Action:       service.ActionDatabaseCreate,
		DatabaseID:   "pds.orders",
		Target:       "orders",
		ResourceKind: service.ResourceInstance,
		ResourceName: "orders",
	})

	databases := &stubDetailDatabases{lookupErr: errors.New("platform down")}

	got, err := service.NewActivityDetails(ring, databases).
		Detail(context.Background(), id)
	require.NoError(t, err)

	require.Len(t, got.Notes, 1)
	assert.Contains(t, got.Notes[0], "could not be read")
}

// TestDetailSaysTheDatabaseIsGoneAfterADelete keeps a successful delete from
// reporting the missing database as a failure to read it.
func TestDetailSaysTheDatabaseIsGoneAfterADelete(t *testing.T) {
	ring, id := recordedEvent(t, service.ActivityEvent{
		Action:       service.ActionDatabaseDelete,
		DatabaseID:   "pds.orders",
		Target:       "orders",
		ResourceKind: service.ResourceInstance,
		ResourceName: "orders",
	})

	databases := &stubDetailDatabases{lookupErr: engine.ErrNotFound}

	got, err := service.NewActivityDetails(ring, databases).
		Detail(context.Background(), id)
	require.NoError(t, err)

	assert.Nil(t, got.Resource)
	require.NotEmpty(t, got.Notes)
	assert.Contains(t, got.Notes[0], "what the delete asked for")
}
