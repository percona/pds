package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/service"
)

// maxRequestBody caps a request body. Every payload on this API is a handful of
// fields, so a megabyte is generous and a runaway client cannot exhaust memory.
const maxRequestBody = 1 << 20

// createDatabaseRequest is the POST /api/databases payload. The tags catch a
// missing or nonsensical field; the exact bounds and the DNS-1123 name rule
// belong to the engine plugin, which is the only place that knows them.
type createDatabaseRequest struct {
	Name        string `json:"name" validate:"required"`
	Engine      string `json:"engine" validate:"omitempty,oneof=mysql postgresql"`
	Profile     string `json:"profile" validate:"required"`
	Namespace   string `json:"namespace"`
	Environment string `json:"environment"`
	CPU         *int   `json:"cpu" validate:"omitempty,gt=0"`
	Memory      *int   `json:"memory" validate:"omitempty,gt=0"`
	Storage     *int   `json:"storage" validate:"omitempty,gt=0"`
	// Access is private or public. Absent is a private load balancer. The
	// engine validates SourceRanges, which a public database must send.
	// 0.0.0.0/0 is open to the internet.
	Access       string   `json:"access"`
	SourceRanges []string `json:"sourceRanges"`
}

// scaleDatabaseRequest is the POST /api/databases/:id/scale payload. Every
// field is optional on its own, but the plugin refuses a request that changes
// nothing. Replicas is the member count: 1 to 3 enables HA.
type scaleDatabaseRequest struct {
	CPU      *int `json:"cpu" validate:"omitempty,gt=0"`
	Memory   *int `json:"memory" validate:"omitempty,gt=0"`
	Storage  *int `json:"storage" validate:"omitempty,gt=0"`
	Replicas *int `json:"replicas" validate:"omitempty,gt=0"`
}

// restoreDatabaseRequest is the POST /api/databases/:id/restore payload. The
// typed confirmation happens in the UI. A restore creates a second database,
// so the copy's name is required.
type restoreDatabaseRequest struct {
	BackupID string `json:"backupId" validate:"required"`
	Name     string `json:"name" validate:"required"`
}

// databaseResponse is one database as the UI reads it. Sizes are numbers, not
// Kubernetes quantities: the plugin parses them so the UI never has to.
type databaseResponse struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Engine      string     `json:"engine"`
	Profile     string     `json:"profile"`
	Namespace   string     `json:"namespace"`
	Environment string     `json:"environment,omitempty"`
	Status      string     `json:"status"`
	Message     string     `json:"message,omitempty"`
	Members     int        `json:"members"`
	CPU         float64    `json:"cpu"`
	MemoryGB    float64    `json:"memoryGb"`
	StorageGB   float64    `json:"storageGb"`
	CreatedAt   *time.Time `json:"createdAt,omitempty"`
	// LastBackupAt is when the newest succeeded backup finished. Absent means
	// none has, which the UI states as "No backups yet".
	LastBackupAt *time.Time `json:"lastBackupAt,omitempty"`
	// BackupSchedule is absent when the instance carries no schedule. One that
	// is turned off takes on-demand backups only, the same as none.
	BackupSchedule *backupScheduleResponse `json:"backupSchedule,omitempty"`
	Connection     *connectionResponse     `json:"connection,omitempty"`
	Access         accessResponse          `json:"access"`
	// Team names the Team the database belongs to. It is set only on a list
	// across Teams.
	Team string `json:"team,omitempty"`
}

// accessResponse is where a database can be reached from. Every database has
// one, so the detail page states reachability without guessing from the host.
type accessResponse struct {
	// External is any load balancer, private or public.
	External bool `json:"external"`
	// Visibility is cluster, private or public.
	Visibility engine.Visibility `json:"visibility"`
	// SourceRanges is what a public database admits. Open to the internet is
	// 0.0.0.0/0. Absent on a private database, and on an older public one
	// that stored no allow list.
	SourceRanges []string `json:"sourceRanges,omitempty"`
	// Address is absent until the load balancer has been given one, which is
	// the pending state the UI renders instead of an empty host.
	Address string `json:"address,omitempty"`
}

