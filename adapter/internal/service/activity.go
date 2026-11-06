package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/platform"
)

// The activity ring records what the control plane did. These rows are that
// record, not output printed by a database process.
const (
	activityDataKey = "data.json"

	// MaxActivityEvents bounds the ring. A ConfigMap tops out near 1 MiB and
	// an event costs roughly 300 bytes, so this leaves a wide margin.
	MaxActivityEvents = 500

	// DefaultActivityLimit is the page size when a request does not ask.
	DefaultActivityLimit = 100

	// MaxActivityText caps detail and error text so one runaway downstream
	// string cannot crowd out the rest of the ring.
	MaxActivityText = 500

	// ActorUnknown stands in when the request has no User and no subject.
	ActorUnknown = "unknown"

	// ActorSystem is the actor of an entry the control plane wrote on its own,
	// such as a window that expired, with no User behind it.
	ActorSystem = "system"

	// ActorRemovedUser is what an event shows when the User who acted has since
	// been removed.
	ActorRemovedUser = "Removed user"
)

// Outcomes an event can carry.
const (
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
	// OutcomeAccepted is for a request that was taken but whose result is not
	// known yet. A backup and a restore both return long before they finish,
	// so recording them as succeeded asserts something nobody has checked, and
	// nothing goes back to correct the row when they fail.
	OutcomeAccepted = "accepted"
)

// Actions the adapter records. Every mutating route maps onto exactly one.
const (
	ActionDatabaseCreate         = "database.create"
	ActionDatabaseScale          = "database.scale"
	ActionDatabaseBackup         = "database.backup"
	ActionDatabaseBackupSchedule = "database.backup-schedule"
	ActionDatabaseRestore        = "database.restore"
	ActionDatabaseDelete         = "database.delete"
	ActionConnectionUpdate       = "connection.update"
	ActionConnectionDelete       = "connection.delete"
	ActionMonitoringPassword     = "monitoring.password-read"
	ActionDatabaseCredential     = "database.credential-read"
	ActionAgentRevoke            = "agent.revoke"
	ActionSupportAccessOpen      = "support_access.open"
	ActionSupportAccessExtend    = "support_access.extend"
	ActionSupportAccessEnd       = "support_access.end"
	ActionSupportAccessInstall   = "support_access.install"
	ActionSupportAccessUninstall = "support_access.uninstall"
	ActionSupportAccessFail      = "support_access.fail"
	ActionSupportAccessExpire    = "support_access.expire"
	ActionSupportAccessRemove    = "support_access.remove"
	ActionOrganizationRename     = "organization.rename"
	ActionOrganizationAI         = "organization.ai"
	ActionInvitationCreate       = "invitation.create"
	ActionInvitationResend       = "invitation.resend"
	ActionInvitationRevoke       = "invitation.revoke"
	ActionInvitationClaim        = "invitation.claim"
	ActionInvitationRefuse       = "invitation.refuse"
)

// Resource kinds an event can point at. The detail view follows the pointer
// to read what happened after the request returned, which is where a backup
// that was accepted and then failed shows its real ending.
const (
	ResourceInstance = "instance"
	ResourceBackup   = "backup"
	ResourceRestore  = "restore"
)

// ErrActivityUnavailable means the ring could not be read.
var ErrActivityUnavailable = errors.New("activity log is not available")

// ErrActivityEventNotFound means the ring holds no event with that id. The
// ring is bounded, so an id that was valid yesterday can age out of it.
var ErrActivityEventNotFound = errors.New("activity event not found")

