// Package mongodb is the MongoDB engine plugin. It is the shared OpenEverest
// plugin with the MongoDB flavor. The hub writes Instance specs.
package mongodb

import (
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/oe"
)

const EngineID = "mongodb"

const DefaultNamespace = oe.DefaultNamespace

type Plugin = oe.Plugin
type Option = oe.Option
type EverestClient = oe.EverestClient

var (
	WithNamespace     = oe.WithNamespace
	WithBackupStorage = oe.WithBackupStorage
	WithBackupDeleter = oe.WithBackupDeleter
)

// New returns a MongoDB plugin that talks to the given Everest client.
func New(client EverestClient, opts ...Option) *Plugin {
	return oe.New(client, oe.MongoDB, opts...)
}

var _ engine.ReadWritePlugin = (*Plugin)(nil)
