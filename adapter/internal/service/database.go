// Package service holds the adapter's business logic. Handlers decode and
// respond; services decide, call downstream clients, and return domain errors
// that the HTTP layer maps onto AppError codes.
package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

// ErrEngineUnavailable means no plugin is registered for the requested engine,
// or the registered plugin cannot serve the read path. The agent hits this
// when OpenEverest is not configured.
var ErrEngineUnavailable = errors.New("database engine is not available")

// Database is the business logic behind /api/databases. It resolves the engine
// plugin and delegates; the plugin owns spec mapping, bounds, and the
// OpenEverest calls.
type Database struct {
	registry *engine.Registry
	recorder ActivityRecorder
	hub      *hub.Service
	hubFor   func(ctx context.Context) (*hub.Service, error)
}

// ActivityRecorder appends one control plane mutation to the activity ring.
// Record returns nothing: a row that cannot be stored is worth a warning, not
// a failed backup.
type ActivityRecorder interface {
	Record(ctx context.Context, e ActivityEvent)
}

// Option configures a Database service.
type Option func(*Database)

// WithRecorder wires the activity log. Without one the service records
// nothing, which is what the tests that do not care about the ring rely on.
func WithRecorder(recorder ActivityRecorder) Option {
	return func(d *Database) {
		d.recorder = recorder
	}
}

// WithHub routes lifecycle through Git and the heartbeat cache. Tests use
// this to pin one Service; Hub then ignores the request context.
func WithHub(h *hub.Service) Option {
	return func(d *Database) {
		d.hub = h
	}
}

// WithHubFor resolves the hub Service from the request. Production wires
// this to a per-team registry.
func WithHubFor(fn func(ctx context.Context) (*hub.Service, error)) Option {
	return func(d *Database) {
		d.hubFor = fn
	}
}

// NewDatabase returns the database service backed by a plugin registry.
func NewDatabase(registry *engine.Registry, opts ...Option) *Database {
	d := &Database{registry: registry}
	for _, opt := range opts {
		opt(d)
	}

	return d
}

// DefaultEngine is the engine Create uses when the request does not name one.
const DefaultEngine = "mysql"

// Create provisions a database instance.
func (d *Database) Create(ctx context.Context, in engine.CreateInput) (*engine.Instance, error) {
	instance, err := d.create(ctx, in)
	dbID := in.Namespace + "." + in.Name
	if id := instanceID(instance); id != "" {
		dbID = id
	}
	d.record(ctx, ActivityEvent{
		Action:       ActionDatabaseCreate,
		DatabaseID:   dbID,
		Target:       in.Name,
		Detail:       createDetail(in, instance),
		ResourceKind: ResourceInstance,
		ResourceName: in.Name,
	}, err)

	return instance, err
}

func (d *Database) create(ctx context.Context, in engine.CreateInput) (*engine.Instance, error) {
	h, err := d.hubService(ctx)
	if err != nil {
		return nil, err
	}

	return h.Create(ctx, in)
}

// List returns every database the adapter manages, across engines.
func (d *Database) List(ctx context.Context) ([]engine.Instance, error) {
	return d.ListEnvironment(ctx, "")
}