// ActivityEvent is one control plane mutation, as stored and as served.
type ActivityEvent struct {
	// ID is a zero padded sequence. It doubles as the polling cursor, which
	// is why it has to sort lexically in the same order it was issued.
	ID string    `json:"id"`
	At time.Time `json:"at"`
	// Actor is who acted, as shown. A row written for a User stores their id
	// and no name, and List and Get fill this in from the User as they are now.
	// A fixed actor such as system has no User row and keeps its label.
	Actor string `json:"actor"`
	// UserID and TeamID are the stored actor and owner. Zero means none.
	UserID uint `json:"-"`
	TeamID uint `json:"-"`
	// Client is how the actor's session signed in, such as "mcp" or "chat". It
	// is empty when the sign in did not say, which covers the UI and older
	// entries. Agent names the program behind it, such as "Cursor" for an MCP
	// client or "Anthropic" for the Chat, and is empty for a person.
	Client     string `json:"client,omitempty"`
	Agent      string `json:"agent,omitempty"`
	RequestID  string `json:"requestId"`
	Action     string `json:"action"`
	DatabaseID string `json:"databaseId,omitempty"`
	Target     string `json:"target"`
	Outcome    string `json:"outcome"`
	Detail     string `json:"detail,omitempty"`
	Error      string `json:"error,omitempty"`

	// ResourceKind and ResourceName name the object the action produced or
	// changed. Events written before the detail view existed carry neither,
	// and the detail view falls back to the database for those.
	ResourceKind string `json:"resourceKind,omitempty"`
	ResourceName string `json:"resourceName,omitempty"`

	// Environment names the environment an action on no database was about, so
	// that environment's history can list it.
	Environment string `json:"environment,omitempty"`
}

// ActivityQuery selects a page of the ring.
type ActivityQuery struct {
	DatabaseID string
	// Since is exclusive: a poll passes back the cursor it last saw and gets
	// only what arrived after it.
	Since string
	Limit int
}

// ActivityPage is one answer to a poll.
type ActivityPage struct {
	Entries []ActivityEvent
	// Cursor is the newest ID in the whole ring, not in the filtered page, so
	// a poll that matches nothing still moves forward.
	Cursor    string
	Truncated bool
}

type activityDocument struct {
	NextSeq int             `json:"nextSeq"`
	Events  []ActivityEvent `json:"events"`
}

// ConfigMapStore reads and writes Kubernetes ConfigMaps. The kube client is
// the production implementation; tests inject a fake.
type ConfigMapStore interface {
	GetConfigMap(ctx context.Context, namespace, name string) (map[string]string, error)
	PutConfigMap(ctx context.Context, namespace, name string, values map[string]string) error
}

// ActivityOptions names the ConfigMap holding the ring.
type ActivityOptions struct {
	Namespace string
	ConfigMap string
}

// Activity records and serves control plane mutations. A nil store keeps the
// ring in memory instead, so a laptop run without Kubernetes access still has a
// working feed; only the in-cluster path survives a restart.
type Activity struct {
	store  ConfigMapStore
	opts   ActivityOptions
	mu     sync.Mutex
	memory activityDocument
	book   HistoryBook
	actors ActorNames
}

// ActorNames reads the display name of Users by id. An id it does not return
// belongs to a User who was removed.
type ActorNames interface {
	UserNames(ctx context.Context, ids []uint) (map[uint]string, error)
}

// NewActivity returns the activity service.
func NewActivity(store ConfigMapStore, opts ActivityOptions) *Activity {
	if strings.TrimSpace(opts.Namespace) == "" {
		opts.Namespace = "everest-system"
	}
	if strings.TrimSpace(opts.ConfigMap) == "" {
		opts.ConfigMap = "pds-activity"
	}

	return &Activity{store: store, opts: opts, memory: activityDocument{NextSeq: 1}}
}

// UseBook records and serves activity from the history table.
func (a *Activity) UseBook(book HistoryBook) {
	if a == nil {
		return
	}
	a.book = book
}

// UseActors names the Users behind the events a read returns.
func (a *Activity) UseActors(actors ActorNames) {
	if a == nil {
		return
	}
	a.actors = actors
}

// Book is the history table behind this ring, when one is wired.
func (a *Activity) Book() HistoryBook {
	if a == nil {
		return nil
	}

	return a.book
}

