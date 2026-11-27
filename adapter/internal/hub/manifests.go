package hub

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/oe"
)

// defaultEngine is what a create that names no engine gets, and what a
// manifest written before the engine label reads as.
const defaultEngine = "mysql"

// flavorFor resolves the engine a request names. An empty one is MySQL, and
// anything the catalog does not know is refused rather than quietly produced
// as MySQL.
func flavorFor(requested string) (oe.Flavor, error) {
	id := strings.ToLower(strings.TrimSpace(requested))
	if id == "" {
		id = defaultEngine
	}
	flavor, ok := oe.LookupFlavor(id)
	if !ok {
		return oe.Flavor{}, fmt.Errorf("%w: %q", ErrEngineNotSupported, requested)
	}

	return flavor, nil
}

// profile is a catalog row with its overrides applied. The rows, the bounds
// and the proxy rule all come from the engine package.
type profile struct {
	flavor     oe.Flavor
	name       string
	preset     string
	members    int32
	cpu        int
	memoryGB   int
	storageGB  int
	daily      bool
	cpuQty     string
	memoryQty  string
	storageQty string
}

func resolveProfile(flavor oe.Flavor, name string, overrides engine.ResourceOverrides) (profile, error) {
	row, err := oe.LookupProfile(name)
	if err != nil {
		return profile{}, err
	}
	sized, err := oe.ResolveSizing(row, overrides)
	if err != nil {
		return profile{}, err
	}

	return profile{
		flavor:    flavor,
		name:      row.Name,
		preset:    oe.PresetName(flavor.ID, row.Name),
		members:   sized.Replicas,
		cpu:       sized.CPUCores,
		memoryGB:  sized.MemoryGB,
		storageGB: sized.StorageGB,
		daily:     row.BackupSchedule == oe.BackupDaily,
	}, nil
}

type objectDoc struct {
	APIVersion string             `json:"apiVersion"`
	Kind       string             `json:"kind"`
	Metadata   everest.ObjectMeta `json:"metadata"`
	Spec       any                `json:"spec"`
}

// profileFromSizing describes an instance the hub never created, so it has no
// catalog row. The size the spoke reports is the only truthful thing to
// rebuild it at, and it carries no preset annotation because there is no
// InstancePreset on the destination matching it.
func profileFromSizing(s engine.Sizing) (profile, bool) {
	members := int32(s.Members)
	if members < 1 || s.CPUCores <= 0 || s.MemoryGB <= 0 || s.StorageGB <= 0 {
		return profile{}, false
	}

	return profile{
		flavor:     oe.MySQL,
		name:       "custom",
		members:    members,
		cpu:        int(math.Ceil(s.CPUCores - 1e-9)),
		memoryGB:   int(math.Ceil(s.MemoryGB - 1e-9)),
		storageGB:  int(math.Ceil(s.StorageGB - 1e-9)),
		cpuQty:     cpuQuantity(s.CPUCores),
		memoryQty:  giQuantity(s.MemoryGB),
		storageQty: giQuantity(s.StorageGB),
		daily:      members >= 3,
	}, true
}

func (p profile) cpuQuantity() string {
	if p.cpuQty != "" {
		return p.cpuQty
	}

	return strconv.Itoa(p.cpu)
}

func (p profile) memoryQuantity() string {
	if p.memoryQty != "" {
		return p.memoryQty
	}

	return strconv.Itoa(p.memoryGB) + "Gi"
}

func (p profile) storageQuantity() string {
	if p.storageQty != "" {
		return p.storageQty
	}

	return strconv.Itoa(p.storageGB) + "Gi"
}

func cpuQuantity(cores float64) string {
	if math.Abs(cores-math.Round(cores)) < 0.0005 {
		return strconv.Itoa(int(math.Round(cores)))
	}

	return strconv.Itoa(int(math.Round(cores*1000))) + "m"
}