// ListEnvironment is List filtered by spoke name. Empty environment lists every spoke.
func (d *Database) ListEnvironment(ctx context.Context, environment string) ([]engine.Instance, error) {
	h, err := d.Hub(ctx)
	if err != nil {
		return nil, err
	}
	if h != nil {
		return h.List(ctx, environment)
	}

	plugins, err := d.readWritePlugins()
	if err != nil {
		return nil, err
	}

	var out []engine.Instance
	for _, plugin := range plugins {
		items, err := plugin.List(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out, nil
}

// Get returns one database with its status and connection details.
func (d *Database) Get(ctx context.Context, id string) (*engine.Instance, error) {
	h, err := d.hubService(ctx)
	if err != nil {
		return nil, err
	}

	return h.Get(ctx, id)
}

// Lookup returns one database without connection secrets.
func (d *Database) Lookup(ctx context.Context, id string) (*engine.Instance, error) {
	h, err := d.hubService(ctx)
	if err != nil {
		return nil, err
	}

	return h.Get(ctx, id)
}

// Scale resizes a database.
func (d *Database) Scale(ctx context.Context, id string, in engine.ScaleInput) error {
	h, err := d.Hub(ctx)
	if err != nil {
		return err
	}
	if h != nil {
		err := h.Scale(ctx, id, in)
		d.record(ctx, ActivityEvent{
			Action:       ActionDatabaseScale,
			DatabaseID:   id,
			Target:       NameFromID(id),
			Detail:       scaleDetail(in),
			ResourceKind: ResourceInstance,
			ResourceName: NameFromID(id),
		}, err)

		return err
	}

	err = d.forOne(ctx, id, func(plugin engine.ReadWritePlugin) error {
		return plugin.Scale(ctx, id, in)
	})
	d.record(ctx, ActivityEvent{
		Action:       ActionDatabaseScale,
		DatabaseID:   id,
		Target:       NameFromID(id),
		Detail:       scaleDetail(in),
		ResourceKind: ResourceInstance,
		ResourceName: NameFromID(id),
	}, err)

	return err
}

// Backup starts an on-demand backup.
func (d *Database) Backup(ctx context.Context, id string) (*engine.Backup, error) {
	h, err := d.Hub(ctx)
	if err != nil {
		return nil, err
	}
	if h != nil {
		found, err := h.Backup(ctx, id)
		d.recordAccepted(ctx, ActivityEvent{
			Action:       ActionDatabaseBackup,
			DatabaseID:   id,
			Target:       NameFromID(id),
			Detail:       backupDetail(found),
			ResourceKind: ResourceBackup,
			ResourceName: backupID(found),
		}, err)
		if err != nil {
			return nil, err
		}

		return found, nil
	}

	var found *engine.Backup
	err = d.forOne(ctx, id, func(plugin engine.ReadWritePlugin) error {
		backup, err := plugin.Backup(ctx, id)
		if err != nil {
			return err
		}
		found = backup

		return nil
	})
	d.recordAccepted(ctx, ActivityEvent{
		Action:       ActionDatabaseBackup,
		DatabaseID:   id,
		Target:       NameFromID(id),
		Detail:       backupDetail(found),
		ResourceKind: ResourceBackup,
		ResourceName: backupID(found),
	}, err)
	if err != nil {
		return nil, err
	}

	return found, nil
}

// BackupNamed starts an on-demand backup with a caller-chosen Backup CR name.
func (d *Database) BackupNamed(ctx context.Context, id, backupID string) (*engine.Backup, error) {
	if strings.TrimSpace(backupID) == "" {
		return d.Backup(ctx, id)
	}

	var found *engine.Backup
	err := d.forOne(ctx, id, func(plugin engine.ReadWritePlugin) error {
		if named, ok := plugin.(interface {
			BackupNamed(context.Context, string, string) (*engine.Backup, error)
		}); ok {
			backup, err := named.BackupNamed(ctx, id, backupID)
			if err != nil {
				return err
			}
			found = backup

			return nil
		}
		backup, err := plugin.Backup(ctx, id)
		if err != nil {
			return err
		}
		found = backup

		return nil
	})
	if err != nil {
		return nil, err
	}

	return found, nil
}

// Backups lists the backups of one database, newest first.
func (d *Database) Backups(ctx context.Context, id string) ([]engine.Backup, error) {
	h, err := d.Hub(ctx)
	if err != nil {
		return nil, err
	}
	if h != nil {
		return h.Backups(ctx, id)
	}

	var found []engine.Backup
	err = d.forOne(ctx, id, func(plugin engine.ReadWritePlugin) error {
		backups, err := plugin.Backups(ctx, id)
		if err != nil {
			return err
		}
		found = backups

		return nil
	})
	if err != nil {
		return nil, err
	}

	return found, nil
}

// NamespaceBackups lists every backup in a namespace, newest first. Each
// plugin lists only the instances of its own provider, so every plugin that
// can list is asked.
func (d *Database) NamespaceBackups(ctx context.Context, namespace string) ([]engine.Backup, error) {
	h, err := d.Hub(ctx)
	if err != nil {
		return nil, err
	}
	if h != nil {
		return nil, fmt.Errorf("namespace backups are not available in hub mode")
	}

	plugins, err := d.readWritePlugins()
	if err != nil {
		return nil, err
	}
	var (
		out    []engine.Backup
		listed bool
	)
	for _, plugin := range plugins {
		lister, ok := plugin.(interface {
			NamespaceBackups(context.Context, string) ([]engine.Backup, error)
		})
		if !ok {
			continue
		}
		found, err := lister.NamespaceBackups(ctx, namespace)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
		listed = true
	}
	if !listed {
		return nil, fmt.Errorf("namespace backups are not available")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })

	return out, nil
}

// SetBackupSchedule writes the one backup schedule a database carries and
// returns the schedule it now has. A nil in turns on the default daily
// schedule when none is running, and repeating that is harmless.
func (d *Database) SetBackupSchedule(ctx context.Context, id string, in *engine.ScheduleInput) (*engine.BackupSchedule, error) {
	event := ActivityEvent{
		Action:       ActionDatabaseBackupSchedule,
		DatabaseID:   id,
		Target:       NameFromID(id),
		ResourceKind: ResourceInstance,
		ResourceName: NameFromID(id),
	}

	h, err := d.Hub(ctx)
	if err != nil {
		return nil, err
	}

	if h == nil {
		return nil, fmt.Errorf("%w: no hub", ErrEngineUnavailable)
	}
	schedule, err := h.SetBackupSchedule(ctx, id, in)
	event.Detail = resultDetail(schedule, in, err)
	d.record(ctx, event, err)

	return schedule, err
}

// resultDetail is the activity line for a schedule change. A write that
// landed is described by the schedule it left, since a repeated default
// request keeps a schedule that is already running. A failed one is described
// by what was asked for.
func resultDetail(schedule *engine.BackupSchedule, in *engine.ScheduleInput, err error) string {
	if err != nil || schedule == nil {
		return scheduleDetail(in)
	}
	if schedule.Disabled {
		return "Turned off"
	}

	parsed, ok := engine.ParseScheduleCron(schedule.Cron)
	if !ok {
		return "Runs on cron " + schedule.Cron
	}
	parsed.Enabled = true
	parsed.RetentionCopies = int(schedule.RetentionCopies)

	if window := retentionWindowText(schedule.RetentionWindow); window != "" {
		return scheduleWhen(&parsed) + ", keeps copies for " + window
	}

	return scheduleDetail(&parsed)
}

// retentionWindowText spells a window such as "4w" as "4 weeks". A value it
// cannot read gives an empty string.
func retentionWindowText(window string) string {
	if len(window) < 2 {
		return ""
	}
	n, err := strconv.Atoi(window[:len(window)-1])
	if err != nil || n < 1 {
		return ""
	}
	unit := map[byte]string{'d': "day", 'w': "week", 'm': "month"}[window[len(window)-1]]
	if unit == "" {
		return ""
	}
	if n != 1 {
		unit += "s"
	}

	return fmt.Sprintf("%d %s", n, unit)
}

var weekdayNames = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// scheduleDetail is the activity line for a schedule change.
func scheduleDetail(in *engine.ScheduleInput) string {
	want := engine.DefaultSchedule()
	if in != nil {
		want = *in
	}
	if !want.Enabled {
		return "Turned off"
	}

	when := scheduleWhen(&want)

	switch want.RetentionCopies {
	case 0:
		// A schedule with no retention keeps copies until something else
		// removes them.
		return when + ", keeps every copy"
	case 1:
		return when + ", keeps 1 copy"
	default:
		return fmt.Sprintf("%s, keeps %d copies", when, want.RetentionCopies)
	}
}

// scheduleWhen says when a schedule runs.
func scheduleWhen(want *engine.ScheduleInput) string {
	switch want.Period {
	case engine.PeriodHourly:
		return fmt.Sprintf("Hourly at %d minutes past", want.Minute)
	case engine.PeriodWeekly:
		day := ""
		if want.Weekday >= 0 && want.Weekday < len(weekdayNames) {
			day = weekdayNames[want.Weekday]
		}

		return fmt.Sprintf("Weekly on %s at %02d:%02d UTC", day, want.Hour, want.Minute)
	default:
		return fmt.Sprintf("Daily at %02d:%02d UTC", want.Hour, want.Minute)
	}
}

// RestoreToNewDatabase restores a backup into a second database on the environment
// the source runs on. The source is left as it is, so the copy needs a name.
func (d *Database) RestoreToNewDatabase(ctx context.Context, id, backupID, name string) (*hub.RestoreIssued, error) {
	h, err := d.hubService(ctx)
	if err != nil {
		return nil, err
	}

	issued, err := h.RestoreToNewDatabase(ctx, id, backupID, name)
	// The event names the database being created, not the one the backup came
	// from: recording the source read as though the database that was
	// deliberately left alone had been restored over. The detail behind the
	// row resolves the recorded id, so that has to move with it, and a restore
	// that was refused created nothing and stays on the source.
	target := strings.TrimSpace(name)
	recordID := id
	if issued != nil {
		target = issued.Name
		recordID = issued.ID
	}
	if target == "" {
		target = NameFromID(id)
	}
	d.recordAccepted(ctx, ActivityEvent{
		Action:       ActionDatabaseRestore,
		DatabaseID:   recordID,
		Target:       target,
		Detail:       "from backup " + backupID,
		ResourceKind: ResourceInstance,
		ResourceName: target,
	}, err)
	if err != nil {
		return nil, err
	}

	return issued, nil
}
func (d *Database) Delete(ctx context.Context, id string) error {
	h, err := d.Hub(ctx)
	if err != nil {
		return err
	}
	if h != nil {
		err := h.Delete(ctx, id)
		d.record(ctx, ActivityEvent{
			Action:       ActionDatabaseDelete,
			DatabaseID:   id,
			Target:       NameFromID(id),
			ResourceKind: ResourceInstance,
			ResourceName: NameFromID(id),
		}, err)

		return err
	}

	err = d.forOne(ctx, id, func(plugin engine.ReadWritePlugin) error {
		return plugin.Delete(ctx, id)
	})
	d.record(ctx, ActivityEvent{
		Action:       ActionDatabaseDelete,
		DatabaseID:   id,
		Target:       NameFromID(id),
		ResourceKind: ResourceInstance,
		ResourceName: NameFromID(id),
	}, err)

	return err
}

// Hub returns the Git backend for this request.
//
// A nil Service and a nil error means this process is the agent, which has
// no Git backend. A nil Service and an error means the team's hub could
// not be built or hydrated.
func (d *Database) Hub(ctx context.Context) (*hub.Service, error) {
	if d == nil {
		return nil, nil
	}
	if d.hub != nil {
		return d.hub, nil
	}
	if d.hubFor != nil {
		return d.hubFor(ctx)
	}

	return nil, nil
}

// hubService is Hub for a call the HTTP role makes. The agent has no hub.
func (d *Database) hubService(ctx context.Context) (*hub.Service, error) {
	h, err := d.Hub(ctx)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, fmt.Errorf("%w: no hub", ErrEngineUnavailable)
	}

	return h, nil
}

