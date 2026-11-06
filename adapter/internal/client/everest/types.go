package everest

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// APIVersion is the Instance API group version OpenEverest v2 serves.
const APIVersion = "core.openeverest.io/v1alpha1"

// BackupAPIVersion is the Backup API group version.
const BackupAPIVersion = "backup.openeverest.io/v1alpha1"

// DefaultCluster is the Kubernetes cluster name the Everest API scopes
// instances under. everestctl and the Everest UI both default to this value.
const DefaultCluster = "main"

// Object kinds.
const (
	KindInstance       = "Instance"
	KindInstancePreset = "InstancePreset"
	KindBackup         = "Backup"
)

// AnnotationInstancePreset is the Instance annotation OpenEverest uses to
// record which InstancePreset a create copied. The Instance is self-contained
// after that; the annotation is tracking, not a live reference.
const AnnotationInstancePreset = "openeverest.io/instance-preset"

// Provider names the adapter writes on Instance.spec.providerRef.
const (
	ProviderPXC        = "percona-xtradb-cluster"
	ProviderPostgreSQL = "provider-percona-postgresql"
)

// BackupClass names the provider charts install as cluster-scoped CRs.
const (
	BackupClassPXC        = "pxc"
	BackupClassPostgreSQL = "pg"
)

// Topology types the PXC and PostgreSQL providers advertise.
const (
	TopologyCluster = "cluster"
)

// Component keys inside Instance.spec.components.
const (
	ComponentEngine     = "engine"
	ComponentProxy      = "proxy"
	ComponentMonitoring = "monitoring"
)

// Component types the providers accept. An empty engine type lets the
// provider fill its default (PXC does that).
const (
	ComponentTypePostgreSQL = "postgresql"
	ComponentTypeHAProxy    = "haproxy"
	ComponentTypePgBouncer  = "pgbouncer"
	ComponentTypePMM        = "pmm"
)

// DeletionPolicyCascade is the value OpenEverest 2.0.0-dev.4 fills into
// spec.deletionPolicy of every InstancePreset and Instance that leaves it empty.
const DeletionPolicyCascade = "Cascade"

// ObjectMeta is the subset of Kubernetes object metadata the adapter reads or
// writes. Everything else on the wire is preserved by Everest, not by us.
type ObjectMeta struct {
	Name              string            `json:"name,omitempty"`
	Namespace         string            `json:"namespace,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	CreationTimestamp *time.Time        `json:"creationTimestamp,omitempty"`
	ResourceVersion   string            `json:"resourceVersion,omitempty"`
	UID               string            `json:"uid,omitempty"`
	Finalizers        []string          `json:"finalizers,omitempty"`
}

// ListMeta is the metadata block on a collection response.
type ListMeta struct {
	ResourceVersion string `json:"resourceVersion,omitempty"`
}

// ObjectRef is a name-only reference used throughout the v2 APIs.
type ObjectRef struct {
	Name string `json:"name"`
}

// Instance is one provisioned database (`core.openeverest.io/v1alpha1`).
type Instance struct {
	APIVersion string         `json:"apiVersion,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Metadata   ObjectMeta     `json:"metadata,omitempty"`
	Spec       InstanceSpec   `json:"spec"`
	Status     InstanceStatus `json:"status,omitzero"`
}

// InstanceList is the response of the instances collection.
type InstanceList struct {
	APIVersion string     `json:"apiVersion,omitempty"`
	Kind       string     `json:"kind,omitempty"`
	Metadata   ListMeta   `json:"metadata,omitempty"`
	Items      []Instance `json:"items"`
}

// InstancePreset is a cluster-scoped template that inlines InstanceSpec
// (`core.openeverest.io/v1alpha1`). Helm installs the PDS ones; create
// resolves one and copies its spec onto a new Instance.
type InstancePreset struct {
	APIVersion string       `json:"apiVersion,omitempty"`
	Kind       string       `json:"kind,omitempty"`
	Metadata   ObjectMeta   `json:"metadata,omitempty"`
	Spec       InstanceSpec `json:"spec"`
}

