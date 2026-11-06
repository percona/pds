package hub_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/tailnet"
	"github.com/percona/pds/adapter/internal/client/tailnet/tailnettest"
	"github.com/percona/pds/adapter/internal/hub"
)

const (
	sweepEnvironmentTag = "tag:pds-environment"
	sweepServiceTag     = "tag:pds-support"
)

// sweepWorld is a hub with a fake clock and tailnet, and a record of the
// windows it reported expired.
type sweepWorld struct {
	svc     *hub.Service
	fake    *tailnettest.Server
	client  *tailnet.Client
	rows    *hub.MemorySupportPersist
	mu      sync.Mutex
	now     time.Time
	expired []string
	// expiredCases are the case references the expired windows carried.
	expiredCases []string
}

func (w *sweepWorld) clock() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.now
}

func (w *sweepWorld) advance(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.now = w.now.Add(d)
}

func (w *sweepWorld) expiries() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return append([]string(nil), w.expired...)
}

func newSweepClient(t *testing.T, fake *tailnettest.Server) *tailnet.Client {
	t.Helper()

	client, err := tailnet.New(tailnet.Options{
		BaseURL: fake.URL, Tailnet: tailnettest.Tailnet, Domain: tailnettest.Domain,
		KeyClientID: tailnettest.KeyClientID, KeyClientSecret: tailnettest.KeyClientSecret,
		ServiceClientID: tailnettest.ServiceClientID, ServiceClientSecret: tailnettest.ServiceClientSecret,
		EnvironmentTag: sweepEnvironmentTag, ServiceTag: sweepServiceTag,
	})
	require.NoError(t, err)

	return client
}

// service builds a hub over the world's rows, as a restarted process would.
func (w *sweepWorld) service(t *testing.T) *hub.Service {
	t.Helper()

	inv := hub.NewInventory(1000 * time.Hour)
	inv.Register("eks-prod", "pds")
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "eks-prod", Namespace: "pds", At: w.clock()})

	return hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
		Now: w.clock, SupportAccess: w.rows, Tailnet: w.client,
		OnSupportExpired: func(_ context.Context, environment string, window hub.SupportWindow) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.expired = append(w.expired, environment)
			w.expiredCases = append(w.expiredCases, window.CaseReference)
		},
	})
}

// newSweepWorld opens a one hour window on eks-prod and joins its device.
func newSweepWorld(t *testing.T) *sweepWorld {
	t.Helper()

	fake := tailnettest.New(t)
	w := &sweepWorld{
		fake: fake, client: newSweepClient(t, fake), rows: hub.NewMemorySupportPersist(),
		now: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
	}
	w.svc = w.service(t)
	_, err := w.svc.OpenSupportAccess(t.Context(), "eks-prod", hub.SupportOpen{Level: hub.SupportLevelReadOnly, Hours: 1, CaseReference: "CS0012345"})
	require.NoError(t, err)
	_, err = w.svc.MintSupportKey(t.Context(), "eks-prod")
	require.NoError(t, err)
	fake.AddDevice("d-window", w.windowID(t), sweepEnvironmentTag)

	return w
}

func (w *sweepWorld) windowID(t *testing.T) string {
	t.Helper()

	rows, err := w.rows.Load(t.Context())
	require.NoError(t, err)
	require.NotNil(t, rows["eks-prod"].Window)

	return rows["eks-prod"].Window.ID
}

func TestAnOverdueWindowClosesItsDeviceIsDeletedAndOneExpireEntryIsWritten(t *testing.T) {
	w := newSweepWorld(t)
	w.advance(time.Hour + time.Second)

	w.svc.ExpireSupport(t.Context())
	w.svc.ExpireSupport(t.Context())

	assert.Empty(t, w.fake.Devices())
	assert.True(t, w.fake.Keys()[len(w.fake.Keys())-1].Revoked)
	assert.Equal(t, []string{"eks-prod"}, w.expiries())
	assert.Equal(t, []string{"CS0012345"}, w.expiredCases, "the case is told which window ran out")
	rows, err := w.rows.Load(t.Context())
	require.NoError(t, err)
	assert.Equal(t, hub.SupportStateClosed, rows["eks-prod"].Window.State)
	assert.Equal(t, hub.SupportClosedExpired, rows["eks-prod"].Window.ClosedReason)
	assert.Empty(t, rows["eks-prod"].Window.KeyID)
}

func TestAHubStartedAfterTheWindowRanOutClosesItAtBoot(t *testing.T) {
	w := newSweepWorld(t)
	w.advance(3 * time.Hour)
	rebooted := w.service(t)

	rebooted.ExpireSupport(t.Context())

	assert.Empty(t, w.fake.Devices())
	assert.Equal(t, []string{"eks-prod"}, w.expiries())
}

