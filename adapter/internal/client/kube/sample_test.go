package kube_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientfake "k8s.io/client-go/kubernetes/fake"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/engine"
)

func TestSampleDatabasesKeepsStorageWhenMetricsServerIsAbsent(t *testing.T) {
	client := kube.New(clientfake.NewClientset())
	events, usage, err := client.SampleDatabases(t.Context(), "pds", []engine.Instance{{
		ID:     "pds.orders",
		Name:   "orders",
		Sizing: engine.Sizing{StorageGB: 20},
	}})
	require.NoError(t, err)
	assert.Empty(t, events)
	require.Len(t, usage, 1)
	assert.Nil(t, usage[0].CPUAvg)
	assert.Nil(t, usage[0].MemoryAvg)
	require.NotNil(t, usage[0].StorageBytes)
	assert.Equal(t, int64(20*1024*1024*1024), *usage[0].StorageBytes)
	assert.Equal(t, "metrics-server did not answer", usage[0].MetricsReason)
}