// EngineComponent returns the engine component, or an empty spec.
func (p *InstancePreset) EngineComponent() ComponentSpec {
	if p == nil {
		return ComponentSpec{}
	}

	in := Instance{Spec: p.Spec}

	return in.EngineComponent()
}

// ProxyComponent returns the proxy component, or an empty spec.
func (p *InstancePreset) ProxyComponent() ComponentSpec {
	if p == nil {
		return ComponentSpec{}
	}

	in := Instance{Spec: p.Spec}

	return in.ProxyComponent()
}

// InstanceSpec is the desired state of an Instance. ProviderRef is required.
//
// The CRD carries more than the adapter writes (`parameters` among them), and
// a scale PUTs the whole object back, so members with no field here are kept
// in Unknown and written out again. See unknown.go.
//
// DataSource is what makes an instance come up holding data: the operator
// provisions it from the named backup instead of empty. It is set once, at
// create time, and an instance already running keeps whatever it was given.
type InstanceSpec struct {
	ProviderRef    ObjectRef                  `json:"providerRef"`
	Version        string                     `json:"version,omitempty"`
	Topology       *TopologySpec              `json:"topology,omitempty"`
	DataSource     *DataSource                `json:"dataSource,omitempty"`
	Components     map[string]ComponentSpec   `json:"components,omitempty"`
	Backup         *InstanceBackupSpec        `json:"backup,omitempty"`
	DeletionPolicy string                     `json:"deletionPolicy,omitempty"`
	Unknown        map[string]json.RawMessage `json:"-"`
}

// TopologySpec selects a provider topology.
type TopologySpec struct {
	Type string `json:"type,omitempty"`
}

// ComponentSpec is one piece of an Instance (engine, proxy, and so on).
type ComponentSpec struct {
	Name       string                     `json:"name,omitempty"`
	Type       string                     `json:"type,omitempty"`
	Version    string                     `json:"version,omitempty"`
	Image      string                     `json:"image,omitempty"`
	Storage    *Storage                   `json:"storage,omitempty"`
	Resources  *ResourceRequirements      `json:"resources,omitempty"`
	Replicas   *int32                     `json:"replicas,omitempty"`
	Parameters json.RawMessage            `json:"parameters,omitempty"`
	Service    *ServiceSpec               `json:"service,omitempty"`
	Unknown    map[string]json.RawMessage `json:"-"`
}

// ServiceTypeLoadBalancer is the service type that asks the cloud for a load
// balancer in front of a component. Unset means the provider default,
// ClusterIP, which only the cluster can reach.
const ServiceTypeLoadBalancer = "LoadBalancer"

// ServiceSpec is how one component is exposed. The providers read it off the
// proxy component when it has one and off the engine otherwise, and write it
// onto the operator's own expose settings.
type ServiceSpec struct {
	ServiceType         string                     `json:"serviceType,omitempty"`
	Annotations         map[string]string          `json:"annotations,omitempty"`
	LoadBalancerService *LoadBalancerService       `json:"loadBalancerService,omitempty"`
	Unknown             map[string]json.RawMessage `json:"-"`
}

// LoadBalancerService carries the settings that only mean something for a
// LoadBalancer service. No source ranges means any address may connect.
type LoadBalancerService struct {
	SourceRanges []string                   `json:"sourceRanges,omitempty"`
	Unknown      map[string]json.RawMessage `json:"-"`
}

// ReplicaCount returns the replica count, or zero when the field is unset.
func (c ComponentSpec) ReplicaCount() int32 {
	if c.Replicas == nil {
		return 0
	}

	return *c.Replicas
}

// CPU returns the CPU limit in force, then the request if no limit is set.
func (c ComponentSpec) CPU() string {
	if c.Resources == nil {
		return ""
	}
	if c.Resources.Limits != nil && c.Resources.Limits.CPU != "" {
		return c.Resources.Limits.CPU
	}
	if c.Resources.Requests != nil {
		return c.Resources.Requests.CPU
	}

	return ""
}

