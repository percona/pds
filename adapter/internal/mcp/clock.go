package mcp

import (
	"context"
	"time"
)

// Clock is time as the wait tools see it. A test replaces it, so that waiting
// half a minute takes no time.
type Clock interface {
	Now() time.Time
	// Sleep returns after d, or as soon as ctx ends.
	Sleep(ctx context.Context, d time.Duration)
	// WithTimeout is context.WithTimeout on this clock.
	WithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc)
}

// systemClock is the clock of the machine.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) Sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

func (systemClock) WithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}
