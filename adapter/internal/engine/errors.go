package engine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Domain errors every engine plugin reports. They live here, not in a plugin,
// so the HTTP layer maps a failure once instead of once per engine.
//
// The mapping onto AppError codes is: the input errors are ErrValidation,
// ErrExists is ErrConflict, ErrNotFound is ErrNotFound, and anything else a
// plugin returns is a downstream failure.
var (
	ErrInvalidName      = errors.New("invalid database name")
	ErrInvalidNamespace = errors.New("invalid namespace")
	// ErrNamespaceNotManaged is a well formed namespace this adapter does not
	// manage. It is separate from ErrInvalidNamespace so the caller is told
	// which namespace works instead of being told to check their spelling.
	ErrNamespaceNotManaged = errors.New("namespace is not managed by this adapter")
	ErrInvalidID           = errors.New("invalid database id")
	ErrUnknownProfile      = errors.New("unknown profile")
	ErrOutOfBounds         = errors.New("resource value out of bounds")
	ErrEmptyScale          = errors.New("scale request changes nothing")
	ErrStorageShrink       = errors.New("storage cannot shrink")
	ErrExists              = errors.New("database already exists")
	ErrNotFound            = errors.New("database not found")
	ErrUnparsableQuantity  = errors.New("unparsable resource quantity")
	ErrBusy                = errors.New("database has an operation running")
	ErrBackupNotFound      = errors.New("backup not found on this database")
	ErrBackupUnusable      = errors.New("backup cannot be restored from")
	// ErrSeedUnavailable means this environment cannot create the database a
	// restore adopts: the agent has no Kubernetes access, or may not touch the
	// operator's objects.
	ErrSeedUnavailable = errors.New("this environment cannot seed a database")
	// ErrRestoreInProgress means a restored database has not finished
	// restoring, so there is nothing to do to it yet.
	ErrRestoreInProgress = errors.New("restore has not finished")
	// ErrBackupsUnavailable means the adapter has no backup storage to write
	// to, so there is nothing for a schedule to run into. That is a deployment
	// state rather than a bad request, so the HTTP layer answers 501.
	ErrBackupsUnavailable = errors.New("backups are not configured")
	// ErrPresetMissing means the InstancePreset the selected profile maps to
	// is not in the cluster. Create stops before it sends an Instance.
	ErrPresetMissing = errors.New("instance preset is missing")
	// ErrPresetIncompatible means the preset exists but does not match this
	// engine (wrong provider, topology, or component types).
	ErrPresetIncompatible = errors.New("instance preset is incompatible")
	// ErrPresetChanged means the preset is installed but its sizing, version,
	// or shape no longer matches what this adapter ships.
	ErrPresetChanged = errors.New("instance preset has changed")
)

// NamespaceError is a create aimed at a namespace this adapter does not
// manage. It carries the namespace that does work, because the alternative is
// telling a caller "no" without telling them where to go instead.
type NamespaceError struct {
	// Wanted is the namespace the request named.
	Wanted string
	// Managed is the namespace this adapter provisions into.
	Managed string
}

// Error implements error. This text is for logs.
func (e *NamespaceError) Error() string {
	return fmt.Sprintf("%s: %q, this adapter manages %q", ErrNamespaceNotManaged, e.Wanted, e.Managed)
}

// Unwrap lets callers match with errors.Is(err, ErrNamespaceNotManaged).
func (e *NamespaceError) Unwrap() error { return ErrNamespaceNotManaged }

// UserMessage is the sentence the customer sees. Their namespace is theirs to
// be told about; what provisions into it is not.
func (e *NamespaceError) UserMessage() string {
	return fmt.Sprintf("Databases can only be created in the `%s` namespace.", e.Managed)
}

// BoundsError is a resource value outside its allowed range. It carries the
// numbers so the HTTP layer can state the range instead of saying "invalid".
type BoundsError struct {
	// Field is the display name of the value, for example "CPU".
	Field string
	// Unit is what the numbers are counted in, for example "cores".
	Unit string
	Value,
	Min,
	Max int
}

