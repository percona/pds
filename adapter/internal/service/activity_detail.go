package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

// MaxTimelineEntries bounds one detail answer. A provision that spends an
// hour failing to schedule can emit thousands of events; the newest of them
// are the ones that say why.
const MaxTimelineEntries = 200

// Timeline sources. A reader needs to know whether a line came from the
// request or from the object that request started.
const (
	TimelineRequest  = "request"
	TimelineResource = "resource"
)

// Timeline entry types. They match the Kubernetes event types so a warning
// stays a warning all the way to the screen.
const (
	TimelineNormal  = "normal"
	TimelineWarning = "warning"
)

// ActivityDetail is one recorded action plus what the platform can still say
// about what that action started.
//
// The ring records the request. Everything after it, whether a backup ran for
// four minutes and then failed, is read live here, which is why a detail can
// contradict the outcome on the row: the request succeeded and the operation
// did not.
type ActivityDetail struct {
	Event    ActivityEvent
	Resource *ActivityResource
	Timeline []TimelineEntry

	// Notes name a source that could not be read. A missing backup list has
	// to say so rather than look like a quiet operation.
	Notes     []string
	Truncated bool
}

// ActivityResource is the live state of the object one action produced.
type ActivityResource struct {
	Kind      string
	Name      string
	Namespace string
	State     string
	Message   string
	StartedAt time.Time
	EndedAt   time.Time

	// SizeBytes is what the engine reported for a finished backup. Zero
	// everywhere else.
	SizeBytes int64
}

// TimelineEntry is one line of what happened, in the order it happened.
type TimelineEntry struct {
	At      time.Time
	Source  string
	Type    string
	Reason  string
	Message string

	// Count is above one when Kubernetes folded a repeat into one record,
	// which is how a back-off reports thirty attempts as one line.
	Count int32
}

// ActivityRingReader reads one recorded event back out of the ring.
type ActivityRingReader interface {
	Get(ctx context.Context, id string) (ActivityEvent, error)
}

// ActivityDatabaseReader is the slice of the database service a detail needs.
// It is read only on purpose: opening an activity row must never change
// anything.
type ActivityDatabaseReader interface {
	Lookup(ctx context.Context, id string) (*engine.Instance, error)
	Backups(ctx context.Context, id string) ([]engine.Backup, error)
}

// ActivityDetails assembles the detail view. The database reader is optional:
// without one the detail still serves the recorded request.
type ActivityDetails struct {
	ring      ActivityRingReader
	databases ActivityDatabaseReader
}

// NewActivityDetails returns the detail service.
func NewActivityDetails(
	ring ActivityRingReader,
	databases ActivityDatabaseReader,
) *ActivityDetails {
	return &ActivityDetails{ring: ring, databases: databases}
}

// Detail returns everything known about one recorded action.
func (a *ActivityDetails) Detail(ctx context.Context, id string) (*ActivityDetail, error) {
	if a == nil || a.ring == nil {
		return nil, ErrActivityUnavailable
	}

	event, err := a.ring.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	out := &ActivityDetail{Event: event}
	out.Timeline = append(out.Timeline, requestEntry(event))

	// A request that failed created nothing, so there is no object to follow
	// and no Kubernetes event that belongs to it. The recorded error is the
	// whole story.
	if event.Outcome == OutcomeFailed {
		return out, nil
	}

	resource := a.resolveResource(ctx, event, out)
	out.Resource = resource
	out.Timeline = append(out.Timeline, resourceEntries(resource)...)

	sort.SliceStable(out.Timeline, func(i, j int) bool {
		return out.Timeline[i].At.Before(out.Timeline[j].At)
	})

	if len(out.Timeline) > MaxTimelineEntries {
		out.Timeline = out.Timeline[len(out.Timeline)-MaxTimelineEntries:]
		out.Truncated = true
	}

	return out, nil
}

// requestEntry is the line the ring itself supplies: who asked for what, and
// when. It always opens the timeline.
func requestEntry(e ActivityEvent) TimelineEntry {
	entry := TimelineEntry{
		At:      e.At,
		Source:  TimelineRequest,
		Type:    TimelineNormal,
		Reason:  "Requested",
		Message: e.Detail,
	}
	if e.Outcome == OutcomeFailed {
		entry.Type = TimelineWarning
		entry.Reason = "Request failed"
		if e.Error != "" {
			entry.Message = e.Error
		}
	}

	return entry
}

