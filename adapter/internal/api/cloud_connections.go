package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

type cloudTokenRequest struct {
	AllowedRegions []string `json:"allowedRegions"`
}

// cloudTokenResponse is the only time a connect command is shown: nothing reads
// it back, and no tool carries it.
type cloudTokenResponse struct {
	ID             string    `json:"id"`
	Command        string    `json:"command"`
	ExpiresAt      time.Time `json:"expiresAt"`
	AllowedRegions []string  `json:"allowedRegions"`
}

type cloudTypedRequest struct {
	AccountID      string   `json:"accountId" validate:"required"`
	AllowedRegions []string `json:"allowedRegions"`
}

// cloudEnrollmentResponse is what the person needs to create the stack by hand.
// It carries the connection's secret, so only the call that made the connection
// answers with it.
type cloudEnrollmentResponse struct {
	ConnectionID    string   `json:"connectionId"`
	ExternalID      string   `json:"externalId"`
	StackName       string   `json:"stackName"`
	StackRegion     string   `json:"stackRegion"`
	AllowedRegions  []string `json:"allowedRegions"`
	TemplateURL     string   `json:"templateUrl"`
	TemplateVersion int      `json:"templateVersion"`
}

type cloudConfirmRequest struct {
	Confirm string `json:"confirm"`
}

type cloudStepResponse struct {
	Step   string    `json:"step"`
	Status string    `json:"status"`
	At     time.Time `json:"at"`
}

