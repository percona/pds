package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/service"
)

func (s *Server) book() service.HistoryBook {
	if s == nil || s.activity == nil {
		return nil
	}

	return s.activity.Book()
}

func (s *Server) handleDatabaseHistory(w http.ResponseWriter, r *http.Request) {
	book := s.book()
	if book == nil {
		Respond(w, r, nil, NotImplemented("History is not available."))

		return
	}
	id := databaseID(r)
	if err := s.requireLiveDatabase(r, id); err != nil {
		Respond(w, r, nil, err)

		return
	}
	q, qErr := historyQuery(r, id)
	if qErr != nil {
		Respond(w, r, nil, qErr)

		return
	}
	page, err := book.ListEntries(r.Context(), q)
	if err != nil {
		Respond(w, r, nil, activityError(err))

		return
	}
	Respond(w, r, page, nil)
}

func (s *Server) handleDatabaseUsage(w http.ResponseWriter, r *http.Request) {
	book := s.book()
	if book == nil {
		Respond(w, r, nil, NotImplemented("History is not available."))

		return
	}
	id := databaseID(r)
	if err := s.requireLiveDatabase(r, id); err != nil {
		Respond(w, r, nil, err)

		return
	}
	since, until, qErr := historyWindow(r)
	if qErr != nil {
		Respond(w, r, nil, qErr)

		return
	}
	limit, limErr := parseActivityLimit(r.URL.Query().Get("limit"))
	if limErr != nil {
		Respond(w, r, nil, limErr)

		return
	}
	points, err := book.ListUsage(r.Context(), service.UsageQuery{
		DatabaseID: id, Since: since, Until: until, Limit: limit,
	})
	if err != nil {
		Respond(w, r, nil, activityError(err))

		return
	}
	if points == nil {
		points = []service.UsagePoint{}
	}
	Respond(w, r, struct {
		Samples []service.UsagePoint `json:"samples"`
	}{Samples: points}, nil)
}

func (s *Server) handleEnvironmentHistory(w http.ResponseWriter, r *http.Request) {
	book := s.book()
	if book == nil {
		Respond(w, r, nil, NotImplemented("History is not available."))

		return
	}
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if !hub.ValidEnvironmentName(name) {
		Respond(w, r, nil, Validation("An environment name must be lowercase letters, numbers, and dashes."))

		return
	}
	runs, err := book.ListJoins(r.Context(), name)
	if err != nil {
		Respond(w, r, nil, activityError(err))

		return
	}
	if runs == nil {
		runs = []service.JoinRun{}
	}
	// What was done to the environment, as the activity log recorded it.
	page, err := book.ListEntries(r.Context(), service.HistoryQuery{Environment: name, Kind: service.HistoryAction})
	if err != nil {
		Respond(w, r, nil, activityError(err))

		return
	}
	if page.Entries == nil {
		page.Entries = []service.HistoryEntry{}
	}
	Respond(w, r, struct {
		Runs    []service.JoinRun      `json:"runs"`
		Entries []service.HistoryEntry `json:"entries"`
	}{Runs: runs, Entries: page.Entries}, nil)
}