func TestAWindowStillInsideItsTimeIsLeftAlone(t *testing.T) {
	w := newSweepWorld(t)
	w.advance(59 * time.Minute)

	w.svc.ExpireSupport(t.Context())

	assert.Len(t, w.fake.Devices(), 1)
	assert.Empty(t, w.expiries())
}

func TestAWindowTheCustomerEndedIsNotExpiredAgain(t *testing.T) {
	w := newSweepWorld(t)
	_, err := w.svc.EndSupportAccess(t.Context(), "eks-prod")
	require.NoError(t, err)
	w.advance(2 * time.Hour)

	w.svc.ExpireSupport(t.Context())

	assert.Empty(t, w.expiries())
}

func TestAnExpiryTheNetworkRefusedIsTriedAgainAndWrittenOnceWhenItWorks(t *testing.T) {
	w := newSweepWorld(t)
	w.advance(2 * time.Hour)
	w.fake.Fail()

	w.svc.ExpireSupport(t.Context())

	assert.Empty(t, w.expiries())
	assert.Len(t, w.fake.Devices(), 1)

	w.fake.Recover()
	w.svc.ExpireSupport(t.Context())
	w.svc.ExpireSupport(t.Context())

	assert.Empty(t, w.fake.Devices())
	assert.Equal(t, []string{"eks-prod"}, w.expiries())
}

// strayWorld is a tailnet with one live window, one closed window and one
// address in use.
type strayWorld struct {
	*sweepWorld
	sweeper *hub.StraySupportSweeper
}

func newStrayWorld(t *testing.T) *strayWorld {
	t.Helper()

	w := newSweepWorld(t)
	census := func(ctx context.Context) ([]hub.SupportRecord, error) {
		rows, err := w.rows.Load(ctx)
		if err != nil {
			return nil, err
		}
		var out []hub.SupportRecord
		for _, rec := range rows {
			out = append(out, rec)
		}

		return out, nil
	}

	return &strayWorld{sweepWorld: w, sweeper: hub.NewStraySupportSweeper(w.client, census, w.clock)}
}

func (w *sweepWorld) addressOf(t *testing.T) string {
	t.Helper()

	rows, err := w.rows.Load(t.Context())
	require.NoError(t, err)

	return rows["eks-prod"].Address
}

func TestADeviceOfALiveWindowStaysAndEveryOtherEnvironmentDeviceGoes(t *testing.T) {
	w := newStrayWorld(t)
	w.fake.AddDevice("d-old", "win_closed_long_ago", sweepEnvironmentTag)
	w.fake.AddDevice("d-hand", "someones-laptop", sweepEnvironmentTag)
	w.fake.AddDevice("d-unrelated", "win_unrelated_tag", "tag:other")

	require.NoError(t, w.sweeper.Sweep(t.Context()))

	assert.ElementsMatch(t, []string{"d-window", "d-unrelated"}, deviceIDs(w.fake))
}

func TestADeviceOfAWindowThatRanOutIsStrayEvenBeforeAnythingSweptTheWindow(t *testing.T) {
	w := newStrayWorld(t)
	w.advance(2 * time.Hour)

	require.NoError(t, w.sweeper.Sweep(t.Context()))

	assert.Empty(t, w.fake.Devices())
}

func TestAnAddressNoEnvironmentRecordHoldsIsDeletedAndTheOneInUseStays(t *testing.T) {
	w := newStrayWorld(t)
	w.fake.SetService(tailnettest.Service{Name: "svc:pds-orphan", Tags: []string{sweepServiceTag}, Ports: []string{"tcp:443"}})
	w.fake.SetService(tailnettest.Service{Name: "svc:not-ours", Tags: []string{sweepServiceTag}, Ports: []string{"tcp:443"}})

	require.NoError(t, w.sweeper.Sweep(t.Context()))

	var names []string
	for _, svc := range w.fake.Services() {
		names = append(names, svc.Name)
	}
	assert.ElementsMatch(t, []string{w.addressOf(t), "svc:not-ours"}, names)
}

func TestASweepThatCannotReadTheRecordsDeletesNothing(t *testing.T) {
	w := newStrayWorld(t)
	w.fake.AddDevice("d-hand", "someones-laptop", sweepEnvironmentTag)
	w.fake.SetService(tailnettest.Service{Name: "svc:pds-orphan", Tags: []string{sweepServiceTag}, Ports: []string{"tcp:443"}})
	unreadable := hub.NewStraySupportSweeper(w.client, func(context.Context) ([]hub.SupportRecord, error) {
		return nil, assert.AnError
	}, w.clock)

	require.Error(t, unreadable.Sweep(t.Context()))

	assert.Len(t, w.fake.Devices(), 2)
	assert.Len(t, w.fake.Services(), 2)
}

func deviceIDs(fake *tailnettest.Server) []string {
	var ids []string
	for _, d := range fake.Devices() {
		ids = append(ids, d.ID)
	}

	return ids
}
