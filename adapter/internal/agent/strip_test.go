package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
)

func readyInstance() engine.Instance {
	return engine.Instance{
		Name:      "orders-dev",
		Namespace: "pds",
		Connection: &engine.Connection{
			Host:        "orders-dev-haproxy.pds",
			Port:        3306,
			Username:    "root",
			SecretName:  "orders-dev-secrets",
			ServiceName: "orders-dev-haproxy",
			URI:         "mysql://root:s3cr3t@orders-dev-haproxy.pds:3306",
		},
	}
}

// The heartbeat leaves the customer environment. A password in it would put every
// credential in the fleet in hub memory, refreshed on every beat.
func TestStripCredentialsRemovesTheURI(t *testing.T) {
	out := stripCredentials([]engine.Instance{readyInstance()})

	require.Len(t, out, 1)
	require.NotNil(t, out[0].Connection)
	assert.Empty(t, out[0].Connection.URI)
}

// Host, port and username are not secrets, and the hub renders them.
func TestStripCredentialsKeepsWhatTheCardRenders(t *testing.T) {
	out := stripCredentials([]engine.Instance{readyInstance()})

	require.Len(t, out, 1)
	require.NotNil(t, out[0].Connection)
	assert.Equal(t, "orders-dev-haproxy.pds", out[0].Connection.Host)
	assert.Equal(t, 3306, out[0].Connection.Port)
	assert.Equal(t, "root", out[0].Connection.Username)
}

// Neither the Secret name nor the Service name is a credential, and the hub
// needs both: they are what the connect dialog builds a get-secret and a
// port-forward command from when it holds no password of its own.
func TestStripCredentialsKeepsSecretAndServiceNames(t *testing.T) {
	out := stripCredentials([]engine.Instance{readyInstance()})

	require.Len(t, out, 1)
	require.NotNil(t, out[0].Connection)
	assert.Equal(t, "orders-dev-secrets", out[0].Connection.SecretName)
	assert.Equal(t, "orders-dev-haproxy", out[0].Connection.ServiceName)
	assert.Empty(t, out[0].Connection.URI)
}

// The caller's instances come from the engine and may be reused, so stripping
// must not reach back into them.
func TestStripCredentialsLeavesTheSourceAlone(t *testing.T) {
	in := []engine.Instance{readyInstance()}

	stripCredentials(in)

	require.NotNil(t, in[0].Connection)
	assert.Contains(t, in[0].Connection.URI, "s3cr3t")
}

// A database that is still provisioning has no connection at all.
func TestStripCredentialsHandlesAMissingConnection(t *testing.T) {
	out := stripCredentials([]engine.Instance{{Name: "orders-dev"}})

	require.Len(t, out, 1)
	assert.Nil(t, out[0].Connection)
}

// A public database is the connection string a client outside the environment
// copies, so the heartbeat carries it. The password of every other database
// stays on the environment.
func TestStripCredentialsKeepsTheURIOfAPublicDatabase(t *testing.T) {
	in := readyInstance()
	in.Exposure = engine.Exposure{External: true, Address: "198.51.100.20"}
	in.Connection.Host = "198.51.100.20"
	in.Connection.URI = "mysql://root:s3cr3t@198.51.100.20:3306"

	out := stripCredentials([]engine.Instance{in})

	require.Len(t, out, 1)
	require.NotNil(t, out[0].Connection)
	assert.Equal(t, "mysql://root:s3cr3t@198.51.100.20:3306", out[0].Connection.URI)
}

// External with no address yet is still the in-cluster host. That password
// does not leave the environment.
func TestStripCredentialsRemovesTheURIWhileTheAddressIsPending(t *testing.T) {
	in := readyInstance()
	in.Exposure = engine.Exposure{External: true}

	out := stripCredentials([]engine.Instance{in})

	require.Len(t, out, 1)
	require.NotNil(t, out[0].Connection)
	assert.Empty(t, out[0].Connection.URI)
}

// A private database is reachable from the customer's VPC, not from wherever
// the hub runs, and every MySQL create gets one. Keeping its string would put
// the whole fleet's passwords in hub memory, so it stays on the environment.
func TestStripCredentialsRemovesTheURIOfAPrivateDatabase(t *testing.T) {
	in := readyInstance()
	in.Exposure = engine.Exposure{External: true, Private: true, Address: "internal-a1b2.elb.amazonaws.com"}
	in.Connection.Host = "internal-a1b2.elb.amazonaws.com"
	in.Connection.URI = "mysql://root:s3cr3t@internal-a1b2.elb.amazonaws.com:3306"

	out := stripCredentials([]engine.Instance{in})

	require.Len(t, out, 1)
	require.NotNil(t, out[0].Connection)
	assert.Empty(t, out[0].Connection.URI)
	assert.Equal(t, "internal-a1b2.elb.amazonaws.com", out[0].Connection.Host)
}

func TestStripCredentialsHandlesNothing(t *testing.T) {
	assert.Empty(t, stripCredentials(nil))
}