// Error implements error. This text is for logs.
func (e *BoundsError) Error() string {
	return fmt.Sprintf("%s: %s is %d %s, want %d to %d %s",
		ErrOutOfBounds, e.Field, e.Value, e.Unit, e.Min, e.Max, e.Unit)
}

// Unwrap lets callers match with errors.Is(err, ErrOutOfBounds).
func (e *BoundsError) Unwrap() error { return ErrOutOfBounds }

// UserMessage is the sentence the developer sees.
func (e *BoundsError) UserMessage() string {
	return fmt.Sprintf("%s must be between %d and %d %s.", e.Field, e.Min, e.Max, e.Unit)
}

// ChoiceError is a value outside the fixed set an engine accepts. It exists
// next to BoundsError because some values are not a range: a Galera cluster
// needs an odd number of members for quorum, so "between 1 and 5" would invite
// a member count the operator cannot run.
type ChoiceError struct {
	// Field is the display name of the value, for example "Members".
	Field string
	Value int
	// Allowed is every value the engine accepts, in the order to state them.
	Allowed []int
}

// Error implements error. This text is for logs.
func (e *ChoiceError) Error() string {
	return fmt.Sprintf("%s: %s is %d, want %s",
		ErrOutOfBounds, e.Field, e.Value, joinNumbers(e.Allowed, "or"))
}

// Unwrap lets callers match with errors.Is(err, ErrOutOfBounds).
func (e *ChoiceError) Unwrap() error { return ErrOutOfBounds }

// UserMessage is the sentence the developer sees.
func (e *ChoiceError) UserMessage() string {
	return fmt.Sprintf("%s must be %s.", e.Field, joinNumbers(e.Allowed, "or"))
}

// BusyError is a request that clashes with an operation already running, which
// is a conflict rather than bad input: the same request works once the running
// operation finishes.
type BusyError struct {
	// Operation is what is already running, for example "backup". An empty
	// value means the platform reported the clash without naming it.
	Operation string
}

// Error implements error. This text is for logs.
func (e *BusyError) Error() string {
	if e.Operation == "" {
		return ErrBusy.Error()
	}

	return fmt.Sprintf("%s: %s", ErrBusy, e.Operation)
}

// Unwrap lets callers match with errors.Is(err, ErrBusy).
func (e *BusyError) Unwrap() error { return ErrBusy }

// UserMessage is the sentence the developer sees.
func (e *BusyError) UserMessage() string {
	if e.Operation == "" {
		return "Another change is running on this database. Scale once it finishes."
	}

	return fmt.Sprintf("A %s is running. Scale once it finishes.", e.Operation)
}

// BackupSourceError is a restore whose source cannot be used: the database has
// no such backup, or the backup has not finished. A restore replaces every row,
// so the source is checked here rather than trusted: a Restore that names a
// backup with no data behind it leaves the database mid restore.
type BackupSourceError struct {
	// BackupID is the backup the request named.
	BackupID string
	// State is the normalized backup state, empty when the database has no
	// backup by that name.
	State string
}

// Error implements error. This text is for logs.
func (e *BackupSourceError) Error() string {
	if e.State == "" {
		return fmt.Sprintf("%s: %s", ErrBackupNotFound, e.BackupID)
	}

	return fmt.Sprintf("%s: %s is %s", ErrBackupUnusable, e.BackupID, e.State)
}

// Unwrap splits the two cases the HTTP layer answers differently: a backup that
// is not there is not found, and one that is not finished is a conflict.
func (e *BackupSourceError) Unwrap() error {
	if e.State == "" {
		return ErrBackupNotFound
	}

	return ErrBackupUnusable
}

// UserMessage is the sentence the developer sees.
func (e *BackupSourceError) UserMessage() string {
	switch e.State {
	case "":
		return "That backup is not on this database."
	case BackupFailed:
		return "That backup failed, so there is nothing to restore from."
	case BackupDeleting:
		return "That backup is being deleted."
	default:
		return "That backup has not finished. Restore once it succeeds."
	}
}

