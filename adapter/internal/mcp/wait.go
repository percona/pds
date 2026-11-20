package mcp

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

const (
	defaultWaitSeconds = 30
	maxWaitSeconds     = 45
	// pollInterval is how often a wait asks the control plane, which answers
	// for every environment of a deployment.
	pollInterval = 5 * time.Second
	// minPoll is how long the poll at the deadline still has, so that it can
	// answer rather than be cut off before it is sent.
	minPoll = time.Second
)

const (
	outcomeSucceeded  = "succeeded"
	outcomeFailed     = "failed"
	outcomeInProgress = "in_progress"
	outcomeUnknown    = "unknown"
	outcomeConnected  = "connected"
	outcomeNotFound   = "not_found"
	outcomeDeleted    = "deleted"
	outcomeBlocked    = "blocked"
	outcomeLost       = "lost"
	// stateLost is the state of an environment whose infrastructure is gone.
	stateLost      = "lost"
	outcomeWaiting = "waiting"
)

const (
	untilReady   = "ready"
	untilDeleted = "deleted"
)

const (
	msgSizeNotReached = "The database is ready, but its size is not the one you asked for yet."
	msgWaitUnknown    = "The wait ended before the control plane answered. Call get_database to check."
)

// timeoutInput is the one argument every wait takes.
type timeoutInput struct {
	TimeoutSeconds *int `json:"timeoutSeconds,omitempty" jsonschema:"How long to wait, in seconds. Defaults to 30, at most 45."`
}

func (in timeoutInput) timeout() time.Duration {
	seconds := defaultWaitSeconds
	if in.TimeoutSeconds != nil {
		seconds = *in.TimeoutSeconds
	}

	return time.Duration(seconds) * time.Second
}

// expectedSizes are the sizes a scale asked for. A size left out is not checked.
type expectedSizes struct {
	CPU       *float64 `json:"cpu,omitempty"`
	MemoryGB  *float64 `json:"memoryGb,omitempty"`
	StorageGB *float64 `json:"storageGb,omitempty"`
	Members   *int     `json:"members,omitempty"`
}

func (e expectedSizes) matches(d database) bool {
	return (e.CPU == nil || *e.CPU == d.CPU) &&
		(e.MemoryGB == nil || *e.MemoryGB == d.MemoryGB) &&
		(e.StorageGB == nil || *e.StorageGB == d.StorageGB) &&
		(e.Members == nil || *e.Members == d.Members)
}

type databaseWaitInput struct {
	timeoutInput
	ID     string         `json:"id" jsonschema:"A database id, copied exactly from list_databases."`
	Until  string         `json:"until" jsonschema:"Use ready after create_database, scale_database or restore_database, deleted after delete_database."`
	Expect *expectedSizes `json:"expect,omitempty" jsonschema:"The sizes you asked for when waiting on a scale. Ready is not reported until they match."`
}

type databaseWait struct {
	Outcome       string    `json:"outcome" jsonschema:"One of succeeded, failed, in_progress when the wait ran out, or unknown when it cannot be told."`
	ID            string    `json:"id"`
	Status        string    `json:"status,omitempty"`
	Message       string    `json:"message,omitempty" jsonschema:"The reason when something blocks the database."`
	WaitedSeconds int       `json:"waitedSeconds"`
	Database      *database `json:"database,omitempty" jsonschema:"Absent once the database is gone."`
}

type backupWaitInput struct {
	timeoutInput
	ID       string `json:"id" jsonschema:"A database id, copied exactly from list_databases."`
	BackupID string `json:"backupId" jsonschema:"The backupId returned by backup_database or list_backups."`
}

type backupWait struct {
	Outcome       string  `json:"outcome" jsonschema:"One of succeeded, failed, in_progress when the wait ran out, or unknown when it cannot be told."`
	ID            string  `json:"id"`
	BackupID      string  `json:"backupId"`
	Backup        *backup `json:"backup,omitempty" jsonschema:"Absent when the backup never appeared."`
	WaitedSeconds int     `json:"waitedSeconds"`
}

type environmentWaitInput struct {
	timeoutInput
	Name  string `json:"name" jsonschema:"The environment name, as given to create_environment or create_managed_environment."`
	Until string `json:"until,omitempty" jsonschema:"connected, the default, waits for the environment to be ready. deleted waits for a managed environment to be gone after delete_managed_environment."`
}

