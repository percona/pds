package oe

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
)

// adminDatabase is where the provider's database administrator is defined, so
// it is the database a client authenticates against.
const adminDatabase = "admin"

// toReplicaSetConnection builds the connection of an engine whose clients
// reach the members directly.
//
// The provider's Secret holds a comma separated member list in its host and a
// constant port, and its own uri names no replica set and leaves the password
// unescaped. None of that is a string a driver can use, so the string is built
// here from the host list, the username and the password, and the provider's
// is never read. The connection inside the Kubernetes cluster is plain: the
// provider's members refuse a TLS connection that presents no client
// certificate.
func (p *Plugin) toReplicaSetConnection(conn *everest.Connection, namespace, secretName string) *engine.Connection {
	members := replicaSetMembers(conn.Host, int(conn.Port))
	if len(members) == 0 {
		return nil
	}

	host, port := splitMember(members[0])
	out := &engine.Connection{
		Host:        host,
		Port:        port,
		Members:     members,
		Username:    conn.Username,
		SecretName:  secretName,
		ServiceName: memberServiceName(host, namespace),
	}

	userInfo := escapeUserInfo(conn.Username)
	if conn.Password != "" {
		userInfo += ":" + escapeUserInfo(conn.Password)
	}
	out.URI = p.flavor.URIScheme + "://" + userInfo + "@" + strings.Join(members, ",") +
		"/" + adminDatabase + "?replicaSet=" + p.flavor.ReplicaSet + "&ssl=false"

	return out
}

// replicaSetMembers splits the Secret's host list into host:port entries. A
// member without a port takes the Secret's.
func replicaSetMembers(list string, defaultPort int) []string {
	var members []string
	for entry := range strings.SplitSeq(list, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(entry); err != nil && defaultPort > 0 {
			entry = net.JoinHostPort(strings.Trim(entry, "[]"), strconv.Itoa(defaultPort))
		}
		members = append(members, entry)
	}

	return members
}

// splitMember returns the host and port of one host:port member. The port is
// zero when the member carries none.
func splitMember(member string) (string, int) {
	host, rawPort, err := net.SplitHostPort(member)
	if err != nil {
		return member, 0
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		return host, 0
	}

	return host, port
}

// memberServiceName is the Service in front of one member, which the provider
// names after the member's pod. The host of a member is
// `<pod>.<headless service>.<namespace>`, and a host outside this namespace
// yields nothing rather than a guess.
func memberServiceName(host, namespace string) string {
	labels := strings.Split(host, ".")
	if namespace == "" || len(labels) < 3 || labels[2] != namespace {
		return ""
	}

	return labels[0]
}

// escapeUserInfo percent-escapes everything but the unreserved characters, so
// a username or password holding any delimiter reads back as it was written.
func escapeUserInfo(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}