func giQuantity(gb float64) string {
	if math.Abs(gb-math.Round(gb)) < 0.0005 {
		return strconv.Itoa(int(math.Round(gb))) + "Gi"
	}

	return strconv.FormatFloat(gb, 'f', -1, 64) + "Gi"
}

// instanceYAML writes the manifest a create asks for. The exposure has been
// validated already; an external one puts the proxy behind a load balancer,
// internal or internet-facing as it says.
func instanceYAML(
	name, namespace, environment string,
	flavor oe.Flavor,
	profileName string,
	targets environmentTargets,
	overrides engine.ResourceOverrides,
	exposure engine.Exposure,
) ([]byte, profile, error) {
	p, err := resolveProfile(flavor, profileName, overrides)
	if err != nil {
		return nil, profile{}, err
	}

	return buildInstanceYAML(name, namespace, environment, p, targets, "", exposure)
}

// environmentTargets names the objects on a spoke that a manifest points at. Each
// spoke reports its own, and an empty one leaves that part of the manifest out.
type environmentTargets struct {
	// BackupStorage is the BackupStorage the backup schedule writes to.
	BackupStorage string
	// MonitoringConfig is the MonitoringConfig the pmm-client reports to.
	MonitoringConfig string
	// OwnershipID tags the database's load balancer, and is empty for an
	// environment the control plane did not build.
	OwnershipID string
}

// buildInstanceYAML writes one Instance manifest. A restoreFrom names the
// backup the operator provisions it from, and an empty one comes up empty.
// Everything else is the same manifest a create of that profile writes.
func buildInstanceYAML(
	name, namespace, environment string,
	p profile,
	targets environmentTargets,
	restoreFrom string,
	exposure engine.Exposure,
) ([]byte, profile, error) {
	backupStorage := targets.BackupStorage

	proxy := oe.ProxyReplicasFor(p.members)

	spec := everest.InstanceSpec{
		ProviderRef: everest.ObjectRef{Name: p.flavor.Provider},
		Topology:    &everest.TopologySpec{Type: p.flavor.Topology},
		Components: map[string]everest.ComponentSpec{
			everest.ComponentEngine: {
				Type:     p.flavor.EngineType,
				Replicas: everest.Ptr(p.members),
				Storage:  &everest.Storage{Size: p.storageQuantity()},
				Resources: &everest.ResourceRequirements{
					Limits: &everest.ResourceList{
						CPU:    p.cpuQuantity(),
						Memory: p.memoryQuantity(),
					},
				},
			},
			everest.ComponentProxy: {
				Type:     p.flavor.ProxyType,
				Replicas: everest.Ptr(proxy),
			},
		},
	}
	if targets.MonitoringConfig != "" {
		monitoring, err := monitoringComponent(targets.MonitoringConfig)
		if err != nil {
			return nil, profile{}, err
		}
		spec.Components[everest.ComponentMonitoring] = monitoring
	}
	if exposure.External {
		proxy := spec.Components[everest.ComponentProxy]
		proxy.Service = oe.LoadBalancerServiceFor(exposure, targets.OwnershipID)
		spec.Components[everest.ComponentProxy] = proxy
	}
	if restoreFrom != "" {
		spec.DataSource = &everest.DataSource{
			Type:   everest.DataSourceTypeBackup,
			Backup: &everest.DataSourceBackup{BackupRef: everest.ObjectRef{Name: restoreFrom}},
		}
	}
	if backupStorage != "" {
		storage := everest.InstanceBackupStorage{
			StorageRef: everest.ObjectRef{Name: backupStorage},
		}
		schedule := oe.ScheduleSpec(engine.DefaultSchedule())
		switch {
		case p.daily:
			storage.Schedules = []everest.InstanceBackupSchedule{schedule}
		case p.flavor.DropsIdleBackupStorage:
			// The provider drops a storage with no schedule entry, and the
			// operator refuses backups enabled with no storage, so the
			// storage carries the schedule it would run, turned off.
			schedule.Enabled = false
			storage.Schedules = []everest.InstanceBackupSchedule{schedule}
		}
		spec.Backup = &everest.InstanceBackupSpec{
			Enabled:  true,
			ClassRef: everest.ObjectRef{Name: oe.BackupClassFor(spec.ProviderRef.Name)},
			Storages: []everest.InstanceBackupStorage{storage},
		}
	}

	doc := objectDoc{
		APIVersion: everest.APIVersion,
		Kind:       everest.KindInstance,
		Metadata: everest.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "pds",
				"pds.percona.com/profile":      p.name,
				oe.LabelEngine:                 p.flavor.ID,
				labelEnvironment:               environment,
			},
			Annotations: presetAnnotation(p),
		},
		Spec: spec,
	}

	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, profile{}, err
	}

	return body, p, nil
}