// resolveResource reads the object the action produced. A read that fails
// leaves a note rather than failing the request: the recorded event is still
// worth showing when Everest is down.
func (a *ActivityDetails) resolveResource(
	ctx context.Context,
	e ActivityEvent,
	out *ActivityDetail,
) *ActivityResource {
	if a.databases == nil || e.DatabaseID == "" {
		return nil
	}

	switch e.ResourceKind {
	case ResourceBackup:
		return a.backupResource(ctx, e, out)

	case ResourceRestore:
		// A restore is a second database. Its phase is what can be read back.
		out.Notes = append(out.Notes,
			"The restore itself cannot be read back, so this shows the database it creates.")

		return a.instanceResource(ctx, e, out)

	case ResourceInstance:
		return a.instanceResource(ctx, e, out)

	default:
		// Rows written before actions carried a resource pointer, and every
		// connection action, which touches no Kubernetes object.
		if strings.HasPrefix(e.Action, "database.") {
			return a.instanceResource(ctx, e, out)
		}

		return nil
	}
}

func (a *ActivityDetails) instanceResource(
	ctx context.Context,
	e ActivityEvent,
	out *ActivityDetail,
) *ActivityResource {
	instance, err := a.databases.Lookup(ctx, e.DatabaseID)
	if err != nil {
		// A database that was deleted on purpose is not a failure to report.
		if e.Action == ActionDatabaseDelete && errors.Is(err, engine.ErrNotFound) {
			out.Notes = append(out.Notes, "The database is gone, which is what the delete asked for.")

			return nil
		}

		out.Notes = append(out.Notes, "The database could not be read: "+Sanitize(err.Error()))

		return nil
	}

	return &ActivityResource{
		Kind:      ResourceInstance,
		Name:      instance.Name,
		Namespace: instance.Namespace,
		State:     instance.Status.Phase,
		Message:   instance.Status.Message,
		StartedAt: instance.CreatedAt,
	}
}

func (a *ActivityDetails) backupResource(
	ctx context.Context,
	e ActivityEvent,
	out *ActivityDetail,
) *ActivityResource {
	backups, err := a.databases.Backups(ctx, e.DatabaseID)
	if err != nil {
		out.Notes = append(out.Notes, "The backups could not be read: "+Sanitize(err.Error()))

		return nil
	}

	for _, backup := range backups {
		if backup.ID != e.ResourceName {
			continue
		}

		_, namespace, _, idErr := hub.SplitID(e.DatabaseID)
		if idErr != nil {
			namespace = ""
		}

		return &ActivityResource{
			Kind:      ResourceBackup,
			Name:      backup.ID,
			Namespace: namespace,
			State:     backup.Status,
			Message:   backup.Message,
			StartedAt: backup.CreatedAt,
			EndedAt:   backup.FinishedAt,
			SizeBytes: backup.SizeBytes,
		}
	}

	if e.ResourceName != "" {
		out.Notes = append(out.Notes, "That backup is no longer in the database's backup list.")
	}

	return nil
}

// resourceEntries turns the object's own timestamps into lines. Only stamps
// the operator actually set become lines: an instance reports a phase with no
// time attached, and inventing one would put a guess on a timeline.
func resourceEntries(r *ActivityResource) []TimelineEntry {
	if r == nil {
		return nil
	}

	var out []TimelineEntry
	if !r.StartedAt.IsZero() && r.Kind != ResourceInstance {
		out = append(out, TimelineEntry{
			At:      r.StartedAt,
			Source:  TimelineResource,
			Type:    TimelineNormal,
			Reason:  "Started",
			Message: r.Name,
		})
	}

	if !r.EndedAt.IsZero() {
		entry := TimelineEntry{
			At:      r.EndedAt,
			Source:  TimelineResource,
			Type:    TimelineNormal,
			Reason:  "Finished",
			Message: r.Message,
		}
		if r.State == engine.BackupFailed || r.State == engine.RestoreFailed {
			entry.Type = TimelineWarning
			entry.Reason = "Failed"
		}
		out = append(out, entry)
	}

	return out
}
