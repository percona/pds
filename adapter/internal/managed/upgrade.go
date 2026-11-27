package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/provision"
)

// KindUpgradePDS is the run kind that moves an environment's pds release to a
// newer chart version through Git and Flux.
const KindUpgradePDS = "upgrade_pds"

const stepApplyRelease = "apply-release"

// UpgradeStepNames lists the steps of an upgrade_pds run.
var UpgradeStepNames = []string{stepApplyRelease}

// UpgradeParams are the parameters of an upgrade_pds run.
type UpgradeParams struct {
	// From is the version the environment runs, and Version the one it moves to.
	From    string `json:"from"`
	Version string `json:"version"`
}

const (
	notUpgrading       = "The upgrade names no version to move to. Contact Percona support."
	upgradeUnfinished  = "The upgrade has not finished. Retry to keep waiting."
	rolledBackSentence = "Upgrade failed, rolled back to %s. PDS skips this version until you retry or a newer one is published."
)

// UpgradeKind is the run that upgrades an environment's chart. It commits the
// version, then waits for the environment to report the outcome. No AWS role is
// involved: Flux applies the version, and a failed one is rolled back by Flux
// after a single attempt.
func UpgradeKind(d Deps) provision.Kind {
	d.Settings = d.Settings.withDefaults()

	return d.kind(KindUpgradePDS, UpgradeStepNames, map[string]stepFunc{stepApplyRelease: d.applyRelease})
}

func (d Deps) applyRelease(ctx context.Context, env provision.Env, s scope) error {
	var params UpgradeParams
	if err := json.Unmarshal([]byte(env.Run.Params), &params); err != nil || !release.MatchString(params.Version) || !release.MatchString(params.From) {
		return provision.Permanent(fmt.Errorf("the run parameters %q are not an upgrade", env.Run.Params), notUpgrading)
	}

	if err := d.commitRelease(ctx, s, params.Version); err != nil {
		return err
	}
	// A report received before the commit describes an earlier attempt.
	committed := time.Now()

	deadline := committed.Add(d.Settings.ReleaseWait)
	for {
		reported, known, err := d.Hub.Release(ctx, s.rec.TeamID, s.rec.Name)
		if err != nil {
			return provision.Retryable(err, controlPlaneSentence)
		}
		if known && reported.At.After(committed) {
			switch {
			case reported.State == hub.ReleaseReady && reported.Version == params.Version:
				return d.upgraded(ctx, s, params)
			case reported.State == hub.ReleaseRolledBack && reported.Attempted == params.Version:
				return d.rolledBack(ctx, s, params)
			}
		}
		if time.Now().After(deadline) {
			return provision.Permanent(errors.New("the environment did not report the upgrade before the deadline"), upgradeUnfinished)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.Settings.PollEvery):
		}
	}
}

func (d Deps) upgraded(ctx context.Context, s scope, params UpgradeParams) error {
	if err := d.Records.SetChartVersion(ctx, s.rec.TeamID, s.rec.Name, params.Version); err != nil {
		return provision.Retryable(err, controlPlaneSentence)
	}
	if err := d.Records.SetSkippedVersion(ctx, s.rec.TeamID, s.rec.Name, ""); err != nil {
		return provision.Retryable(err, controlPlaneSentence)
	}

	return nil
}

// rolledBack puts Git back to the version Flux restored, so what Git says is
// what runs, and remembers the version that failed.
func (d Deps) rolledBack(ctx context.Context, s scope, params UpgradeParams) error {
	if err := d.commitRelease(ctx, s, params.From); err != nil {
		return err
	}
	if err := d.Records.SetSkippedVersion(ctx, s.rec.TeamID, s.rec.Name, params.Version); err != nil {
		return provision.Retryable(err, controlPlaneSentence)
	}

	return provision.Permanent(fmt.Errorf("flux rolled %s back to %s", params.Version, params.From), fmt.Sprintf(rolledBackSentence, params.From))
}