// Memory returns the memory limit in force, then the request if no limit is set.
func (c ComponentSpec) Memory() string {
	if c.Resources == nil {
		return ""
	}
	if c.Resources.Limits != nil && c.Resources.Limits.Memory != "" {
		return c.Resources.Limits.Memory
	}
	if c.Resources.Requests != nil {
		return c.Resources.Requests.Memory
	}

	return ""
}

// StorageSize returns the volume size, or empty when the component is stateless.
func (c ComponentSpec) StorageSize() string {
	if c.Storage == nil {
		return ""
	}

	return c.Storage.Size
}

// EngineComponent returns the engine component, or an empty spec.
func (in *Instance) EngineComponent() ComponentSpec {
	if in == nil || in.Spec.Components == nil {
		return ComponentSpec{}
	}

	return in.Spec.Components[ComponentEngine]
}

// ProxyComponent returns the proxy component, or an empty spec.
func (in *Instance) ProxyComponent() ComponentSpec {
	if in == nil || in.Spec.Components == nil {
		return ComponentSpec{}
	}

	return in.Spec.Components[ComponentProxy]
}

// SetEngineComponent replaces the engine component.
func (in *Instance) SetEngineComponent(c ComponentSpec) {
	if in.Spec.Components == nil {
		in.Spec.Components = map[string]ComponentSpec{}
	}
	in.Spec.Components[ComponentEngine] = c
}

// SetProxyComponent replaces the proxy component.
func (in *Instance) SetProxyComponent(c ComponentSpec) {
	if in.Spec.Components == nil {
		in.Spec.Components = map[string]ComponentSpec{}
	}
	in.Spec.Components[ComponentProxy] = c
}

// Storage is a persistent volume request. Size is a Kubernetes quantity.
type Storage struct {
	Size         string  `json:"size,omitempty"`
	StorageClass *string `json:"storageClass,omitempty"`
}

// ResourceRequirements is the Kubernetes-shaped compute request the Instance
// CRD carries. Limits are what the adapter writes; requests are left to the
// provider unless an operator set them with kubectl.
type ResourceRequirements struct {
	Limits   *ResourceList `json:"limits,omitempty"`
	Requests *ResourceList `json:"requests,omitempty"`
}

// ResourceList is a CPU and memory pair.
type ResourceList struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// InstanceBackupSpec toggles backups on an Instance and names the storages
// the engine may write to. A schedule lives under the storage it writes to.
type InstanceBackupSpec struct {
	Enabled  bool                    `json:"enabled"`
	ClassRef ObjectRef               `json:"classRef"`
	Storages []InstanceBackupStorage `json:"storages,omitempty"`
}

// InstanceBackupStorage registers one BackupStorage on the Instance.
type InstanceBackupStorage struct {
	StorageRef ObjectRef                `json:"storageRef"`
	Schedules  []InstanceBackupSchedule `json:"schedules,omitempty"`
}

// InstanceBackupSchedule is one recurring backup task.
type InstanceBackupSchedule struct {
	Name      string             `json:"name"`
	Enabled   bool               `json:"enabled"`
	Cron      string             `json:"cron"`
	Retention *ScheduleRetention `json:"retention,omitempty"`
}

// Retention types a schedule accepts. A schedule with no retention keeps
// every backup.
const (
	RetentionTypeCount = "count"
	RetentionTypeTime  = "time"
)

// ScheduleRetention says how long a schedule's backups are kept: the newest
// Count of them, or those inside the Duration window.
type ScheduleRetention struct {
	Type     string `json:"type,omitempty"`
	Count    int32  `json:"count,omitempty"`
	Duration string `json:"duration,omitempty"`
}

// CountRetention keeps the newest n backups.
func CountRetention(n int32) *ScheduleRetention {
	return &ScheduleRetention{Type: RetentionTypeCount, Count: n}
}