// backupScheduleResponse is the schedule the Backups tab states above the list
// and prefills its editor from. Every time is UTC, the way the provider reads
// it. Period and the fields after it are absent for a cron the control plane
// did not write, which the UI states as the cron itself.
type backupScheduleResponse struct {
	Name string `json:"name"`
	// Enabled is false for a schedule turned off, which keeps its settings.
	Enabled         bool   `json:"enabled"`
	Cron            string `json:"cron"`
	RetentionCopies int32  `json:"retentionCopies,omitempty"`
	Period          string `json:"period,omitempty"`
	Minute          *int   `json:"minute,omitempty"`
	// Hour is absent on an hourly schedule, Weekday on all but a weekly one.
	Hour    *int `json:"hour,omitempty"`
	Weekday *int `json:"weekday,omitempty"`
}

// backupScheduleRequest is the optional body of the backup schedule route.
// The pointers tell a field left out from one set to zero, since midnight and
// Sunday are both zero.
type backupScheduleRequest struct {
	Enabled         *bool  `json:"enabled"`
	Period          string `json:"period"`
	Minute          *int   `json:"minute"`
	Hour            *int   `json:"hour"`
	Weekday         *int   `json:"weekday"`
	RetentionCopies *int   `json:"retentionCopies"`
}

// input turns a body that named at least one field into a schedule. The
// caller answers an empty object before this, so a body whose fields are all
// null or blank is validated here rather than read as the default. A field
// that does not apply to the period is refused rather than dropped, so a
// client never believes it set a weekday the schedule ignores.
func (r backupScheduleRequest) input() (*engine.ScheduleInput, error) {
	if r.Enabled != nil && !*r.Enabled {
		return &engine.ScheduleInput{Enabled: false}, nil
	}

	in := engine.ScheduleInput{
		Enabled:         true,
		Period:          r.Period,
		RetentionCopies: engine.DefaultRetentionCopies,
	}
	if r.Period == "" {
		return nil, &engine.ScheduleError{Field: "period", Reason: "is required"}
	}
	if r.Minute != nil {
		in.Minute = *r.Minute
	}
	if r.RetentionCopies != nil {
		in.RetentionCopies = *r.RetentionCopies
	}

	switch {
	case r.Period == engine.PeriodHourly && r.Hour != nil:
		return nil, &engine.ScheduleError{Field: "hour", Reason: "only applies to a daily or weekly schedule"}
	case r.Period != engine.PeriodHourly && r.Hour == nil:
		return nil, &engine.ScheduleError{Field: "hour", Reason: "is required"}
	case r.Hour != nil:
		in.Hour = *r.Hour
	}

	switch {
	case r.Period != engine.PeriodWeekly && r.Weekday != nil:
		return nil, &engine.ScheduleError{Field: "weekday", Reason: "only applies to a weekly schedule"}
	case r.Period == engine.PeriodWeekly && r.Weekday == nil:
		return nil, &engine.ScheduleError{Field: "weekday", Reason: "is required"}
	case r.Weekday != nil:
		in.Weekday = *r.Weekday
	}

	if err := in.Validate(); err != nil {
		return nil, err
	}

	return &in, nil
}

// connectionResponse is the connect card. It deliberately carries no password:
// the URI lives behind GET /api/databases/{id}/credential so that a page view
// never moves a credential and a list never moves one for every database at
// once.
type connectionResponse struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	// MaskedURI is the connection string with the password replaced. It is
	// what the card renders, so the shape is visible and the credential is not.
	MaskedURI string `json:"maskedUri,omitempty"`
	// SecretName is the connection Secret the provider published. A hub holds
	// no password of its own, so this is how it points at one.
	SecretName string `json:"secretName,omitempty"`
	// ServiceName is the Service a port-forward targets. Absent when the host
	// is not an in-cluster address, and the dialog then offers no
	// port-forward rather than a command that cannot work.
	ServiceName string `json:"serviceName,omitempty"`
}

type databaseListResponse struct {
	Databases []databaseResponse `json:"databases"`
}

