package oe

import "github.com/percona/pds/adapter/internal/engine"

// The hub writes Instance manifests without going through a plugin, so the
// profile table, the override bounds and the proxy replica rule have to be
// reachable from outside this package. Two copies of any of them is two
// answers to what a Prod database is.

// Sizing is a catalog profile with its overrides applied and bounds checked.
type Sizing struct {
	Replicas  int32
	CPUCores  int
	MemoryGB  int
	StorageGB int
}

// LookupProfile returns one catalog profile by name.
func LookupProfile(name string) (engine.Profile, error) {
	return lookupProfile(name)
}

// ResolveSizing applies the advanced overrides to a profile and validates the
// result. This is the one place those bounds are enforced.
func ResolveSizing(p engine.Profile, o engine.ResourceOverrides) (Sizing, error) {
	s, err := resolveSizing(p, o)
	if err != nil {
		return Sizing{}, err
	}

	return Sizing(s), nil
}

// ProxyReplicasFor is how many proxies sit in front of a given engine tier.
func ProxyReplicasFor(engineReplicas int32) int32 {
	return proxyReplicasFor(engineReplicas)
}

// LookupFlavor returns the flavor an engine id names.
func LookupFlavor(id string) (Flavor, bool) {
	switch id {
	case MySQL.ID:
		return MySQL, true
	case PostgreSQL.ID:
		return PostgreSQL, true
	case MongoDB.ID:
		return MongoDB, true
	}

	return Flavor{}, false
}
