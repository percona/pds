package oe

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
)

func TestToConnectionFillsSecretName(t *testing.T) {
	t.Parallel()

	p := &Plugin{flavor: MySQL}
	out := p.toConnection(&everest.Connection{
		Host:     "orders-dev-haproxy.pds",
		Port:     3306,
		Username: "app",
		URI:      "mysql://app:s3cr3t@orders-dev-haproxy.pds:3306",
	}, "pds", "orders-dev-secrets")

	require.NotNil(t, out)
	assert.Equal(t, "orders-dev-secrets", out.SecretName)
}

// The Secret name is what a developer reads the credential with when the
// adapter cannot hand it over, so an instance that has not published one yet
// must not invent a name.
func TestToConnectionLeavesSecretNameEmptyWhenUnpublished(t *testing.T) {
	t.Parallel()

	p := &Plugin{flavor: MySQL}
	out := p.toConnection(&everest.Connection{
		Host:     "orders-dev-haproxy.pds",
		Port:     3306,
		Username: "app",
	}, "pds", "")

	require.NotNil(t, out)
	assert.Empty(t, out.SecretName)
}

// The Service name is what a port-forward targets. A host that does not name a
// Service in this namespace yields nothing rather than a guess, because a
// command built on a guess fails with a kubectl error that reads like the
// database is broken.
func TestServiceNameFromHost(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		host      string
		namespace string
		want      string
	}{
		{
			name:      "in-cluster short form",
			host:      "orders-dev-haproxy.pds",
			namespace: "pds",
			want:      "orders-dev-haproxy",
		},
		{
			name:      "in-cluster fully qualified",
			host:      "orders-dev-haproxy.pds.svc.cluster.local",
			namespace: "pds",
			want:      "orders-dev-haproxy",
		},
		{
			name:      "another namespace",
			host:      "orders-dev-haproxy.other-namespace",
			namespace: "pds",
			want:      "",
		},
		{
			name:      "external host",
			host:      "orders-dev.example.com",
			namespace: "pds",
			want:      "",
		},
		{
			name:      "bare service name",
			host:      "orders-dev-haproxy",
			namespace: "pds",
			want:      "",
		},
		{
			name:      "ipv4 address",
			host:      "10.43.12.7",
			namespace: "pds",
			want:      "",
		},
		{
			name:      "empty host",
			host:      "",
			namespace: "pds",
			want:      "",
		},
		{
			name:      "empty namespace",
			host:      "orders-dev-haproxy.pds",
			namespace: "",
			want:      "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, serviceNameFromHost(tc.host, tc.namespace))
		})
	}
}

func TestToConnectionFillsServiceName(t *testing.T) {
	t.Parallel()

	p := &Plugin{flavor: MySQL}
	out := p.toConnection(&everest.Connection{
		Host:     "orders-dev-haproxy.pds",
		Port:     3306,
		Username: "app",
	}, "pds", "orders-dev-secrets")

	require.NotNil(t, out)
	assert.Equal(t, "orders-dev-haproxy", out.ServiceName)
}

// A load balancer can hand out an IPv6 address, and a URI only parses with
// that address in brackets.
func TestToConnectionBracketsAnIPv6Host(t *testing.T) {
	t.Parallel()

	p := &Plugin{flavor: MySQL}
	for _, tc := range []struct {
		port int32
		want string
	}{
		{port: 3306, want: "mysql://root:s3cr3t@[2001:db8::20]:3306"},
		{port: 0, want: "mysql://root:s3cr3t@[2001:db8::20]"},
	} {
		out := p.toConnection(&everest.Connection{
			Host:     "2001:db8::20",
			Port:     everest.Port(tc.port),
			Username: "root",
			Password: "s3cr3t",
		}, "pds", "")

		require.NotNil(t, out)
		assert.Equal(t, tc.want, out.URI)
		assert.Equal(t, "2001:db8::20", out.Host, "the host field stays the bare address")
	}
}
