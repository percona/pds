package hub

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"
)

// SupportSweepEvery is how often the sweeps look for windows that ran out and
// for strays.
const SupportSweepEvery = time.Minute

// RunSupportSweeper closes the windows of this hub's Team that ran out, now and
// then every SupportSweepEvery, until ctx ends. A window reads closed from its
// expiry on its own: this is what takes the access away.
func (s *Service) RunSupportSweeper(ctx context.Context) {
	every(ctx, SupportSweepEvery, s.ExpireSupport)
}

// ExpireSupport closes every window whose time has run out and whose access
// the network has been told to take back. One that the network would not take
// back is left as it was and tried again on the next sweep, so the entry for it
// is written once, when its access is gone.
func (s *Service) ExpireSupport(ctx context.Context) {
	rows, err := s.support.Load(ctx)
	if err != nil {
		slog.WarnContext(ctx, "the hub could not read support access to expire windows",
			slog.String("error", err.Error()))

		return
	}
	now := s.now()
	for environment, rec := range rows {
		if rec.Window != nil && rec.Window.active() && !now.Before(rec.Window.ExpiresAt) {
			s.expireSupportWindow(ctx, environment)
		}
	}
}

func (s *Service) expireSupportWindow(ctx context.Context, environment string) {
	unlock := s.spokes.lock(environment)
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, inventoryPersistTimeout)
	defer cancel()

	// What was read before the lock may have been ended or replaced since.
	rec, err := s.supportRecord(ctx, environment)
	if err != nil {
		slog.WarnContext(ctx, "the hub could not read a support access record to expire its window",
			slog.String("environment", environment), slog.String("error", err.Error()))

		return
	}
	if rec.Window == nil || !rec.Window.active() || s.now().Before(rec.Window.ExpiresAt) {
		return
	}
	if err := s.revokeSupportAccess(ctx, rec.Window); err != nil {
		slog.WarnContext(ctx, "the hub could not take back access for a window that ran out",
			slog.String("environment", environment), slog.String("error", err.Error()))

		return
	}
	rec.Window.State = SupportStateClosed
	rec.Window.ClosedReason = SupportClosedExpired
	rec.Window.KeyID = ""
	if err := s.support.Save(ctx, environment, rec); err != nil {
		slog.WarnContext(ctx, "the hub could not close a support access window that ran out",
			slog.String("environment", environment), slog.String("error", err.Error()))

		return
	}
	if s.supportExpired != nil {
		s.supportExpired(ctx, environment, rec.Window.SupportWindow)
	}
}

// SupportCensus reads the support access record of every environment of every
// Team. A hub service knows only its own Team's, and the network is shared.
type SupportCensus func(ctx context.Context) ([]SupportRecord, error)

// StraySupportSweeper deletes what the network holds that no record accounts
// for: a device whose window is not active, and an address no environment
// holds. It acts only on what a census of every Team's records leaves out, so a
// census it could not read deletes nothing.
type StraySupportSweeper struct {
	tailnet Tailnet
	census  SupportCensus
	now     func() time.Time
}

// NewStraySupportSweeper returns a sweeper over the network and the census.
func NewStraySupportSweeper(tn Tailnet, census SupportCensus, now func() time.Time) *StraySupportSweeper {
	return &StraySupportSweeper{tailnet: tn, census: census, now: now}
}

// Run sweeps now and then every SupportSweepEvery, until ctx ends.
func (w *StraySupportSweeper) Run(ctx context.Context) {
	every(ctx, SupportSweepEvery, func(ctx context.Context) {
		if err := w.Sweep(ctx); err != nil {
			slog.WarnContext(ctx, "the hub could not sweep stray support access", slog.String("error", err.Error()))
		}
	})
}

// Sweep deletes the strays once. It carries on past a delete that failed and
// returns the first failure.
func (w *StraySupportSweeper) Sweep(ctx context.Context) error {
	return firstError(w.sweepDevices(ctx), w.sweepAddresses(ctx))
}

// sweepDevices lists the devices before reading the records. A window is saved
// before its key is minted, so a device in the list belongs to a window the
// read will find.
func (w *StraySupportSweeper) sweepDevices(ctx context.Context) error {
	devices, err := w.tailnet.ListDevices(ctx)
	if err != nil {
		return err
	}
	records, err := w.census(ctx)
	if err != nil {
		return err
	}
	now := w.now()
	var live []string
	for _, rec := range records {
		if window := rec.current(now); window != nil && window.active() {
			live = append(live, window.ID)
		}
	}

	var first error
	for _, device := range devices {
		if slices.Contains(live, device.Hostname) {
			continue
		}
		if err := w.tailnet.DeleteDevice(ctx, device.ID); err != nil {
			first = firstError(first, err)

			continue
		}
		slog.InfoContext(ctx, "the hub deleted a support access device no window accounts for",
			slog.String("device", device.ID))
	}

	return first
}

// sweepAddresses holds supportAddressMu across the list and the read, so an
// address being created has its record by the time it is seen.
func (w *StraySupportSweeper) sweepAddresses(ctx context.Context) error {
	supportAddressMu.Lock()
	defer supportAddressMu.Unlock()

	names, err := w.tailnet.ListAddresses(ctx)
	if err != nil {
		return err
	}
	records, err := w.census(ctx)
	if err != nil {
		return err
	}
	var held []string
	for _, rec := range records {
		held = append(held, rec.Address)
	}

	var first error
	for _, name := range names {
		if !strings.HasPrefix(name, supportAddressPrefix) || slices.Contains(held, name) {
			continue
		}
		if err := w.tailnet.DeleteAddress(ctx, name); err != nil {
			first = firstError(first, err)

			continue
		}
		slog.InfoContext(ctx, "the hub deleted a support access address no environment holds",
			slog.String("address", name))
	}

	return first
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	return nil
}

// every runs f now and then each interval until ctx ends. It is the only place
// the sweeps are scheduled.
func every(ctx context.Context, interval time.Duration, f func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		f(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
