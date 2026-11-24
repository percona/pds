// Package oe is the OpenEverest Instance engine plugin. One Flavor selects
// MySQL, PostgreSQL or MongoDB; both share profiles, bounds, and the Instance API.
package oe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
)

// DefaultNamespace holds instances when no namespace is configured or
// requested. The adapter runs in the release namespace, which is also the
// Everest managed namespace.
const DefaultNamespace = "pds"

// Labels the hub writes on every instance it creates. The plugin reads them.
// The profile label is how a profile survives a round trip through Everest,
// which stores a spec and knows nothing about our sizing presets.
const (
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelProfile   = "pds.percona.com/profile"
	ManagedBy      = "pds"
)

// What a release before the product was renamed to PDS wrote on the same
// instances. Those objects outlive the upgrade, so reads accept these and
// writes never produce them. Once no cluster carries them any more, both
// constants and the reads that mention them can go.
const (
	LegacyLabelProfile = "services.percona.com/profile" // allow-legacy-name
	LegacyManagedBy    = "percona-services"             // allow-legacy-name
)

// labelValue returns the first label that is set, so an object labeled before
// the rename answers the same question as one labeled after it.
func labelValue(labels map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := labels[key]; value != "" {
			return value
		}
	}

	return ""
}

// ProfileCustom is reported for a PDS-managed instance with no profile
// label. Instances created outside the adapter are omitted, not listed as
// custom.
const ProfileCustom = "custom"

// Backup defaults.
const (
	DefaultBackupStorage = ""
	DailyBackupCron      = "0 2 * * *"
	DailyBackupRetention = 7
)

var (
	ErrUnknownProfile      = engine.ErrUnknownProfile
	ErrInvalidName         = engine.ErrInvalidName
	ErrNamespaceNotManaged = engine.ErrNamespaceNotManaged
	ErrInvalidID           = engine.ErrInvalidID
	ErrOutOfBounds         = engine.ErrOutOfBounds
	ErrEmptyScale          = engine.ErrEmptyScale
	ErrStorageShrink       = engine.ErrStorageShrink
	ErrBusy                = engine.ErrBusy
	ErrInstanceExists      = engine.ErrExists
	ErrInstanceNotFound    = engine.ErrNotFound
	ErrUnparsableQuantity  = engine.ErrUnparsableQuantity
	ErrBackupsUnavailable  = engine.ErrBackupsUnavailable
)

var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

const maxLabelLength = 63

// Flavor is one engine the plugin can serve.
type Flavor struct {
	ID          string
	Provider    string
	Topology    string
	EngineType  string
	ProxyType   string
	BackupClass string
	DefaultPort int
	URIScheme   string
	// ReplicaSet is the name of the replica set the connection string names.
	// It is empty for an engine whose clients reach one address.
	ReplicaSet string
	// DropsIdleBackupStorage is whether the provider removes a backup storage
	// with no schedule entry, turning backups off when none is left. A storage
	// whose only schedule is turned off is kept and runs nothing, which is how
	// a database without a schedule keeps a storage its manifest can list.
	DropsIdleBackupStorage bool
	// EngineOnly is a provider whose Instance carries the engine component
	// and its backup section alone: no proxy and no monitoring component. The
	// hub writes nothing for a part of the Instance until PDS serves it.
	EngineOnly bool
	// ProvisioningIsUpdating is a provider that reports Provisioning for every
	// change it makes, not only for the first start. A database it has
	// reported Ready is then Updating while it reports Provisioning.
	ProvisioningIsUpdating bool
	// FinishOnly is a provider whose Backup reports only when it finished.
	// Its creation time can precede the real start by minutes, so the start
	// is never shown and no duration is worked out.
	FinishOnly bool
}

// MySQL is the Percona XtraDB Cluster flavor.
var MySQL = Flavor{
	ID:          "mysql",
	Provider:    everest.ProviderPXC,
	Topology:    everest.TopologyCluster,
	ProxyType:   everest.ComponentTypeHAProxy,
	BackupClass: everest.BackupClassPXC,
	DefaultPort: 3306,
	URIScheme:   "mysql",
}