func createEngine(requested, fallback string) string {
	if v := strings.TrimSpace(requested); v != "" {
		return v
	}

	return fallback
}

func (d *Database) readWritePlugins() ([]engine.ReadWritePlugin, error) {
	if d == nil || d.registry == nil {
		return nil, fmt.Errorf("%w: no engine registry", ErrEngineUnavailable)
	}

	var out []engine.ReadWritePlugin
	for _, found := range d.registry.All() {
		plugin, ok := found.(engine.ReadWritePlugin)
		if !ok {
			continue
		}
		out = append(out, plugin)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no engine registry", ErrEngineUnavailable)
	}

	return out, nil
}

// forOne tries each registered plugin until one owns the instance. A wrong
// engine answers ErrNotFound; a malformed ID or a downstream failure stops
// the walk immediately.
func (d *Database) forOne(_ context.Context, id string, fn func(engine.ReadWritePlugin) error) error {

	plugins, err := d.readWritePlugins()
	if err != nil {
		return err
	}

	var notFound error
	for _, plugin := range plugins {
		err := fn(plugin)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, engine.ErrInvalidID):
			return err
		case errors.Is(err, engine.ErrNotFound):
			notFound = err
		default:
			return err
		}
	}

	if notFound != nil {
		return notFound
	}

	return fmt.Errorf("%w: %q", engine.ErrNotFound, id)
}

