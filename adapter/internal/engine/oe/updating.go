package oe

import (
	"sync"
	"time"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
)

const updatingMessage = "Applying changes"

// readyMemory is the instances a plugin has seen Ready. A provider that
// reports every change as Provisioning cannot tell a database that is being
// changed from one that is being created, so the only difference is whether it
// has been Ready. The memory lives as long as the process: a database that is
// already changing when the agent starts reads Provisioning until it is Ready
// once.
type readyMemory struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (m *readyMemory) markReady(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = map[string]bool{}
	}
	m.seen[id] = true
}

func (m *readyMemory) wasReady(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.seen[id]
}

// keepOnly forgets every instance that is not in ids, so a deleted database
// that is created again under the same name starts as new.
func (m *readyMemory) keepOnly(ids map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.seen {
		if !ids[id] {
			delete(m.seen, id)
		}
	}
}

// settleUpdating turns the provider's Provisioning into Updating for a
// database that has been Ready, on a flavor whose provider reports every
// change as Provisioning. A database that was never Ready keeps what the
// provider said. It is the only place the rule lives, so it goes when the
// provider reports Updating itself.
func (p *Plugin) settleUpdating(id string, in everest.InstanceStatus, status engine.Status) engine.Status {
	if !p.flavor.ProvisioningIsUpdating {
		return status
	}
	switch in.Phase {
	case everest.PhaseReady:
		p.ready.markReady(id)
	case everest.PhaseProvisioning:
		if p.ready.wasReady(id) {
			message := startBlockerMessage(in, time.Now())
			if message == "" {
				message = updatingMessage
			}

			return engine.Status{Phase: engine.PhaseUpdating, Message: message}
		}
	}

	return status
}