type backupResponse struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	SizeBytes  int64      `json:"sizeBytes"`
	Scheduled  bool       `json:"scheduled"`
	CreatedAt  *time.Time `json:"createdAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Message    string     `json:"message,omitempty"`
}

type backupListResponse struct {
	Backups []backupResponse `json:"backups"`
}

// handleCreateDatabase provisions a database.
func (s *Server) handleCreateDatabase(w http.ResponseWriter, r *http.Request) {
	var req createDatabaseRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	instance, err := s.databases.Create(r.Context(), engine.CreateInput{
		Name:        strings.TrimSpace(req.Name),
		Engine:      strings.TrimSpace(req.Engine),
		Profile:     req.Profile,
		Namespace:   req.Namespace,
		Environment: strings.TrimSpace(req.Environment),
		Overrides: engine.ResourceOverrides{
			CPUCores:  req.CPU,
			MemoryGB:  req.Memory,
			StorageGB: req.Storage,
		},
		Access:       engine.Access(req.Access),
		SourceRanges: req.SourceRanges,
	})
	if err != nil {
		Respond(w, r, nil, databaseError(err, strings.TrimSpace(req.Name)))

		return
	}

	RespondWithStatus(w, r, http.StatusCreated, toDatabaseResponse(*instance), nil)
}

// handleListDatabases lists every database the adapter manages.
func (s *Server) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	environment := strings.TrimSpace(r.URL.Query().Get("environment"))

	out := databaseListResponse{Databases: []databaseResponse{}}
	appErr := s.eachTeam(r, func(ctx context.Context, team auth.Team, all bool) *AppError {
		instances, err := s.databases.ListEnvironment(ctx, environment)
		if err != nil {
			return databaseError(err, "")
		}
		for _, instance := range instances {
			row := toDatabaseResponse(instance)
			if all {
				row.Team = team.Name
			}
			out.Databases = append(out.Databases, row)
		}

		return nil
	})
	if appErr != nil {
		Respond(w, r, nil, appErr)

		return
	}

	Respond(w, r, out, nil)
}

// handleGetDatabase returns one database with its connection details.
func (s *Server) handleGetDatabase(w http.ResponseWriter, r *http.Request) {
	id := databaseID(r)

	instance, err := s.databases.Get(r.Context(), id)
	if err != nil {
		Respond(w, r, nil, databaseError(err, service.NameFromID(id)))

		return
	}

	Respond(w, r, toDatabaseResponse(*instance), nil)
}

// handleScaleDatabase resizes a database.
func (s *Server) handleScaleDatabase(w http.ResponseWriter, r *http.Request) {
	id := databaseID(r)

	var req scaleDatabaseRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	if err := s.databases.Scale(r.Context(), id, engine.ScaleInput{
		CPUCores:  req.CPU,
		MemoryGB:  req.Memory,
		StorageGB: req.Storage,
		Replicas:  req.Replicas,
	}); err != nil {
		Respond(w, r, nil, databaseError(err, service.NameFromID(id)))

		return
	}

	instance, err := s.databases.Get(r.Context(), id)
	if err != nil {
		Respond(w, r, nil, databaseError(err, service.NameFromID(id)))

		return
	}

	Respond(w, r, toDatabaseResponse(*instance), nil)
}

// handleBackupDatabase starts an on-demand backup. The response is 202: the
// operator runs the backup, the adapter only asked for it.
func (s *Server) handleBackupDatabase(w http.ResponseWriter, r *http.Request) {
	id := databaseID(r)

	backup, err := s.databases.Backup(r.Context(), id)
	if err != nil {
		Respond(w, r, nil, databaseError(err, service.NameFromID(id)))

		return
	}

	RespondWithStatus(w, r, http.StatusAccepted, toBackupResponse(*backup), nil)
}

// handleSetBackupSchedule writes the one backup schedule a database carries.
// The body is optional: without one the route turns on the default daily
// schedule a chat schedule action sends, and repeating that answers 200
// with the schedule it has rather than a conflict.
//
// The answer is the database, with the schedule this call wrote. In hub mode
// the environment reports a schedule only after it applies the change, so reading
// it back would state the old one.
func (s *Server) handleSetBackupSchedule(w http.ResponseWriter, r *http.Request) {
	id := databaseID(r)

	in, err := s.decodeBackupSchedule(r)
	if err != nil {
		Respond(w, r, nil, err)

		return
	}

	schedule, err := s.databases.SetBackupSchedule(r.Context(), id, in)
	if err != nil {
		Respond(w, r, nil, databaseError(err, service.NameFromID(id)))

		return
	}

	instance, err := s.databases.Get(r.Context(), id)
	if err != nil {
		Respond(w, r, nil, databaseError(err, service.NameFromID(id)))

		return
	}

	out := toDatabaseResponse(*instance)
	out.BackupSchedule = toBackupScheduleResponse(schedule)
	Respond(w, r, out, nil)
}

// decodeBackupSchedule reads the optional body. Nothing, whitespace and an
// empty object all mean the default schedule.
func (s *Server) decodeBackupSchedule(r *http.Request) (*engine.ScheduleInput, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		return nil, Validation("That request body could not be read.").WithCause(err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	// Only an object is a schedule. A bare null would otherwise decode to the
	// empty request and turn on the default.
	if trimmed[0] != '{' {
		return nil, Validation("That request body must be one JSON object.")
	}

	// Unknown fields are refused: a misspelt field would otherwise leave the
	// body empty, and an empty body turns on the default schedule.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var req backupScheduleRequest
	if err := decoder.Decode(&req); err != nil {
		if field, ok := strings.CutPrefix(err.Error(), `json: unknown field "`); ok {
			return nil, Validation(fmt.Sprintf("`%s` is not a schedule field.", strings.TrimSuffix(field, `"`))).
				WithCause(err)
		}

		return nil, Validation("That request body is not valid JSON.").WithCause(err)
	}
	// One object and nothing after it. A second value would otherwise be
	// ignored while the first one still changed the schedule.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, Validation("That request body must be one JSON object.").WithCause(err)
	}

	// Only an object with no keys at all asks for the default. Counting keys
	// rather than comparing the decoded struct keeps {"period":""} and
	// {"enabled":null} from reading as {}.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &keys); err != nil {
		return nil, Validation("That request body is not valid JSON.").WithCause(err)
	}
	if len(keys) == 0 {
		return nil, nil
	}

	in, err := req.input()
	if err != nil {
		return nil, databaseError(err, "")
	}

	return in, nil
}