// Location reports the namespace and ConfigMap the ring lives in.
func (a *Activity) Location() (string, string) {
	if a == nil {
		return "", ""
	}

	return a.opts.Namespace, a.opts.ConfigMap
}

// Record appends one event. It returns nothing on purpose: an activity row
// that cannot be written is worth a warning, not a failed backup, so every
// error stops here rather than reaching the caller.
func (a *Activity) Record(ctx context.Context, e ActivityEvent) {
	if a == nil {
		return
	}
	if strings.TrimSpace(e.Action) == "" {
		return
	}

	if a.book != nil {
		e = a.normalize(ctx, e, 0)
		e.ID = ""
		a.book.RecordAction(ctx, e)

		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	doc, err := a.loadLocked(ctx)
	if err != nil {
		log.FromContext(ctx).Warn("activity read failed, event dropped",
			"action", e.Action, "error", err)

		return
	}

	if doc.NextSeq < 1 {
		doc.NextSeq = 1
	}
	doc.Events = append(doc.Events, a.normalize(ctx, e, doc.NextSeq))
	doc.NextSeq++
	if len(doc.Events) > MaxActivityEvents {
		doc.Events = doc.Events[len(doc.Events)-MaxActivityEvents:]
	}

	if err := a.saveLocked(ctx, doc); err != nil {
		log.FromContext(ctx).Warn("activity write failed, event dropped",
			"action", e.Action, "error", err)
	}
}

// List returns a page of the ring, newest first.
func (a *Activity) List(ctx context.Context, q ActivityQuery) (ActivityPage, error) {
	if a == nil {
		return ActivityPage{}, ErrActivityUnavailable
	}
	if a.book != nil {
		page, err := a.book.ListActions(ctx, q)
		if err != nil {
			return ActivityPage{}, err
		}
		if err := a.nameActors(ctx, page.Entries); err != nil {
			return ActivityPage{}, err
		}

		return page, nil
	}

	a.mu.Lock()
	doc, err := a.loadLocked(ctx)
	a.mu.Unlock()
	if err != nil {
		return ActivityPage{}, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultActivityLimit
	}
	if limit > MaxActivityEvents {
		limit = MaxActivityEvents
	}

	cursor := ""
	matched := make([]ActivityEvent, 0, len(doc.Events))
	databaseID := strings.TrimSpace(q.DatabaseID)
	since := strings.TrimSpace(q.Since)
	for _, e := range doc.Events {
		if e.ID > cursor {
			cursor = e.ID
		}
		if databaseID != "" && e.DatabaseID != databaseID {
			continue
		}
		if since != "" && e.ID <= since {
			continue
		}
		matched = append(matched, e)
	}

	// Newest first, and truncation drops the oldest of the matches so a busy
	// window never hides what just happened.
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].ID > matched[j].ID })
	truncated := len(matched) > limit
	if truncated {
		matched = matched[:limit]
	}

	return ActivityPage{Entries: matched, Cursor: cursor, Truncated: truncated}, nil
}

// Get returns one event by id.
func (a *Activity) Get(ctx context.Context, id string) (ActivityEvent, error) {
	if a == nil {
		return ActivityEvent{}, ErrActivityUnavailable
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return ActivityEvent{}, ErrActivityEventNotFound
	}
	if a.book != nil {
		event, err := a.book.GetAction(ctx, id)
		if err != nil {
			return ActivityEvent{}, err
		}
		events := []ActivityEvent{event}
		if err := a.nameActors(ctx, events); err != nil {
			return ActivityEvent{}, err
		}

		return events[0], nil
	}

	a.mu.Lock()
	doc, err := a.loadLocked(ctx)
	a.mu.Unlock()
	if err != nil {
		return ActivityEvent{}, err
	}

	for _, e := range doc.Events {
		if e.ID == id {
			return e, nil
		}
	}

	return ActivityEvent{}, fmt.Errorf("%w: %q", ErrActivityEventNotFound, id)
}

