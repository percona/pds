package hub_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/tailnet"
	"github.com/percona/pds/adapter/internal/hub"
)

// slowTailnet answers every call after delay, or gives up when the caller's
// context ends first, as a network client does. It lives in the process, not
// behind tailnettest's HTTP server, so a synctest bubble can run its delays on
// the fake clock.
type slowTailnet struct {
	mu      sync.Mutex
	delay   time.Duration
	minted  []string
	revoked []string
}

func (n *slowTailnet) slowBy(d time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.delay = d
}

func (n *slowTailnet) wait(ctx context.Context) error {
	n.mu.Lock()
	delay := n.delay
	n.mu.Unlock()
	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (n *slowTailnet) CreateAddress(ctx context.Context, _ string) error { return n.wait(ctx) }
func (n *slowTailnet) CheckAddress(ctx context.Context, _ string) error  { return n.wait(ctx) }
func (n *slowTailnet) AddressApproved(ctx context.Context, _, _ string) (bool, error) {
	err := n.wait(ctx)

	return err == nil, err
}
func (n *slowTailnet) AddressHost(name string) string { return name + ".example.ts.net" }

func (n *slowTailnet) MintKey(ctx context.Context, _ time.Duration) (string, string, error) {
	if err := n.wait(ctx); err != nil {
		return "", "", err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	id := fmt.Sprintf("key-%d", len(n.minted)+1)
	n.minted = append(n.minted, id)

	return id, "secret-" + id, nil
}

func (n *slowTailnet) RevokeKey(ctx context.Context, id string) error {
	if err := n.wait(ctx); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.revoked = append(n.revoked, id)

	return nil
}

func (n *slowTailnet) DeleteDevices(ctx context.Context, _ string) error { return n.wait(ctx) }
func (n *slowTailnet) ListDevices(ctx context.Context) ([]tailnet.Device, error) {
	return nil, n.wait(ctx)
}
func (n *slowTailnet) DeleteDevice(ctx context.Context, _ string) error { return n.wait(ctx) }
func (n *slowTailnet) ListAddresses(ctx context.Context) ([]string, error) {
	return nil, n.wait(ctx)
}
func (n *slowTailnet) DeleteAddress(ctx context.Context, _ string) error { return n.wait(ctx) }

func (n *slowTailnet) keys() (minted, revoked []string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	return append([]string(nil), n.minted...), append([]string(nil), n.revoked...)
}

type slowWorld struct {
	svc  *hub.Service
	net  *slowTailnet
	rows *flakySupportPersist
}

// newSlowWorld opens a window on eks-prod over a tailnet that answers at once.
// It must run inside a synctest bubble, so a slow call costs no real time.
func newSlowWorld(t *testing.T) *slowWorld {
	t.Helper()

	inv := hub.NewInventory(1000 * time.Hour)
	inv.Register("eks-prod", "pds")
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "eks-prod", Namespace: "pds", At: time.Now()})
	w := &slowWorld{net: &slowTailnet{}, rows: &flakySupportPersist{MemorySupportPersist: hub.NewMemorySupportPersist()}}
	w.svc = hub.NewService(hub.NewMemoryStore(), inv, hub.Options{SupportAccess: w.rows, Tailnet: w.net})
	_, err := w.svc.OpenSupportAccess(t.Context(), "eks-prod",
		hub.SupportOpen{Level: hub.SupportLevelReadOnly, Hours: 1, CaseReference: "CS0012345"})
	require.NoError(t, err)

	return w
}

func (w *slowWorld) beat(t *testing.T, support hub.HeartbeatSupport) ([]hub.Work, error) {
	t.Helper()

	work, _, err := w.svc.AcceptHeartbeat(hub.Heartbeat{Environment: "eks-prod", Namespace: "pds", Support: &support}, nil)

	return work, err
}

func (w *slowWorld) record(t *testing.T) hub.SupportRecord {
	t.Helper()

	rows, err := w.rows.Load(t.Context())
	require.NoError(t, err)
	require.NotNil(t, rows["eks-prod"].Window)

	return rows["eks-prod"]
}

// The tailnet's answer uses up nearly all of a save's budget, and the save
// still needs its own round trip after it.
const (
	slowTailnetCall = 4900 * time.Millisecond
	saveRoundTrip   = 200 * time.Millisecond
)

func TestAWindowOpenedOnASlowTailnetIsSaved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newSlowWorld(t)
		_, err := w.beat(t, hub.HeartbeatSupport{})
		require.NoError(t, err)
		id := w.record(t).Window.ID
		w.net.slowBy(slowTailnetCall)
		w.rows.saveTakes = saveRoundTrip

		_, err = w.beat(t, hub.HeartbeatSupport{
			Held: []string{id}, Serving: []string{id},
			Confirmations: []hub.SupportConfirmation{{Window: id, Event: hub.SupportInstalled}},
		})

		require.NoError(t, err)
		assert.Equal(t, hub.SupportStateOpen, w.record(t).Window.State)
	})
}

func TestAKeyMintedOnASlowTailnetIsRecorded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newSlowWorld(t)
		w.net.slowBy(slowTailnetCall)
		w.rows.saveTakes = saveRoundTrip

		_, err := w.beat(t, hub.HeartbeatSupport{})

		require.NoError(t, err)
		assert.Equal(t, "key-1", w.record(t).Window.KeyID)
	})
}

func TestAKeyThatCouldNotBeRecordedIsRevokedBeforeTheBeatReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newSlowWorld(t)
		w.net.slowBy(slowTailnetCall)
		w.rows.failing = true

		_, err := w.beat(t, hub.HeartbeatSupport{})

		require.NoError(t, err)
		_, revoked := w.net.keys()
		assert.Equal(t, []string{"key-1"}, revoked)
	})
}
