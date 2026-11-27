package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

func TestAbandonCancelsWhatIsUnfinishedAndRecordsASucceededDelete(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	prov := db.Provisioning()
	now := time.Now().UTC()
	created, err := prov.Start(t.Context(), admin.TeamID, "orders", "create", []string{"assume-role"}, now)
	require.NoError(t, err)

	gone, err := prov.Abandon(t.Context(), admin.TeamID, "orders", "Removed from PDS only.", now)
	require.NoError(t, err)

	assert.Equal(t, store.KindDelete, gone.Kind)
	assert.Equal(t, store.RunSucceeded, gone.Status)
	assert.Equal(t, "Removed from PDS only.", gone.Message)
	got, err := prov.Run(t.Context(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, store.RunCancelled, got.Status)

	_, err = prov.Start(t.Context(), admin.TeamID, "orders", "create", []string{"assume-role"}, now)
	require.NoError(t, err, "the name is free for a new environment")
}

func TestAbandonRefusesWhileARunHoldsALiveLease(t *testing.T) {
	db, admin := newOrganizationWithAdmin(t)
	prov := db.Provisioning()
	now := time.Now().UTC()
	// Another test's queued run of the shared database must not be the one claimed.
	kind := fmt.Sprintf("create-%d", now.UnixNano())
	_, err := prov.Start(t.Context(), admin.TeamID, "orders", kind, []string{"assume-role"}, now)
	require.NoError(t, err)
	claimed, err := prov.Claim(t.Context(), "worker-1", []string{kind}, time.Minute, now)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	_, err = prov.Abandon(t.Context(), admin.TeamID, "orders", "Removed from PDS only.", now)

	require.ErrorIs(t, err, store.ErrRunActive)
}