// normalize fills in what the call site did not supply and strips anything
// that authenticates. Detail is built from named fields at the call site, so
// Sanitize is the second line of defence: it catches a credential that arrived
// inside a downstream error string.
func (a *Activity) normalize(ctx context.Context, e ActivityEvent, seq int) ActivityEvent {
	e.ID = fmt.Sprintf("%020d", seq)
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	} else {
		e.At = e.At.UTC()
	}
	scope, scoped := auth.ScopeFromContext(ctx)
	if scoped {
		e.TeamID = scope.Team.ID
	}
	if strings.TrimSpace(e.Actor) == "" {
		switch subject, hasSubject := auth.SubjectFromContext(ctx); {
		case scoped && scope.UserID != 0:
			e.UserID = scope.UserID
		case hasSubject:
			e.Actor = subject
		default:
			e.Actor = ActorUnknown
		}
	}
	if e.Client == "" {
		client := auth.ClientFromContext(ctx)
		e.Client = client.Kind
		if e.Agent == "" {
			e.Agent = client.Agent
		}
	}
	if strings.TrimSpace(e.RequestID) == "" {
		e.RequestID = RequestIDFromContext(ctx)
	}
	if strings.TrimSpace(e.Outcome) == "" {
		e.Outcome = OutcomeSucceeded
	}
	e.Detail = clampActivityText(Sanitize(e.Detail))
	e.Error = clampActivityText(Sanitize(e.Error))

	return e
}

// nameActors fills in the Actor of every event written for a User. The names
// are read now, so a renamed User shows under the new name on old events, and
// an id with no User row reads as a removed User.
func (a *Activity) nameActors(ctx context.Context, events []ActivityEvent) error {
	var ids []uint
	for _, e := range events {
		if e.UserID != 0 {
			ids = append(ids, e.UserID)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	var names map[uint]string
	if a.actors != nil {
		var err error
		if names, err = a.actors.UserNames(ctx, ids); err != nil {
			return fmt.Errorf("%w: %v", ErrActivityUnavailable, err)
		}
	}
	for i := range events {
		if events[i].UserID == 0 {
			continue
		}
		events[i].Actor = names[events[i].UserID]
		if events[i].Actor == "" {
			events[i].Actor = ActorRemovedUser
		}
	}

	return nil
}

// RequestIDFromContext returns the correlator the HTTP middleware attached.
// It reads chi's key directly because that is where the request ID already
// lives, and until session validation lands it is the only thing tying an
// activity row back to a log line.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(middleware.RequestIDKey).(string)

	return id
}

func clampActivityText(v string) string {
	if len(v) <= MaxActivityText {
		return v
	}

	return v[:MaxActivityText]
}

func (a *Activity) loadLocked(ctx context.Context) (activityDocument, error) {
	if a.store == nil {
		return a.memory, nil
	}

	doc := activityDocument{NextSeq: 1}
	values, err := a.store.GetConfigMap(ctx, a.opts.Namespace, a.opts.ConfigMap)
	if err != nil {
		if errors.Is(err, kube.ErrConfigMapNotFound) || errors.Is(err, platform.ErrUnavailable) {
			return doc, nil
		}

		return activityDocument{}, fmt.Errorf("%w: %v", ErrActivityUnavailable, err)
	}
	raw := values[activityDataKey]
	if strings.TrimSpace(raw) == "" {
		return doc, nil
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return activityDocument{}, fmt.Errorf("activity: decode: %w", err)
	}
	if doc.NextSeq < 1 {
		doc.NextSeq = 1
	}

	return doc, nil
}

func (a *Activity) saveLocked(ctx context.Context, doc activityDocument) error {
	if a.store == nil {
		a.memory = doc

		return nil
	}

	body, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("activity: encode: %w", err)
	}

	return a.store.PutConfigMap(ctx, a.opts.Namespace, a.opts.ConfigMap, map[string]string{
		activityDataKey: string(body),
	})
}
