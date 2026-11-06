package hub

import (
	"errors"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/oe"
)

// DefaultPMMChartVersion is the percona/pmm chart a spoke installs when the
// hub is not told otherwise. It matches the pin in the chart's compatibility
// file, so a spoke and a laptop stack run the same PMM.
const DefaultPMMChartVersion = "1.9.1"

// PMM settings bounds. They keep a typo from starving the operators next to
// PMM, or from asking for a volume no node can hold.
const (
	pmmMinCPUMillicores = 250
	pmmMaxCPUMillicores = 16000
	pmmMinMemoryMi      = 512
	pmmMaxMemoryMi      = 65536
	pmmMinStorageGi     = 5
	pmmMaxStorageGi     = 2000
)

// How far monitoring is reachable. Cluster only is the default: nothing
// outside the Kubernetes cluster can open it. Private puts it behind an internal cloud
// load balancer, and public behind an internet-facing one. The names match
// databases, so a customer reads the same words in both places.
const (
	PMMAccessCluster = "cluster"
	PMMAccessPrivate = "private"
	PMMAccessPublic  = "public"
)

// pmmServiceName is the Service database pods and the agent reach PMM by. The
// source range patch targets it by name.
const pmmServiceName = "monitoring-service"

// PMMSettings is what the hub lets an operator change about the PMM a spoke
// runs. The hub renders it into the HelmRelease Flux applies, so a change here
// reaches the environment without anyone touching it.
type PMMSettings struct {
	// Access is how far monitoring is reachable: cluster, private or public.
	Access string `json:"access"`
	// SourceRanges are the addresses a load balancer admits. A private one
	// defaults to the private address ranges and can be narrowed. A public
	// one needs at least one. It is empty while access is cluster only.
	SourceRanges []string `json:"sourceRanges,omitempty"`
	// CPUMillicores and MemoryMi are the container limits.
	CPUMillicores int `json:"cpuMillicores"`
	MemoryMi      int `json:"memoryMi"`
	// StorageGi sizes the PMM data volume.
	StorageGi int `json:"storageGi"`
}

// DefaultPMMSettings matches what the umbrella chart installs today, so
// moving a spoke to hub-managed PMM changes where PMM is defined and nothing
// about how it runs.
func DefaultPMMSettings() PMMSettings {
	return PMMSettings{
		Access:        PMMAccessCluster,
		CPUMillicores: 1000,
		MemoryMi:      1024,
		StorageGi:     5,
	}
}

// Validate reports the first setting outside its bounds.
func (s PMMSettings) Validate() error {
	_, err := s.Normalize()

	return err
}

// Normalize validates the settings and returns them in the form the hub
// stores: ranges written as CIDR blocks with host bits cleared, and a private
// service with no ranges holding the private ranges it is limited to. What the
// panel reads back is then what the Service enforces.
func (s PMMSettings) Normalize() (PMMSettings, error) {
	out := s
	switch s.Access {
	case PMMAccessCluster, PMMAccessPrivate, PMMAccessPublic:
	default:
		return PMMSettings{}, fmt.Errorf("access must be environment, private, or public, not %q", s.Access)
	}

	ranges, err := engine.NormalizeSourceRanges(s.SourceRanges)
	if err != nil {
		var bad *engine.SourceRangeError
		switch {
		case errors.As(err, &bad) && bad.TooMany:
			return PMMSettings{}, fmt.Errorf("allow at most %d ranges", engine.MaxSourceRanges)
		case errors.As(err, &bad):
			return PMMSettings{}, fmt.Errorf("%q is not an IP address or CIDR range", bad.Value)
		}

		return PMMSettings{}, err
	}
	switch s.Access {
	case PMMAccessCluster:
		if len(ranges) > 0 {
			return PMMSettings{}, errors.New("allowed addresses apply only when monitoring is exposed")
		}
	case PMMAccessPrivate:
		if len(ranges) == 0 {
			ranges = append([]string(nil), engine.PrivateSourceRanges...)
		}
	case PMMAccessPublic:
		if len(ranges) == 0 {
			return PMMSettings{}, errors.New("public access needs at least one allowed address or range")
		}
	}
	out.SourceRanges = ranges

	if s.CPUMillicores < pmmMinCPUMillicores || s.CPUMillicores > pmmMaxCPUMillicores {
		return PMMSettings{}, fmt.Errorf("cpu must be between %dm and %dm", pmmMinCPUMillicores, pmmMaxCPUMillicores)
	}
	if s.MemoryMi < pmmMinMemoryMi || s.MemoryMi > pmmMaxMemoryMi {
		return PMMSettings{}, fmt.Errorf("memory must be between %dMi and %dMi", pmmMinMemoryMi, pmmMaxMemoryMi)
	}
	if s.StorageGi < pmmMinStorageGi || s.StorageGi > pmmMaxStorageGi {
		return PMMSettings{}, fmt.Errorf("storage must be between %dGi and %dGi", pmmMinStorageGi, pmmMaxStorageGi)
	}

	return out, nil
}

// pmmDir is where an environment's PMM definition lives in Git. It sits beside
// instances/, and a separate Flux Kustomization on the spoke applies it.
func pmmDir(environment string) string {
	return fmt.Sprintf("clusters/%s/platform/pmm", environment)
}

func cpuQuantityMillis(m int) string {
	if m%1000 == 0 {
		return fmt.Sprintf("%d", m/1000)
	}

	return fmt.Sprintf("%dm", m)
}

