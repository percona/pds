package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
)

// stubPlugin is a minimal Plugin used to exercise the registry and interface conformance.
type stubPlugin struct {
	engineID string
}

func (s stubPlugin) Engine() string { return s.engineID }

func (s stubPlugin) Scale(_ context.Context, _ string, _ engine.ScaleInput) error { return nil }

func (s stubPlugin) Backup(_ context.Context, _ string) (*engine.Backup, error) { return nil, nil }

func (s stubPlugin) Delete(_ context.Context, _ string) error { return nil }

// Compile-time check: stubPlugin must satisfy engine.Plugin.
var _ engine.Plugin = stubPlugin{}

func TestRegistryRegisterAndGet(t *testing.T) {
	reg := engine.NewRegistry()
	plugin := stubPlugin{engineID: "mysql"}

	reg.Register(plugin)

	got, err := reg.Get("mysql")
	require.NoError(t, err)
	assert.Equal(t, plugin.Engine(), got.Engine())
}

func TestRegistryGetUnknownEngine(t *testing.T) {
	reg := engine.NewRegistry()

	_, err := reg.Get("postgres")
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrUnknownEngine))
}

func TestPluginInterfaceConformance(t *testing.T) {
	var p engine.Plugin = stubPlugin{engineID: "mysql"}
	assert.Equal(t, "mysql", p.Engine())
}
