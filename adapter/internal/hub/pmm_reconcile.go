package hub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"sigs.k8s.io/yaml"
)

// alertRulesRetry is how long a failed reconcile of one environment waits
// before the next beat tries again, so a Git outage is one log line per
// environment rather than one per heartbeat.
const alertRulesRetry = 5 * time.Minute

// alertRulesBound caps one environment's reconcile, which holds the
// environment's lock while it reads and commits to Git.
const alertRulesBound = 30 * time.Second

// AlertRulesReport names the environments each outcome of a reconcile touched.
type AlertRulesReport struct {
	// Updated had its PMM tree rewritten.
	Updated []string
	// Current already held the rules and was left alone.
	Current []string
	// Skipped has no monitoring to give rules to.
	Skipped []string
	// NeedsJoin runs monitoring but its agent has not run a join command that
	// lets the platform Kustomization apply the rules, so nothing was written.
	NeedsJoin []string
	// Failed could not be read or written.
	Failed []string
}

type alertRulesOutcome int

const (
	rulesCurrent alertRulesOutcome = iota
	rulesUpdated
	rulesSkipped
	rulesNeedJoin
)

// alertRulesGate remembers which environments this process has made current,
// and when a failed one may be tried again.
type alertRulesGate struct {
	mu      sync.Mutex
	done    map[string]bool
	retry   map[string]time.Time
	running map[string]bool
}

func newAlertRulesGate() *alertRulesGate {
	return &alertRulesGate{done: map[string]bool{}, retry: map[string]time.Time{}, running: map[string]bool{}}
}

// start claims the environment for a reconcile when one is due and none is
// already running.
func (g *alertRulesGate) start(environment string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done[environment] || g.running[environment] || now.Before(g.retry[environment]) {
		return false
	}
	g.running[environment] = true

	return true
}

// forget drops what the gate knows about an environment, so the next beat
// from an agent with the mark reconciles it again. A tree rendered without the
// rules, or an agent that lost the mark, makes "done" untrue.
func (g *alertRulesGate) forget(environment string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.done, environment)
	delete(g.retry, environment)
}

func (g *alertRulesGate) finish(environment string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.running, environment)
}

func (g *alertRulesGate) settle(environment string, err error, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		g.retry[environment] = now.Add(alertRulesRetry)

		return
	}
	g.done[environment] = true
	delete(g.retry, environment)
}

// ReconcileAlertRules rewrites the PMM tree of every environment that runs
// monitoring and is missing the alert rules, with the writer a join uses and
// the settings the tree already holds. A tree that is current is not written,
// so a second pass makes no commit. One environment failing does not stop the
// next.
func (s *Service) ReconcileAlertRules(ctx context.Context) AlertRulesReport {
	var report AlertRulesReport
	for _, environment := range s.inv.List(s.now()) {
		if ctx.Err() != nil {
			break
		}
		name := environment.Name
		outcome, err := s.reconcileAlertRulesBounded(ctx, name)

		switch {
		case err != nil:
			report.Failed = append(report.Failed, name)
			slog.WarnContext(ctx, "the hub could not add the alert rules to an environment",
				slog.String("environment", name), slog.String("cause", err.Error()))
		case outcome == rulesUpdated:
			report.Updated = append(report.Updated, name)
			slog.InfoContext(ctx, "the hub added the alert rules to an environment", slog.String("environment", name))
		case outcome == rulesSkipped:
			report.Skipped = append(report.Skipped, name)
		case outcome == rulesNeedJoin:
			report.NeedsJoin = append(report.NeedsJoin, name)
			slog.InfoContext(ctx, "an environment gets the alert rules once its join command is run again",
				slog.String("environment", name))
		default:
			report.Current = append(report.Current, name)
		}
	}

	return report
}

