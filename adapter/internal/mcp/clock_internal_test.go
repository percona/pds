package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTheSystemClockSleepEndsWhenTheContextDoes(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	slept := make(chan struct{})

	go func() {
		systemClock{}.Sleep(ctx, time.Hour)
		close(slept)
	}()

	select {
	case <-slept:
	case <-time.After(10 * time.Second):
		t.Fatal("Sleep did not return when its context ended")
	}
}

func TestTheSystemClockSleepsForTheDuration(t *testing.T) {
	clock := systemClock{}
	start := clock.Now()

	clock.Sleep(t.Context(), 20*time.Millisecond)

	assert.GreaterOrEqual(t, clock.Now().Sub(start), 20*time.Millisecond)
}

func TestTheSystemClockTimeoutEndsTheContext(t *testing.T) {
	ctx, cancel := systemClock{}.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	<-ctx.Done()

	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
}