// PresetError is a create that cannot use the selected profile's
// InstancePreset. It is a client error because the same request works once
// the chart is reinstalled; Create never sends an Instance in these cases.
type PresetError struct {
	// Profile is the wire profile name, for example "dev".
	Profile string
	// Name is the InstancePreset object name Create looked up.
	Name string
	// Kind is ErrPresetMissing, ErrPresetIncompatible, or ErrPresetChanged.
	Kind error
}

// Error implements error. This text is for logs.
func (e *PresetError) Error() string {
	if e == nil {
		return ""
	}

	return fmt.Sprintf("%s: profile %q preset %q", e.Kind, e.Profile, e.Name)
}

// Unwrap lets callers match with errors.Is on Kind.
func (e *PresetError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Kind
}

// UserMessage is the sentence the developer sees.
func (e *PresetError) UserMessage() string {
	label := profileLabel(e.Profile)

	switch {
	case errors.Is(e.Kind, ErrPresetMissing):
		return fmt.Sprintf("The %s profile preset is not installed.", label)
	case errors.Is(e.Kind, ErrPresetIncompatible):
		return fmt.Sprintf("The %s profile preset is not compatible with this engine.", label)
	default:
		return fmt.Sprintf("The %s profile preset has changed. Contact Percona support.", label)
	}
}

func profileLabel(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "dev":
		return "Dev"
	case "staging":
		return "Staging"
	case "prod":
		return "Prod"
	default:
		return name
	}
}

// joinNumbers renders a list the way a sentence does: "1, 3, or 5".
func joinNumbers(values []int, conjunction string) string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, strconv.Itoa(v))
	}

	switch len(out) {
	case 0:
		return ""
	case 1:
		return out[0]
	case 2:
		return out[0] + " " + conjunction + " " + out[1]
	default:
		return strings.Join(out[:len(out)-1], ", ") + ", " + conjunction + " " + out[len(out)-1]
	}
}

// RestoreNameTooLongError is a restore target whose name leaves the provider
// no room to build its restore job.
//
// The provider names a restore object after the database, and the operator
// names the job after both, so the label it ends up with is longer than the
// name anyone typed. Kubernetes refuses that label past 63 characters, and the
// failure lands in an operator object minutes later, with the database up and
// empty. This turns it into a refusal at the request, where the name is still
// on screen.
type RestoreNameTooLongError struct {
	// Name is what the request asked for.
	Name string
	// Limit is the longest name that still leaves room.
	Limit int
	// Reason says what the name is built into, when it is not the restore
	// job. Empty means the restore job.
	Reason RestoreNameReason
}

// RestoreNameReason is what a restored copy's name is built into.
type RestoreNameReason string

// RestoreNameMembers is the PostgreSQL case: every member's labels carry the
// copy's name.
const RestoreNameMembers RestoreNameReason = "members"

// Error implements error. This text is for logs.
func (e *RestoreNameTooLongError) Error() string {
	return fmt.Sprintf("%s: %q is longer than %d characters", ErrInvalidName, e.Name, e.Limit)
}

// Unwrap keeps this a name problem for every caller that only asks that much.
func (e *RestoreNameTooLongError) Unwrap() error { return ErrInvalidName }

// UserMessage is what the developer reads. It gives the limit and the reason,
// because a bare limit reads as arbitrary next to the 63 characters every
// other name field takes.
func (e *RestoreNameTooLongError) UserMessage() string {
	if e.Reason == RestoreNameMembers {
		return fmt.Sprintf(
			"Use %d characters or fewer. The database platform builds the labels of every member from this name, and refuses a longer one.",
			e.Limit)
	}

	return fmt.Sprintf(
		"Use %d characters or fewer. The database platform builds its restore job name from this name twice over, and refuses a longer one.",
		e.Limit)
}