// handleListBackups lists the backups of one database, newest first.
func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	id := databaseID(r)

	backups, err := s.databases.Backups(r.Context(), id)
	if err != nil {
		Respond(w, r, nil, databaseError(err, service.NameFromID(id)))

		return
	}

	out := backupListResponse{Backups: make([]backupResponse, 0, len(backups))}
	for _, backup := range backups {
		out.Backups = append(out.Backups, toBackupResponse(backup))
	}

	Respond(w, r, out, nil)
}

// handleRestoreDatabase restores a backup into a new database. The source is
// left as it is.
func (s *Server) handleRestoreDatabase(w http.ResponseWriter, r *http.Request) {
	id := databaseID(r)

	var req restoreDatabaseRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	if _, err := s.hub(r); err != nil {
		Respond(w, r, nil, hubUnavailable(err))

		return
	}

	issued, err := s.databases.RestoreToNewDatabase(r.Context(), id, req.BackupID, req.Name)
	if err != nil {
		Respond(w, r, nil, restoreError(err, service.NameFromID(id), req.Name))

		return
	}

	RespondWithStatus(w, r, http.StatusAccepted, restoreAccepted{
		Status:      "accepted",
		Environment: issued.Environment,
		Name:        issued.Name,
		ID:          issued.ID,
	}, nil)
}

type restoreAccepted struct {
	Status      string `json:"status"`
	Environment string `json:"environment,omitempty"`
	// Name and ID are the database the restore creates, which is not the
	// one the request was addressed to.
	Name string `json:"name,omitempty"`
	ID   string `json:"id,omitempty"`
}

// handleDeleteDatabase removes a database.
func (s *Server) handleDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	id := databaseID(r)

	if err := s.databases.Delete(r.Context(), id); err != nil {
		Respond(w, r, nil, databaseError(err, service.NameFromID(id)))

		return
	}

	Respond(w, r, nil, nil)
}

// decode reads and validates a JSON request body. A body the adapter cannot
// read is a client error, so both failures render as ErrValidation.
func (s *Server) decode(r *http.Request, out any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody))
	if err := decoder.Decode(out); err != nil {
		return Validation("That request body is not valid JSON.").WithCause(err)
	}

	if err := s.validate.Struct(out); err != nil {
		return Validation(validationMessage(err)).WithCause(err)
	}

	return nil
}

// validationMessage turns the first validator failure into one sentence. One
// sentence is enough: the UI validates the same fields before submitting, so a
// failure here means a client that skipped the form.
func validationMessage(err error) string {
	var invalid validator.ValidationErrors
	if !errors.As(err, &invalid) || len(invalid) == 0 {
		return "That request is missing a required field."
	}

	first := invalid[0]
	field := jsonFieldName(first.Field())

	if first.Tag() == "required" {
		return fmt.Sprintf("%s is required.", field)
	}

	return fmt.Sprintf("%s is not a usable value.", field)
}

