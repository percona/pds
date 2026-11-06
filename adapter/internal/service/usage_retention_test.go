package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/service"
)

type fakePruner struct {
	mu      sync.Mutex
	befores []time.Time
	err     error
}

func (f *fakePruner) PruneUsage(_ context.Context, before time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.befores = append(f.befores, before)

	return 1, f.err
}

func (f *fakePruner) calls() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]time.Time(nil), f.befores...)
}

// The loop prunes once at start and then on every tick, each time cutting at
// the retention before that moment, until its context ends.
func TestKeepUsageRetentionPrunesOnAScheduleUntilStopped(t *testing.T) {
	pruner := &fakePruner{}
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		service.KeepUsageRetention(ctx, pruner, 10*time.Millisecond,
			func() time.Time { return now }, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()

	require.Eventually(t, func() bool { return len(pruner.calls()) >= 3 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the retention loop did not stop with its context")
	}
	for _, before := range pruner.calls() {
		assert.Equal(t, now.Add(-service.UsageRetention), before)
	}
}

// A failed prune is logged and the loop keeps going.
func TestKeepUsageRetentionSurvivesAFailedPrune(t *testing.T) {
	pruner := &fakePruner{err: errors.New("the adapter database is not connected yet")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go service.KeepUsageRetention(ctx, pruner, 10*time.Millisecond, time.Now,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	require.Eventually(t, func() bool { return len(pruner.calls()) >= 2 }, 2*time.Second, 5*time.Millisecond)
}
