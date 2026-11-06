package engine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The periods a backup schedule can run on.
const (
	PeriodHourly = "hourly"
	PeriodDaily  = "daily"
	PeriodWeekly = "weekly"
)

// Retention bounds. One copy is the least a schedule can usefully keep, and
// thirty keeps a month of dailies without letting a typo fill the bucket.
const (
	MinRetentionCopies     = 1
	MaxRetentionCopies     = 30
	DefaultRetentionCopies = 7
)

// ScheduleName is the name of the one schedule the control plane writes. It
// stays "daily" whatever the period, because every schedule written before the
// period could change carries that name, and keeping it means an edit updates
// that entry in place and its retention keeps counting the copies it made.
const ScheduleName = "daily"

// ErrInvalidSchedule is a schedule request with a value out of range.
var ErrInvalidSchedule = errors.New("invalid backup schedule")

// ScheduleInput is the one backup schedule a database carries. Every time is
// UTC. When Enabled is false the rest is ignored: turning a schedule off keeps
// the settings it had, so turning it back on restores them.
type ScheduleInput struct {
	Enabled bool
	Period  string
	Minute  int
	// Hour is ignored for an hourly schedule.
	Hour int
	// Weekday is 0 for Sunday through 6 for Saturday, and only read for a
	// weekly schedule.
	Weekday         int
	RetentionCopies int
}

// DefaultSchedule is the schedule the Staging and Prod profiles are created
// with, and what an empty schedule request turns on.
func DefaultSchedule() ScheduleInput {
	return ScheduleInput{
		Enabled:         true,
		Period:          PeriodDaily,
		Minute:          0,
		Hour:            2,
		RetentionCopies: DefaultRetentionCopies,
	}
}

// Validate checks an enabled schedule. A disabled one carries no settings to
// check.
func (in ScheduleInput) Validate() error {
	if !in.Enabled {
		return nil
	}

	switch in.Period {
	case PeriodHourly, PeriodDaily, PeriodWeekly:
	default:
		return &ScheduleError{Field: "period", Reason: "must be hourly, daily or weekly"}
	}
	if in.Minute < 0 || in.Minute > 59 {
		return rangeError("minute", 0, 59)
	}
	if in.Period != PeriodHourly && (in.Hour < 0 || in.Hour > 23) {
		return rangeError("hour", 0, 23)
	}
	if in.Period == PeriodWeekly && (in.Weekday < 0 || in.Weekday > 6) {
		return rangeError("weekday", 0, 6)
	}
	if in.RetentionCopies < MinRetentionCopies || in.RetentionCopies > MaxRetentionCopies {
		return rangeError("retentionCopies", MinRetentionCopies, MaxRetentionCopies)
	}

	return nil
}

// Cron renders the schedule in standard five field cron syntax.
func (in ScheduleInput) Cron() string {
	switch in.Period {
	case PeriodHourly:
		return fmt.Sprintf("%d * * * *", in.Minute)
	case PeriodWeekly:
		return fmt.Sprintf("%d %d * * %d", in.Minute, in.Hour, in.Weekday)
	default:
		return fmt.Sprintf("%d %d * * *", in.Minute, in.Hour)
	}
}

// ParseScheduleCron reads back a cron this package writes. Anything else, a
// step, a range, a day of the month, is refused rather than approximated,
// because the caller would then state a schedule the cluster does not run.
func ParseScheduleCron(cron string) (ScheduleInput, bool) {
	fields := strings.Fields(cron)
	if len(fields) != 5 || fields[2] != "*" || fields[3] != "*" {
		return ScheduleInput{}, false
	}

	minute, ok := cronNumber(fields[0], 59)
	if !ok {
		return ScheduleInput{}, false
	}

	if fields[1] == "*" {
		if fields[4] != "*" {
			return ScheduleInput{}, false
		}

		return ScheduleInput{Period: PeriodHourly, Minute: minute}, true
	}

	hour, ok := cronNumber(fields[1], 23)
	if !ok {
		return ScheduleInput{}, false
	}
	if fields[4] == "*" {
		return ScheduleInput{Period: PeriodDaily, Minute: minute, Hour: hour}, true
	}

	weekday, ok := cronNumber(fields[4], 6)
	if !ok {
		return ScheduleInput{}, false
	}

	return ScheduleInput{Period: PeriodWeekly, Minute: minute, Hour: hour, Weekday: weekday}, true
}

func cronNumber(field string, highest int) (int, bool) {
	n, err := strconv.Atoi(field)
	if err != nil || n < 0 || n > highest {
		return 0, false
	}

	return n, true
}

// ScheduleError names the schedule field that is wrong, so the caller can put
// the message next to it.
type ScheduleError struct {
	// Field is the request body's spelling of the field.
	Field  string
	Reason string
}

// Error implements error. This text is for logs.
func (e *ScheduleError) Error() string {
	return fmt.Sprintf("%s: %s %s", ErrInvalidSchedule, e.Field, e.Reason)
}

// Unwrap lets callers match with errors.Is(err, ErrInvalidSchedule).
func (e *ScheduleError) Unwrap() error { return ErrInvalidSchedule }

// UserMessage is the sentence the developer sees.
func (e *ScheduleError) UserMessage() string {
	return fmt.Sprintf("`%s` %s.", e.Field, e.Reason)
}

func rangeError(field string, lowest, highest int) *ScheduleError {
	return &ScheduleError{Field: field, Reason: fmt.Sprintf("must be between %d and %d", lowest, highest)}
}
