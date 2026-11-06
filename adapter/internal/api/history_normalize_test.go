package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

func TestNormalizeHeartbeatUsesHubIDs(t *testing.T) {
	instances, backups, events, usage := normalizeHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		Instances: []engine.Instance{{
			ID: "pds.orders", Name: "orders", Namespace: "pds",
		}},
		Backups: []engine.Backup{{ID: "bak-1", Instance: "orders"}},
		Events: []hub.HeartbeatEvent{{
			DatabaseID: "pds.orders", At: time.Now(), Message: "pulled",
		}},
		Usage: []hub.HeartbeatUsage{{
			DatabaseID: "orders", At: time.Now(),
		}},
	})
	want := hub.JoinID("spoke-a", "pds", "orders")
	require.Len(t, instances, 1)
	assert.Equal(t, want, instances[0].ID)
	require.Len(t, backups, 1)
	assert.Equal(t, want, backups[0].Instance)

	_, omitted, _, _ := normalizeHeartbeat(hub.Heartbeat{
		Environment: "spoke-a", Namespace: "pds", OmitInstances: true,
		Backups: []engine.Backup{{ID: "bak-stale", Instance: "orders"}},
	})
	assert.Empty(t, omitted)
	require.Len(t, events, 1)
	assert.Equal(t, want, events[0].DatabaseID)
	require.Len(t, usage, 1)
	assert.Equal(t, want, usage[0].DatabaseID)

	_, foreignBackups, foreignEvents, foreignUsage := normalizeHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		Instances:   []engine.Instance{{ID: "pds.orders", Name: "orders", Namespace: "pds"}},
		Backups:     []engine.Backup{{ID: "bak-other", Instance: "other-environment/pds.billing"}},
		Events:      []hub.HeartbeatEvent{{DatabaseID: "billing", Message: "no"}},
		Usage:       []hub.HeartbeatUsage{{DatabaseID: "spoke-b/pds.billing"}},
	})
	assert.Empty(t, foreignBackups)
	assert.Empty(t, foreignEvents)
	assert.Empty(t, foreignUsage)
}
