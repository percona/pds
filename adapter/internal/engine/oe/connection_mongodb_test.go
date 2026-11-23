package oe

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
)

const mongoMembers = "orders-rs0-0.orders-rs0.pds.svc.cluster.local:27017," +
	"orders-rs0-1.orders-rs0.pds.svc.cluster.local:27017," +
	"orders-rs0-2.orders-rs0.pds.svc.cluster.local:27017"

func mongoSecret(password string) *everest.Connection {
	return &everest.Connection{
		Host:     mongoMembers,
		Port:     27017,
		Username: "databaseAdmin",
		Password: password,
		// The provider's own string has no replica set and an unescaped
		// password, so it must never reach the result.
		URI: "mongodb://databaseAdmin:" + password + "@" + mongoMembers + "/admin?ssl=false",
	}
}

func TestMongoDBConnectionListsEveryMember(t *testing.T) {
	t.Parallel()

	p := &Plugin{flavor: MongoDB}
	out := p.toConnection(mongoSecret("s3cr3t"), "pds", "orders-conn")

	require.NotNil(t, out)
	assert.Equal(t, []string{
		"orders-rs0-0.orders-rs0.pds.svc.cluster.local:27017",
		"orders-rs0-1.orders-rs0.pds.svc.cluster.local:27017",
		"orders-rs0-2.orders-rs0.pds.svc.cluster.local:27017",
	}, out.Members)
	assert.Equal(t, "orders-rs0-0.orders-rs0.pds.svc.cluster.local", out.Host)
	assert.Equal(t, 27017, out.Port)
	assert.Equal(t, "databaseAdmin", out.Username)
	assert.Equal(t, "orders-conn", out.SecretName)
	assert.Equal(t, "orders-rs0-0", out.ServiceName, "a port-forward targets one member's Service")
}

func TestMongoDBConnectionStringNamesTheReplicaSetAndIgnoresTheProviderString(t *testing.T) {
	t.Parallel()

	p := &Plugin{flavor: MongoDB}
	out := p.toConnection(mongoSecret("s3cr3t"), "pds", "")

	require.NotNil(t, out)
	assert.Equal(t, "mongodb://databaseAdmin:s3cr3t@"+mongoMembers+"/admin?replicaSet=rs0&ssl=false", out.URI)
}

func TestMongoDBConnectionOfOneMember(t *testing.T) {
	t.Parallel()

	p := &Plugin{flavor: MongoDB}
	out := p.toConnection(&everest.Connection{
		Host:     "orders-rs0-0.orders-rs0.pds.svc.cluster.local:27017",
		Port:     27017,
		Username: "databaseAdmin",
		Password: "s3cr3t",
	}, "pds", "")

	require.NotNil(t, out)
	assert.Equal(t, []string{"orders-rs0-0.orders-rs0.pds.svc.cluster.local:27017"}, out.Members)
	assert.Equal(t,
		"mongodb://databaseAdmin:s3cr3t@orders-rs0-0.orders-rs0.pds.svc.cluster.local:27017/admin?replicaSet=rs0&ssl=false",
		out.URI)
}

func TestMongoDBMemberWithoutAPortTakesTheSecretsPort(t *testing.T) {
	t.Parallel()

	p := &Plugin{flavor: MongoDB}
	out := p.toConnection(&everest.Connection{
		Host: "orders-rs0-0.orders-rs0.pds.svc.cluster.local", Port: 27017, Username: "databaseAdmin", Password: "x",
	}, "pds", "")

	require.NotNil(t, out)
	assert.Equal(t, []string{"orders-rs0-0.orders-rs0.pds.svc.cluster.local:27017"}, out.Members)
}

// A driver reads the password out of the string, so a reserved character has
// to survive the round trip.
func TestMongoDBConnectionStringEscapesTheCredential(t *testing.T) {
	t.Parallel()

	for _, password := range []string{"p@ss:w/rd", "a%b#c?d", "sp ace+plus", "[x]-y_z.~", "é"} {
		p := &Plugin{flavor: MongoDB}
		out := p.toConnection(mongoSecret(password), "pds", "")

		require.NotNil(t, out)
		parsed, err := url.Parse(out.URI)
		require.NoError(t, err, password)
		got, ok := parsed.User.Password()
		require.True(t, ok)
		assert.Equal(t, password, got)
		assert.Equal(t, "databaseAdmin", parsed.User.Username())
		assert.Equal(t, mongoMembers, parsed.Host)
		assert.Equal(t, "/admin", parsed.Path)
		assert.Equal(t, "rs0", parsed.Query().Get("replicaSet"))
		assert.Equal(t, "false", parsed.Query().Get("ssl"))
		assert.NotContains(t, out.URI, password, "the raw password would break a parser")
	}
}

func TestMongoDBServiceNameIsEmptyOutsideTheNamespace(t *testing.T) {
	t.Parallel()

	p := &Plugin{flavor: MongoDB}
	out := p.toConnection(mongoSecret("x"), "other", "")

	require.NotNil(t, out)
	assert.Empty(t, out.ServiceName)
}

func TestMySQLAndPostgreSQLConnectionsCarryNoMembers(t *testing.T) {
	t.Parallel()

	for _, flavor := range []Flavor{MySQL, PostgreSQL} {
		p := &Plugin{flavor: flavor}
		out := p.toConnection(&everest.Connection{Host: "orders.pds", Port: 3306, Username: "u", Password: "p"}, "pds", "")

		require.NotNil(t, out)
		assert.Empty(t, out.Members)
	}
}
