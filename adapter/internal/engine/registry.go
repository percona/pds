package engine

import (
	"errors"
	"sort"
)

// ErrUnknownEngine is returned when Get is called for an engine that was never registered.
var ErrUnknownEngine = errors.New("unknown engine")

// Registry holds engine plugins keyed by engine identifier.
type Registry struct {
	plugins map[string]Plugin
}

// NewRegistry returns an empty plugin registry.
func NewRegistry() *Registry {
	return &Registry{
		plugins: make(map[string]Plugin),
	}
}

// Register adds a plugin. A second registration for the same engine replaces the first.
func (r *Registry) Register(p Plugin) {
	r.plugins[p.Engine()] = p
}

// Get returns the plugin for engine, or ErrUnknownEngine when it is not registered.
func (r *Registry) Get(engine string) (Plugin, error) {
	p, ok := r.plugins[engine]
	if !ok {
		return nil, ErrUnknownEngine
	}

	return p, nil
}

// All returns the registered plugins, ordered by engine identifier.
func (r *Registry) All() []Plugin {
	if r == nil {
		return nil
	}

	out := make([]Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Engine() < out[j].Engine() })

	return out
}
