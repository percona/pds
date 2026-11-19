package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Run statuses. A run is unfinished while it is queued, running, paused or
// failed: a failed run still holds its environment until Retry or Delete.
const (
	RunQueued    = "queued"
	RunRunning   = "running"
	RunPaused    = "paused"
	RunFailed    = "failed"
	RunSucceeded = "succeeded"
	RunCancelled = "cancelled"
)

// Step statuses.
const (
	StepPending   = "pending"
	StepRunning   = "running"
	StepSucceeded = "succeeded"
	StepFailed    = "failed"
)

// KindDelete is the run kind that may start beside an unfinished run of its
// environment, and that preempts it.
const KindDelete = "delete"

const unfinished = "status IN ('queued','running','paused','failed')"

var (
	// ErrRunActive means the environment already has an unfinished run, or the
	// run is not in a state the call can act on.
	ErrRunActive = errors.New("the environment already has an unfinished provisioning run")
	// ErrNoFailedRun means Retry found no failed run to re-run.
	ErrNoFailedRun = errors.New("the environment has no failed provisioning run")
	// ErrRunNotFound means no run has that id.
	ErrRunNotFound = errors.New("provisioning run not found")
	// ErrLeaseLost means the caller no longer holds the run: its lease lapsed
	// and another worker took it, or the run was cancelled under it.
	ErrLeaseLost = errors.New("the provisioning run lease is no longer held")
)

// ProvisioningRun is one ordered attempt to create, upgrade, rebuild or delete
// a managed environment. It never holds a credential, token, key or kubeconfig.
type ProvisioningRun struct {
	ID              uint   `gorm:"primaryKey"`
	TeamID          uint   `gorm:"not null;index:idx_provisioning_runs_environment,priority:1"`
	Environment     string `gorm:"size:253;not null;index:idx_provisioning_runs_environment,priority:2"`
	Kind            string `gorm:"size:32;not null"`
	Status          string `gorm:"size:16;not null;index"`
	CancelRequested bool   `gorm:"not null"`
	LeaseOwner      string `gorm:"size:64;not null;default:''"`
	LeaseExpiresAt  *time.Time
	// NotBefore keeps a queued run unclaimed while its step backs off.
	NotBefore time.Time `gorm:"not null"`
	// Message is the safe sentence a person reads for a failed or paused run.
	Message string `gorm:"size:512;not null;default:''"`
	// WindowOpenedAt is set while the environment's Kubernetes API is reachable
	// from the control plane for this run.
	WindowOpenedAt *time.Time
	WindowNote     string    `gorm:"size:512;not null;default:''"`
	CreatedAt      time.Time `gorm:"not null"`
	UpdatedAt      time.Time `gorm:"not null"`
	FinishedAt     *time.Time
}

// TableName pins the table name.
func (ProvisioningRun) TableName() string { return "provisioning_runs" }

