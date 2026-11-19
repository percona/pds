package managed

import (
	"errors"
	"fmt"

	_ "embed"

	"sigs.k8s.io/yaml"
)

// SpokeValues is the connected-environment overlay a managed environment's
// release starts from. It is a copy of charts/pds/values-spoke.yaml, which a
// test holds equal, because the adapter cannot embed a file outside its module.
//
//go:embed values-spoke.yaml
var SpokeValues string

// Names in the environment that the release and the steps agree on.
const (
	// AgentTokenSecret holds the agent's credential in the release's namespace.
	// The release names it, so the token never reaches Git.
	AgentTokenSecret = "pds-hub-agent-token"
	// ReleaseName and ReleaseNamespace are those of the pds release, the same
	// ones the join script uses for a connected environment.
	ReleaseName      = "pds"
	ReleaseNamespace = "everest-system"
	// HelmServiceAccount applies the release with cluster-admin equivalent
	// rights, and is created once when Flux is bootstrapped.
	HelmServiceAccount = "pds-helm"
	// ChartRepository is the Flux source the release installs the chart from.
	ChartRepository = "pds-charts"
	// ChartURL is where the control plane's published chart lives.
	ChartURL = "oci://ghcr.io/percona/charts"
)

// ReleaseInput is the small named set of values a managed environment adds to
// the connected-environment overlay.
type ReleaseInput struct {
	ChartVersion    string
	HubURL          string
	EnvironmentName string
	ImageRepository string
	ImageTag        string
}

// RenderRelease renders the HelmRelease Flux applies for the whole agent
// release: OpenEverest, the providers, PMM and the adapter as agent.
func RenderRelease(in ReleaseInput) ([]byte, error) {
	for name, value := range map[string]string{
		"chart version": in.ChartVersion, "hub address": in.HubURL,
		"environment name": in.EnvironmentName, "agent image": in.ImageRepository,
	} {
		if value == "" {
			return nil, fmt.Errorf("render the pds release: the %s is empty", name)
		}
	}

	values := map[string]any{}
	if err := yaml.Unmarshal([]byte(SpokeValues), &values); err != nil {
		return nil, fmt.Errorf("read the connected-environment values: %w", err)
	}
	managedValues := map[string]any{
		"adapter": map[string]any{
			"image": map[string]any{"repository": in.ImageRepository, "tag": in.ImageTag},
			"hub": map[string]any{
				"url": in.HubURL, "environmentName": in.EnvironmentName,
				"agentTokenExistingSecret": AgentTokenSecret,
			},
			// The hub annotates each Instance with the preset a create resolves
			// against, so the environment has to have them.
			"installInstancePresets": true,
		},
		// Flux installs PMM from the environment's Git, so the release leaves it out.
		"stack": map[string]any{"pmm": map[string]any{"subchart": false}},
		"openeverest": map[string]any{
			"upgrade": map[string]any{"preflightChecks": false},
		},
	}
	merge(values, managedValues)

	release := map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   map[string]any{"name": ReleaseName, "namespace": "flux-system"},
		"spec": map[string]any{
			"interval":           "30m",
			"releaseName":        ReleaseName,
			"targetNamespace":    ReleaseNamespace,
			"storageNamespace":   ReleaseNamespace,
			"serviceAccountName": HelmServiceAccount,
			"chart": map[string]any{"spec": map[string]any{
				"chart": "pds", "version": in.ChartVersion,
				"sourceRef": map[string]any{"kind": "HelmRepository", "name": ChartRepository},
			}},
			"install": map[string]any{
				"createNamespace": true,
				"remediation":     map[string]any{"retries": 3},
			},
			// A failed upgrade rolls back once and stops retrying.
			"upgrade": map[string]any{
				"remediation":   map[string]any{"retries": 1, "remediateLastFailure": true},
				"cleanupOnFail": true,
			},
			"values": values,
		},
	}

	body, err := yaml.Marshal(release)
	if err != nil {
		return nil, errors.Join(errors.New("render the pds release"), err)
	}

	return body, nil
}

// merge copies overlay into base, descending into maps both sides hold.
func merge(base, overlay map[string]any) {
	for key, value := range overlay {
		if next, ok := value.(map[string]any); ok {
			if existing, ok := base[key].(map[string]any); ok {
				merge(existing, next)

				continue
			}
		}
		base[key] = value
	}
}