func memoryQuantityMi(mi int) string {
	if mi%1024 == 0 {
		return fmt.Sprintf("%dGi", mi/1024)
	}

	return fmt.Sprintf("%dMi", mi)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}

	return b
}

// pmmFiles renders the Git tree for one environment's PMM. No file names a
// namespace: the Kustomization on the spoke sets it, the same way it does for
// instances, so PMM lands beside the release the agent was installed with.
// withRules adds the alert rules ConfigMap and its mount. An environment whose
// agent cannot apply a ConfigMap gets a tree without them, because Flux drops
// the whole apply when one resource is refused.
func pmmFiles(environment string, in PMMSettings, chartVersion string, withRules bool) (map[string][]byte, error) {
	s, err := in.Normalize()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(chartVersion) == "" {
		chartVersion = DefaultPMMChartVersion
	}

	service := map[string]any{
		"name": pmmServiceName,
		"type": "ClusterIP",
	}
	spec := map[string]any{}
	if s.Access != PMMAccessCluster {
		// The annotations come from the same place a database's do, so the two
		// cannot drift on what private and public mean to a cloud.
		lb := oe.LoadBalancerServiceFor(engine.Exposure{External: true, Private: s.Access == PMMAccessPrivate})
		service["type"] = lb.ServiceType
		service["annotations"] = lb.Annotations
		// The chart renders no loadBalancerSourceRanges, so the list is added
		// to the Service after it renders. The patch finds it by name.
		patch, err := yaml.Marshal([]map[string]any{{
			"op":    "add",
			"path":  "/spec/loadBalancerSourceRanges",
			"value": s.SourceRanges,
		}})
		if err != nil {
			return nil, err
		}
		spec["postRenderers"] = []any{map[string]any{
			"kustomize": map[string]any{
				"patches": []any{map[string]any{
					"target": map[string]any{"kind": "Service", "name": pmmServiceName},
					"patch":  string(patch),
				}},
			},
		}}
	}

	release := map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   map[string]any{"name": "pmm"},
		"spec": map[string]any{
			"interval":    "30m",
			"releaseName": "pmm",
			"chart": map[string]any{
				"spec": map[string]any{
					"chart":   "pmm",
					"version": chartVersion,
					"sourceRef": map[string]any{
						"kind": "HelmRepository",
						"name": "percona",
					},
				},
			},
			"install": map[string]any{"remediation": map[string]any{"retries": 3}},
			"upgrade": map[string]any{"remediation": map[string]any{"retries": 3}},
			"values": map[string]any{
				"fullnameOverride":  "pmm",
				"priorityClassName": "pds-bulk",
				"service":           service,
				"storage":           map[string]any{"size": fmt.Sprintf("%dGi", s.StorageGi)},
				"resources": map[string]any{
					"requests": map[string]any{
						"cpu":    cpuQuantityMillis(minInt(250, s.CPUMillicores)),
						"memory": memoryQuantityMi(minInt(512, s.MemoryMi)),
					},
					"limits": map[string]any{
						"cpu":    cpuQuantityMillis(s.CPUMillicores),
						"memory": memoryQuantityMi(s.MemoryMi),
					},
				},
				"nodeSelector": map[string]any{"kubernetes.io/arch": "amd64"},
				// The admin password is created once in the environment. Putting
				// it in this tree would put it in Git.
				"secret": map[string]any{"create": false, "name": "pmm-secret"},
			},
		},
	}
	releaseSpec, _ := release["spec"].(map[string]any)
	for key, value := range spec {
		releaseSpec[key] = value
	}
	resources := []string{"helmrepository.yaml", "helmrelease.yaml"}
	var rulesManifest []byte
	if withRules {
		var rulesDigest string
		rulesManifest, rulesDigest, err = pmmRulesConfigMap()
		if err != nil {
			return nil, err
		}
		values, _ := releaseSpec["values"].(map[string]any)
		// Setting extraVolumes replaces the chart's own list, so its
		// pmm-config volume and mount are written back beside the rules.
		values["extraVolumes"] = []any{
			map[string]any{"name": "pmm-config", "emptyDir": map[string]any{}},
			map[string]any{"name": pmmRulesName, "configMap": map[string]any{"name": pmmRulesName}},
		}
		values["extraVolumeMounts"] = []any{
			map[string]any{"name": "pmm-config", "mountPath": "/usr/local/percona/pmm/config"},
			map[string]any{"name": pmmRulesName, "mountPath": pmmRulesMountPath, "readOnly": true},
		}
		values["podAnnotations"] = map[string]any{"pds.percona.com/alert-rules": rulesDigest}
		resources = append(resources, "alert-rules.yaml")
	}
	repository := map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "HelmRepository",
		"metadata":   map[string]any{"name": "percona"},
		"spec": map[string]any{
			"interval": "1h",
			"url":      "https://percona.github.io/percona-helm-charts/",
		},
	}

	dir := pmmDir(environment)
	files := map[string][]byte{}
	for name, doc := range map[string]map[string]any{
		"helmrepository.yaml": repository,
		"helmrelease.yaml":    release,
	} {
		body, err := yaml.Marshal(doc)
		if err != nil {
			return nil, err
		}
		files[dir+"/"+name] = body
	}
	if withRules {
		files[dir+"/alert-rules.yaml"] = rulesManifest
	}
	index, err := kustomizeYAML(resources)
	if err != nil {
		return nil, err
	}
	files[dir+"/kustomization.yaml"] = index

	return files, nil
}
