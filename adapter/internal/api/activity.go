package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
)

// activityResponse is one answer to GET /api/activity.
//
// Cursor is the newest id in the whole ring, not in this page. A poll hands it
// back as `since` and gets only what arrived after it, so a live feed costs
// one empty response per interval while nothing is happening.
type activityResponse struct {
	Entries   []activityEntryResponse `json:"entries"`
	Cursor    string                  `json:"cursor"`
	Truncated bool                    `json:"truncated"`
}

// activityDetailResponse is one answer to GET /api/activity/:id.
//
// The recorded event says what was asked for. Resource and timeline are read
// live, so a detail can contradict the row it was opened from: the request
// succeeded and the operation it started did not.
type activityDetailResponse struct {
	Event     activityEntryResponse     `json:"event"`
	Resource  *activityResourceResponse `json:"resource,omitempty"`
	Timeline  []timelineEntryResponse   `json:"timeline"`
	Notes     []string                  `json:"notes,omitempty"`
	Truncated bool                      `json:"truncated"`
}

// activityResourceResponse is the live state of the object the action made.
type activityResourceResponse struct {
	Kind      string     `json:"kind"`
	Name      string     `json:"name"`
	Namespace string     `json:"namespace,omitempty"`
	State     string     `json:"state"`
	Message   string     `json:"message,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	SizeBytes int64      `json:"sizeBytes,omitempty"`
}

// timelineEntryResponse is one line of what happened.
type timelineEntryResponse struct {
	At      time.Time `json:"at"`
	Source  string    `json:"source"`
	Type    string    `json:"type"`
	Reason  string    `json:"reason,omitempty"`
	Message string    `json:"message,omitempty"`
	Count   int32     `json:"count,omitempty"`
}

// activityEntryResponse is one recorded mutation.
type activityEntryResponse struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	Actor      string    `json:"actor"`
	Client     string    `json:"client,omitempty"`
	Agent      string    `json:"agent,omitempty"`
	RequestID  string    `json:"requestId,omitempty"`
	Action     string    `json:"action"`
	DatabaseID string    `json:"databaseId,omitempty"`
	Target     string    `json:"target"`
	Outcome    string    `json:"outcome"`
	Detail     string    `json:"detail,omitempty"`
	Error      string    `json:"error,omitempty"`

	// ResourceKind and ResourceName are absent on rows recorded before the
	// detail view existed, and on every connection action.
	ResourceKind string `json:"resourceKind,omitempty"`
	ResourceName string `json:"resourceName,omitempty"`

	// Team is the Team the row belongs to. It is only present on a list across
	// Teams, where nothing else says which one a row is in.
	Team string `json:"team,omitempty"`
}

// handleListActivity returns a page of the activity ring.
func (s *Server) handleListActivity(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	limit, err := parseActivityLimit(query.Get("limit"))
	if err != nil {
		Respond(w, r, nil, err)

		return
	}

	activityQuery := service.ActivityQuery{
		DatabaseID: strings.TrimSpace(query.Get("databaseId")),
		Since:      strings.TrimSpace(query.Get("since")),
		Limit:      limit,
	}

	// Ids come from one sequence, so rows of several Teams merge by id.
	out := activityResponse{Entries: []activityEntryResponse{}}
	appErr := s.eachTeam(r, func(ctx context.Context, team auth.Team, all bool) *AppError {
		page, listErr := s.activity.List(ctx, activityQuery)
		if listErr != nil {
			return activityError(listErr)
		}
		for _, e := range page.Entries {
			row := toActivityEntryResponse(e)
			if all {
				row.Team = team.Name
			}
			out.Entries = append(out.Entries, row)
		}
		out.Truncated = out.Truncated || page.Truncated
		if page.Cursor > out.Cursor {
			out.Cursor = page.Cursor
		}

		return nil
	})
	if appErr != nil {
		Respond(w, r, nil, appErr)

		return
	}

	sort.SliceStable(out.Entries, func(i, j int) bool { return out.Entries[i].ID > out.Entries[j].ID })
	if limit == 0 {
		limit = service.DefaultActivityLimit
	}
	if len(out.Entries) > limit {
		out.Entries = out.Entries[:limit]
		out.Truncated = true
	}

	Respond(w, r, out, nil)
}

// handleGetActivityDetail returns one recorded action with everything the
// platform can still say about what it started.
func (s *Server) handleGetActivityDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := s.activityDetails.Detail(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Respond(w, r, nil, activityError(err))

		return
	}

	Respond(w, r, toActivityDetailResponse(*detail), nil)
}

// parseActivityLimit reads the page size. A limit that is not a number is a
// client that skipped the UI, so it is rejected rather than quietly defaulted.
func parseActivityLimit(raw string) (int, *AppError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}

	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		return 0, Validation("The limit has to be a whole number above zero.")
	}

	return limit, nil
}

// activityError maps a ring failure onto the API error contract. The ring
// lives in a ConfigMap, so a read that fails is an environment problem rather than
// anything the caller did.
func activityError(err error) *AppError {
	if err == nil {
		return nil
	}
	if errors.Is(err, service.ErrActivityEventNotFound) {
		return NotFound("That activity entry is not in the log. The log keeps the most " +
			"recent changes, so an older one may have aged out.").WithCause(err)
	}
	if errors.Is(err, service.ErrActivityUnavailable) {
		return Downstream("The activity log is not available right now. Try again in a moment.", err)
	}

	return Downstream("The activity log could not be read.", err)
}

func toActivityDetailResponse(in service.ActivityDetail) activityDetailResponse {
	out := activityDetailResponse{
		Event:     toActivityEntryResponse(in.Event),
		Timeline:  make([]timelineEntryResponse, 0, len(in.Timeline)),
		Notes:     in.Notes,
		Truncated: in.Truncated,
	}

	if in.Resource != nil {
		out.Resource = &activityResourceResponse{
			Kind:      in.Resource.Kind,
			Name:      in.Resource.Name,
			Namespace: in.Resource.Namespace,
			State:     in.Resource.State,
			Message:   in.Resource.Message,
			StartedAt: optionalTime(in.Resource.StartedAt),
			EndedAt:   optionalTime(in.Resource.EndedAt),
			SizeBytes: in.Resource.SizeBytes,
		}
	}

	for _, entry := range in.Timeline {
		out.Timeline = append(out.Timeline, timelineEntryResponse{
			At:      entry.At.UTC(),
			Source:  entry.Source,
			Type:    entry.Type,
			Reason:  entry.Reason,
			Message: entry.Message,
			Count:   entry.Count,
		})
	}

	return out
}

// optionalTime keeps a stamp the operator never set out of the response, so
// the UI renders nothing rather than the zero year.
func optionalTime(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}

	utc := at.UTC()

	return &utc
}

func toActivityEntryResponse(e service.ActivityEvent) activityEntryResponse {
	return activityEntryResponse{
		ID:           e.ID,
		At:           e.At.UTC(),
		Actor:        e.Actor,
		Client:       e.Client,
		Agent:        e.Agent,
		RequestID:    e.RequestID,
		Action:       e.Action,
		DatabaseID:   e.DatabaseID,
		Target:       e.Target,
		Outcome:      e.Outcome,
		Detail:       e.Detail,
		Error:        e.Error,
		ResourceKind: e.ResourceKind,
		ResourceName: e.ResourceName,
	}
}