// reconcileAlertRulesBounded takes the environment's lock, which
// SetPMMSettings takes too, and reconciles within alertRulesBound. The bound
// starts once the lock is held, so waiting for another call does not spend it.
func (s *Service) reconcileAlertRulesBounded(ctx context.Context, environment string) (alertRulesOutcome, error) {
	unlock := s.spokes.lock(environment)
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, alertRulesBound)
	defer cancel()

	return s.reconcileAlertRulesLocked(ctx, environment)
}

// reconcileAlertRulesOnBeat covers an environment that joined again after the
// hub started: its first beat from the current agent brings the rules. The
// work runs in the background, because the agent gives up on a reply after a
// few seconds and Git can be slower than that. One run per environment at a
// time, and a failure is logged and waits alertRulesRetry for the next try.
func (s *Service) reconcileAlertRulesOnBeat(environment string) {
	if _, granted := s.inv.PMMReport(environment); !granted {
		s.rulesGate.forget(environment)

		return
	}
	if !s.rulesGate.start(environment, s.now()) {
		return
	}
	s.rulesWork.Add(1)
	go func() {
		defer s.rulesWork.Done()
		defer s.rulesGate.finish(environment)

		// The beat's request is over by the time this runs, so the work
		// carries the process's context: shutdown stops it.
		ctx := s.lifetime
		outcome, err := s.reconcileAlertRulesBounded(ctx, environment)
		switch {
		case err != nil:
			slog.WarnContext(ctx, "the hub could not add the alert rules to an environment",
				slog.String("environment", environment), slog.String("cause", err.Error()))
		case outcome == rulesUpdated:
			slog.InfoContext(ctx, "the hub added the alert rules to an environment", slog.String("environment", environment))
		}
	}()
}

// reconcileAlertRulesLocked brings one environment's PMM tree up to date. The
// caller holds the environment's lock.
func (s *Service) reconcileAlertRulesLocked(ctx context.Context, environment string) (alertRulesOutcome, error) {
	monitoring, granted := s.inv.PMMReport(environment)
	if !monitoring {
		return rulesSkipped, nil
	}
	if !granted {
		return rulesNeedJoin, nil
	}
	outcome, err := s.makeAlertRulesCurrent(ctx, environment)
	s.rulesGate.settle(environment, err, s.now())

	return outcome, err
}

func (s *Service) makeAlertRulesCurrent(ctx context.Context, environment string) (alertRulesOutcome, error) {
	release, err := s.store.Get(ctx, pmmDir(environment)+"/helmrelease.yaml")
	if errors.Is(err, ErrNotFound) {
		return rulesSkipped, nil
	}
	if err != nil {
		return rulesCurrent, fmt.Errorf("read the monitoring definition: %w", err)
	}
	settings, err := parsePMMSettings(release)
	if err != nil {
		return rulesCurrent, err
	}
	version, err := pmmChartVersion(release)
	if err != nil {
		return rulesCurrent, err
	}
	want, err := pmmFiles(environment, settings, version, true)
	if err != nil {
		return rulesCurrent, fmt.Errorf("render the monitoring definition: %w", err)
	}

	for path, body := range want {
		have, err := s.store.Get(ctx, path)
		if errors.Is(err, ErrNotFound) || (err == nil && !bytes.Equal(have, body)) {
			if err := s.store.Write(ctx, want, "add alert rules "+environment); err != nil {
				return rulesCurrent, fmt.Errorf("write the monitoring definition: %w", err)
			}

			return rulesUpdated, nil
		}
		if err != nil {
			return rulesCurrent, fmt.Errorf("read %s: %w", path, err)
		}
	}

	return rulesCurrent, nil
}

// pmmChartVersion is the PMM chart a tree already installs. A reconcile keeps
// it, so adding the rules never moves an environment to another PMM.
func pmmChartVersion(release []byte) (string, error) {
	var doc struct {
		Spec struct {
			Chart struct {
				Spec struct {
					Version string `json:"version"`
				} `json:"spec"`
			} `json:"chart"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(release, &doc); err != nil {
		return "", fmt.Errorf("monitoring definition is not readable: %w", err)
	}

	return doc.Spec.Chart.Spec.Version, nil
}
