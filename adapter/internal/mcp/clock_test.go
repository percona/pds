package mcp_test

import (
	"context"
	"sync"
	"time"
)

// manualClock is time that only moves when a test says so, or when the code
// under test sleeps: a sleep takes no time and moves the clock by the length of
// the sleep, so a wait of half a minute runs at once.
type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*manualTimer]struct{}
	slept  []time.Duration
}

type manualTimer struct {
	at   time.Time
	fire func()
}

func newManualClock() *manualClock {
	return &manualClock{
		now:    time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC),
		timers: map[*manualTimer]struct{}{},
	}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *manualClock) Sleep(ctx context.Context, d time.Duration) {
	if ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	c.slept = append(c.slept, d)
	c.mu.Unlock()
	c.Advance(d)
}

// WithTimeout ends the context once the clock has moved d past now.
func (c *manualClock) WithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	timer := &manualTimer{fire: func() { cancel(context.DeadlineExceeded) }}

	c.mu.Lock()
	timer.at = c.now.Add(d)
	c.timers[timer] = struct{}{}
	c.mu.Unlock()

	return ctx, func() {
		c.mu.Lock()
		delete(c.timers, timer)
		c.mu.Unlock()
		cancel(context.Canceled)
	}
}

// Advance moves the clock and ends every context whose time has come.
func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*manualTimer
	for timer := range c.timers {
		if !timer.at.After(c.now) {
			due = append(due, timer)
			delete(c.timers, timer)
		}
	}
	c.mu.Unlock()

	for _, timer := range due {
		timer.fire()
	}
}

func (c *manualClock) sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]time.Duration(nil), c.slept...)
}
