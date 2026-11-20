package agent

import (
	"context"

	"github.com/percona/pds/adapter/internal/hub"
)

// FluxRelease reports a Flux HelmRelease the way the heartbeat carries it. get
// returns the HelmRelease's object, or nil when there is none, which is how a
// connected environment looks. The report says what Helm runs now (the newest
// revision it marked deployed), the version the last upgrade tried when that
// differs, and whether the release is ready, still upgrading, or was rolled
// back after the attempt failed.
func FluxRelease(get func(ctx context.Context) (map[string]any, error)) func(ctx context.Context) (*hub.HeartbeatRelease, error) {
	return func(ctx context.Context) (*hub.HeartbeatRelease, error) {
		object, err := get(ctx)
		if err != nil || object == nil {
			return nil, err
		}
		status, _ := object["status"].(map[string]any)

		version := deployedVersion(status)
		attempted, _ := status["lastAttemptedRevision"].(string)
		if version == "" && attempted == "" {
			return nil, nil
		}

		report := &hub.HeartbeatRelease{Version: version, State: hub.ReleaseUpgrading}
		if attempted != version {
			report.Attempted = attempted
		}
		switch {
		case report.Attempted != "" && version != "" && !conditionHolds(status, "Ready") && conditionHolds(status, "Remediated"):
			report.State = hub.ReleaseRolledBack
		case conditionHolds(status, "Ready") && report.Attempted == "" && observedCurrent(object, status):
			report.State = hub.ReleaseReady
		}

		return report, nil
	}
}

// deployedVersion is the chart version of the newest Helm revision marked
// deployed. A revision being upgraded to, or one that failed, is not it.
func deployedVersion(status map[string]any) string {
	history, _ := status["history"].([]any)
	for _, item := range history {
		entry, _ := item.(map[string]any)
		if entry["status"] == "deployed" {
			version, _ := entry["chartVersion"].(string)

			return version
		}
	}

	return ""
}

func conditionHolds(status map[string]any, kind string) bool {
	conditions, _ := status["conditions"].([]any)
	for _, item := range conditions {
		condition, _ := item.(map[string]any)
		if condition["type"] == kind {
			return condition["status"] == "True"
		}
	}

	return false
}

// observedCurrent is whether Flux has looked at the spec as it stands: a commit
// Flux has not reconciled yet leaves the old outcome on a spec that has changed.
func observedCurrent(object, status map[string]any) bool {
	metadata, _ := object["metadata"].(map[string]any)

	return number(status["observedGeneration"]) >= number(metadata["generation"])
}

func number(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}

	return 0
}
