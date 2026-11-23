package oe

import (
	"strings"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
)

// PresetVersion is the InstancePreset generation this adapter accepts.
// Helm writes the same value on the PDS presets. Bump both together
// when Dev, Staging, or Prod sizing changes.
const PresetVersion = "1"

// AnnotationPresetVersion is the chart annotation a PDS preset carries.
const AnnotationPresetVersion = "pds.percona.com/preset-version"

// LabelEngine marks which engine a PDS preset, or an Instance the hub wrote,
// belongs to.
const LabelEngine = "pds.percona.com/engine"

// PresetName is the cluster-scoped InstancePreset for one engine and profile,
// for example pds-mysql-dev.
func PresetName(engineID, profile string) string {
	return "pds-" + engineID + "-" + strings.ToLower(strings.TrimSpace(profile))
}

// ParsePresetName splits a PDS preset name into engine and profile.
func ParsePresetName(name string) (engineID, profile string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(name), "pds-")
	if !found || rest == "" {
		return "", "", false
	}

	for _, candidate := range []string{ProfileDev, ProfileStaging, ProfileProd} {
		suffix := "-" + candidate
		if strings.HasSuffix(rest, suffix) {
			engineID = strings.TrimSuffix(rest, suffix)
			if engineID == "" {
				return "", "", false
			}

			return engineID, candidate, true
		}
	}

	return "", "", false
}

// CatalogPreset is the InstancePreset shape this adapter expects Helm to
// install for one engine and profile.
func CatalogPreset(flavor Flavor, profile engine.Profile) *everest.InstancePreset {
	replicas := int32(profile.Members)
	components := map[string]everest.ComponentSpec{
		everest.ComponentEngine: {
			Type:     flavor.EngineType,
			Replicas: everest.Ptr(replicas),
			Storage:  &everest.Storage{Size: formatGi(profile.StorageGB)},
			Resources: &everest.ResourceRequirements{
				Limits: &everest.ResourceList{
					CPU:    formatCores(profile.CPUCores),
					Memory: formatGi(profile.MemoryGB),
				},
			},
		},
	}
	if !flavor.EngineOnly {
		components[everest.ComponentProxy] = everest.ComponentSpec{
			Type:     flavor.ProxyType,
			Replicas: everest.Ptr(proxyReplicasFor(replicas)),
		}
	}

	return &everest.InstancePreset{
		APIVersion: everest.APIVersion,
		Kind:       everest.KindInstancePreset,
		Metadata: everest.ObjectMeta{
			Name: PresetName(flavor.ID, profile.Name),
			Labels: map[string]string{
				LabelEngine:  flavor.ID,
				LabelProfile: profile.Name,
			},
			Annotations: map[string]string{
				AnnotationPresetVersion: PresetVersion,
			},
		},
		Spec: everest.InstanceSpec{
			ProviderRef: everest.ObjectRef{Name: flavor.Provider},
			Topology:    &everest.TopologySpec{Type: flavor.Topology},
			Components:  components,
		},
	}
}

// LookupCatalogPreset returns the shipped preset for a PDS name.
func LookupCatalogPreset(name string) (*everest.InstancePreset, bool) {
	engineID, profileName, ok := ParsePresetName(name)
	if !ok {
		return nil, false
	}

	flavor, found := LookupFlavor(engineID)
	if !found {
		return nil, false
	}

	profile, err := lookupProfile(profileName)
	if err != nil {
		return nil, false
	}

	return CatalogPreset(flavor, profile), true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}
