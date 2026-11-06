package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/hub"
)

// SupportWorkloads is what the agent needs of the environment to run a support
// access window: start its workload, remove it, and say which are there.
type SupportWorkloads interface {
	InstallSupportWorkload(ctx context.Context, w kube.SupportWorkload) error
	ExtendSupportWorkload(ctx context.Context, window string, expiresAt time.Time) error
	RemoveSupportWorkload(ctx context.Context, window string) error
	SupportWindows(ctx context.Context) ([]kube.SupportWindow, error)
	SupportServes(ctx context.Context, w kube.SupportWindow) (bool, error)
}

var errSupportUnavailable = errors.New("this agent cannot run support access")

var _ SupportWorkloads = (*kube.Client)(nil)

// supportConfirmations holds what the agent did to windows until a beat the hub
// accepted has carried it. The hub counts a repeated confirmation once, so a
// beat that failed in transit is simply sent again.
type supportConfirmations struct {
	mu    sync.Mutex
	items []hub.SupportConfirmation
}

func (c *supportConfirmations) add(item hub.SupportConfirmation) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = append(c.items, item)
}

func (c *supportConfirmations) pending() []hub.SupportConfirmation {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]hub.SupportConfirmation(nil), c.items...)
}

// sent drops the first n confirmations, the ones a beat carried that the hub
// took.
func (c *supportConfirmations) sent(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = c.items[min(n, len(c.items)):]
}

// supportReport is what this beat says about support access. Nil means this
// agent cannot run it, or could not tell what it holds: the hub reads no report
// as nothing known, so a failed read never looks like every window gone.
func supportReport(ctx context.Context, opts Options) *hub.HeartbeatSupport {
	if opts.Support == nil {
		return nil
	}
	windows, err := opts.Support.SupportWindows(ctx)
	if err != nil {
		opts.Logger.WarnContext(ctx, "support access windows could not be listed", slog.String("err", err.Error()))

		return nil
	}
	held := make([]string, 0, len(windows))
	var serving []string
	for _, window := range windows {
		held = append(held, window.ID)
		// A window that cannot be checked is left out: the hub then keeps it
		// opening, which is the side that never tells a customer access works.
		serves, err := opts.Support.SupportServes(ctx, window)
		if err != nil {
			opts.Logger.WarnContext(ctx, "support access window could not be checked for its certificate",
				slog.String("window", window.ID), slog.String("err", err.Error()))

			continue
		}
		if serves {
			serving = append(serving, window.ID)
		}
	}

	return &hub.HeartbeatSupport{Held: held, Serving: serving, Confirmations: opts.supportConfirmations.pending()}
}

// expireSupport removes the workload of any window past its expiry. It runs
// whether or not the hub can be reached, so an environment that lost contact
// is not left open.
func expireSupport(ctx context.Context, opts Options, now time.Time) {
	if opts.Support == nil {
		return
	}
	windows, err := opts.Support.SupportWindows(ctx)
	if err != nil {
		opts.Logger.WarnContext(ctx, "support access windows could not be checked for expiry", slog.String("err", err.Error()))

		return
	}
	for _, window := range windows {
		if now.Before(window.ExpiresAt) {
			continue
		}
		if err := opts.Support.RemoveSupportWorkload(ctx, window.ID); err != nil {
			opts.Logger.WarnContext(ctx, "an expired support access workload could not be removed",
				slog.String("window", window.ID), slog.String("err", err.Error()))

			continue
		}
		opts.supportConfirmations.add(hub.SupportConfirmation{Window: window.ID, Event: hub.SupportRemoved})
	}
}

// isSupportWork says whether the agent runs an item without the database
// service, which an agent with no databases still has.
func isSupportWork(item hub.Work) bool {
	return item.Kind == hub.WorkSupportOpen || item.Kind == hub.WorkSupportExtend || item.Kind == hub.WorkSupportClose
}

// runSupportWork runs support access items and reports on each. Whatever it
// did, and why it failed, is also queued as a confirmation for the next beat.
func runSupportWork(ctx context.Context, opts Options, items []hub.Work) []hub.WorkResult {
	results := make([]hub.WorkResult, 0, len(items))
	for _, item := range items {
		ctx, cancel := context.WithTimeout(ctx, workTimeout)
		confirmation := hub.SupportConfirmation{Window: item.Window}
		var err error
		switch {
		case opts.Support == nil:
			err = errSupportUnavailable
		case item.Kind == hub.WorkSupportOpen:
			confirmation.Event = hub.SupportInstalled
			err = opts.Support.InstallSupportWorkload(ctx, kube.SupportWorkload{
				Window: item.Window, Level: item.Level, Address: item.Address, Host: item.AddressHost, Key: item.Key, ExpiresAt: item.ExpiresAt,
			})
		case item.Kind == hub.WorkSupportExtend:
			confirmation.Event = hub.SupportExtended
			confirmation.ExpiresAt = item.ExpiresAt
			err = opts.Support.ExtendSupportWorkload(ctx, item.Window, item.ExpiresAt)
		default:
			confirmation.Event = hub.SupportRemoved
			err = opts.Support.RemoveSupportWorkload(ctx, item.Window)
		}
		cancel()

		result := hub.WorkResult{ID: item.ID}
		if err != nil {
			// The key is single use, but a Kubernetes error can echo what was
			// being written, and nothing about a key may leave in a reason.
			reason := redact(err.Error(), item.Key)
			opts.Logger.WarnContext(ctx, "support access work failed",
				slog.String("kind", item.Kind), slog.String("window", item.Window), slog.String("err", reason))
			result.Error = reason
			confirmation.Event = hub.SupportFailed
			confirmation.Reason = reason
		}
		opts.supportConfirmations.add(confirmation)
		results = append(results, result)
	}

	return results
}

func redact(text, secret string) string {
	if secret == "" {
		return text
	}

	return strings.ReplaceAll(text, secret, "[redacted]")
}