// monitoringComponent adds the pmm-client to every member, reporting to the
// named MonitoringConfig, so a database charts the same series wherever it runs.
func monitoringComponent(config string) (everest.ComponentSpec, error) {
	params, err := json.Marshal(map[string]string{"monitoringConfigName": config})
	if err != nil {
		return everest.ComponentSpec{}, err
	}

	return everest.ComponentSpec{Type: everest.ComponentTypePMM, Parameters: params}, nil
}

// presetAnnotation names the InstancePreset a create resolves against. An
// instance rebuilt from a reported size matches no preset, and naming one the
// destination does not have is worse than naming none.
func presetAnnotation(p profile) map[string]string {
	if p.preset == "" {
		return nil
	}

	return map[string]string{"openeverest.io/instance-preset": p.preset}
}

// mutateInstanceYAML reads a manifest the hub wrote, hands it to fn, and
// gives back the rewritten document. Editing the parsed spec rather than
// rebuilding it keeps whatever the manifest already carries: the labels, the
// preset annotation, and any backup schedule already on it.
func mutateInstanceYAML(body []byte, fn func(*everest.InstanceSpec) error) ([]byte, error) {
	var doc struct {
		APIVersion string               `json:"apiVersion"`
		Kind       string               `json:"kind"`
		Metadata   everest.ObjectMeta   `json:"metadata"`
		Spec       everest.InstanceSpec `json:"spec"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	if err := fn(&doc.Spec); err != nil {
		return nil, err
	}
	renameLegacyEnvironmentLabel(doc.Metadata.Labels)

	return yaml.Marshal(doc)
}

func storageStateFromYAML(body []byte) (oe.StorageState, error) {
	var doc struct {
		Spec everest.InstanceSpec `json:"spec"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return oe.StorageState{}, err
	}
	comp, ok := doc.Spec.Components[everest.ComponentEngine]
	if !ok || comp.Storage == nil {
		return oe.StorageState{}, nil
	}
	gb, err := oe.ParseSizeGB(comp.Storage.Size)
	if err != nil {
		return oe.StorageState{}, fmt.Errorf("read current storage %q: %w", comp.Storage.Size, err)
	}

	return oe.StorageState{Present: true, GB: gb}, nil
}

// applyScale writes a scale request onto a parsed spec. Only CPU, memory,
// storage, and replicas are touched.
func applyScale(spec *everest.InstanceSpec, in engine.ScaleInput) error {
	comp, ok := spec.Components[everest.ComponentEngine]
	if !ok {
		return fmt.Errorf("%w: the manifest has no engine component", engine.ErrInvalidID)
	}
	if comp.Resources == nil {
		comp.Resources = &everest.ResourceRequirements{}
	}
	if comp.Resources.Limits == nil {
		comp.Resources.Limits = &everest.ResourceList{}
	}
	if in.CPUCores != nil {
		comp.Resources.Limits.CPU = strconv.Itoa(*in.CPUCores)
	}
	if in.MemoryGB != nil {
		comp.Resources.Limits.Memory = strconv.Itoa(*in.MemoryGB) + "Gi"
	}
	if in.StorageGB != nil {
		if comp.Storage == nil {
			comp.Storage = &everest.Storage{}
		}
		comp.Storage.Size = strconv.Itoa(*in.StorageGB) + "Gi"
	}
	if in.Replicas != nil {
		replicas := int32(*in.Replicas)
		comp.Replicas = everest.Ptr(replicas)
		proxy := spec.Components[everest.ComponentProxy]
		proxy.Replicas = everest.Ptr(oe.ProxyReplicasFor(replicas))
		spec.Components[everest.ComponentProxy] = proxy
	}
	spec.Components[everest.ComponentEngine] = comp

	return nil
}

