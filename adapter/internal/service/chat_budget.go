package service

import (
	"context"
	"sync"
	"time"
)

// startTimer runs fire once after d and returns a function that stops it,
// which reports whether it stopped it before it fired.
type startTimer func(d time.Duration, fire func()) (stop func() bool)

func systemTimer(d time.Duration, fire func()) func() bool {
	return time.AfterFunc(d, fire).Stop
}

// WithChatTimer replaces the timers behind a turn's time budget and an
// approval's expiry, for tests that move time by hand.
func WithChatTimer(timer func(d time.Duration, fire func()) (stop func() bool)) ChatOption {
	return func(ch *Chat) {
		ch.timer = timer
	}
}

// turnBudget is the time a turn may run. It stops while the turn waits on a
// person, since the person's time is not the model's.
type turnBudget struct {
	mu      sync.Mutex
	timer   startTimer
	now     func() time.Time
	cancel  context.CancelCauseFunc
	stop    func() bool
	left    time.Duration
	since   time.Time
	paused  bool
	elapsed bool
}

// newTurnBudget returns a context that ends after d of running time. It ends
// with context.DeadlineExceeded as its cause, and the budget says that it
// did, since the context's own error is only that it was canceled.
func newTurnBudget(parent context.Context, d time.Duration, timer startTimer, now func() time.Time) (context.Context, *turnBudget) {
	ctx, cancel := context.WithCancelCause(parent)
	b := &turnBudget{timer: timer, now: now, cancel: cancel}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.run(d)

	return ctx, b
}

// run starts the clock with d left. The caller holds the lock.
func (b *turnBudget) run(d time.Duration) {
	b.left, b.since, b.paused = d, b.now(), false
	if d <= 0 {
		b.expire()

		return
	}
	b.stop = b.timer(d, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.expire()
	})
}

func (b *turnBudget) expire() {
	b.elapsed = true
	b.cancel(context.DeadlineExceeded)
}

// pause stops the clock until the returned function is called.
func (b *turnBudget) pause() (resume func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.paused || b.elapsed {
		return func() {}
	}
	b.stop()
	b.left -= b.now().Sub(b.since)
	b.paused = true

	var once sync.Once

	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.paused && !b.elapsed {
				b.run(b.left)
			}
		})
	}
}

// exhausted reports whether the budget ran out, as opposed to the turn being
// canceled for another reason.
func (b *turnBudget) exhausted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.elapsed
}

// end releases the timer and the context.
func (b *turnBudget) end() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.paused && !b.elapsed && b.stop != nil {
		b.stop()
	}
	b.cancel(context.Canceled)
}