// record hands one mutation to the activity log, if one was wired. The outcome
// and the reason both come from err, so no call site has to spell them out and
// none can report a success it did not have.
func (d *Database) record(ctx context.Context, e ActivityEvent, err error) {
	d.recordOutcome(ctx, e, err, OutcomeSucceeded)
}

// recordAccepted is record for an operation that finishes after the request
// returns. A refused request is still a failure; a taken one is only accepted,
// because whether it worked is decided later and elsewhere.
func (d *Database) recordAccepted(ctx context.Context, e ActivityEvent, err error) {
	d.recordOutcome(ctx, e, err, OutcomeAccepted)
}

func (d *Database) recordOutcome(ctx context.Context, e ActivityEvent, err error, ok string) {
	if d == nil || d.recorder == nil {
		return
	}
	if err != nil {
		e.Outcome = OutcomeFailed
		e.Error = err.Error()
	} else {
		e.Outcome = ok
	}

	d.recorder.Record(ctx, e)
}

// NameFromID returns the name half of a namespace.name id. A malformed id has
// no name half and stands for itself.
func NameFromID(id string) string {
	_, name, found := strings.Cut(id, ".")
	if !found {
		return id
	}

	return name
}

// NamespaceFromID returns the namespace half of a namespace.name id. A
// malformed id has no namespace half and reports none.
func NamespaceFromID(id string) string {
	namespace, _, found := strings.Cut(id, ".")
	if !found {
		return ""
	}

	return namespace
}