// Copies is how many backups the schedule keeps. Zero means no fixed number:
// either nothing is pruned or a time window decides.
func (s InstanceBackupSchedule) Copies() int32 {
	if s.Retention == nil || (s.Retention.Type != "" && s.Retention.Type != RetentionTypeCount) {
		return 0
	}

	return s.Retention.Count
}

// InstanceStatus is the observed state. Connection details live on a Secret
// the provider writes; the API also exposes them on the connection route.
type InstanceStatus struct {
	Phase               string            `json:"phase,omitempty"`
	Version             string            `json:"version,omitempty"`
	Message             string            `json:"message,omitempty"`
	ConnectionSecretRef *ObjectRef        `json:"connectionSecretRef,omitempty"`
	Components          []ComponentStatus `json:"components,omitempty"`
	Conditions          []Condition       `json:"conditions,omitempty"`
}

// ConditionPodsScheduled is the condition the provider sets while it waits for
// a node. The PostgreSQL provider has no Failed phase, so a database that
// cannot be placed stays Initializing and this says why.
const ConditionPodsScheduled = "PodsScheduled"

// Condition is one entry of status.conditions.
type Condition struct {
	Type               string     `json:"type"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason,omitempty"`
	Message            string     `json:"message,omitempty"`
	LastTransitionTime *time.Time `json:"lastTransitionTime,omitempty"`
}

// SchedulingBlocker returns the PodsScheduled condition when it has been False
// for at least heldFor. A shorter False is a volume binding or a node scaling
// up, which a healthy start goes through. The bool is false when the condition
// is True, absent or carries no transition time.
func (s InstanceStatus) SchedulingBlocker(heldFor time.Duration, now time.Time) (Condition, bool) {
	for _, c := range s.Conditions {
		if c.Type != ConditionPodsScheduled || c.Status != "False" || c.LastTransitionTime == nil {
			continue
		}

		if now.Sub(*c.LastTransitionTime) >= heldFor {
			return c, true
		}
	}

	return Condition{}, false
}

// ComponentStatus counts the pods of one component. Name is a key of
// spec.components.
type ComponentStatus struct {
	Name          string `json:"name,omitempty"`
	Selector      string `json:"selector,omitempty"`
	Replicas      *int32 `json:"replicas,omitempty"`
	ReadyReplicas *int32 `json:"readyReplicas,omitempty"`
}

// ReadyCounts returns the ready and total pods of the engine component.
func (s InstanceStatus) ReadyCounts() (ready, total int32) {
	for _, c := range s.Components {
		if c.Name != ComponentEngine {
			continue
		}
		if c.Replicas != nil {
			total = *c.Replicas
		}
		if c.ReadyReplicas != nil {
			ready = *c.ReadyReplicas
		}
	}

	return ready, total
}

// ConnectionSecretName is the Secret the provider published, if any.
func (s InstanceStatus) ConnectionSecretName() string {
	if s.ConnectionSecretRef == nil {
		return ""
	}

	return s.ConnectionSecretRef.Name
}

// Instance phases the operator reports in `status.phase`. They are PascalCase
// on the wire.
const (
	PhasePending      = "Pending"
	PhaseProvisioning = "Provisioning"
	PhaseInitializing = "Initializing"
	PhaseReady        = "Ready"
	PhaseUpdating     = "Updating"
	PhaseTerminating  = "Terminating"
	PhaseFailed       = "Failed"
	PhaseRestoring    = "Restoring"
	PhaseSuspending   = "Suspending"
	PhaseSuspended    = "Suspended"
	PhaseResuming     = "Resuming"
)

// Connection is what `/instances/{name}/connection` returns. It carries the
// password and must never be logged.
type Connection struct {
	Type     string `json:"type,omitempty"`
	Provider string `json:"provider,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     Port   `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	URI      string `json:"uri,omitempty"`
}

// Port is a connection port. The provider Secret uses a string; the HTTP
// connection route may send a number. Both decode here.
type Port int

