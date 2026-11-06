package engine

import (
	"context"
	"time"
)

// Plugin is the contract the agent uses to change a database on its cluster.
type Plugin interface {
	// Engine returns the engine identifier ("mysql").
	Engine() string

	Scale(ctx context.Context, id string, in ScaleInput) error
	Backup(ctx context.Context, id string) (*Backup, error)
	Delete(ctx context.Context, id string) error
}

// Reader is the read path the agent uses alongside Plugin.
type Reader interface {
	// List returns every instance the adapter manages.
	List(ctx context.Context) ([]Instance, error)

	// Backups returns the backups of one instance, newest first.
	Backups(ctx context.Context, id string) ([]Backup, error)
}

// ReadWritePlugin is an engine plugin the agent can both read and change.
type ReadWritePlugin interface {
	Plugin
	Reader
}

// Profile is a named sizing preset.
type Profile struct {
	Name           string
	Members        int
	CPUCores       int
	MemoryGB       int
	StorageGB      int
	HA             bool
	BackupSchedule string
}

// ResourceOverrides carries optional advanced sizing.
type ResourceOverrides struct {
	CPUCores  *int
	MemoryGB  *int
	StorageGB *int
	Namespace *string
}

// CreateInput is the validated input for provisioning a database instance.
type CreateInput struct {
	Name      string
	Engine    string
	Profile   string
	Namespace string
	// Environment is the environment this database runs in.
	Environment string
	Overrides   ResourceOverrides

	// Access is the load balancer the database sits behind. Empty lets the
	// engine choose: private where it offers a load balancer, none otherwise.
	Access Access
	// SourceRanges are the addresses allowed through a public load balancer.
	// Required when Access is public. Open to the internet is 0.0.0.0/0.
	SourceRanges []string
}

// ScaleInput is the validated input for resizing an existing instance. Replicas
// is the member count: raising it from 1 to 3 is what turns a single node
// database into an HA cluster.
type ScaleInput struct {
	CPUCores  *int
	MemoryGB  *int
	StorageGB *int
	Replicas  *int
}

// Instance is a provisioned database returned after create or get.
type Instance struct {
	ID        string
	Name      string
	Engine    string
	Profile   string
	Namespace string
	// Environment is the environment this database runs in.
	Environment string
	Status      Status
	CreatedAt   time.Time
	Spec        InstanceSpec
	Sizing      Sizing

	// LastBackupAt is when the newest succeeded backup finished. Nil means
	// none has, which the UI states as "No backups yet".
	LastBackupAt *time.Time

	// Connection is nil until the provider publishes the connection Secret.
	Connection *Connection

	// Exposure is read back off the spec, with the address filled from the
	// published connection once a load balancer has one.
	Exposure Exposure

	// BackupSchedule is the schedule the instance spec carries, read back off
	// the spec rather than off the profile so the UI states what the cluster
	// will do. Nil means on-demand backups only, and so does a Disabled one.
	BackupSchedule *BackupSchedule
}

// BackupSchedule is a recurring backup an instance spec carries.
type BackupSchedule struct {
	Name string
	// Disabled is a schedule turned off, which runs nothing but keeps its
	// settings. It is the negative so that a heartbeat from an agent older
	// than this field, which only ever reported enabled schedules, still
	// reads as enabled.
	Disabled bool
	// Cron is the schedule in standard cron syntax, in UTC.
	Cron string
	// RetentionCopies is how many copies the schedule keeps. Zero means the
	// provider keeps them until something else removes them.
	RetentionCopies int32
	// RetentionWindow is how long the schedule keeps copies, such as "4w",
	// when it prunes by age instead of by count. It is empty otherwise.
	RetentionWindow string
}

// Runs reports whether the schedule will take backups, which a nil or
// disabled one will not.
func (s *BackupSchedule) Runs() bool {
	return s != nil && !s.Disabled
}

// Sizing is the spec of an instance as numbers, for the UI to render. The spec
// itself carries Kubernetes quantity strings; parsing them is engine-specific,
// so the plugin does it once and the HTTP layer never sees a quantity.
type Sizing struct {
	Members   int
	CPUCores  float64
	MemoryGB  float64
	StorageGB float64
}

// Connection is how an application reaches an instance. URI carries the
// password, so it is never logged.
type Connection struct {
	Host     string
	Port     int
	Username string
	// SecretName is the connection Secret the provider published. It names
	// where the credential lives for anyone the adapter cannot hand one to.
	SecretName string
	// ServiceName is the Service a port-forward targets. Empty when the host
	// does not name one in this namespace.
	ServiceName string
	URI         string
}

// InstanceSpec is the OpenEverest Instance spec an engine plugin builds.
type InstanceSpec struct {
	Components InstanceComponents
}

// InstanceComponents groups the workload pieces inside an Instance spec.
type InstanceComponents struct {
	Engine EngineComponent
}

// EngineComponent describes the database engine workload inside an Instance spec.
type EngineComponent struct {
	Replicas  int32
	Resources EngineResources
}

// EngineResources holds Kubernetes-style resource quantities for an engine.
type EngineResources struct {
	CPU     string
	Memory  string
	Storage string
}

// Status is the normalized lifecycle state the UI renders.
type Status struct {
	Phase   string
	Message string
}

// Normalized instance phases. Every engine maps its own vocabulary onto these
// so the UI has one set of status pills to render. They are developer words:
// a cluster that is going away is "deleting", not "terminating".
const (
	PhasePending      = "pending"
	PhaseProvisioning = "provisioning"
	PhaseInitializing = "initializing"
	PhaseReady        = "ready"
	PhaseUpdating     = "updating"
	PhaseDeleting     = "deleting"
	PhaseFailed       = "failed"
	PhaseRestoring    = "restoring"
	PhaseSuspending   = "suspending"
	PhaseSuspended    = "suspended"
	PhaseResuming     = "resuming"
	PhaseUnknown      = "unknown"
)

// Normalized backup states.
const (
	BackupPending   = "pending"
	BackupRunning   = "running"
	BackupSucceeded = "succeeded"
	BackupFailed    = "failed"
	BackupDeleting  = "deleting"
	BackupUnknown   = "unknown"
)

// Backup is a point-in-time backup of a database instance.
type Backup struct {
	ID        string
	Status    string
	SizeBytes int64
	CreatedAt time.Time
	Scheduled bool

	// Message is what the operator said about the state. A failed backup
	// carries its reason here, which is the only place the reason exists:
	// the request that started the backup returned long before it failed.
	Message string

	// FinishedAt is when the backup stopped running, in either direction.
	// Zero means it is still going.
	FinishedAt time.Time

	// Instance is the database this backup belongs to. A heartbeat lists every
	// database's backups together, and matching them back by name prefix makes
	// two databases share a backup when one name is a prefix of the other.
	Instance string
}

// Normalized restore states. They mirror the backup states because both
// objects report the same lifecycle.
const (
	RestorePending   = "pending"
	RestoreRunning   = "running"
	RestoreSucceeded = "succeeded"
	RestoreFailed    = "failed"
	RestoreUnknown   = "unknown"
)

// Restore is one restore of an instance from a backup.
type Restore struct {
	ID        string
	BackupID  string
	Status    string
	CreatedAt time.Time

	// Message and FinishedAt carry the same facts they do on Backup.
	Message    string
	FinishedAt time.Time
}
