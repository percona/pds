package hub

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestPMMDirIsUnderThePlatformTree(t *testing.T) {
	assert.Equal(t, "clusters/spoke-a/platform/pmm", pmmDir("spoke-a"))
}

func TestDefaultPMMSettingsAreValid(t *testing.T) {
	require.NoError(t, DefaultPMMSettings().Validate())
}

func TestPMMSettingsValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PMMSettings)
		want   string
	}{
		{"no access", func(s *PMMSettings) { s.Access = "" }, "access"},
		{"unknown access", func(s *PMMSettings) { s.Access = "loadbalancer" }, "access"},
		{"ranges while cluster only", func(s *PMMSettings) { s.SourceRanges = []string{"10.0.0.0/8"} }, "exposed"},
		{"public without a range", func(s *PMMSettings) { s.Access = PMMAccessPublic }, "at least one"},
		{"public with only blanks", func(s *PMMSettings) { s.Access = PMMAccessPublic; s.SourceRanges = []string{" ", ""} }, "at least one"},
		{"not an address", func(s *PMMSettings) { s.Access = PMMAccessPublic; s.SourceRanges = []string{"office"} }, "office"},
		{"bad prefix length", func(s *PMMSettings) { s.Access = PMMAccessPrivate; s.SourceRanges = []string{"10.0.0.0/33"} }, "10.0.0.0/33"},
		{"too many ranges", func(s *PMMSettings) {
			s.Access = PMMAccessPublic
			for i := 0; i < 33; i++ {
				s.SourceRanges = append(s.SourceRanges, fmt.Sprintf("10.%d.0.0/16", i))
			}
		}, "32"},
		{"cpu below the floor", func(s *PMMSettings) { s.CPUMillicores = 50 }, "cpu"},
		{"cpu above the ceiling", func(s *PMMSettings) { s.CPUMillicores = 64000 }, "cpu"},
		{"memory below the floor", func(s *PMMSettings) { s.MemoryMi = 64 }, "memory"},
		{"storage below the floor", func(s *PMMSettings) { s.StorageGi = 1 }, "storage"},
		{"storage above the ceiling", func(s *PMMSettings) { s.StorageGi = 100000 }, "storage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := DefaultPMMSettings()
			tt.mutate(&s)
			err := s.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestPMMFilesListEveryFileInTheKustomization(t *testing.T) {
	files, err := pmmFiles("spoke-a", DefaultPMMSettings(), "1.9.1", true)
	require.NoError(t, err)

	var index struct {
		Resources []string `json:"resources"`
	}
	require.NoError(t, yaml.Unmarshal(files[pmmDir("spoke-a")+"/kustomization.yaml"], &index))
	require.NotEmpty(t, index.Resources)
	for _, name := range index.Resources {
		assert.Contains(t, files, pmmDir("spoke-a")+"/"+name, "kustomization names a file that was not written")
	}
}

// The spoke's Kustomization sets the namespace, so the files must not pin one.
// A pinned namespace here would install PMM somewhere the agent does not look.
func TestPMMFilesLeaveTheNamespaceToTheSpoke(t *testing.T) {
	files, err := pmmFiles("spoke-a", DefaultPMMSettings(), "1.9.1", true)
	require.NoError(t, err)

	for path, body := range files {
		var doc struct {
			Metadata struct {
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		}
		require.NoError(t, yaml.Unmarshal(body, &doc), path)
		assert.Empty(t, doc.Metadata.Namespace, path)
	}
}

type helmReleaseDoc struct {
	Kind string `json:"kind"`
	Spec struct {
		ReleaseName string `json:"releaseName"`
		Chart       struct {
			Spec struct {
				Chart   string `json:"chart"`
				Version string `json:"version"`
			} `json:"spec"`
		} `json:"chart"`
		Values struct {
			Service struct {
				Type        string            `json:"type"`
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"service"`
			Storage struct {
				Size string `json:"size"`
			} `json:"storage"`
			Resources struct {
				Requests map[string]string `json:"requests"`
				Limits   map[string]string `json:"limits"`
			} `json:"resources"`
			Secret struct {
				Create bool   `json:"create"`
				Name   string `json:"name"`
			} `json:"secret"`
		} `json:"values"`
	} `json:"spec"`
}

func readHelmRelease(t *testing.T, s PMMSettings, version string) helmReleaseDoc {
	t.Helper()
	files, err := pmmFiles("spoke-a", s, version, true)
	require.NoError(t, err)
	var doc helmReleaseDoc
	require.NoError(t, yaml.Unmarshal(files[pmmDir("spoke-a")+"/helmrelease.yaml"], &doc))

	return doc
}

func TestPMMHelmReleasePinsTheChartAndKeepsTheServiceName(t *testing.T) {
	doc := readHelmRelease(t, DefaultPMMSettings(), "1.9.1")

	assert.Equal(t, "HelmRelease", doc.Kind)
	assert.Equal(t, "pmm", doc.Spec.Chart.Spec.Chart)
	assert.Equal(t, "1.9.1", doc.Spec.Chart.Spec.Version)
	assert.Equal(t, "pmm", doc.Spec.ReleaseName)
	// Database pods and the agent reach PMM by this name.
	assert.Equal(t, "monitoring-service", doc.Spec.Values.Service.Name)
	assert.Equal(t, "ClusterIP", doc.Spec.Values.Service.Type)
	// The admin password is created in the environment, never rendered here.
	assert.False(t, doc.Spec.Values.Secret.Create)
	assert.Equal(t, "pmm-secret", doc.Spec.Values.Secret.Name)
}

func TestPMMHelmReleaseCarriesTheSettings(t *testing.T) {
	s := DefaultPMMSettings()
	s.Access = PMMAccessPrivate
	s.CPUMillicores = 2000
	s.MemoryMi = 2048
	s.StorageGi = 20

	doc := readHelmRelease(t, s, "1.9.1")

	assert.Equal(t, "LoadBalancer", doc.Spec.Values.Service.Type)
	assert.Equal(t, "true", doc.Spec.Values.Service.Annotations["service.beta.kubernetes.io/aws-load-balancer-internal"])
	assert.Equal(t, "20Gi", doc.Spec.Values.Storage.Size)
	assert.Equal(t, "2", doc.Spec.Values.Resources.Limits["cpu"])
	assert.Equal(t, "2Gi", doc.Spec.Values.Resources.Limits["memory"])
}

func TestPMMFilesRejectInvalidSettings(t *testing.T) {
	s := DefaultPMMSettings()
	s.Access = "everywhere"
	_, err := pmmFiles("spoke-a", s, "1.9.1", true)
	require.Error(t, err)
}

func TestPMMFilesAreStableForTheSameSettings(t *testing.T) {
	a, err := pmmFiles("spoke-a", DefaultPMMSettings(), "1.9.1", true)
	require.NoError(t, err)
	b, err := pmmFiles("spoke-a", DefaultPMMSettings(), "1.9.1", true)
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

// The percona/pmm chart already labels every object app.kubernetes.io/name.
// Setting that label again through extraLabels renders it twice. Helm accepts
// the duplicate, but the post-render step Flux runs rejects it as invalid YAML
// and the install never starts.
func TestPMMHelmReleaseDoesNotRepeatLabelsTheChartSets(t *testing.T) {
	files, err := pmmFiles("spoke-a", DefaultPMMSettings(), "1.9.1", true)
	require.NoError(t, err)

	var doc struct {
		Spec struct {
			Values map[string]any `json:"values"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(files[pmmDir("spoke-a")+"/helmrelease.yaml"], &doc))

	assert.NotContains(t, doc.Spec.Values, "extraLabels")
}

// pmmPatch is the post-renderer patch the hub adds to open the Service to a
// list of addresses. The chart cannot render loadBalancerSourceRanges.
type pmmPatch struct {
	TargetKind string
	TargetName string
	Ops        []struct {
		Op    string   `json:"op"`
		Path  string   `json:"path"`
		Value []string `json:"value"`
	}
}

func readPostRenderers(t *testing.T, s PMMSettings) []pmmPatch {
	t.Helper()
	files, err := pmmFiles("spoke-a", s, "1.9.1", true)
	require.NoError(t, err)
	var doc struct {
		Spec struct {
			PostRenderers []struct {
				Kustomize struct {
					Patches []struct {
						Target struct {
							Kind string `json:"kind"`
							Name string `json:"name"`
						} `json:"target"`
						Patch string `json:"patch"`
					} `json:"patches"`
				} `json:"kustomize"`
			} `json:"postRenderers"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(files[pmmDir("spoke-a")+"/helmrelease.yaml"], &doc))

	var out []pmmPatch
	for _, pr := range doc.Spec.PostRenderers {
		for _, p := range pr.Kustomize.Patches {
			patch := pmmPatch{TargetKind: p.Target.Kind, TargetName: p.Target.Name}
			require.NoError(t, yaml.Unmarshal([]byte(p.Patch), &patch.Ops))
			out = append(out, patch)
		}
	}

	return out
}

func TestPMMEnvironmentOnlyRendersAClusterIPServiceAndNothingElse(t *testing.T) {
	doc := readHelmRelease(t, DefaultPMMSettings(), "1.9.1")

	assert.Equal(t, "ClusterIP", doc.Spec.Values.Service.Type)
	assert.Empty(t, doc.Spec.Values.Service.Annotations)
	assert.Empty(t, readPostRenderers(t, DefaultPMMSettings()), "nothing opens a service nobody asked to open")
}

func TestPMMPrivateRendersAnInternalLoadBalancerLimitedToPrivateAddresses(t *testing.T) {
	s := DefaultPMMSettings()
	s.Access = PMMAccessPrivate

	doc := readHelmRelease(t, s, "1.9.1")
	assert.Equal(t, "LoadBalancer", doc.Spec.Values.Service.Type)
	assert.Equal(t, "true", doc.Spec.Values.Service.Annotations["service.beta.kubernetes.io/aws-load-balancer-internal"])
	assert.Equal(t, "internal", doc.Spec.Values.Service.Annotations["service.beta.kubernetes.io/aws-load-balancer-scheme"])

	// A cloud that ignores the annotations still must not open it to the internet.
	patches := readPostRenderers(t, s)
	require.Len(t, patches, 1)
	assert.Equal(t, "Service", patches[0].TargetKind)
	assert.Equal(t, "monitoring-service", patches[0].TargetName)
	require.Len(t, patches[0].Ops, 1)
	assert.Equal(t, "add", patches[0].Ops[0].Op)
	assert.Equal(t, "/spec/loadBalancerSourceRanges", patches[0].Ops[0].Path)
	assert.Equal(t, []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}, patches[0].Ops[0].Value)
}

func TestPMMPrivateCanBeNarrowed(t *testing.T) {
	s := DefaultPMMSettings()
	s.Access = PMMAccessPrivate
	// A bare address means one host, and host bits are cleared.
	s.SourceRanges = []string{"10.1.2.3", " 10.9.9.9/16 ", "10.1.2.3"}

	patches := readPostRenderers(t, s)

	require.Len(t, patches, 1)
	assert.Equal(t, []string{"10.1.2.3/32", "10.9.0.0/16"}, patches[0].Ops[0].Value)
}

func TestPMMPublicRendersAnInternetFacingLoadBalancerWithTheAllowList(t *testing.T) {
	s := DefaultPMMSettings()
	s.Access = PMMAccessPublic
	s.SourceRanges = []string{"203.0.113.0/24"}

	doc := readHelmRelease(t, s, "1.9.1")
	assert.Equal(t, "LoadBalancer", doc.Spec.Values.Service.Type)
	assert.Equal(t, "internet-facing", doc.Spec.Values.Service.Annotations["service.beta.kubernetes.io/aws-load-balancer-scheme"])
	assert.NotContains(t, doc.Spec.Values.Service.Annotations, "service.beta.kubernetes.io/aws-load-balancer-internal")

	patches := readPostRenderers(t, s)
	require.Len(t, patches, 1)
	assert.Equal(t, []string{"203.0.113.0/24"}, patches[0].Ops[0].Value)
}

// Open to the internet is a choice the customer can make, and the form warns
// about it. It is written as an explicit range, never an omitted list.
func TestPMMPublicCanBeOpenToAnyAddress(t *testing.T) {
	s := DefaultPMMSettings()
	s.Access = PMMAccessPublic
	s.SourceRanges = []string{"0.0.0.0/0"}

	require.NoError(t, s.Validate())
	assert.Equal(t, []string{"0.0.0.0/0"}, readPostRenderers(t, s)[0].Ops[0].Value)
}

func TestPMMRangesKeepTheOrderTheCustomerGaveThem(t *testing.T) {
	s := DefaultPMMSettings()
	s.Access = PMMAccessPublic
	s.SourceRanges = []string{"198.51.100.0/24", "192.0.2.0/24"}

	assert.Equal(t, []string{"198.51.100.0/24", "192.0.2.0/24"}, readPostRenderers(t, s)[0].Ops[0].Value)
}
