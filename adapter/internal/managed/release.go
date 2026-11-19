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
	// BackupStorage names the BackupStorage the release registers for the
	// bucket, and BackupSecret the Secret with its access key. The agent
	// creates the key in the environment and writes the Secret itself, so the
	// key never reaches the control plane.
	BackupStorage = "s3"
	BackupSecret  = "pds-s3"
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
	// EnvironmentID is the value of the percona.com/pds tag the environment's
	// volumes carry.
	EnvironmentID   string
	ImageRepository string
	ImageTag        string
	// Bucket and Region are the environment's backup store.
	Bucket string
	Region string
}

// RenderRelease renders the HelmRelease Flux applies for the whole agent
// release: OpenEverest, the providers, PMM and the adapter as agent.
func RenderRelease(in ReleaseInput) ([]byte, error) {
	for name, value := range map[string]string{
		"chart version": in.ChartVersion, "hub address": in.HubURL,
		"environment name": in.EnvironmentName, "environment id": in.EnvironmentID, "agent image": in.ImageRepository,
		"backup bucket": in.Bucket, "backup region": in.Region,
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
			// The bucket's backup store replaces the MinIO a connected overlay starts from.
			"everestBackupStorage": BackupStorage,
		},
		"stack": map[string]any{
			// Flux installs PMM from the environment's Git, so the release leaves it out.
			"pmm":   map[string]any{"subchart": false},
			"minio": map[string]any{"enabled": false},
			"s3": map[string]any{
				"enabled": true, "bucket": in.Bucket, "region": in.Region,
				"storageName": BackupStorage, "existingSecret": BackupSecret,
			},
		},
		// Every volume a database claims carries the ownership tag, so the delete
		// finds it by tag after the cluster is gone.
		"taggedStorageClass": map[string]any{
			"enabled": true, "tags": map[string]any{OwnershipTag: in.EnvironmentID},
		},
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

// ReleaseFiles are the files that make the pds release part of the
// environment's Git repository, under clusters/<environment>/platform/pds.
func ReleaseFiles(environment string, in ReleaseInput) (map[string][]byte, error) {
	release, err := RenderRelease(in)
	if err != nil {
		return nil, err
	}
	repository, err := yaml.Marshal(map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "HelmRepository",
		"metadata":   map[string]any{"name": ChartRepository, "namespace": "flux-system"},
		"spec":       map[string]any{"type": "oci", "interval": "1h", "url": ChartURL},
	})
	if err != nil {
		return nil, fmt.Errorf("render the chart source: %w", err)
	}
	index, err := yaml.Marshal(map[string]any{
		"apiVersion": "kustomize.config.k8s.io/v1beta1",
		"kind":       "Kustomization",
		"resources":  []string{"helmrepository.yaml", "helmrelease.yaml"},
	})
	if err != nil {
		return nil, fmt.Errorf("render the release index: %w", err)
	}
	dir := "clusters/" + environment + "/platform/pds/"

	return map[string][]byte{
		dir + "helmrelease.yaml":    release,
		dir + "helmrepository.yaml": repository,
		dir + "kustomization.yaml":  index,
	}, nil
}
