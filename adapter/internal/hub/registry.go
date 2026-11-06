package hub

import (
	"context"
	"errors"
	"sync"
)

// errBlankTeam means Get was asked for a service with no Team. A blank team must
// not share or invent one: that is how two callers without an identity would be
// handed the same fleet.
var errBlankTeam = errors.New("a team is required")

// entry is one Team's build. ready closes when the factory finishes, so
// concurrent Get calls wait for the same attempt rather than starting another.
type entry struct {
	ready chan struct{}
	svc   *Service
	err   error
}

// Registry holds one Service per Team, built on first Get.
//
// The factory runs outside the map lock after the slot is reserved, so two
// Teams hydrating at once do not block each other. A failed build is
// dropped so the next Get may try again.
type Registry struct {
	mu  sync.Mutex
	m   map[uint]*entry
	new func(ctx context.Context, team uint) (*Service, error)
}

// NewRegistry returns a registry that calls new the first time a Team is
// asked for. new must not be nil.
func NewRegistry(new func(ctx context.Context, team uint) (*Service, error)) *Registry {
	return &Registry{
		m:   map[uint]*entry{},
		new: new,
	}
}

// Get returns the Service for a Team, building it once if needed.
func (r *Registry) Get(ctx context.Context, team uint) (*Service, error) {
	if team == 0 || r == nil || r.new == nil {
		return nil, errBlankTeam
	}

	r.mu.Lock()
	if e, ok := r.m[team]; ok {
		r.mu.Unlock()
		select {
		case <-e.ready:
			return e.svc, e.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	e := &entry{ready: make(chan struct{})}
	r.m[team] = e
	r.mu.Unlock()

	svc, err := r.new(ctx, team)

	r.mu.Lock()
	if err != nil {
		delete(r.m, team)
		e.err = err
		close(e.ready)
		r.mu.Unlock()

		return nil, err
	}
	e.svc = svc
	close(e.ready)
	r.mu.Unlock()

	return svc, nil
}