type environmentWait struct {
	Outcome       string        `json:"outcome" jsonschema:"One of connected, deleted, failed or blocked when a run stopped, lost when the Kubernetes cluster is gone from the AWS account, waiting when a run is paused until the AWS account answers, in_progress when the wait ran out, or not_found."`
	Name          string        `json:"name"`
	Status        string        `json:"status,omitempty"`
	State         string        `json:"state,omitempty"`
	Provisioning  *provisioning `json:"provisioning,omitempty" jsonschema:"How the latest run of a managed environment stands. On failed, tell the user failure.sentence."`
	WaitedSeconds int           `json:"waitedSeconds"`
}

func waitTools() []tool {
	return []tool{
		define(spec{
			name:  "wait_for_database",
			title: "Wait for a database",
			description: "Wait for a database to reach a state, up to timeoutSeconds (default 30, at most 45). " +
				"Use until ready after create_database, scale_database or restore_database, and deleted after delete_database. " +
				"Pass expect with the sizes you asked for when waiting on a scale. " +
				"On in_progress a database can still take several minutes: call again. " +
				"If message names a blocking problem, tell the user.",
			annotations: readOnly(),
			tune: func(input, output *jsonschema.Schema) {
				tuneTimeout(input)
				input.Properties["until"].Enum = []any{untilReady, untilDeleted}
				output.Properties["outcome"].Enum = []any{outcomeSucceeded, outcomeFailed, outcomeInProgress, outcomeUnknown}
			},
		}, waitForDatabase),

		define(spec{
			name:  "wait_for_backup",
			title: "Wait for a backup",
			description: "Wait for a backup to finish, up to timeoutSeconds (default 30, at most 45). " +
				"Returns succeeded, failed, in_progress or unknown. " +
				"A large backup can take many minutes: call again on in_progress.",
			annotations: readOnly(),
			tune: func(input, output *jsonschema.Schema) {
				tuneTimeout(input)
				output.Properties["outcome"].Enum = []any{outcomeSucceeded, outcomeFailed, outcomeInProgress, outcomeUnknown}
			},
		}, waitForBackup),

		define(spec{
			name:  "wait_for_environment",
			title: "Wait for an environment",
			description: "Wait for an environment to connect, up to timeoutSeconds (default 30, at most 45). " +
				"Use it after create_environment, once the user has run the command from the console. " +
				"Returns connected, in_progress or not_found. " +
				"On in_progress, call it again: connecting can take several minutes.",
			annotations: readOnly(),
			tune: func(input, output *jsonschema.Schema) {
				tuneTimeout(input)
				output.Properties["outcome"].Enum = []any{outcomeConnected, outcomeInProgress, outcomeNotFound}
			},
		}, waitForEnvironment),
	}
}

func tuneTimeout(input *jsonschema.Schema) {
	bounds(input.Properties["timeoutSeconds"], 1, maxWaitSeconds)
}

func waitForDatabase(ctx context.Context, c *call, in databaseWaitInput) (databaseWait, error) {
	path, err := databasePath(in.ID)
	if err != nil {
		return databaseWait{}, err
	}

	var (
		last    *database
		note    string
		gone    bool
		outcome string
	)
	waited, done, err := c.pollUntil(ctx, in.timeout(), func(ctx context.Context) (bool, error) {
		var d database
		if err := c.get(ctx, "/api/databases/"+path, nil, &d); err != nil {
			var failure *toolError
			if in.Until == untilDeleted && errors.As(err, &failure) && failure.Kind == kindNotFound {
				gone, outcome = true, outcomeSucceeded

				return true, nil
			}

			return false, err
		}
		last, note = &d, ""

		switch {
		case in.Until == untilDeleted:
			return false, nil
		case d.Status == "failed":
			outcome = outcomeFailed

			return true, nil
		case d.Status != "ready" || d.Connection == nil:
			return false, nil
		case in.Expect != nil && !in.Expect.matches(d):
			note = msgSizeNotReached

			return false, nil
		}
		outcome = outcomeSucceeded

		return true, nil
	})
	if err != nil {
		return databaseWait{}, err
	}

	out := databaseWait{ID: in.ID, WaitedSeconds: seconds(waited)}
	switch {
	case done:
		out.Outcome = outcome
	case last == nil:
		out.Outcome, out.Message = outcomeUnknown, msgWaitUnknown
	default:
		out.Outcome = outcomeInProgress
	}
	// A database that is gone has nothing to show, whatever was last seen.
	if last != nil && !gone {
		out.Status, out.Database = last.Status, last
		out.Message = orDefault(note, last.Message)
	}

	return out, nil
}

