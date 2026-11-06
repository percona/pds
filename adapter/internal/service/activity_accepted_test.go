package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/service"
)

// A backup is requested now and finishes later. The request returning without
// an error means the operator accepted the job, not that the backup worked.
func TestABackupIsRecordedAsAcceptedNotSucceeded(t *testing.T) {
	t.Parallel()

	svc, spy := databaseWithRecorder(t, &stubPlugin{})
	_, err := svc.Backup(context.Background(), "pds.orders")
	require.NoError(t, err)

	got := spy.only(t)
	assert.Equal(t, service.ActionDatabaseBackup, got.Action)
	assert.Equal(t, service.OutcomeAccepted, got.Outcome,
		"the request was accepted; whether the backup worked is not known yet")
}

// A request the operator refused is still a failure, and that has not changed.
func TestARefusedBackupIsStillRecordedAsFailed(t *testing.T) {
	t.Parallel()

	svc, spy := databaseWithRecorder(t, &stubPlugin{err: errors.New("boom")})
	_, err := svc.Backup(context.Background(), "pds.orders")
	require.Error(t, err)

	got := spy.only(t)
	assert.Equal(t, service.OutcomeFailed, got.Outcome)
	assert.NotEmpty(t, got.Error)
}

// Delete returns when the desired state has been written, so its outcome is
// genuinely known at request time and must stay succeeded.
func TestASynchronousActionKeepsSucceeded(t *testing.T) {
	t.Parallel()

	svc, spy := databaseWithRecorder(t, &stubPlugin{})
	require.NoError(t, svc.Delete(context.Background(), "pds.orders"))

	assert.Equal(t, service.OutcomeSucceeded, spy.only(t).Outcome)
}

// Accepted is a distinct outcome, not a relabelling of succeeded, so a reader
// filtering the log can tell the two apart.
func TestAcceptedIsItsOwnOutcome(t *testing.T) {
	t.Parallel()

	assert.NotEqual(t, service.OutcomeSucceeded, service.OutcomeAccepted)
	assert.NotEqual(t, service.OutcomeFailed, service.OutcomeAccepted)
	var _ engine.CreateInput
}