// jsonFieldName renders a struct field name the way the request body spells it.
func jsonFieldName(field string) string {
	switch field {
	case "BackupID":
		return "backupId"
	case "CPU":
		return "cpu"
	default:
		if field == "" {
			return "That field"
		}

		return strings.ToLower(field[:1]) + field[1:]
	}
}

// restoreError is databaseError for a hub restore, where two databases are in
// play. A name already taken is about the copy being created, so the refusal
// names that rather than the database the backup came from.
func restoreError(err error, source, name string) *AppError {
	if errors.Is(err, engine.ErrExists) {
		return Conflict(fmt.Sprintf("A database named `%s` is already in that environment.", name)).WithCause(err)
	}

	return databaseError(err, source)
}

// databaseError maps a plugin or service failure onto the API error contract.
// Subject is the database name, used in the copy where naming it helps.
// Anything unrecognized is a downstream failure: the adapter itself only fails
// on the input cases listed here.
func databaseError(err error, subject string) *AppError {
	if err == nil {
		return nil
	}

	var (
		bounds *engine.BoundsError
		choice *engine.ChoiceError
		busy   *engine.BusyError
		source *engine.BackupSourceError
		nspace *engine.NamespaceError
		preset *engine.PresetError
		// A restore name that is too long carries its own limit and reason,
		// which the generic name message cannot.
		longName *engine.RestoreNameTooLongError
		ranges   *engine.SourceRangeError
		access   *engine.AccessError
		schedule *engine.ScheduleError
	)

	switch {
	case errors.Is(err, hub.ErrEnvironmentRequired):
		return Validation("Pick an environment.").WithCause(err)

	case errors.Is(err, hub.ErrUnreachable):
		return Conflict("That environment is unreachable. Try again after it reports in.").
			WithCause(err)

	case errors.Is(err, hub.ErrRestoreNotSupported):
		return NotImplemented("Restoring into a new database is not available for that engine. Contact Percona support to restore this backup.").
			WithCause(err)

	case errors.Is(err, hub.ErrEngineNotSupported):
		return NotImplemented("Only MySQL and PostgreSQL databases can be created here. Pick one of them.").
			WithCause(err)

	case errors.Is(err, hub.ErrNotSupported):
		return NotImplemented("That action is not available here.").WithCause(err)

	case errors.Is(err, hub.ErrInvalidEnvironment):
		if hub.ReservedEnvironmentName(subject) {
			return Validation("That name is taken by a page in this app. Pick another.").WithCause(err)
		}

		return Validation("Pick an environment name that is a DNS label.").WithCause(err)

	case errors.Is(err, hub.ErrPersist):
		return Downstream("That change could not be saved.", err)

	case errors.Is(err, hub.ErrGitHost):
		return Downstream("That environment could not be set up to receive changes. Contact Percona support.",
			fmt.Errorf("github could not create the repository or deploy key, check the hub GitHub token and org: %w", err))

	case errors.Is(err, engine.ErrBackupsUnavailable):
		return NotImplemented("Backups are not set up for this environment.").
			WithCause(fmt.Errorf("set a backup storage to turn backups on: %w", err))

	case errors.Is(err, engine.ErrExists):
		return Conflict(fmt.Sprintf("A database named `%s` already exists.", subject)).WithCause(err)

	case errors.Is(err, engine.ErrNotFound):
		return NotFound(fmt.Sprintf("No database named `%s`.", subject)).WithCause(err)

	// A restore source that is not there is not found; one that has not
	// finished is a conflict, because the same request works once it does.
	case errors.As(err, &source):
		if errors.Is(err, engine.ErrBackupNotFound) {
			return NotFound(source.UserMessage()).WithCause(err)
		}

		return Conflict(source.UserMessage()).WithCause(err)

	case errors.As(err, &busy):
		return Conflict(busy.UserMessage()).WithCause(err)

	case errors.As(err, &bounds):
		return Validation(bounds.UserMessage()).WithCause(err)

	case errors.As(err, &choice):
		return Validation(choice.UserMessage()).WithCause(err)

	case errors.As(err, &schedule):
		return Validation(schedule.UserMessage()).WithCause(err)

	case errors.As(err, &nspace):
		return Validation(nspace.UserMessage()).WithCause(err)

	case errors.As(err, &preset):
		return Validation(preset.UserMessage()).WithCause(err)

	case errors.Is(err, engine.ErrStorageShrink):
		return Validation("Storage can only be increased.").WithCause(err)

	case errors.Is(err, engine.ErrEmptyScale):
		return Validation("Set at least one of cpu, memory, storage, or replicas.").WithCause(err)

	case errors.Is(err, engine.ErrUnknownProfile):
		return Validation("Pick a profile: dev, staging, or prod.").WithCause(err)

	case errors.As(err, &ranges):
		return Validation(ranges.UserMessage()).WithCause(err)

	case errors.As(err, &access):
		return Validation(access.UserMessage()).WithCause(err)

	case errors.As(err, &longName):
		return Validation(longName.UserMessage()).WithCause(err)

	case errors.Is(err, engine.ErrInvalidName):
		return Validation("A database name starts with a letter and uses lowercase letters, numbers, and dashes, up to 63 characters.").
			WithCause(err)

	case errors.Is(err, engine.ErrInvalidNamespace):
		return Validation("A namespace is lowercase letters, numbers, and dashes, up to 63 characters.").
			WithCause(err)

	case errors.Is(err, engine.ErrInvalidID):
		return Validation("That database id is not valid.").WithCause(err)

	case errors.Is(err, context.DeadlineExceeded):
		return Downstream("The database platform took too long to answer. Try again in a moment.", err)

	default:
		return Downstream("The database platform did not answer. Try again in a moment.", err)
	}
}