func backupID(in *engine.Backup) string {
	if in == nil {
		return ""
	}

	return in.ID
}

func instanceID(in *engine.Instance) string {
	if in == nil {
		return ""
	}

	return in.ID
}

// The detail builders below name fields one at a time on purpose. A formatted
// struct or a dumped map is how a credential ends up in a feed, so nothing
// reaches an event that was not asked for by name.

// createDetail states a create. A create that succeeded states the access the
// engine wrote, which covers the private default a request that named none
// gets; a failed one states what was asked for.
func createDetail(in engine.CreateInput, created *engine.Instance) string {
	parts := []string{"engine " + createEngine(in.Engine, DefaultEngine), "profile " + in.Profile}
	parts = append(parts, overrideParts(in.Overrides)...)

	access, ranges := in.Access, in.SourceRanges
	if created != nil {
		switch created.Exposure.Visibility() {
		case engine.VisibilityPrivate:
			access = engine.AccessPrivate
		case engine.VisibilityPublic:
			access, ranges = engine.AccessPublic, created.Exposure.SourceRanges
		case engine.VisibilityCluster:
			access = ""
		}
	}

	switch access {
	case engine.AccessPrivate:
		parts = append(parts, "private access")
	case engine.AccessPublic:
		// The log states what the spec carries, so the ranges are normalized
		// the way the engine writes them. A list the engine refused leaves the
		// create failed, and the raw entries are what the caller sent.
		normalized, err := engine.NormalizeSourceRanges(ranges)
		if err != nil {
			normalized = ranges
		}
		from := "any address"
		if len(normalized) > 0 {
			from = strings.Join(normalized, " ")
		}
		parts = append(parts, "public from "+from)
	}

	return strings.Join(parts, ", ")
}

func overrideParts(in engine.ResourceOverrides) []string {
	var parts []string
	if in.CPUCores != nil {
		parts = append(parts, fmt.Sprintf("cpu %d", *in.CPUCores))
	}
	if in.MemoryGB != nil {
		parts = append(parts, fmt.Sprintf("memory %d GB", *in.MemoryGB))
	}
	if in.StorageGB != nil {
		parts = append(parts, fmt.Sprintf("storage %d GB", *in.StorageGB))
	}

	return parts
}

func scaleDetail(in engine.ScaleInput) string {
	var parts []string
	if in.CPUCores != nil {
		parts = append(parts, fmt.Sprintf("cpu %d", *in.CPUCores))
	}
	if in.MemoryGB != nil {
		parts = append(parts, fmt.Sprintf("memory %d GB", *in.MemoryGB))
	}
	if in.StorageGB != nil {
		parts = append(parts, fmt.Sprintf("storage %d GB", *in.StorageGB))
	}
	if in.Replicas != nil {
		parts = append(parts, fmt.Sprintf("replicas %d", *in.Replicas))
	}
	if len(parts) == 0 {
		return "no change requested"
	}

	return strings.Join(parts, ", ")
}

func backupDetail(in *engine.Backup) string {
	if in == nil || in.ID == "" {
		return "on demand"
	}

	return "on demand, " + in.ID
}