// cloudConnectionResponse is an AWS account the Team connected. It holds no
// secret: not the ExternalId, not the role.
type cloudConnectionResponse struct {
	ID              string     `json:"id"`
	AccountID       string     `json:"accountId"`
	Alias           string     `json:"alias,omitempty"`
	State           string     `json:"state"`
	Reason          string     `json:"reason,omitempty"`
	AllowedRegions  []string   `json:"allowedRegions"`
	StackRegion     string     `json:"stackRegion"`
	TemplateVersion int        `json:"templateVersion"`
	LatestVersion   int        `json:"latestTemplateVersion"`
	CheckedAt       *time.Time `json:"checkedAt,omitempty"`
	ReadyAt         *time.Time `json:"readyAt,omitempty"`
	// SignalAt is the last time the connect command said anything, which is how
	// the console knows it stopped reporting.
	SignalAt     *time.Time `json:"signalAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	Environments []string   `json:"environments"`
	// Steps is what the connect command reported, oldest first. Only on one
	// connection's read.
	Steps []cloudStepResponse `json:"steps,omitempty"`
}

type cloudConnectionListResponse struct {
	CloudConnections []cloudConnectionResponse `json:"cloudConnections"`
}

// handleIssueCloudToken mints the connect token and the one command that holds it.
func (s *Server) handleIssueCloudToken(w http.ResponseWriter, r *http.Request) {
	cloud, scope, ok := s.cloudCaller(w, r)
	if !ok {
		return
	}
	var req cloudTokenRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}
	if len(req.AllowedRegions) == 0 {
		req.AllowedRegions = []string{s.clusterRegion()}
	}
	hubURL, err := s.scriptOrigin(r)
	if err != nil {
		Respond(w, r, nil, err)

		return
	}
	issued, err := cloud.IssueToken(r.Context(), scope, req.AllowedRegions)
	if err != nil {
		Respond(w, r, nil, cloudError(err))

		return
	}

	RespondWithStatus(w, r, http.StatusCreated, cloudTokenResponse{
		ID: issued.ID, ExpiresAt: issued.ExpiresAt, AllowedRegions: issued.AllowedRegions,
		Command: "curl -fsSL " + shellQuote(hubURL+cloudRoutePrefix+"connect.sh") + " | bash -s -- --token " + shellQuote(issued.Token),
	}, nil)
}

// cloudTokenStatusResponse is where a connect command stands. The console polls
// it by the id the mint answered with, and once connectionId appears it reads the
// connection. It never holds the command or the token.
type cloudTokenStatusResponse struct {
	ID             string    `json:"id"`
	State          string    `json:"state"`
	ConnectionID   string    `json:"connectionId,omitempty"`
	ExpiresAt      time.Time `json:"expiresAt"`
	AllowedRegions []string  `json:"allowedRegions"`
}

// handleGetCloudToken tells where one of the Team's connect commands stands.
func (s *Server) handleGetCloudToken(w http.ResponseWriter, r *http.Request) {
	cloud, scope, ok := s.cloudCaller(w, r)
	if !ok {
		return
	}
	got, err := cloud.TokenStatus(r.Context(), scope, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrCloudTokenInvalid) {
		Respond(w, r, nil, NotFound("That connect command does not exist.").WithCause(err))

		return
	}
	if err != nil {
		Respond(w, r, nil, cloudError(err))

		return
	}

	Respond(w, r, cloudTokenStatusResponse{
		ID: got.ID, State: got.State, ConnectionID: got.ConnectionID, ExpiresAt: got.ExpiresAt, AllowedRegions: got.AllowedRegions,
	}, nil)
}

// handleCancelCloudToken ends a connect command before it has been used or
// while it is still running.
func (s *Server) handleCancelCloudToken(w http.ResponseWriter, r *http.Request) {
	cloud, scope, ok := s.cloudCaller(w, r)
	if !ok {
		return
	}
	err := cloud.CancelToken(r.Context(), scope, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrCloudTokenInvalid) {
		Respond(w, r, nil, NotFound("That connect command does not exist or has ended.").WithCause(err))

		return
	}
	if err != nil {
		Respond(w, r, nil, cloudError(err))

		return
	}

	RespondWithStatus(w, r, http.StatusNoContent, nil, nil)
}

// handleCreateCloudConnection connects an account the person typed, for the
// path that needs no script.
func (s *Server) handleCreateCloudConnection(w http.ResponseWriter, r *http.Request) {
	cloud, scope, ok := s.cloudCaller(w, r)
	if !ok {
		return
	}
	var req cloudTypedRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}
	if len(req.AllowedRegions) == 0 {
		req.AllowedRegions = []string{s.clusterRegion()}
	}
	hubURL, err := s.scriptOrigin(r)
	if err != nil {
		Respond(w, r, nil, err)

		return
	}
	got, err := cloud.CreateTyped(r.Context(), scope, req.AccountID, req.AllowedRegions)
	if err != nil {
		Respond(w, r, nil, cloudError(err))

		return
	}

	RespondWithStatus(w, r, http.StatusCreated, cloudEnrollmentResponse{
		ConnectionID: got.ConnectionID, ExternalID: got.ExternalID, StackName: got.StackName, StackRegion: got.StackRegion,
		AllowedRegions: got.AllowedRegions, TemplateURL: hubURL + cloudRoutePrefix + "template.yaml", TemplateVersion: got.TemplateVersion,
	}, nil)
}

func (s *Server) handleListCloudConnections(w http.ResponseWriter, r *http.Request) {
	cloud, scope, ok := s.cloudCaller(w, r)
	if !ok {
		return
	}
	rows, err := cloud.List(r.Context(), scope)
	if err != nil {
		Respond(w, r, nil, cloudError(err))

		return
	}
	out := cloudConnectionListResponse{CloudConnections: make([]cloudConnectionResponse, 0, len(rows))}
	for _, row := range rows {
		item, err := s.cloudConnectionView(r, scope, row)
		if err != nil {
			Respond(w, r, nil, err)

			return
		}
		out.CloudConnections = append(out.CloudConnections, item)
	}

	Respond(w, r, out, nil)
}

func (s *Server) handleGetCloudConnection(w http.ResponseWriter, r *http.Request) {
	cloud, scope, ok := s.cloudCaller(w, r)
	if !ok {
		return
	}
	row, err := cloud.Get(r.Context(), scope, chi.URLParam(r, "id"))
	if err != nil {
		Respond(w, r, nil, cloudError(err))

		return
	}
	item, err := s.cloudConnectionView(r, scope, row)
	if err != nil {
		Respond(w, r, nil, err)

		return
	}
	events, err := cloud.Events(r.Context(), scope, row.ID)
	if err != nil {
		Respond(w, r, nil, cloudError(err))

		return
	}
	for _, event := range events {
		item.Steps = append(item.Steps, cloudStepResponse{Step: event.Step, Status: event.Status, At: event.CreatedAt})
	}

	Respond(w, r, item, nil)
}

// handleRemoveCloudConnection forgets a connection in PDS. The stack in the
// account stays, and the person removes it there.
func (s *Server) handleRemoveCloudConnection(w http.ResponseWriter, r *http.Request) {
	cloud, scope, ok := s.cloudCaller(w, r)
	if !ok {
		return
	}
	var req cloudConfirmRequest
	if err := decodeOptionalBody(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}
	if err := cloud.Remove(r.Context(), scope, chi.URLParam(r, "id"), req.Confirm); err != nil {
		Respond(w, r, nil, cloudError(err))

		return
	}

	RespondWithStatus(w, r, http.StatusNoContent, nil, nil)
}

// cloudConnectionView is the connection as the API shows it, with the managed
// environments that run through it.
func (s *Server) cloudConnectionView(r *http.Request, scope auth.Scope, row store.CloudConnection) (cloudConnectionResponse, error) {
	names := []string{}
	if s.managed != nil {
		managedEnvs, err := s.managed.Environments(r.Context(), scope.Team.ID)
		if err != nil {
			return cloudConnectionResponse{}, managedError(err)
		}
		for _, env := range managedEnvs {
			if env.Record.ConnectionID == row.ID {
				names = append(names, env.Record.Name)
			}
		}
	}

	return cloudConnectionResponse{
		ID: row.ID, AccountID: row.AccountID, Alias: row.Alias, State: row.State, Reason: row.Reason,
		AllowedRegions: row.AllowedRegions, StackRegion: row.StackRegion,
		TemplateVersion: row.TemplateVersion, LatestVersion: service.CloudTemplateVersion,
		CheckedAt: row.CheckedAt, ReadyAt: row.ReadyAt, SignalAt: row.SignalAt, CreatedAt: row.CreatedAt,
		Environments: names,
	}, nil
}

// cloudCaller answers 501 where there is no control plane principal and 401
// where no member is signed in, and returns the service and the caller's scope.
func (s *Server) cloudCaller(w http.ResponseWriter, r *http.Request) (*service.CloudConnections, auth.Scope, bool) {
	cloud, ok := s.cloudService(w, r)
	if !ok {
		return nil, auth.Scope{}, false
	}
	scope, ok := s.scopeOf(w, r)

	return cloud, scope, ok
}

// scopeOf is the scope of the signed in member the request acts for.
func (s *Server) scopeOf(w http.ResponseWriter, r *http.Request) (auth.Scope, bool) {
	scope, ok := auth.ScopeFromContext(r.Context())
	if !ok {
		Respond(w, r, nil, Unauthorized("Sign in to do that."))

		return auth.Scope{}, false
	}

	return scope, true
}