// PostgreSQL is the Percona PostgreSQL flavor.
var PostgreSQL = Flavor{
	ID:          "postgresql",
	Provider:    everest.ProviderPostgreSQL,
	Topology:    everest.TopologyCluster,
	EngineType:  everest.ComponentTypePostgreSQL,
	ProxyType:   everest.ComponentTypePgBouncer,
	BackupClass: everest.BackupClassPostgreSQL,
	DefaultPort: 5432,
	URIScheme:   "postgres",

	DropsIdleBackupStorage: true,
}

// MongoDB is the Percona Server for MongoDB flavor. A database is one replica
// set and clients reach its members directly, so there is no proxy.
var MongoDB = Flavor{
	ID:          "mongodb",
	Provider:    everest.ProviderMongoDB,
	Topology:    everest.TopologyReplicaSet,
	BackupClass: everest.BackupClassMongoDB,
	DefaultPort: 27017,
	URIScheme:   "mongodb",
	ReplicaSet:  "rs0",

	EngineOnly:             true,
	ProvisioningIsUpdating: true,
	FinishOnly:             true,
}

// EverestClient is the slice of the Everest API this plugin depends on.
type EverestClient interface {
	ListInstances(ctx context.Context, namespace string) ([]everest.Instance, error)
	GetInstance(ctx context.Context, namespace, name string) (*everest.Instance, error)
	UpdateInstance(ctx context.Context, namespace, name string, in *everest.Instance) (*everest.Instance, error)
	DeleteInstance(ctx context.Context, namespace, name string) error
	GetInstanceConnection(ctx context.Context, namespace, name string) (*everest.Connection, error)
	ListInstanceBackups(ctx context.Context, namespace, name string) ([]everest.Backup, error)
	CreateBackup(ctx context.Context, namespace string, in *everest.Backup) (*everest.Backup, error)
}

// Plugin is one engine implementation of engine.ReadWritePlugin.
type Plugin struct {
	everest EverestClient
	flavor  Flavor
	ready   readyMemory

	namespace     string
	backupStorage string
	deleteBackup  BackupDeleter
}

// Option configures a Plugin.
type Option func(*Plugin)

// WithNamespace sets the Everest managed namespace new instances land in.
func WithNamespace(namespace string) Option {
	return func(p *Plugin) {
		if v := strings.TrimSpace(namespace); v != "" {
			p.namespace = v
		}
	}
}

// WithBackupStorage sets the BackupStorage backups are written to.
func WithBackupStorage(name string) Option {
	return func(p *Plugin) {
		p.backupStorage = strings.TrimSpace(name)
	}
}

