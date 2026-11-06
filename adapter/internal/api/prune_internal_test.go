package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// One team's prune must not hold back another's: a hub serving several
// teams keeps each one's seven day retention.
func TestPruneGateIsPerTeam(t *testing.T) {
	var gate pruneGate
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	assert.True(t, gate.due(1, now))
	assert.False(t, gate.due(1, now.Add(time.Minute)), "acme pruned a minute ago")
	assert.True(t, gate.due(2, now.Add(time.Minute)), "acme's prune says nothing about globex")
	assert.False(t, gate.due(2, now.Add(2*time.Minute)))
	assert.True(t, gate.due(1, now.Add(pruneEvery)))
}
