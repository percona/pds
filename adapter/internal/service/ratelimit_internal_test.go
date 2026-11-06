package service

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRateLimiterPrunesKeysOnceTheirWindowEnds(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	limiter := newRateLimiter(5, time.Minute, 100, func() time.Time { return now })

	for i := range 50 {
		assert.True(t, limiter.allow("client-"+strconv.Itoa(i)))
	}
	assert.Len(t, limiter.events, 50)

	now = now.Add(30 * time.Second)
	assert.True(t, limiter.allow("late"))
	assert.Len(t, limiter.events, 51, "nothing has aged out yet")

	now = now.Add(45 * time.Second)
	assert.True(t, limiter.allow("fresh"))
	assert.Len(t, limiter.events, 2, "only the clients seen within the window remain")
	assert.Contains(t, limiter.events, "late")
	assert.Contains(t, limiter.events, "fresh")
}

func TestRateLimiterKeepsAtMostLimitEventsPerKey(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	limiter := newRateLimiter(3, time.Minute, 100, func() time.Time { return now })

	for range 3 {
		assert.True(t, limiter.allow("a"))
	}
	for range 10 {
		assert.False(t, limiter.allow("a"))
	}
	assert.Len(t, limiter.events["a"], 3, "refused events are not recorded")
}

func TestRateLimiterSlidesRatherThanResetting(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	limiter := newRateLimiter(2, time.Minute, 100, func() time.Time { return now })

	assert.True(t, limiter.allow("a"))
	now = now.Add(40 * time.Second)
	assert.True(t, limiter.allow("a"))
	assert.False(t, limiter.allow("a"))

	now = now.Add(21 * time.Second)
	assert.True(t, limiter.allow("a"), "the first event aged out")
	assert.False(t, limiter.allow("a"), "the second one has not")
}

func TestRateLimiterRefusesNewKeysWhenFull(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	limiter := newRateLimiter(5, time.Minute, 3, func() time.Time { return now })

	for _, key := range []string{"a", "b", "c"} {
		assert.True(t, limiter.allow(key))
	}
	assert.False(t, limiter.allow("d"), "every slot is still in its window")
	assert.True(t, limiter.allow("a"), "a known client still counts as usual")
	assert.Len(t, limiter.events, 3)

	now = now.Add(2 * time.Minute)
	assert.True(t, limiter.allow("d"), "the old clients aged out")
	assert.Len(t, limiter.events, 1)
}