// ProvisioningStep is one step of a run, written before it starts and after it
// ends. The step is the unit of retry.
type ProvisioningStep struct {
	RunID    uint   `gorm:"primaryKey;autoIncrement:false"`
	Position int    `gorm:"primaryKey;autoIncrement:false"`
	Name     string `gorm:"size:64;not null"`
	Status   string `gorm:"size:16;not null"`
	Attempts int    `gorm:"not null"`
	// Failure is the safe sentence. Detail is the raw error, redacted and
	// truncated, for support.
	Failure string `gorm:"size:512;not null;default:''"`
	Detail  string `gorm:"size:1024;not null;default:''"`
	// Refusal types a failure AWS caused and the customer fixes. It is empty
	// for every other failure.
	Refusal    StepRefusal `gorm:"embedded;embeddedPrefix:refusal_"`
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// TableName pins the table name.
func (ProvisioningStep) TableName() string { return "provisioning_steps" }

// StepRefusal is the typed part of a failure a customer fixes in their own
// account: why, which service and quota or action, and where. It holds no raw
// error text.
type StepRefusal struct {
	Reason  string `gorm:"size:24;not null;default:''"`
	Service string `gorm:"size:64;not null;default:''"`
	Item    string `gorm:"size:128;not null;default:''"`
	Region  string `gorm:"size:32;not null;default:''"`
}

// StepEnd is how a step ended and what happens to its run next.
type StepEnd struct {
	StepStatus string
	// RunStatus is empty to keep the run going on the same lease.
	RunStatus string
	Failure   string
	Detail    string
	Refusal   StepRefusal
	Message   string
	NotBefore time.Time
	// Refund gives back the attempt BeginStep counted, for a pause.
	Refund bool
}

// Provisioning is the store of provisioning runs. It spans Teams because one
// worker serves every run; each run carries its Team.
type Provisioning struct{ db *gorm.DB }

// Provisioning returns the provisioning run store.
func (s *Store) Provisioning() *Provisioning { return &Provisioning{db: s.db} }

// ensureProvisioningIndexes makes the invariant the engine relies on: at most
// one unfinished run per environment, and one unfinished delete beside it.
// GORM cannot express a partial index with a list in its tags.
func ensureProvisioningIndexes(db *gorm.DB) error {
	for _, stmt := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_provisioning_runs_one_active ON provisioning_runs (team_id, environment) WHERE ` + unfinished + ` AND kind <> 'delete'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_provisioning_runs_one_delete ON provisioning_runs (team_id, environment) WHERE ` + unfinished + ` AND kind = 'delete'`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("create provisioning index: %w", err)
		}
	}

	return nil
}

// lockEnvironment serializes the writers that decide on an environment's runs.
func lockEnvironment(tx *gorm.DB, team uint, environment string) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", fmt.Sprintf("provisioning/%d/%s", team, environment)).Error
}

// Start queues a run with its steps. Delete is the one kind that may start while
// another run is unfinished: it cancels what is idle and flags what is running.
func (p *Provisioning) Start(ctx context.Context, team uint, environment, kind string, steps []string, now time.Time) (ProvisioningRun, error) {
	run := ProvisioningRun{
		TeamID: team, Environment: environment, Kind: kind, Status: RunQueued,
		NotBefore: now, CreatedAt: now, UpdatedAt: now,
	}

	err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEnvironment(tx, team, environment); err != nil {
			return err
		}
		scope := tx.Model(&ProvisioningRun{}).Where("team_id = ? AND environment = ?", team, environment)

		if kind == KindDelete {
			if err := scope.Session(&gorm.Session{}).Where(unfinished + " AND kind <> 'delete' AND status <> 'running'").
				Updates(map[string]any{"status": RunCancelled, "finished_at": now, "updated_at": now, "lease_owner": ""}).Error; err != nil {
				return err
			}
			if err := scope.Session(&gorm.Session{}).Where("status = 'running' AND kind <> 'delete'").
				Updates(map[string]any{"cancel_requested": true, "updated_at": now}).Error; err != nil {
				return err
			}
		} else {
			var open int64
			if err := scope.Session(&gorm.Session{}).Where(unfinished).Count(&open).Error; err != nil {
				return err
			}
			if open > 0 {
				return ErrRunActive
			}
		}

		if err := tx.Create(&run).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return ErrRunActive
			}

			return err
		}
		rows := make([]ProvisioningStep, len(steps))
		for i, name := range steps {
			rows[i] = ProvisioningStep{RunID: run.ID, Position: i, Name: name, Status: StepPending}
		}
		if len(rows) == 0 {
			return nil
		}

		return tx.Create(&rows).Error
	})
	if err != nil {
		return ProvisioningRun{}, scrub(err)
	}

	return run, nil
}

// Run reads one run.
func (p *Provisioning) Run(ctx context.Context, id uint) (ProvisioningRun, error) {
	var run ProvisioningRun
	err := p.db.WithContext(ctx).Take(&run, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ProvisioningRun{}, ErrRunNotFound
	}
	if err != nil {
		return ProvisioningRun{}, scrub(err)
	}

	return run, nil
}

// Latest reads the newest run of one of the Team's environments, or
// ErrRunNotFound when it has none.
func (p *Provisioning) Latest(ctx context.Context, team uint, environment string) (ProvisioningRun, error) {
	var run ProvisioningRun
	res := p.db.WithContext(ctx).Where("team_id = ? AND environment = ?", team, environment).Order("id DESC").Limit(1).Find(&run)
	if res.Error != nil {
		return ProvisioningRun{}, scrub(res.Error)
	}
	if res.RowsAffected == 0 {
		return ProvisioningRun{}, ErrRunNotFound
	}

	return run, nil
}

// Steps reads a run's steps in order.
func (p *Provisioning) Steps(ctx context.Context, runID uint) ([]ProvisioningStep, error) {
	var rows []ProvisioningStep
	if err := p.db.WithContext(ctx).Where("run_id = ?", runID).Order("position").Find(&rows).Error; err != nil {
		return nil, scrub(err)
	}

	return rows, nil
}

// Claim leases the next run a worker may work on, or returns nil. A run is
// claimable when it is queued past its backoff, or running on a lapsed lease. A
// delete waits while another run of its environment holds a live lease, and
// cancels whatever else of that environment is left when it is claimed.
func (p *Provisioning) Claim(ctx context.Context, owner string, kinds []string, ttl time.Duration, now time.Time) (*ProvisioningRun, error) {
	var claimed *ProvisioningRun

	err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run ProvisioningRun
		res := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("kind IN ?", kinds).
			Where("((status = 'queued' AND not_before <= ?) OR (status = 'running' AND lease_expires_at < ?))", now, now).
			Where(`(kind <> 'delete' OR NOT EXISTS (
				SELECT 1 FROM provisioning_runs o
				WHERE o.team_id = provisioning_runs.team_id AND o.environment = provisioning_runs.environment
				AND o.id <> provisioning_runs.id AND o.status = 'running' AND o.lease_expires_at >= ?))`, now).
			Order("id").Limit(1).Find(&run)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}

		if run.Kind == KindDelete {
			if err := tx.Model(&ProvisioningRun{}).
				Where("team_id = ? AND environment = ? AND id <> ? AND kind <> 'delete' AND "+unfinished, run.TeamID, run.Environment, run.ID).
				Updates(map[string]any{"status": RunCancelled, "finished_at": now, "updated_at": now, "lease_owner": ""}).Error; err != nil {
				return err
			}
		}

		expires := now.Add(ttl)
		run.Status, run.LeaseOwner, run.LeaseExpiresAt, run.UpdatedAt = RunRunning, owner, &expires, now
		if err := tx.Model(&ProvisioningRun{}).Where("id = ?", run.ID).
			Updates(map[string]any{"status": run.Status, "lease_owner": owner, "lease_expires_at": expires, "updated_at": now}).Error; err != nil {
			return err
		}
		claimed = &run

		return nil
	})
	if err != nil {
		return nil, scrub(err)
	}

	return claimed, nil
}

// Renew extends the lease and reports whether a cancel was requested. It
// returns ErrLeaseLost when the caller no longer holds the run.
func (p *Provisioning) Renew(ctx context.Context, runID uint, owner string, ttl time.Duration, now time.Time) (bool, error) {
	var cancel bool
	err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := owned(tx, runID, owner)
		if err != nil {
			return err
		}
		cancel = run.CancelRequested

		return tx.Model(&ProvisioningRun{}).Where("id = ?", runID).
			Updates(map[string]any{"lease_expires_at": now.Add(ttl), "updated_at": now}).Error
	})

	return cancel, scrub(err)
}

// owned locks the run row and checks the caller holds it.
func owned(tx *gorm.DB, runID uint, owner string) (ProvisioningRun, error) {
	var run ProvisioningRun
	res := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND lease_owner = ? AND status = 'running'", runID, owner).Limit(1).Find(&run)
	if res.Error != nil {
		return run, res.Error
	}
	if res.RowsAffected == 0 {
		return run, ErrLeaseLost
	}

	return run, nil
}

// BeginStep marks a step running and counts the attempt, before it starts.
func (p *Provisioning) BeginStep(ctx context.Context, runID uint, owner string, position int, now time.Time) error {
	return scrub(p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := owned(tx, runID, owner); err != nil {
			return err
		}

		return tx.Model(&ProvisioningStep{}).Where("run_id = ? AND position = ?", runID, position).
			Updates(map[string]any{"status": StepRunning, "attempts": gorm.Expr("attempts + 1"), "started_at": now, "finished_at": nil}).Error
	}))
}

// EndStep records how a step ended, after it ends, and moves the run when the
// outcome says so.
func (p *Provisioning) EndStep(ctx context.Context, runID uint, owner string, position int, end StepEnd, now time.Time) error {
	return scrub(p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := owned(tx, runID, owner); err != nil {
			return err
		}

		step := map[string]any{
			"status": end.StepStatus, "failure": end.Failure, "detail": end.Detail, "finished_at": now,
			"refusal_reason": end.Refusal.Reason, "refusal_service": end.Refusal.Service,
			"refusal_item": end.Refusal.Item, "refusal_region": end.Refusal.Region,
		}
		if end.Refund {
			step["attempts"] = gorm.Expr("GREATEST(attempts - 1, 0)")
		}
		if err := tx.Model(&ProvisioningStep{}).Where("run_id = ? AND position = ?", runID, position).Updates(step).Error; err != nil {
			return err
		}
		if end.RunStatus == "" {
			return nil
		}

		return setRunStatus(tx, runID, end.RunStatus, end.Message, end.NotBefore, now)
	}))
}

// EndRun ends a run the caller holds as succeeded or cancelled.
func (p *Provisioning) EndRun(ctx context.Context, runID uint, owner, status string, now time.Time) error {
	return scrub(p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := owned(tx, runID, owner); err != nil {
			return err
		}

		return setRunStatus(tx, runID, status, "", now, now)
	}))
}

func setRunStatus(tx *gorm.DB, runID uint, status, message string, notBefore, now time.Time) error {
	set := map[string]any{"status": status, "message": message, "lease_owner": "", "lease_expires_at": nil, "updated_at": now}
	if status == RunQueued {
		set["not_before"] = notBefore
	}
	if status == RunSucceeded || status == RunCancelled {
		set["finished_at"] = now
	}

	return tx.Model(&ProvisioningRun{}).Where("id = ?", runID).Updates(set).Error
}

// Retry re-queues the environment's failed run so it re-runs from its failed
// step. A run that is not failed is refused.
func (p *Provisioning) Retry(ctx context.Context, team uint, environment string, now time.Time) error {
	return scrub(p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEnvironment(tx, team, environment); err != nil {
			return err
		}
		var run ProvisioningRun
		res := tx.Where("team_id = ? AND environment = ? AND "+unfinished, team, environment).Order("id DESC").Limit(1).Find(&run)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNoFailedRun
		}
		if run.Status != RunFailed {
			return ErrRunActive
		}

		if err := tx.Model(&ProvisioningStep{}).Where("run_id = ? AND status = ?", run.ID, StepFailed).
			Updates(map[string]any{
				"status": StepPending, "attempts": 0, "failure": "", "detail": "",
				"refusal_reason": "", "refusal_service": "", "refusal_item": "", "refusal_region": "",
			}).Error; err != nil {
			return err
		}

		return tx.Model(&ProvisioningRun{}).Where("id = ?", run.ID).
			Updates(map[string]any{"status": RunQueued, "message": "", "not_before": now, "updated_at": now}).Error
	}))
}

// Paused lists the paused runs of the given kinds.
func (p *Provisioning) Paused(ctx context.Context, kinds []string) ([]ProvisioningRun, error) {
	var runs []ProvisioningRun
	if err := p.db.WithContext(ctx).Where("status = 'paused' AND kind IN ?", kinds).Order("id").Find(&runs).Error; err != nil {
		return nil, scrub(err)
	}

	return runs, nil
}

// Resume re-queues a paused run.
func (p *Provisioning) Resume(ctx context.Context, runID uint, now time.Time) error {
	return scrub(p.db.WithContext(ctx).Model(&ProvisioningRun{}).Where("id = ? AND status = 'paused'", runID).
		Updates(map[string]any{"status": RunQueued, "message": "", "not_before": now, "updated_at": now}).Error)
}

// OpenWindow records that the run opened the environment's Access window.
func (p *Provisioning) OpenWindow(ctx context.Context, runID uint, owner string, now time.Time) error {
	return scrub(p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := owned(tx, runID, owner); err != nil {
			return err
		}

		return tx.Model(&ProvisioningRun{}).Where("id = ?", runID).
			Updates(map[string]any{"window_opened_at": now, "window_note": "", "updated_at": now}).Error
	}))
}

// CloseWindow records that the window is closed and what closed it. It matches
// the opening it saw, so a window a run reopened meanwhile is left alone.
func (p *Provisioning) CloseWindow(ctx context.Context, runID uint, openedAt time.Time, note string, now time.Time) error {
	return scrub(p.db.WithContext(ctx).Model(&ProvisioningRun{}).Where("id = ? AND window_opened_at = ?", runID, openedAt).
		Updates(map[string]any{"window_opened_at": nil, "window_note": note, "updated_at": now}).Error)
}

// StaleWindows lists runs whose Access window should be closed: the run is not
// running, has no live lease, or the window is older than maxAge.
func (p *Provisioning) StaleWindows(ctx context.Context, maxAge time.Duration, now time.Time) ([]ProvisioningRun, error) {
	var runs []ProvisioningRun
	err := p.db.WithContext(ctx).
		Where("window_opened_at IS NOT NULL AND (status <> 'running' OR lease_expires_at < ? OR window_opened_at < ?)", now, now.Add(-maxAge)).
		Order("id").Find(&runs).Error
	if err != nil {
		return nil, scrub(err)
	}

	return runs, nil
}

// PruneRuns deletes the runs and steps of every environment whose delete
// finished before the cutoff. Runs live as long as their environment plus the
// retention.
func (p *Provisioning) PruneRuns(ctx context.Context, cutoff time.Time) (int64, error) {
	var removed int64
	err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		const gone = `SELECT d.team_id, d.environment FROM provisioning_runs d
			WHERE d.kind = 'delete' AND d.status = 'succeeded' AND d.finished_at < ?`
		const ids = `SELECT r.id FROM provisioning_runs r WHERE (r.team_id, r.environment) IN (` + gone + `)`
		if err := tx.Exec("DELETE FROM provisioning_steps WHERE run_id IN ("+ids+")", cutoff).Error; err != nil {
			return err
		}
		res := tx.Exec("DELETE FROM provisioning_runs WHERE id IN ("+ids+")", cutoff)
		removed = res.RowsAffected

		return res.Error
	})
	if err != nil {
		return 0, scrub(err)
	}

	return removed, nil
}
