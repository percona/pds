// Package postgresql is the PostgreSQL engine plugin. It is the MySQL plugin
// with the PostgreSQL flavor. The hub writes Instance specs.
package postgresql

import (
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/oe"
)

const EngineID = "postgresql"

// LeftoverSecrets names the Secrets the PostgreSQL operator leaves behind when
// the database is deleted: the users it generates and the TLS Secrets its
// finalizer removes by name. The agent may delete Secrets in the database
// namespace but not list or read them, so cleanup names each one instead of
// searching by the operator's cluster label. The per-member "-certs" Secrets
// end in a random suffix and cannot be named, so they stay.
func LeftoverSecrets(database string) []string {
	return []string{
		database + "-pguser-postgres",
		database + "-pguser-monitor",
		database + "-pgbouncer",
		database + "-cluster-cert",
		database + "-replication-cert",
		database + "-pgbackrest",
	}
}

const DefaultNamespace = oe.DefaultNamespace

type Plugin = oe.Plugin
type Option = oe.Option
type EverestClient = oe.EverestClient

var (
	WithNamespace     = oe.WithNamespace
	WithBackupStorage = oe.WithBackupStorage
)

// New returns a PostgreSQL plugin that talks to the given Everest client.
func New(client EverestClient, opts ...Option) *Plugin {
	return oe.New(client, oe.PostgreSQL, opts...)
}

var _ engine.ReadWritePlugin = (*Plugin)(nil)
