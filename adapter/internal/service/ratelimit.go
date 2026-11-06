package service

import (
	"sync"
	"time"
)

// rateLimiter allows limit events per key in any sliding window. It keeps at
// most limit timestamps per key and at most maxKeys keys, and drops keys whose
// last event is older than the window once per window, so its memory stays
// bounded whatever the traffic.
type rateLimiter struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	maxKeys   int
	now       func() time.Time
	events    map[string][]time.Time
	lastPrune time.Time
}

func newRateLimiter(limit int, window time.Duration, maxKeys int, now func() time.Time) *rateLimiter {
	return &rateLimiter{
		limit:   limit,
		window:  window,
		maxKeys: maxKeys,
		now:     now,
		events:  make(map[string][]time.Time),
	}
}

// allow records one event for key and reports whether it is within the limit.
// A refused event is not recorded, so a client that keeps trying is let back
// in once its earlier events age out.
func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-l.window)

	if now.Sub(l.lastPrune) >= l.window {
		l.prune(cutoff)
		l.lastPrune = now
	}

	recent := l.events[key]
	kept := recent[:0]
	for _, at := range recent {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}

	if len(kept) >= l.limit {
		l.events[key] = kept

		return false
	}

	if _, known := l.events[key]; !known && len(l.events) >= l.maxKeys {
		// Every slot holds a client seen within the last window. Refusing the
		// newcomer keeps memory bounded and fails closed.
		l.prune(cutoff)
		if len(l.events) >= l.maxKeys {
			return false
		}
	}

	l.events[key] = append(kept, now)

	return true
}

// prune drops every key with no event after cutoff.
func (l *rateLimiter) prune(cutoff time.Time) {
	for key, recent := range l.events {
		if len(recent) == 0 || !recent[len(recent)-1].After(cutoff) {
			delete(l.events, key)
		}
	}
}