type joinReport struct {
	RunID      string     `json:"runId"`
	Phase      string     `json:"phase"`
	Outcome    string     `json:"outcome"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	DurationMS int64      `json:"durationMs"`
	Output     string     `json:"output"`
}

func (s *Server) handleJoinPhase(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if !hub.ValidEnvironmentName(name) {
		Respond(w, r, nil, Validation("An environment name must be lowercase letters, numbers, and dashes."))

		return
	}
	r, bindErr := s.bindAgentIdentity(r)
	if bindErr != nil {
		Respond(w, r, nil, bindErr)

		return
	}
	if err := s.authorizeJoinOrAgent(r, name); err != nil {
		Respond(w, r, nil, err)

		return
	}
	book := s.book()
	if book == nil {
		Respond(w, r, nil, NotImplemented("History is not available."))

		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxRequestBody)+1))
	if err != nil {
		Respond(w, r, nil, Validation("The request body could not be read.").WithCause(err))

		return
	}
	var report joinReport
	if err := json.Unmarshal(body, &report); err != nil {
		Respond(w, r, nil, Validation("The request body is not valid JSON.").WithCause(err))

		return
	}
	if strings.TrimSpace(report.Phase) == "" {
		Respond(w, r, nil, Validation("A phase name is required."))

		return
	}
	if strings.TrimSpace(report.RunID) == "" {
		Respond(w, r, nil, Validation("A run id is required."))

		return
	}
	step := service.JoinStep{
		Environment: name, RunID: report.RunID, Phase: report.Phase, Outcome: report.Outcome,
		DurationMS: report.DurationMS, Output: report.Output,
	}
	if report.StartedAt != nil {
		step.StartedAt = *report.StartedAt
	}
	if report.FinishedAt != nil {
		step.FinishedAt = *report.FinishedAt
	}
	if err := book.RecordPhase(r.Context(), step); err != nil {
		Respond(w, r, nil, activityError(err))

		return
	}
	RespondWithStatus(w, r, http.StatusNoContent, nil, nil)
}

func (s *Server) authorizeJoinOrAgent(r *http.Request, name string) *AppError {
	if err := s.authorizeAgent(r, name); err == nil {
		return nil
	}
	hubSvc, err := s.hub(r)
	if err != nil {
		return agentHubError(s.cfg, r, err)
	}
	got, previewErr := hubSvc.PreviewJoin(r.Context(), bearerToken(r))
	if previewErr != nil || got != name {
		return Unauthorized("That agent token is not valid.")
	}

	return nil
}

func (s *Server) requireLiveDatabase(r *http.Request, id string) *AppError {
	if s.databases == nil {
		return NotImplemented("Databases are not available.")
	}
	if _, err := s.databases.Get(r.Context(), id); err != nil {
		return databaseError(err, id)
	}

	return nil
}

func (s *Server) observeHeartbeat(ctx context.Context, beat hub.Heartbeat, prev []engine.Instance) error {
	book := s.book()
	if book == nil {
		return nil
	}
	instances, backups, events, usage := normalizeHeartbeat(beat)
	if err := book.Observe(ctx, beat.Environment, prev, instances, backups, nil, events, usage); err != nil {
		return err
	}
	s.pruneUsage(ctx, book)

	return nil
}

// pruneEvery is the floor between two usage prunes. Heartbeats arrive every
// few seconds from every environment, and a delete behind each one would be most
// of the database's work for rows that age out once a week.
const pruneEvery = time.Hour

// pruneGate spaces prunes pruneEvery apart for each Team. A prune deletes
// only the rows of the Team the heartbeat belongs to, so one gate for the
// whole server would let a busy Team hold back everyone else's retention.
type pruneGate struct {
	mu   sync.Mutex
	last map[uint]time.Time
}

// due reports whether the Team's prune should run now, and if so records it.
func (g *pruneGate) due(team uint, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if last, ok := g.last[team]; ok && now.Sub(last) < pruneEvery {
		return false
	}
	if g.last == nil {
		g.last = map[uint]time.Time{}
	}
	g.last[team] = now

	return true
}

// pruneTimeout bounds one prune. The first one after an upgrade may have a
// long backlog to delete.
const pruneTimeout = 2 * time.Minute

// pruneUsage drops samples past the retention, at most once per pruneEvery,
// beside the beat rather than inside it, so the agent's answer never waits on
// a delete. A failed prune is logged and tried again pruneEvery later; the
// beat itself has already been recorded.
func (s *Server) pruneUsage(ctx context.Context, book service.HistoryBook) {
	now := time.Now()
	team, _ := auth.TeamFromContext(ctx)
	if !s.usagePrune.due(team, now) {
		return
	}
	// The request context carries the Team the rows belong to, and ends
	// with the request, so the prune keeps its values and not its deadline.
	pruneCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pruneTimeout)
	go func() {
		defer cancel()
		if _, err := book.PruneUsage(pruneCtx, now.Add(-service.UsageRetention)); err != nil {
			s.logger.WarnContext(pruneCtx, "usage prune failed", slog.String("cause", err.Error()))
		}
	}()
}

// normalizeHeartbeat rewrites spoke-local names onto the hub id the inventory
// stores, environment/namespace.name. Events, samples, and backups keep the same
// id, so a later read of that database finds the rows the beat just wrote.
func normalizeHeartbeat(beat hub.Heartbeat) (
	[]engine.Instance,
	[]engine.Backup,
	[]service.PodEvent,
	[]service.UsagePoint,
) {
	ns := strings.TrimSpace(beat.Namespace)
	ids := map[string]string{}
	instances := make([]engine.Instance, 0, len(beat.Instances))
	for _, inst := range beat.Instances {
		name := strings.TrimSpace(inst.Name)
		instNS := strings.TrimSpace(inst.Namespace)
		if instNS == "" {
			instNS = ns
		}
		if name == "" || instNS == "" || !hub.ValidEnvironmentName(beat.Environment) {
			continue
		}
		id := hub.JoinID(beat.Environment, instNS, name)
		if inst.ID != "" {
			ids[inst.ID] = id
		}
		ids[instNS+"."+name] = id
		ids[name] = id
		inst.ID = id
		inst.Namespace = instNS
		inst.Environment = beat.Environment
		instances = append(instances, inst)
	}
	lookup := func(raw string) (string, bool) {
		id, ok := ids[strings.TrimSpace(raw)]
		return id, ok
	}
	backups := make([]engine.Backup, 0, len(beat.Backups))
	// A beat that omitted the instance list is carrying the last backup
	// cache, whose names are not hub ids. Skip them rather than store rows
	// the history route cannot find. An id this beat does not list is dropped
	// so a spoke cannot attach history to another database.
	if !beat.OmitInstances && !beat.OmitBackups {
		for _, backup := range beat.Backups {
			id, ok := lookup(backup.Instance)
			if !ok {
				continue
			}
			backup.Instance = id
			backups = append(backups, backup)
		}
	}
	events := make([]service.PodEvent, 0, len(beat.Events))
	for _, event := range beat.Events {
		id, ok := lookup(event.DatabaseID)
		if !ok {
			continue
		}
		events = append(events, service.PodEvent{
			DatabaseID: id, At: event.At, Type: event.Type,
			Reason: event.Reason, Message: event.Message, Count: event.Count,
		})
	}
	usage := make([]service.UsagePoint, 0, len(beat.Usage))
	for _, point := range beat.Usage {
		id, ok := lookup(point.DatabaseID)
		if !ok {
			continue
		}
		usage = append(usage, service.UsagePoint{
			DatabaseID: id, At: point.At,
			CPUAvg: point.CPUAvg, CPUMax: point.CPUMax,
			MemoryAvg: point.MemoryAvg, MemoryMax: point.MemoryMax,
			StorageBytes: point.StorageBytes, MetricsReason: point.MetricsReason,
			Monitoring: monitoringReading(point.Monitoring),
		})
	}
	return instances, backups, events, usage
}

func monitoringReading(in *hub.HeartbeatMonitoring) *service.MonitoringReading {
	if in == nil {
		return nil
	}
	reading := service.MonitoringReading(*in)

	return &reading
}

func historyQuery(r *http.Request, id string) (service.HistoryQuery, *AppError) {
	since, until, err := historyWindow(r)
	if err != nil {
		return service.HistoryQuery{}, err
	}
	limit, limErr := parseActivityLimit(r.URL.Query().Get("limit"))
	if limErr != nil {
		return service.HistoryQuery{}, limErr
	}

	return service.HistoryQuery{
		DatabaseID: id,
		Kind:       strings.TrimSpace(r.URL.Query().Get("kind")),
		Since:      since,
		Until:      until,
		Cursor:     strings.TrimSpace(r.URL.Query().Get("cursor")),
		Limit:      limit,
	}, nil
}

func historyWindow(r *http.Request) (time.Time, time.Time, *AppError) {
	since, err := parseHistoryTime(r.URL.Query().Get("since"))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	until, err := parseHistoryTime(r.URL.Query().Get("until"))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}

	return since, until, nil
}

func parseHistoryTime(raw string) (time.Time, *AppError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, Validation("since and until must be RFC3339 timestamps.")
	}

	return parsed, nil
}
