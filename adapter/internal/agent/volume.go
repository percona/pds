package agent

import (
	"strings"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

const volumeResizeRefused = "Storage cannot grow. The storage class of the Kubernetes cluster does not allow volume expansion, so the volume keeps its current size."

// markRefusedResizes keeps a database from reading Ready at a size its volume
// does not have. When Kubernetes refuses to expand a volume, the PostgreSQL
// operator records a warning and carries on, and the provider still reports
// Ready with the size from the spec. A database with that warning in this
// beat's events is reported as updating, with the reason, until the warning
// stops.
func markRefusedResizes(items []engine.Instance, events []hub.HeartbeatEvent) {
	refused := map[string]bool{}
	for _, event := range events {
		if event.Type == "Warning" && event.Reason == "PersistentVolumeError" &&
			strings.Contains(strings.ToLower(event.Message), "resiz") {
			refused[event.DatabaseID] = true
		}
	}
	if len(refused) == 0 {
		return
	}

	for i := range items {
		if refused[items[i].ID] && items[i].Status.Phase == engine.PhaseReady {
			items[i].Status = engine.Status{Phase: engine.PhaseUpdating, Message: volumeResizeRefused}
		}
	}
}