// applySchedule writes in as the manifest's one schedule, replacing the one
// it has. The backup class follows the provider the manifest names, so a
// PostgreSQL instance gets the PostgreSQL class.
func applySchedule(spec *everest.InstanceSpec, storage string, in engine.ScheduleInput) error {
	if (spec.Backup == nil || len(spec.Backup.Storages) == 0) && strings.TrimSpace(storage) == "" {
		return fmt.Errorf("%w: the environment has reported no backup storage", engine.ErrBackupsUnavailable)
	}
	spec.Backup = oe.ApplySchedule(spec.Backup, storage, oe.BackupClassFor(spec.ProviderRef.Name), in)

	return nil
}

// labelEnvironment names the environment an Instance belongs to. The key used
// the old cluster spelling until an environment was called one, and a manifest
// written then keeps it in Git until something rewrites it; the rewrite
// carries the value over to the new key.
const (
	labelEnvironment       = "pds.percona.com/environment"
	legacyLabelEnvironment = "pds.percona.com/cluster" // allow-legacy-name
)

func renameLegacyEnvironmentLabel(labels map[string]string) {
	legacy, ok := labels[legacyLabelEnvironment]
	if !ok {
		return
	}
	if _, current := labels[labelEnvironment]; !current {
		labels[labelEnvironment] = legacy
	}
	delete(labels, legacyLabelEnvironment)
}

// engineFromInstanceYAML reads the engine a manifest was written for. One
// written before the engine label existed is a MySQL database.
func engineFromInstanceYAML(body []byte) string {
	var doc objectDoc
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return defaultEngine
	}
	if id := strings.ToLower(strings.TrimSpace(doc.Metadata.Labels[oe.LabelEngine])); id != "" {
		return id
	}

	return defaultEngine
}

func profileFromInstanceYAML(body []byte) string {
	var doc objectDoc
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return ""
	}
	if doc.Metadata.Labels == nil {
		return ""
	}

	profile := doc.Metadata.Labels["pds.percona.com/profile"]
	if profile == "" {
		// A manifest the hub wrote before the product was renamed to PDS
		// carries the old label domain, and it stays in the repository until
		// something rewrites it.
		profile = doc.Metadata.Labels["services.percona.com/profile"] // allow-legacy-name
	}

	return strings.ToLower(strings.TrimSpace(profile))
}

// exposureFromInstanceYAML reads back the exposure a manifest asks for. An
// unreadable manifest reads as internal, the same default a create writes.
func exposureFromInstanceYAML(body []byte) engine.Exposure {
	var doc struct {
		Spec everest.InstanceSpec `json:"spec"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return engine.Exposure{}
	}

	return oe.ExposureOf(&everest.Instance{Spec: doc.Spec})
}

func instancePath(environment, name string) string {
	return instanceDir(environment) + "/" + name + ".yaml"
}

func environmentPath(environment string) string {
	return fmt.Sprintf("clusters/%s/cluster.yaml", environment)
}

func instanceDir(environment string) string {
	return fmt.Sprintf("clusters/%s/instances", environment)
}

func kustomizeYAML(resources []string) ([]byte, error) {
	doc := struct {
		APIVersion string   `json:"apiVersion"`
		Kind       string   `json:"kind"`
		Resources  []string `json:"resources"`
	}{
		APIVersion: "kustomize.config.k8s.io/v1beta1",
		Kind:       "Kustomization",
		Resources:  resources,
	}
	if doc.Resources == nil {
		doc.Resources = []string{}
	}

	return yaml.Marshal(doc)
}