func toDatabaseResponse(in engine.Instance) databaseResponse {
	profile := strings.ToLower(strings.TrimSpace(in.Profile))
	if profile == "" {
		profile = "custom"
	}
	out := databaseResponse{
		ID:          in.ID,
		Name:        in.Name,
		Engine:      in.Engine,
		Profile:     profile,
		Namespace:   in.Namespace,
		Environment: in.Environment,
		Status:      in.Status.Phase,
		Message:     in.Status.Message,
		Members:     in.Sizing.Members,
		CPU:         in.Sizing.CPUCores,
		MemoryGB:    in.Sizing.MemoryGB,
		StorageGB:   in.Sizing.StorageGB,
		Access: accessResponse{
			External:     in.Exposure.External,
			Visibility:   in.Exposure.Visibility(),
			SourceRanges: in.Exposure.SourceRanges,
			Address:      in.Exposure.Address,
		},
	}
	if !in.CreatedAt.IsZero() {
		created := in.CreatedAt
		out.CreatedAt = &created
	}
	if in.LastBackupAt != nil {
		at := in.LastBackupAt.UTC()
		out.LastBackupAt = &at
	}
	out.BackupSchedule = toBackupScheduleResponse(in.BackupSchedule)
	if in.Connection != nil {
		out.Connection = &connectionResponse{
			Host:        in.Connection.Host,
			Port:        in.Connection.Port,
			Username:    in.Connection.Username,
			MaskedURI:   maskURI(in.Connection.URI),
			SecretName:  in.Connection.SecretName,
			ServiceName: in.Connection.ServiceName,
		}
	}

	return out
}

func toBackupScheduleResponse(in *engine.BackupSchedule) *backupScheduleResponse {
	if in == nil {
		return nil
	}

	out := &backupScheduleResponse{
		Name:            in.Name,
		Enabled:         !in.Disabled,
		Cron:            in.Cron,
		RetentionCopies: in.RetentionCopies,
	}
	parsed, ok := engine.ParseScheduleCron(in.Cron)
	if !ok {
		return out
	}

	out.Period = parsed.Period
	out.Minute = &parsed.Minute
	if parsed.Period != engine.PeriodHourly {
		out.Hour = &parsed.Hour
	}
	if parsed.Period == engine.PeriodWeekly {
		out.Weekday = &parsed.Weekday
	}

	return out
}

func toBackupResponse(in engine.Backup) backupResponse {
	out := backupResponse{
		ID:        in.ID,
		Status:    in.Status,
		SizeBytes: in.SizeBytes,
		Scheduled: in.Scheduled,
		Message:   in.Message,
	}
	if !in.CreatedAt.IsZero() {
		created := in.CreatedAt
		out.CreatedAt = &created
	}
	if !in.FinishedAt.IsZero() {
		finished := in.FinishedAt
		out.FinishedAt = &finished
	}

	return out
}
