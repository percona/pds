package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
)

func TestRetainHandsTheListedDatabasesToTheRetentionPass(t *testing.T) {
	var got []engine.Instance
	opts := Options{Retain: func(_ context.Context, items []engine.Instance) (int, error) {
		got = items

		return 1, nil
	}}
	items := []engine.Instance{{Name: "orders", Engine: "mongodb"}}

	retainBackups(context.Background(), opts, items)

	assert.Equal(t, items, got)
}

func TestRetainWithoutAPassDoesNothing(t *testing.T) {
	require.NotPanics(t, func() {
		retainBackups(context.Background(), Options{}, []engine.Instance{{Name: "orders"}})
	})
}

func TestARetentionErrorIsLoggedAndNeverStopsTheBeat(t *testing.T) {
	called := false
	opts := Options{Retain: func(context.Context, []engine.Instance) (int, error) {
		called = true

		return 0, errors.New("forbidden")
	}}

	require.NotPanics(t, func() { retainBackups(context.Background(), opts, nil) })
	assert.True(t, called)
}