func waitForBackup(ctx context.Context, c *call, in backupWaitInput) (backupWait, error) {
	path, err := databasePath(in.ID)
	if err != nil {
		return backupWait{}, err
	}

	var (
		found   *backup
		outcome string
	)
	waited, done, err := c.pollUntil(ctx, in.timeout(), func(ctx context.Context) (bool, error) {
		var list backups
		if err := c.get(ctx, "/api/databases/"+path+"/backups", nil, &list); err != nil {
			return false, err
		}
		for i := range list.Backups {
			b := list.Backups[i]
			if b.ID != in.BackupID {
				continue
			}
			found = &b

			switch b.Status {
			case "succeeded":
				outcome = outcomeSucceeded
			case "failed":
				outcome = outcomeFailed
			default:
				return false, nil
			}

			return true, nil
		}

		return false, nil
	})
	if err != nil {
		return backupWait{}, err
	}

	out := backupWait{ID: in.ID, BackupID: in.BackupID, Backup: found, WaitedSeconds: seconds(waited)}
	switch {
	case done:
		out.Outcome = outcome
	case found == nil:
		out.Outcome = outcomeUnknown
	default:
		out.Outcome = outcomeInProgress
	}

	return out, nil
}

func waitForEnvironment(ctx context.Context, c *call, in environmentWaitInput) (environmentWait, error) {
	deleting := in.Until == untilDeleted
	var found *environment
	waited, done, err := c.pollUntil(ctx, in.timeout(), func(ctx context.Context) (bool, error) {
		var list environments
		if err := c.get(ctx, "/api/environments", nil, &list); err != nil {
			return false, err
		}
		found = nil
		for i := range list.Environments {
			if list.Environments[i].Name == in.Name {
				found = &list.Environments[i]

				break
			}
		}
		if found == nil {
			return deleting, nil
		}
		progress := ""
		if found.Provisioning != nil {
			progress = found.Provisioning.Progress
		}
		if deleting {
			return progress == outcomeBlocked, nil
		}

		return found.Status == "connected" && progress != "in_progress" || progress == "failed" || progress == "blocked" || found.State == stateLost, nil
	})
	if err != nil {
		return environmentWait{}, err
	}

	out := environmentWait{Name: in.Name, WaitedSeconds: seconds(waited)}
	if found != nil {
		out.Status, out.State, out.Provisioning = found.Status, found.State, found.Provisioning
	}
	progress := ""
	if out.Provisioning != nil {
		progress = out.Provisioning.Progress
	}
	switch {
	case deleting && done && found == nil:
		out.Outcome = outcomeDeleted
	case done && (progress == outcomeFailed || progress == outcomeBlocked):
		out.Outcome = progress
	case done && out.State == stateLost:
		out.Outcome = outcomeLost
	case done:
		out.Outcome = outcomeConnected
	case found == nil:
		out.Outcome = outcomeNotFound
	case progress == outcomeWaiting:
		out.Outcome = outcomeWaiting
	default:
		out.Outcome = outcomeInProgress
	}

	return out, nil
}

func seconds(d time.Duration) int { return int(math.Round(d.Seconds())) }

// pollUntil asks again every pollInterval until a poll says it is done or the
// timeout passes. It answers how long it waited and whether a poll was done.
//
// Each poll is cut off at the deadline, so one slow poll cannot run past it,
// and a poll that is cut off ends the wait quietly: the caller reports the last
// thing it saw. Any other failure, including the caller leaving mid-poll, is
// returned as it is.
func (c *call) pollUntil(ctx context.Context, timeout time.Duration, poll func(ctx context.Context) (bool, error)) (time.Duration, bool, error) {
	clock := c.registry.clock
	start := clock.Now()
	deadline := start.Add(timeout)
	waited := func() time.Duration { return clock.Now().Sub(start) }

	for {
		pollCtx, cancel := clock.WithTimeout(ctx, max(deadline.Sub(clock.Now()), minPoll))
		done, err := poll(pollCtx)
		cutOff := pollCtx.Err() != nil && ctx.Err() == nil
		cancel()

		switch {
		case err != nil && cutOff:
			return waited(), false, nil
		case err != nil:
			return waited(), false, err
		case done:
			return waited(), true, nil
		}

		left := deadline.Sub(clock.Now())
		if left <= 0 || ctx.Err() != nil {
			return waited(), false, nil
		}
		clock.Sleep(ctx, min(pollInterval, left))
		if ctx.Err() != nil {
			return waited(), false, nil
		}
	}
}