// New returns a plugin that talks to the given Everest client for one flavor.
func New(client EverestClient, flavor Flavor, opts ...Option) *Plugin {
	p := &Plugin{
		everest:       client,
		flavor:        flavor,
		namespace:     DefaultNamespace,
		backupStorage: DefaultBackupStorage,
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Engine returns the engine identifier.
func (p *Plugin) Engine() string { return p.flavor.ID }

// attachConnection fills connection details once the operator has published
// them. A missing Secret is the window between Ready and the user Secret,
// not an error: the UI keeps polling until the URI appears.
func (p *Plugin) attachConnection(ctx context.Context, out *engine.Instance, found *everest.Instance) error {
	if found.Status.Phase != everest.PhaseReady && found.Status.ConnectionSecretName() == "" {
		return nil
	}

	conn, err := p.everest.GetInstanceConnection(ctx, found.Metadata.Namespace, found.Metadata.Name)
	switch {
	case err == nil:
		out.Connection = p.toConnection(conn, found.Metadata.Namespace, found.Status.ConnectionSecretName())
		if out.Exposure.External && out.Connection != nil && !inClusterHost(out.Connection.Host, found.Metadata.Namespace) {
			out.Exposure.Address = out.Connection.Host
		}

		return nil
	case errors.Is(err, everest.ErrNotFound):
		return nil
	default:
		return fmt.Errorf("get connection for %q: %w", out.ID, err)
	}
}

// attachLastBackup fills when the newest succeeded backup finished. A missing
// backups collection is the same as none yet, not an error: the UI states
// "No backups yet" until one succeeds.
func (p *Plugin) attachLastBackup(ctx context.Context, out *engine.Instance) error {
	backups, err := p.everest.ListInstanceBackups(ctx, out.Namespace, out.Name)
	switch {
	case err == nil:
		out.LastBackupAt = newestSucceededAt(backups)

		return nil
	case errors.Is(err, everest.ErrNotFound):
		return nil
	default:
		return fmt.Errorf("list backups of %q: %w", out.ID, err)
	}
}

// List returns every instance this flavor owns in the configured namespace.
func (p *Plugin) List(ctx context.Context) ([]engine.Instance, error) {
	found, err := p.everest.ListInstances(ctx, p.namespace)
	if err != nil {
		return nil, fmt.Errorf("list instances in %q: %w", p.namespace, err)
	}

	out := make([]engine.Instance, 0, len(found))
	listed := map[string]bool{}
	for _, inst := range found {
		if !p.owns(&inst) {
			continue
		}
		item := p.toInstance(&inst, p.namespace)
		listed[item.ID] = true
		if err := p.attachConnection(ctx, &item, &inst); err != nil {
			return nil, err
		}
		if err := p.attachLastBackup(ctx, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}

	if p.flavor.ProvisioningIsUpdating {
		p.ready.keepOnly(listed)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out, nil
}

// Scale applies the requested resource and member changes.
func (p *Plugin) Scale(ctx context.Context, id string, in engine.ScaleInput) error {
	namespace, name, err := splitID(id)
	if err != nil {
		return err
	}

	current, err := p.getInstance(ctx, namespace, name)
	if err != nil {
		return err
	}

	eng := current.EngineComponent()
	if eng.Resources == nil {
		eng.Resources = &everest.ResourceRequirements{}
	}
	if eng.Resources.Limits == nil {
		eng.Resources.Limits = &everest.ResourceList{CPU: eng.CPU(), Memory: eng.Memory()}
	}

	storage, storageErr := scaleStorageState(eng.Storage)
	if storageErr != nil && in.StorageGB != nil {
		return storageErr
	}
	if err := CheckScale(in, storage, false); err != nil {
		return err
	}

	if in.CPUCores != nil {
		eng.Resources.Limits.CPU = formatCores(*in.CPUCores)
	}
	if in.MemoryGB != nil {
		eng.Resources.Limits.Memory = formatGi(*in.MemoryGB)
	}
	if in.StorageGB != nil {
		if err := p.applyStorage(eng.Storage, *in.StorageGB); err != nil {
			return err
		}
	}
	if in.Replicas != nil {
		eng.Replicas = everest.Ptr(int32(*in.Replicas))
		proxy := current.ProxyComponent()
		if !p.flavor.EngineOnly && (proxy.Type != "" || proxy.Replicas != nil) {
			proxy.Replicas = everest.Ptr(proxyReplicasFor(int32(*in.Replicas)))
			current.SetProxyComponent(proxy)
		}
	}

	current.SetEngineComponent(eng)

	if err := p.checkNoBackupRunning(ctx, namespace, name); err != nil {
		return err
	}

	if _, err := p.everest.UpdateInstance(ctx, namespace, name, current); err != nil {
		switch {
		case errors.Is(err, everest.ErrNotFound):
			return fmt.Errorf("%w: %q: %w", ErrInstanceNotFound, id, err)
		case errors.Is(err, everest.ErrConflict):
			return fmt.Errorf("%w: %q: %w", &engine.BusyError{}, id, err)
		default:
			return fmt.Errorf("scale instance %q: %w", id, err)
		}
	}

	return nil
}

func (p *Plugin) checkNoBackupRunning(ctx context.Context, namespace, name string) error {
	backups, err := p.everest.ListInstanceBackups(ctx, namespace, name)
	if err != nil {
		if errors.Is(err, everest.ErrNotFound) {
			return fmt.Errorf("%w: %q: %w", ErrInstanceNotFound, instanceID(namespace, name), err)
		}

		return fmt.Errorf("list backups of %q: %w", instanceID(namespace, name), err)
	}

	for _, backup := range backups {
		switch toBackupState(backup.Status.State) {
		case engine.BackupPending, engine.BackupRunning:
			return fmt.Errorf("%w: %q is backing up in %q",
				&engine.BusyError{Operation: "backup"}, backup.Metadata.Name, namespace)
		}
	}

	return nil
}

func scaleStorageState(storage *everest.Storage) (StorageState, error) {
	if storage == nil {
		return StorageState{}, nil
	}
	gb, err := parseSizeGB(storage.Size)
	if err != nil {
		return StorageState{}, fmt.Errorf("read current storage %q: %w", storage.Size, err)
	}

	return StorageState{Present: true, GB: gb}, nil
}

func (p *Plugin) applyStorage(storage *everest.Storage, wantGB int) error {
	if storage == nil {
		return fmt.Errorf("%w: engine has no storage", ErrUnparsableQuantity)
	}
	if err := checkBounds(FieldStorage, wantGB, MinStorageGB, MaxStorageGB, UnitGB); err != nil {
		return err
	}

	currentGB, err := parseSizeGB(storage.Size)
	if err != nil {
		return fmt.Errorf("read current storage %q: %w", storage.Size, err)
	}

	if float64(wantGB) < currentGB {
		return fmt.Errorf("%w: %d GB is smaller than the current %g GB",
			ErrStorageShrink, wantGB, currentGB)
	}

	storage.Size = formatGi(wantGB)

	return nil
}

// Backup takes an on-demand backup. An empty name lets the plugin mint one.
func (p *Plugin) Backup(ctx context.Context, id string) (*engine.Backup, error) {
	return p.BackupNamed(ctx, id, "")
}

// BackupNamed creates a Backup CR with the given name, or a minted name when
// backupID is empty. A caller-chosen name that already exists is success: the
// hub retries unacknowledged backup work with the same id, and treating the
// Everest conflict as failure would make the agent report a completed backup
// as failed.
func (p *Plugin) BackupNamed(ctx context.Context, id, backupID string) (*engine.Backup, error) {
	namespace, name, err := splitID(id)
	if err != nil {
		return nil, err
	}

	if p.backupStorage == "" {
		return nil, fmt.Errorf("%w: this adapter has no backup storage", ErrBackupsUnavailable)
	}

	if _, err := p.getInstance(ctx, namespace, name); err != nil {
		return nil, err
	}

	crName := strings.TrimSpace(backupID)
	if crName == "" {
		crName = backupName(name)
	} else if err := validateLabel(crName, ErrInvalidName); err != nil {
		return nil, err
	}

	created, err := p.everest.CreateBackup(ctx, namespace, &everest.Backup{
		Metadata: everest.ObjectMeta{Name: crName, Namespace: namespace},
		Spec: everest.BackupSpec{
			Origin:     everest.InstanceBackupOrigin(name),
			ClassRef:   everest.ObjectRef{Name: p.flavor.BackupClass},
			StorageRef: everest.ObjectRef{Name: p.backupStorage},
		},
	})
	if err != nil {
		if errors.Is(err, everest.ErrConflict) && strings.TrimSpace(backupID) != "" {
			existing, findErr := p.backupByName(ctx, namespace, name, crName)
			if findErr != nil {
				return nil, findErr
			}
			if existing != nil {
				out := p.toBackup(*existing)

				return &out, nil
			}
		}
		if errors.Is(err, everest.ErrConflict) {
			return nil, fmt.Errorf("%w: a backup of %q is already starting: %w", ErrInstanceExists, name, err)
		}

		return nil, fmt.Errorf("back up instance %q: %w", id, err)
	}

	out := p.toBackup(*created)

	return &out, nil
}

func (p *Plugin) backupByName(ctx context.Context, namespace, instance, crName string) (*everest.Backup, error) {
	backups, err := p.everest.ListInstanceBackups(ctx, namespace, instance)
	if err != nil {
		if errors.Is(err, everest.ErrNotFound) {
			return nil, fmt.Errorf("%w: %q: %w", ErrInstanceNotFound, instanceID(namespace, instance), err)
		}

		return nil, fmt.Errorf("list backups of %q: %w", instanceID(namespace, instance), err)
	}
	for i := range backups {
		if backups[i].Metadata.Name == crName {
			return &backups[i], nil
		}
	}

	return nil, nil
}

// Backups returns the backups of one instance, newest first.
func (p *Plugin) Backups(ctx context.Context, id string) ([]engine.Backup, error) {
	namespace, name, err := splitID(id)
	if err != nil {
		return nil, err
	}

	if _, err := p.getInstance(ctx, namespace, name); err != nil {
		return nil, err
	}

	found, err := p.everest.ListInstanceBackups(ctx, namespace, name)
	if err != nil {
		if errors.Is(err, everest.ErrNotFound) {
			return nil, fmt.Errorf("%w: %q: %w", ErrInstanceNotFound, id, err)
		}

		return nil, fmt.Errorf("list backups of %q: %w", id, err)
	}

	out := make([]engine.Backup, 0, len(found))
	for _, backup := range found {
		out = append(out, p.toBackup(backup))
	}

	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })

	return out, nil
}

// NamespaceBackups returns every backup of the instances this plugin owns in a
// namespace, newest first. It lists each instance because the namespace backup
// collection is create-only on some OpenEverest versions and answers 405 to a
// GET. An instance deleted between the two calls has no backups to report, so
// it is skipped rather than failing the whole list.
func (p *Plugin) NamespaceBackups(ctx context.Context, namespace string) ([]engine.Backup, error) {
	namespace = firstNonEmpty(strings.TrimSpace(namespace), p.namespace, DefaultNamespace)
	instances, err := p.everest.ListInstances(ctx, namespace)
	if err != nil {
		if errors.Is(err, everest.ErrNotFound) {
			return nil, nil
		}

		return nil, fmt.Errorf("list instances in %q: %w", namespace, err)
	}

	out := []engine.Backup{}
	for i := range instances {
		if !p.owns(&instances[i]) {
			continue
		}
		name := instances[i].Metadata.Name
		found, err := p.everest.ListInstanceBackups(ctx, namespace, name)
		if errors.Is(err, everest.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list backups of %q: %w", instanceID(namespace, name), err)
		}
		for _, backup := range found {
			out = append(out, p.toBackup(backup))
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })

	return out, nil
}

// Delete removes an instance.
func (p *Plugin) Delete(ctx context.Context, id string) error {
	namespace, name, err := splitID(id)
	if err != nil {
		return err
	}

	if _, err := p.getInstance(ctx, namespace, name); err != nil {
		return err
	}

	if err := p.everest.DeleteInstance(ctx, namespace, name); err != nil {
		if errors.Is(err, everest.ErrNotFound) {
			return fmt.Errorf("%w: %q: %w", ErrInstanceNotFound, id, err)
		}

		return fmt.Errorf("delete instance %q: %w", id, err)
	}

	return nil
}

func (p *Plugin) getInstance(ctx context.Context, namespace, name string) (*everest.Instance, error) {
	id := instanceID(namespace, name)
	if namespace != p.namespace {
		return nil, fmt.Errorf("%w: %q", ErrInstanceNotFound, id)
	}

	found, err := p.everest.GetInstance(ctx, namespace, name)
	if err != nil {
		if errors.Is(err, everest.ErrNotFound) {
			return nil, fmt.Errorf("%w: %q: %w", ErrInstanceNotFound, id, err)
		}

		return nil, fmt.Errorf("get instance %q: %w", id, err)
	}

	if !p.owns(found) {
		return nil, fmt.Errorf("%w: %q", ErrInstanceNotFound, id)
	}

	return found, nil
}

// owns is whether this plugin may expose or mutate the instance: it lives in
// the configured namespace, carries the Percona Database Service managed-by label, and
// matches this flavor's provider.
func (p *Plugin) owns(in *everest.Instance) bool {
	if in == nil {
		return false
	}
	if ns := strings.TrimSpace(in.Metadata.Namespace); ns != "" && ns != p.namespace {
		return false
	}
	switch in.Metadata.Labels[LabelManagedBy] {
	case ManagedBy, LegacyManagedBy:
	default:
		return false
	}

	return in.Spec.ProviderRef.Name == p.flavor.Provider
}

func validateLabel(value string, sentinel error) error {
	switch {
	case value == "":
		return fmt.Errorf("%w: must not be blank", sentinel)
	case len(value) > maxLabelLength:
		return fmt.Errorf("%w: %q is longer than %d characters", sentinel, value, maxLabelLength)
	case !dns1123Label.MatchString(value):
		return fmt.Errorf("%w: %q must be lowercase letters, numbers, and dashes", sentinel, value)
	}

	return nil
}

func backupName(instance string) string {
	return derivedName(instance, time.Now().UTC())
}

func derivedName(instance string, now time.Time) string {
	suffix := now.Format("20060102150405") + "-" + randomSuffix()

	room := maxLabelLength - len(suffix) - 1
	if room < 1 {
		return suffix
	}
	if len(instance) > room {
		instance = strings.TrimRight(instance[:room], "-")
	}

	return instance + "-" + suffix
}

func randomSuffix() string {
	var buf [2]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%04x", time.Now().UnixNano()&0xffff)
	}

	return hex.EncodeToString(buf[:])
}

var _ engine.ReadWritePlugin = (*Plugin)(nil)
