package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/store"
)

// Where an environment came from.
const (
	originConnected = "connected"
	originManaged   = "managed"
)

type refusalResponse struct {
	Reason  string `json:"reason"`
	Service string `json:"service,omitempty"`
	Item    string `json:"item,omitempty"`
	Region  string `json:"region,omitempty"`
}

type failureResponse struct {
	// Sentence is the safe sentence a person reads, with what to do next.
	Sentence string           `json:"sentence"`
	Action   string           `json:"action"`
	Refusal  *refusalResponse `json:"refusal,omitempty"`
}

type phaseResponse struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type runResponse struct {
	ID         uint       `json:"id"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// provisioningResponse is how the latest run of a managed environment stands.
type provisioningResponse struct {
	Run      runResponse      `json:"run"`
	Progress string           `json:"progress"`
	Message  string           `json:"message,omitempty"`
	Phases   []phaseResponse  `json:"phases"`
	Failure  *failureResponse `json:"failure,omitempty"`
}

type runStartedResponse struct {
	Run          runResponse          `json:"run"`
	Provisioning provisioningResponse `json:"provisioning"`
}

type managedCreateRequest struct {
	Name   string `json:"name" validate:"required"`
	Region string `json:"region" validate:"required"`
}

type managedCreatedResponse struct {
	Environment environmentResponse `json:"environment"`
	Run         runResponse         `json:"run"`
}

type startRunRequest struct {
	Kind string `json:"kind" validate:"required,oneof=upgrade_pds upgrade_kubernetes rebuild"`
}

type deleteEnvironmentRequest struct {
	Confirm string `json:"confirm"`
	// Scope is empty to delete everything, or pds_only to forget the
	// environment in PDS and leave what is in the AWS account.
	Scope string `json:"scope" validate:"omitempty,oneof=pds_only"`
}

func provisioningView(p managed.Provisioning) provisioningResponse {
	out := provisioningResponse{
		Run: runResponse{
			ID: p.Run.ID, Kind: p.Run.Kind, Status: p.Run.Status, StartedAt: p.Run.StartedAt.UTC(), FinishedAt: p.Run.FinishedAt,
		},
		Progress: p.Progress, Message: p.Message, Phases: make([]phaseResponse, 0, len(p.Phases)),
	}
	for _, phase := range p.Phases {
		out.Phases = append(out.Phases, phaseResponse{Name: phase.Name, Status: phase.Status})
	}
	if p.Failure != nil {
		out.Failure = &failureResponse{Sentence: p.Failure.Sentence, Action: p.Failure.Action}
		if r := p.Failure.Refusal; r != nil {
			out.Failure.Refusal = &refusalResponse{Reason: r.Reason, Service: r.Service, Item: r.Item, Region: r.Region}
		}
	}

	return out
}

func runView(run store.ProvisioningRun) runResponse {
	return runResponse{ID: run.ID, Kind: run.Kind, Status: run.Status, StartedAt: run.CreatedAt.UTC(), FinishedAt: run.FinishedAt}
}

// connectedState is the state of a connected environment: whether it reports.
func connectedState(connected bool) string {
	if connected {
		return managed.StateReady
	}

	return managed.StateDegraded
}

// withManaged adds what only a managed environment has to its row. row is the
// connected environment's row when the environment has reported in, and the
// zero value before then.
func withManaged(row environmentResponse, env managed.Managed, connected bool) environmentResponse {
	row.Name, row.Origin = env.Record.Name, originManaged
	row.ConnectionID, row.Region = env.Record.ConnectionID, env.Record.Region
	if row.Status == "" {
		row.Status = "pending"
	}
	row.State = managed.StateOf(env.Provisioning, connected)
	if env.Provisioning.Run.ID != 0 {
		view := provisioningView(env.Provisioning)
		row.Provisioning = &view
	}

	return row
}

// handleGetEnvironment reads one environment of the Team the request acts in.
func (s *Server) handleGetEnvironment(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	rows, err := s.environmentRows(r.Context())
	if err != nil {
		Respond(w, r, nil, hubUnavailable(err))

		return
	}
	for _, row := range rows {
		if row.Name == name {
			Respond(w, r, row, nil)

			return
		}
	}

	Respond(w, r, nil, NotFound("That environment does not exist."))
}

// handleCreateManagedEnvironment records the environment and queues the run
// that builds it, then answers before anything exists in AWS.
func (s *Server) handleCreateManagedEnvironment(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.scopeOf(w, r)
	if !ok {
		return
	}
	if s.managedCreate == nil || s.managed == nil {
		Respond(w, r, nil, managedError(managed.ErrUnavailable))

		return
	}
	var req managedCreateRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}
	created, err := s.managedCreate.Create(r.Context(), scope, managed.CreateInput{
		ConnectionID: chi.URLParam(r, "id"), Name: strings.TrimSpace(req.Name), Region: strings.TrimSpace(req.Region),
	})
	if err != nil {
		Respond(w, r, nil, managedError(err))

		return
	}
	p, err := s.managed.Provisioning(r.Context(), scope.Team.ID, created.Environment.Name)
	if err != nil {
		Respond(w, r, nil, managedError(err))

		return
	}

	RespondWithStatus(w, r, http.StatusAccepted, managedCreatedResponse{
		Environment: withManaged(environmentResponse{}, managed.Managed{Record: created.Environment, Provisioning: p}, false),
		Run:         runView(created.Run),
	}, nil)
}

// handleStartRun queues an upgrade or a rebuild of a managed environment.
func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.managerScope(w, r)
	if !ok {
		return
	}
	var req startRunRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	run, err := s.managed.Start(r.Context(), scope, name, req.Kind)
	if err != nil {
		Respond(w, r, nil, managedError(err))

		return
	}

	s.respondRunStarted(w, r, scope, name, run)
}

// handleRetryRun re-runs the failed step of the environment's latest run.
func (s *Server) handleRetryRun(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.managerScope(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	id, err := strconv.ParseUint(chi.URLParam(r, "runId"), 10, 64)
	if err != nil {
		Respond(w, r, nil, NotFound("That run does not exist."))

		return
	}
	if err := s.managed.Retry(r.Context(), scope, name, uint(id)); err != nil {
		Respond(w, r, nil, managedError(err))

		return
	}
	run, err := s.managedLatest(r, scope, name)
	if err != nil {
		Respond(w, r, nil, managedError(err))

		return
	}

	s.respondRunStarted(w, r, scope, name, run)
}

// deleteManagedEnvironment deletes a managed environment after its typed name,
// or forgets it in PDS only. It reports false for an environment that is not a
// managed one of the Team, which the caller then treats as a connected one.
func (s *Server) deleteManagedEnvironment(w http.ResponseWriter, r *http.Request, name string) bool {
	scope, ok := auth.ScopeFromContext(r.Context())
	if !ok || s.managed == nil || !s.managed.Has(r.Context(), scope.Team.ID, name) {
		return false
	}
	var req deleteEnvironmentRequest
	if err := decodeOptionalBody(r, &req); err != nil {
		Respond(w, r, nil, err)

		return true
	}
	if err := s.validate.Struct(&req); err != nil {
		Respond(w, r, nil, Validation("Scope can only be pds_only.").WithCause(err))

		return true
	}

	if req.Scope == "pds_only" {
		if err := s.managed.RemoveFromPDS(r.Context(), scope, name, req.Confirm); err != nil {
			Respond(w, r, nil, managedError(err))

			return true
		}
		RespondWithStatus(w, r, http.StatusNoContent, nil, nil)

		return true
	}
	run, err := s.managed.Delete(r.Context(), scope, name, req.Confirm)
	if err != nil {
		Respond(w, r, nil, managedError(err))

		return true
	}
	s.respondRunStarted(w, r, scope, name, run)

	return true
}

func (s *Server) respondRunStarted(w http.ResponseWriter, r *http.Request, scope auth.Scope, name string, run store.ProvisioningRun) {
	p, err := s.managed.Provisioning(r.Context(), scope.Team.ID, name)
	if err != nil {
		Respond(w, r, nil, managedError(err))

		return
	}

	RespondWithStatus(w, r, http.StatusAccepted, runStartedResponse{Run: runView(run), Provisioning: provisioningView(p)}, nil)
}

func (s *Server) managedLatest(r *http.Request, scope auth.Scope, name string) (store.ProvisioningRun, error) {
	p, err := s.managed.Provisioning(r.Context(), scope.Team.ID, name)
	if err != nil {
		return store.ProvisioningRun{}, err
	}

	return store.ProvisioningRun{
		ID: p.Run.ID, Kind: p.Run.Kind, Status: p.Run.Status, CreatedAt: p.Run.StartedAt, FinishedAt: p.Run.FinishedAt,
	}, nil
}

// managerScope is the caller's scope, with 501 where this control plane cannot
// build managed environments.
func (s *Server) managerScope(w http.ResponseWriter, r *http.Request) (auth.Scope, bool) {
	scope, ok := s.scopeOf(w, r)
	if !ok {
		return auth.Scope{}, false
	}
	if s.managed == nil {
		Respond(w, r, nil, managedError(managed.ErrUnavailable))

		return auth.Scope{}, false
	}

	return scope, true
}

// decodeOptionalBody reads a JSON object when the request has a body, and
// leaves out untouched when it has none.
func decodeOptionalBody(r *http.Request, out any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		return Validation("That request body could not be read.").WithCause(err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return Validation("That request body is not valid JSON.").WithCause(err)
	}

	return nil
}

// managedError maps what managed environments and their runs refuse onto the
// API's errors. Every sentence says what to do next, and none carries AWS text.
func managedError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrManagedEnvironmentUnknown):
		return NotFound("That managed environment does not exist.").WithCause(err)
	case errors.Is(err, store.ErrManagedEnvironmentExists):
		return Conflict("An environment with that name already exists in your team. Choose another name.").WithCause(err)
	case errors.Is(err, managed.ErrInvalid):
		return Validation("Check the environment name and the region, then try again.").WithCause(err)
	case errors.Is(err, managed.ErrUnavailable):
		return NotImplemented("This control plane cannot build or change managed environments. Contact Percona support.").WithCause(err)
	case errors.Is(err, managed.ErrConnectionNotReady):
		return Conflict("This AWS account is not ready. Open it, check the connection, then try again.").WithCause(err)
	case errors.Is(err, managed.ErrConfirm):
		return Validation("The name you typed is not the name of this environment. Type it exactly, then try again.").WithCause(err)
	case errors.Is(err, managed.ErrReachable):
		return Conflict("PDS can still reach this AWS account. Delete the environment instead of removing it from PDS only.").WithCause(err)
	case errors.Is(err, store.ErrRunActive):
		return Conflict("This environment already has a run in progress. Wait for it to finish, or retry it if it failed.").WithCause(err)
	case errors.Is(err, store.ErrNoFailedRun):
		return Conflict("This environment has no failed run to retry.").WithCause(err)
	case errors.Is(err, store.ErrRunNotFound):
		return NotFound("That run does not exist.").WithCause(err)
	case errors.Is(err, store.ErrCloudConnectionUnknown):
		return cloudError(err)
	case errors.Is(err, store.ErrNotConnected):
		return Downstream("The control plane is not available. Try again in a moment.", err)
	}

	return Internal(somethingWentWrong, err)
}
