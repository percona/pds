package hub

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// hydrateRetry is the first wait between hydration attempts. It doubles up to
// hydrateMaxRetry, so a database that comes back is picked up quickly and one
// that stays down is not hammered.
const (
	hydrateRetry    = time.Second
	hydrateMaxRetry = 30 * time.Second
)

// errNotHydrated is what readiness reports until the state is up.
var errNotHydrated = errors.New("the hub has not loaded its state from the database yet")

// Persists are the stores the hub keeps its state in. A nil member is state this
// deployment never keeps, which is the ordinary case on a hub with no
// Kubernetes access.
type Persists struct {
	Inventory InventoryPersist
	Tokens    TokenPersist
	Work      WorkPersist
	CloneKeys CloneKeyPersist
	// SupportAccess is nil where nothing keeps it, and the hub holds its
	// support access records in memory.
	SupportAccess SupportPersist
}

// Hydration brings the hub's in-memory state up from where it is kept: the
// database, or the ConfigMaps and Secrets on a deployment with none.
//
// It runs after the connection lands rather than at start, because the process
// serves throughout while the database is still coming up. Readiness is red for
// that whole window, so nothing is answered from the empty state it starts on.
type Hydration struct {
	// Inventory, Tokens and Service hold the in-memory state to fill.
	Inventory *Inventory
	Tokens    *TokenStore
	Service   *Service
	// Work is the persist the queue reads back through.
	Work WorkPersist
}

// Run loads the state. It is safe to call again after a failure: every load
// replaces rather than appends.
func (h Hydration) Run(ctx context.Context) error {
	if h.Inventory != nil {
		if err := h.Inventory.Load(ctx); err != nil {
			return err
		}
	}

	if h.Tokens != nil {
		if err := h.Tokens.Load(ctx); err != nil {
			return err
		}
		// Grants minted before redemption existed carry a TTL and no stamp, so
		// an environment that joined under an older build would be refused a day
		// later with no way back. The inventory knows which environments actually
		// beat, and that is the evidence for adopting their grants.
		if err := h.Tokens.AdoptRedeemed(ctx, h.lastSeen()); err != nil {
			return err
		}
	}

	if h.Service != nil {
		if h.Work != nil {
			if err := h.Service.SetWorkPersist(ctx, h.Work); err != nil {
				return err
			}
		}
		if err := h.Service.LoadCloneKeys(ctx); err != nil {
			return err
		}
	}

	return nil
}

func (h Hydration) lastSeen() map[string]time.Time {
	seen := map[string]time.Time{}
	if h.Inventory == nil {
		return seen
	}
	for _, environment := range h.Inventory.List(time.Now()) {
		if !environment.LastSeen.IsZero() {
			seen[environment.Name] = environment.LastSeen
		}
	}

	return seen
}

// Hydrator runs a Hydration in the background and reports whether it landed.
//
// Readiness reads it, so a pod that has not loaded the fleet does not take
// traffic and answer with an empty one.
type Hydrator struct {
	mu   sync.Mutex
	err  error
	done bool

	cancel context.CancelFunc
	once   sync.Once
	wait   sync.WaitGroup
}

// StartHydration begins hydrating. It returns immediately.
//
// connect blocks until the database is reachable. A failed attempt is retried
// with a growing backoff rather than exiting: the state is still in the objects
// it came from, and a process that exited would restart into the same wait.
func StartHydration(ctx context.Context, connect func(context.Context) error, h Hydration, logger *slog.Logger) *Hydrator {
	ctx, cancel := context.WithCancel(ctx)
	hydrator := &Hydrator{err: errNotHydrated, cancel: cancel}

	if logger == nil {
		logger = slog.Default()
	}

	hydrator.wait.Add(1)
	go hydrator.run(ctx, connect, h, logger)

	return hydrator
}

func (r *Hydrator) run(ctx context.Context, connect func(context.Context) error, h Hydration, logger *slog.Logger) {
	defer r.wait.Done()

	backoff := hydrateRetry
	for {
		err := connect(ctx)
		if err == nil {
			err = h.Run(ctx)
		}
		if err == nil {
			r.mu.Lock()
			r.err = nil
			r.done = true
			r.mu.Unlock()

			return
		}
		if ctx.Err() != nil {
			return
		}

		r.mu.Lock()
		r.err = err
		r.mu.Unlock()

		logger.WarnContext(ctx, "the hub has not loaded its state yet, readiness stays red",
			slog.String("cause", err.Error()),
			slog.Duration("retry_in", backoff))

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()

			return
		case <-timer.C:
		}

		if backoff *= 2; backoff > hydrateMaxRetry {
			backoff = hydrateMaxRetry
		}
	}
}

// Ready reports whether the hub has its state.
func (r *Hydrator) Ready(context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.done {
		return nil
	}
	if r.err != nil {
		return r.err
	}

	return errNotHydrated
}

// Close stops retrying.
func (r *Hydrator) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(r.cancel)
	r.wait.Wait()

	return nil
}
