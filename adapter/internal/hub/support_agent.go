package hub

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"
)

// What an agent confirms about a window's workload.
const (
	SupportInstalled = "installed"
	SupportExtended  = "extended"
	SupportRemoved   = "removed"
	SupportFailed    = "failed"
)

// SupportConfirmation is the agent saying what it did to one window's workload
// since its last beat. Reason is for the hub's log only. ExpiresAt is the expiry
// an extended workload now has.
type SupportConfirmation struct {
	Window    string    `json:"window"`
	Event     string    `json:"event"`
	Reason    string    `json:"reason,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
}

// HeartbeatSupport is what an agent reports about support access on each beat:
// every window whose workload it holds, so the hub can close any it should
// not, the ones whose proxy holds the address's certificate, and what it did
// since the last beat.
type HeartbeatSupport struct {
	Held []string `json:"held"`
	// Serving lists the held windows whose proxy can answer a TLS handshake.
	// Until then the address answers no one, however the network routes it.
	Serving       []string              `json:"serving,omitempty"`
	Confirmations []SupportConfirmation `json:"confirmations,omitempty"`
}

// The things a beat can make the hub say in the activity log.
const (
	SupportEventInstalled     = "installed"
	SupportEventRemoved       = "removed"
	SupportEventInstallFailed = "installFailed"
	// SupportEventStartFailed is the agent saying a workload did not start. It
	// is for the log: the window may still start on the next key.
	SupportEventStartFailed = "startFailed"
)

// SupportEvent is something that happened to a window because of a beat. The
// hub has no activity log of its own, so the heartbeat route takes these and
// writes them.
type SupportEvent struct {
	Kind string
	// Window and Reason say which window and what the agent said, for the log.
	Window string
	Reason string
	// Opened is the window an install opened, for the note on its case. It is
	// set on an installed event only.
	Opened *SupportWindow
	// AddressHost is the DNS name of the window's address. It is set on an
	// installed event only.
	AddressHost string
}

// TakeSupportEvents returns the events recent beats produced for an
// environment, once.
func (s *Service) TakeSupportEvents(environment string) []SupportEvent {
	s.supportEventsMu.Lock()
	defer s.supportEventsMu.Unlock()

	events := s.supportEvents[environment]
	delete(s.supportEvents, environment)

	return events
}

func (s *Service) addSupportEvents(environment string, events []SupportEvent) {
	if len(events) == 0 {
		return
	}
	s.supportEventsMu.Lock()
	defer s.supportEventsMu.Unlock()

	s.supportEvents[environment] = append(s.supportEvents[environment], events...)
}

// installed says whether the agent has confirmed the window's workload, which
// is all it can say: whether anyone can connect is the network's to answer.
func (w *SupportWindowRecord) installed() bool {
	return slices.Contains(w.Confirmed, SupportInstalled)
}

// confirm notes that an event was seen for the window and reports whether it
// is new. Seeing the same one again changes nothing.
func (w *SupportWindowRecord) confirm(event string) bool {
	if slices.Contains(w.Confirmed, event) {
		return false
	}
	w.Confirmed = append(w.Confirmed, event)

	return true
}

// reconcileSupport settles what the agent said about support access against
// the window the hub holds, and decides what to ask next: close a workload the
// hub does not want, hand out the open again, or give up on a window that
// never started. The caller holds the environment's lock. It takes the
// support block off the beat, which nothing else needs.
func (s *Service) reconcileSupport(beat *Heartbeat) error {
	report := beat.Support
	beat.Support = nil
	if report == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(s.lifetime, inventoryPersistTimeout)
	defer cancel()

	environment := beat.Environment
	rec, err := s.supportRecord(ctx, environment)
	if err != nil {
		return err
	}
	var events []SupportEvent
	changed := false

	if rec.Window != nil {
		for _, c := range report.Confirmations {
			if c.Window != rec.Window.ID {
				continue
			}
			switch c.Event {
			case SupportInstalled:
				if rec.Window.confirm(c.Event) {
					changed = true
				}
			case SupportExtended:
				// Only the expiry the hub is waiting for counts, so a repeated
				// or stale confirmation moves nothing and needs no list.
				if !c.ExpiresAt.IsZero() && rec.Window.ExtendingTo.Equal(c.ExpiresAt) && rec.current(s.now()).active() {
					rec.Window.ExpiresAt = rec.Window.ExtendingTo
					rec.Window.ExtendingTo = time.Time{}
					changed = true
				}
			case SupportRemoved:
				if rec.Window.confirm(c.Event) {
					events = append(events, SupportEvent{Kind: SupportEventRemoved})
					changed = true
				}
			case SupportFailed:
				events = append(events, SupportEvent{Kind: SupportEventStartFailed, Window: c.Window, Reason: c.Reason})
			}
		}
	}

	now := s.now()
	if event := s.openWhenServing(ctx, &rec, report.Serving, now); event != nil {
		changed = true
		events = append(events, *event)
	}
	if rec.Window != nil && rec.Window.OpenedAt.IsZero() {
		// Opened before the hub kept the time: it gets the full wait from now.
		rec.Window.OpenedAt = now.UTC()
		changed = true
	}
	current := rec.current(now)
	for _, id := range report.Held {
		if current != nil && current.ID == id && current.active() {
			continue
		}
		if err := s.work.Push(environment, Work{Kind: WorkSupportClose, Window: id}); err != nil {
			return wrapPersist(err)
		}
	}

	if current != nil && current.active() && rec.Window.State == SupportStateOpening {
		if now.Sub(rec.Window.OpenedAt) >= supportInstallWithin {
			failed, err := s.failSupportInstall(ctx, environment, &rec)
			if err != nil {
				return err
			}
			if failed {
				events = append(events, SupportEvent{Kind: SupportEventInstallFailed})
				changed = true
			}
		} else if len(s.work.owed(environment, WorkSupportOpen)) == 0 && !rec.Window.installed() {
			open := Work{
				Kind: WorkSupportOpen, Window: current.ID, Level: current.Level,
				ExpiresAt: current.ExpiresAt, Address: rec.Address,
			}
			if s.tailnet != nil {
				open.AddressHost = s.tailnet.AddressHost(rec.Address)
			}
			if err := s.work.Push(environment, open); err != nil {
				return wrapPersist(err)
			}
		}
	}

	if current != nil && current.State == SupportStateOpen && !rec.Window.ExtendingTo.IsZero() &&
		len(s.work.owed(environment, WorkSupportExtend)) == 0 {
		extend := Work{Kind: WorkSupportExtend, Window: current.ID, ExpiresAt: rec.Window.ExtendingTo}
		if err := s.work.Push(environment, extend); err != nil {
			return wrapPersist(err)
		}
	}

	if changed {
		// The tailnet calls above may have used up ctx. What they changed must
		// still be saved, so the save gets a budget of its own.
		saveCtx, cancelSave := context.WithTimeout(s.lifetime, inventoryPersistTimeout)
		defer cancelSave()
		if err := s.support.Save(saveCtx, environment, rec); err != nil {
			return wrapPersist(err)
		}
	}
	s.addSupportEvents(environment, events)

	return nil
}

// openWhenServing opens an installed window once its proxy holds the
// address's certificate and the private network approves its device to host
// the address. The agent confirming the workload proves the workload exists,
// not that anyone can connect: the certificate can fail to issue, and approval
// says nothing about it. A window that is open is not asked about again, and a
// network that cannot answer moves nothing. It returns the event an opening
// makes, or nil when the window did not open. With no private network there is
// no address and nothing to ask, and the agent's word is all there is.
func (s *Service) openWhenServing(ctx context.Context, rec *SupportRecord, serving []string, now time.Time) *SupportEvent {
	window := rec.Window
	if window == nil || window.State != SupportStateOpening || !window.installed() {
		return nil
	}
	if current := rec.current(now); !current.active() {
		return nil
	}
	if s.tailnet != nil {
		if !slices.Contains(serving, window.ID) {
			return nil
		}
		approved, err := s.tailnet.AddressApproved(ctx, rec.Address, window.ID)
		if err != nil {
			slog.WarnContext(ctx, "the hub could not tell whether the network approves a support access window's device",
				slog.String("window", window.ID), slog.String("error", err.Error()))

			return nil
		}
		if !approved {
			return nil
		}
	}

	window.State = SupportStateOpen
	opened := window.SupportWindow
	event := SupportEvent{Kind: SupportEventInstalled, Opened: &opened}
	if s.tailnet != nil {
		event.AddressHost = s.tailnet.AddressHost(rec.Address)
	}

	return &event
}

// failSupportInstall closes an opening window that never started, once its
// access is gone. It reports false, and leaves the window opening for the next
// beat, when the network would not take access away.
func (s *Service) failSupportInstall(ctx context.Context, environment string, rec *SupportRecord) (bool, error) {
	if err := s.revokeSupportAccess(ctx, rec.Window); err != nil {
		slog.Warn("hub could not take back access for a window that did not start",
			slog.String("environment", environment), slog.String("error", err.Error()))

		return false, nil
	}
	rec.Window.State = SupportStateClosed
	rec.Window.ClosedReason = SupportClosedInstallFailed
	rec.Window.KeyID = ""
	if err := s.work.Cancel(environment, WorkSupportOpen, ""); err != nil {
		return false, wrapPersist(err)
	}
	// Part of the workload may exist even though it never came up.
	if err := s.work.Push(environment, Work{Kind: WorkSupportClose, Window: rec.Window.ID}); err != nil {
		return false, wrapPersist(err)
	}

	return true, nil
}

// relaySupportKeys puts a freshly minted key on each open the agent is about to
// receive. The key is on the reply only: the queue and the record never hold
// it, and asking again mints another and revokes the one before. An open that
// cannot get a key is held back and comes round again.
func (s *Service) relaySupportKeys(environment string, work []Work) []Work {
	ctx, cancel := context.WithTimeout(s.lifetime, inventoryPersistTimeout)
	defer cancel()

	out := work[:0:0]
	for _, item := range work {
		if item.Kind != WorkSupportOpen {
			out = append(out, item)

			continue
		}
		key, err := s.mintSupportKeyLocked(ctx, environment, item.Window)
		switch {
		case err == nil:
			item.Key = key
			out = append(out, item)
		case errors.Is(err, ErrSupportNoWindow):
			// The window is over, so the open is too.
			if err := s.work.Cancel(environment, WorkSupportOpen, ""); err != nil {
				slog.Warn("hub could not drop a support access open",
					slog.String("environment", environment), slog.String("error", err.Error()))
			}
		default:
			slog.Warn("hub could not mint a support access key",
				slog.String("environment", environment), slog.String("error", err.Error()))
		}
	}

	return out
}
