package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

func TestARotationRunsOnTheNamedDatabase(t *testing.T) {
	var namespace, name string
	rotate := func(_ context.Context, ns, n string) error {
		namespace, name = ns, n

		return nil
	}

	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, nil, rotate, []hub.Work{
		{ID: "w1", Kind: hub.WorkRotate, Database: "pds.orders-copy"},
	})

	require.Len(t, results, 1)
	assert.Empty(t, results[0].Error)
	assert.Equal(t, "pds", namespace)
	assert.Equal(t, "orders-copy", name)
}

// A restore that has not finished is no answer: the item stays with the hub
// and comes back, instead of being dropped with the copy still on the source's
// passwords.
func TestARotationBeforeTheRestoreFinishesIsLeftUnreported(t *testing.T) {
	rotate := func(context.Context, string, string) error { return engine.ErrRestoreInProgress }

	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, nil, rotate, []hub.Work{
		{ID: "w1", Kind: hub.WorkRotate, Database: "pds.orders-copy"},
	})

	assert.Empty(t, results)
}

func TestAFailedRotationIsReported(t *testing.T) {
	rotate := func(context.Context, string, string) error { return errors.New("forbidden") }

	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, nil, rotate, []hub.Work{
		{ID: "w1", Kind: hub.WorkRotate, Database: "pds.orders-copy"},
	})

	require.Len(t, results, 1)
	assert.Contains(t, results[0].Error, "forbidden")
}

func TestARotationWithNoRotatorIsReported(t *testing.T) {
	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, nil, nil, []hub.Work{
		{ID: "w1", Kind: hub.WorkRotate, Database: "pds.orders-copy"},
	})

	require.Len(t, results, 1)
	assert.Contains(t, results[0].Error, "cannot")
}

func TestARotationWithoutANamespacedDatabaseIsReported(t *testing.T) {
	rotate := func(context.Context, string, string) error { return nil }

	results := applyWork(context.Background(), quietLogger(), &fakeDatabases{}, nil, nil, rotate, []hub.Work{
		{ID: "w1", Kind: hub.WorkRotate, Database: "orders-copy"},
	})

	require.Len(t, results, 1)
	assert.Contains(t, results[0].Error, "namespace")
}
