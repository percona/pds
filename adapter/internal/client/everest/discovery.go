package everest

import (
	"context"
	"net/http"
	"net/url"
)

// PinnedRelease is the OpenEverest version this adapter's client paths
// were read from. A 404 on one of those paths is a drift, not a transient
// fault.
const PinnedRelease = "2.0.0-dev.4"

// Token bootstrap paths on the pinned release. AuthToken is the password
// grant the chart Job posts with the admin password, and the refresh grant.
// AuthRevoke is logout and is not required for the adapter.
const (
	AuthTokenPath  = APIPrefix + "/auth/token"
	AuthRevokePath = APIPrefix + "/auth/revoke"
)

// NamedObject is a GET response reduced to metadata, which is enough to
// confirm a MonitoringConfig exists.
type NamedObject struct {
	Metadata ObjectMeta `json:"metadata"`
}

// GetMonitoringConfig returns one MonitoringConfig in a namespace.
func (c *Client) GetMonitoringConfig(ctx context.Context, namespace, name string) (*NamedObject, error) {
	if err := requireAll("namespace", namespace, "monitoring config name", name); err != nil {
		return nil, err
	}

	var out NamedObject
	path := c.namespacePath(namespace) + "/monitoring-configs/" + url.PathEscape(name)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}

	return &out, nil
}
