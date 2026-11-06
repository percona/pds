package kube

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseResourceQuantities(t *testing.T) {
	cpu, err := resourceQuantity("487558n")
	assert.NoError(t, err)
	assert.InDelta(t, 0.000487558, cpu, 0.0000001)

	cpu, err = resourceQuantity("500m")
	assert.NoError(t, err)
	assert.InDelta(t, 0.5, cpu, 0.001)

	cpu, err = resourceQuantity("2")
	assert.NoError(t, err)
	assert.Equal(t, 2.0, cpu)
	_, err = resourceQuantity("")
	assert.Error(t, err)
	_, err = parseMilli("nope")
	assert.Error(t, err)

	mem, err := memoryQuantity("2Mi")
	assert.NoError(t, err)
	assert.Equal(t, 2*1024*1024.0, mem)
	mem, err = memoryQuantity("1Gi")
	assert.NoError(t, err)
	assert.Equal(t, 1024*1024*1024.0, mem)
	mem, err = memoryQuantity("4Ki")
	assert.NoError(t, err)
	assert.Equal(t, 4*1024.0, mem)
	_, err = memoryQuantity("")
	assert.Error(t, err)
	_, err = memoryQuantity("nope")
	assert.Error(t, err)

	got, ok := metricFor("orders", []string{"orders", "orders-prod"}, map[string]cpuMem{
		"orders-pxc-0":      {cpu: 0.2, memory: 10},
		"orders-prod-pxc-0": {cpu: 4, memory: 40},
	})
	assert.True(t, ok)
	assert.InDelta(t, 0.2, got.cpu, 0.001)
	assert.Equal(t, 10.0, got.memory)
	_, ok = metricFor("", nil, map[string]cpuMem{"orders": {}})
	assert.False(t, ok)
}
