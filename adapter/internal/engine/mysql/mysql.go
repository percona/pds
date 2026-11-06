// Package mysql is the MySQL engine plugin. The hub writes Instance specs.
// This package lists, backs up, scales, and deletes them.
package mysql

import (
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/oe"
)

const EngineID = "mysql"

const (
	DefaultNamespace     = oe.DefaultNamespace
	LabelManagedBy       = oe.LabelManagedBy
	LabelProfile         = oe.LabelProfile
	LegacyLabelProfile   = oe.LegacyLabelProfile
	ManagedBy            = oe.ManagedBy
	LegacyManagedBy      = oe.LegacyManagedBy
	ProfileCustom        = oe.ProfileCustom
	ProfileDev           = oe.ProfileDev
	ProfileStaging       = oe.ProfileStaging
	ProfileProd          = oe.ProfileProd
	BackupOnDemand       = oe.BackupOnDemand
	BackupDaily          = oe.BackupDaily
	MinCPUCores          = oe.MinCPUCores
	MaxCPUCores          = oe.MaxCPUCores
	MinMemoryGB          = oe.MinMemoryGB
	MaxMemoryGB          = oe.MaxMemoryGB
	MinStorageGB         = oe.MinStorageGB
	MaxStorageGB         = oe.MaxStorageGB
	DailyBackupCron      = oe.DailyBackupCron
	DailyBackupRetention = oe.DailyBackupRetention
	DefaultBackupStorage = oe.DefaultBackupStorage
)

var (
	ErrUnknownProfile      = oe.ErrUnknownProfile
	ErrInvalidName         = oe.ErrInvalidName
	ErrNamespaceNotManaged = oe.ErrNamespaceNotManaged
	ErrInvalidID           = oe.ErrInvalidID
	ErrOutOfBounds         = oe.ErrOutOfBounds
	ErrEmptyScale          = oe.ErrEmptyScale
	ErrStorageShrink       = oe.ErrStorageShrink
	ErrBusy                = oe.ErrBusy
	ErrInstanceExists      = oe.ErrInstanceExists
	ErrInstanceNotFound    = oe.ErrInstanceNotFound
	ErrUnparsableQuantity  = oe.ErrUnparsableQuantity
	ErrBackupsUnavailable  = oe.ErrBackupsUnavailable
)

// Plugin is the MySQL implementation of engine.ReadWritePlugin.
type Plugin = oe.Plugin

// Option configures a Plugin.
type Option = oe.Option

// EverestClient is the slice of the Everest API this plugin depends on.
type EverestClient = oe.EverestClient

// WithNamespace sets the Everest managed namespace new instances land in.
var WithNamespace = oe.WithNamespace

// WithBackupStorage sets the BackupStorage backups are written to.
var WithBackupStorage = oe.WithBackupStorage

// New returns a MySQL plugin that talks to the given Everest client.
func New(client EverestClient, opts ...Option) *Plugin {
	return oe.New(client, oe.MySQL, opts...)
}

var _ engine.ReadWritePlugin = (*Plugin)(nil)