// UnmarshalJSON accepts a JSON number or a numeric string.
func (p *Port) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) == 0 {
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return err
		}
		*p = Port(n)

		return nil
	}

	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*p = Port(n)

	return nil
}

// Backup is one point-in-time backup (`backup.openeverest.io/v1alpha1`).
type Backup struct {
	APIVersion string       `json:"apiVersion,omitempty"`
	Kind       string       `json:"kind,omitempty"`
	Metadata   ObjectMeta   `json:"metadata,omitempty"`
	Spec       BackupSpec   `json:"spec"`
	Status     BackupStatus `json:"status,omitzero"`
}

// BackupList is the response of a backups collection.
type BackupList struct {
	APIVersion string   `json:"apiVersion,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Metadata   ListMeta `json:"metadata,omitempty"`
	Items      []Backup `json:"items"`
}

// Backup origin types. Instance is a backup this control plane requested.
// External is data already sitting in a BackupStorage.
const (
	BackupOriginTypeInstance = "Instance"
	BackupOriginTypeExternal = "External"
)

// BackupSpec names where the data came from, the BackupClass, and the storage.
type BackupSpec struct {
	Origin       BackupOrigin `json:"origin"`
	ClassRef     ObjectRef    `json:"classRef"`
	StorageRef   ObjectRef    `json:"storageRef"`
	ScheduleName string       `json:"scheduleName,omitempty"`
}

// InstanceName is the Instance that produced this backup, or empty when the
// origin is external data already in storage.
func (s BackupSpec) InstanceName() string {
	if s.Origin.InstanceRef == nil {
		return ""
	}

	return s.Origin.InstanceRef.Name
}

// BackupOrigin identifies a live Instance or data already in a BackupStorage.
type BackupOrigin struct {
	Type        string                `json:"type"`
	InstanceRef *ObjectRef            `json:"instanceRef,omitempty"`
	External    *BackupOriginExternal `json:"external,omitempty"`
}

// BackupOriginExternal is the path and times of data already in storage.
type BackupOriginExternal struct {
	Path        string    `json:"path"`
	StartedAt   time.Time `json:"startedAt"`
	CompletedAt time.Time `json:"completedAt"`
}

// InstanceBackupOrigin marks a backup produced by the named Instance.
func InstanceBackupOrigin(name string) BackupOrigin {
	return BackupOrigin{
		Type:        BackupOriginTypeInstance,
		InstanceRef: &ObjectRef{Name: name},
	}
}

// BackupStatus is the observed state of a backup. Size is the engine-reported
// byte count as a string.
type BackupStatus struct {
	State       string     `json:"state,omitempty"`
	Size        *string    `json:"size,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Message     string     `json:"message,omitempty"`
}

// Backup states the operator reports.
const (
	BackupStatePending   = "Pending"
	BackupStateRunning   = "Running"
	BackupStateSucceeded = "Succeeded"
	BackupStateFailed    = "Failed"
	BackupStateError     = "Error"
	BackupStateDeleting  = "Deleting"
)

// DataSource identifies what a restore reads, and what an instance is
// provisioned from.
//
// It keeps the members the adapter does not model for the same reason the
// specs around it do: a scale reads an instance, edits it, and PUTs the whole
// object back, so a point in time or any other source detail with no field
// here would come back absent and be read as a delete.
type DataSource struct {
	Type    string                     `json:"type"`
	Backup  *DataSourceBackup          `json:"backup,omitempty"`
	Unknown map[string]json.RawMessage `json:"-"`
}

// DataSourceTypeBackup restores from an existing Backup CR.
const DataSourceTypeBackup = "Backup"

// DataSourceBackup names the Backup CR to restore from.
type DataSourceBackup struct {
	BackupRef ObjectRef `json:"backupRef"`
}

// Ptr returns a pointer to v. Used when the CRD stores optional scalars.
func Ptr[T any](v T) *T {
	return &v
}
