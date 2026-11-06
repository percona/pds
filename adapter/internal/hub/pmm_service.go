package hub

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/oe"
)

// ErrInvalidPMMSettings means the settings are outside what the hub will write.
var ErrInvalidPMMSettings = errors.New("invalid monitoring settings")

// helmReleaseValues is the part of the HelmRelease the hub reads back. The
// file is the one record of the settings, so a change made here and a change
// someone commits to Git are the same thing.
type helmReleaseValues struct {
	Spec struct {
		// PostRenderers carry the source range patch, which the chart cannot
		// render itself.
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
		Values struct {
			Service struct {
				Type        string            `json:"type"`
				Annotations map[string]string `json:"annotations"`
			} `json:"service"`
			Storage struct {
				Size string `json:"size"`
			} `json:"storage"`
			Resources struct {
				Limits struct {
					CPU    string `json:"cpu"`
					Memory string `json:"memory"`
				} `json:"limits"`
			} `json:"resources"`
		} `json:"values"`
	} `json:"spec"`
}

// PMMSettings reads the PMM settings written for an environment. An environment with no
// stored definition gets the defaults, which is what a join would have
// written.
func (s *Service) PMMSettings(ctx context.Context, environment string) (PMMSettings, error) {
	environment = strings.TrimSpace(environment)
	if err := s.requireInventory(environment); err != nil {
		return PMMSettings{}, err
	}
	body, err := s.store.Get(ctx, pmmDir(environment)+"/helmrelease.yaml")
	if err != nil {
		return DefaultPMMSettings(), nil //nolint:nilerr // no file yet means defaults
	}

	return parsePMMSettings(body)
}

// SetPMMSettings validates the settings and rewrites the environment's PMM
// definition. The spoke's Flux applies it, and the PMM release upgrades in
// place.
func (s *Service) SetPMMSettings(ctx context.Context, environment string, in PMMSettings) error {
	environment = strings.TrimSpace(environment)
	if err := s.requireInventory(environment); err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidPMMSettings, err.Error())
	}
	unlock := s.spokes.lock(environment)
	defer unlock()
	// Only an agent that reported the mark can have the ConfigMap applied. The
	// mark is read under the lock a beat takes to record it, so a beat that
	// lands while this call waits is not overwritten by a stale answer.
	_, withRules := s.inv.PMMReport(environment)
	files, err := pmmFiles(environment, in, s.pmmVersion, withRules)
	if err != nil {
		return err
	}
	if err := s.store.Write(ctx, files, "set monitoring "+environment); err != nil {
		return err
	}
	if !withRules {
		s.rulesGate.forget(environment)
	}

	return nil
}

func (s *Service) requireInventory(environment string) error {
	if !ValidEnvironmentName(environment) {
		return fmt.Errorf("%w: %q", ErrInvalidEnvironment, environment)
	}
	if _, known := s.inv.Get(environment, s.now()); !known {
		return fmt.Errorf("%w: %q", engine.ErrNotFound, environment)
	}

	return nil
}

func parsePMMSettings(body []byte) (PMMSettings, error) {
	var doc helmReleaseValues
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return PMMSettings{}, fmt.Errorf("monitoring definition is not readable: %w", err)
	}
	v := doc.Spec.Values
	out := DefaultPMMSettings()
	out.Access, out.SourceRanges = readPMMAccess(doc)
	if m, ok := parseMillicores(v.Resources.Limits.CPU); ok {
		out.CPUMillicores = m
	}
	if mi, ok := parseMebibytes(v.Resources.Limits.Memory); ok {
		out.MemoryMi = mi
	}
	if gi, ok := parseGibibytes(v.Storage.Size); ok {
		out.StorageGi = gi
	}

	return out, nil
}

// The quantity parsers read only the forms pmmFiles writes: whole cores or
// millicores, and Mi or Gi. Anything else keeps the default, so a hand edit to
// another unit is not misread as a number.
func parseMillicores(q string) (int, bool) {
	if v, ok := strings.CutSuffix(q, "m"); ok {
		n, err := strconv.Atoi(v)

		return n, err == nil
	}
	n, err := strconv.Atoi(q)

	return n * 1000, err == nil
}

func parseMebibytes(q string) (int, bool) {
	if v, ok := strings.CutSuffix(q, "Gi"); ok {
		n, err := strconv.Atoi(v)

		return n * 1024, err == nil
	}
	if v, ok := strings.CutSuffix(q, "Mi"); ok {
		n, err := strconv.Atoi(v)

		return n, err == nil
	}

	return 0, false
}

func parseGibibytes(q string) (int, bool) {
	v, ok := strings.CutSuffix(q, "Gi")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(v)

	return n, err == nil
}

// readPMMAccess reads how far monitoring is reachable from what pmmFiles
// wrote. Anything but a load balancer reads as cluster only, which is the
// narrowest answer: a hand edit to another type is not reported as exposed.
func readPMMAccess(doc helmReleaseValues) (string, []string) {
	service := doc.Spec.Values.Service
	if service.Type != "LoadBalancer" {
		return PMMAccessCluster, nil
	}
	access := PMMAccessPublic
	if strings.EqualFold(strings.TrimSpace(service.Annotations[oe.AnnotationAWSLoadBalancerScheme]), "internal") {
		access = PMMAccessPrivate
	}

	for _, renderer := range doc.Spec.PostRenderers {
		for _, p := range renderer.Kustomize.Patches {
			if p.Target.Kind != "Service" || p.Target.Name != pmmServiceName {
				continue
			}
			var ops []struct {
				Path  string   `json:"path"`
				Value []string `json:"value"`
			}
			if err := yaml.Unmarshal([]byte(p.Patch), &ops); err != nil {
				continue
			}
			for _, op := range ops {
				if op.Path == "/spec/loadBalancerSourceRanges" {
					return access, op.Value
				}
			}
		}
	}

	return access, nil
}
