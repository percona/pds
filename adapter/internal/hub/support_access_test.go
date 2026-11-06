package hub_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/tailnet"
	"github.com/percona/pds/adapter/internal/client/tailnet/tailnettest"
	"github.com/percona/pds/adapter/internal/hub"
)

type documents map[string]string

func (d documents) SupportAccess(context.Context) (map[string]string, error) { return d, nil }

func (d documents) SaveSupportAccess(_ context.Context, environment, document string) error {
	d[environment] = document

	return nil
}

func (d documents) RemoveSupportAccess(_ context.Context, environment string) error {
	delete(d, environment)

	return nil
}

// disconnect is Disconnect for the tests that care only whether it failed.
func disconnect(ctx context.Context, svc *hub.Service, environment string) error {
	_, err := svc.Disconnect(ctx, environment)

	return err
}

func TestSupportRecordsSurviveTheDatabaseRoundTrip(t *testing.T) {
	persist := hub.NewDBSupportPersist(documents{})
	want := hub.SupportRecord{
		Address: "t7q2-private",
		Window: &hub.SupportWindowRecord{
			ID:    "win_1",
			KeyID: "key-1",
			SupportWindow: hub.SupportWindow{
				State: hub.SupportStateOpening, Level: hub.SupportLevelAdmin,
				ExpiresAt: time.Date(2026, 9, 8, 16, 0, 0, 0, time.UTC), CaseReference: "CS1",
			},
		},
	}
	require.NoError(t, persist.Save(t.Context(), "eks-prod", want))

	got, err := persist.Load(t.Context())

	require.NoError(t, err)
	assert.Equal(t, map[string]hub.SupportRecord{"eks-prod": want}, got)
}

// Reading a record it cannot parse as none would let a second window open over
// one that is still there.
func TestAnUnreadableSupportRecordIsAnErrorNotNone(t *testing.T) {
	persist := hub.NewDBSupportPersist(documents{"eks-prod": "{not json"})

	_, err := persist.Load(t.Context())

	require.Error(t, err)
}

func TestTheMemoryStoreHandsOutCopies(t *testing.T) {
	persist := hub.NewMemorySupportPersist()
	require.NoError(t, persist.Save(t.Context(), "eks-prod", hub.SupportRecord{
		Window: &hub.SupportWindowRecord{ID: "win_1"},
	}))
	got, err := persist.Load(t.Context())
	require.NoError(t, err)

	got["eks-prod"].Window.ID = "changed"

	again, err := persist.Load(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "win_1", again["eks-prod"].Window.ID)
}

// flakySupportPersist is a row store that can start refusing writes. A save
// takes saveTakes, as a database round trip does, and fails when its context
// ends first.
type flakySupportPersist struct {
	*hub.MemorySupportPersist
	failing   bool
	saveTakes time.Duration
}

func (f *flakySupportPersist) Save(ctx context.Context, environment string, rec hub.SupportRecord) error {
	if f.failing {
		return errors.New("the database is down")
	}
	select {
	case <-time.After(f.saveTakes):
		return f.MemorySupportPersist.Save(ctx, environment, rec)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// mintWorld is a hub with one window open on a fake tailnet.
func mintWorld(t *testing.T) (*hub.Service, *tailnettest.Server, *flakySupportPersist) {
	t.Helper()

	fake := tailnettest.New(t)
	client, err := tailnet.New(tailnet.Options{
		BaseURL: fake.URL, Tailnet: tailnettest.Tailnet, Domain: tailnettest.Domain,
		KeyClientID: tailnettest.KeyClientID, KeyClientSecret: tailnettest.KeyClientSecret,
		ServiceClientID: tailnettest.ServiceClientID, ServiceClientSecret: tailnettest.ServiceClientSecret,
		EnvironmentTag: "tag:pds-environment", ServiceTag: "tag:pds-support",
	})
	require.NoError(t, err)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv := hub.NewInventory(time.Hour)
	inv.Register("eks-prod", "pds")
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "eks-prod", Namespace: "pds", At: now})
	rows := &flakySupportPersist{MemorySupportPersist: hub.NewMemorySupportPersist()}
	svc := hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
		Now: func() time.Time { return now }, SupportAccess: rows, Tailnet: client,
	})
	_, err = svc.OpenSupportAccess(t.Context(), "eks-prod", hub.SupportOpen{Level: hub.SupportLevelReadOnly, Hours: 1})
	require.NoError(t, err)

	return svc, fake, rows
}

func TestMintingAgainRevokesTheKeyBeforeItAndKeepsOnlyTheNewID(t *testing.T) {
	svc, fake, rows := mintWorld(t)
	first, err := svc.MintSupportKey(t.Context(), "eks-prod")
	require.NoError(t, err)

	second, err := svc.MintSupportKey(t.Context(), "eks-prod")

	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	keys := fake.Keys()
	require.Len(t, keys, 2)
	assert.True(t, keys[0].Revoked)
	assert.False(t, keys[1].Revoked)
	stored, err := rows.Load(t.Context())
	require.NoError(t, err)
	assert.Equal(t, keys[1].ID, stored["eks-prod"].Window.KeyID)
}

func TestNoKeyIsMintedWithoutAnActiveWindow(t *testing.T) {
	svc, fake, _ := mintWorld(t)
	_, err := svc.EndSupportAccess(t.Context(), "eks-prod")
	require.NoError(t, err)

	_, err = svc.MintSupportKey(t.Context(), "eks-prod")

	require.ErrorIs(t, err, hub.ErrSupportNoWindow)
	assert.Empty(t, fake.Keys())
}

func TestNoKeyIsMintedWhenTheHubHasNoTailnet(t *testing.T) {
	inv := hub.NewInventory(time.Hour)
	inv.Register("eks-prod", "pds")
	svc := hub.NewService(hub.NewMemoryStore(), inv, hub.Options{})

	_, err := svc.MintSupportKey(t.Context(), "eks-prod")

	require.ErrorIs(t, err, hub.ErrSupportNetwork)
}

func TestAKeyWhoseIDCannotBeKeptIsRevokedAtOnce(t *testing.T) {
	svc, fake, rows := mintWorld(t)
	rows.failing = true

	_, err := svc.MintSupportKey(t.Context(), "eks-prod")

	require.ErrorIs(t, err, hub.ErrPersist)
	require.Len(t, fake.Keys(), 1)
	assert.True(t, fake.Keys()[0].Revoked)
}
