package hub

import (
	"context"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/percona/pds/adapter/internal/engine"
)

// Environment connectivity the UI renders.
const (
	StatusConnected   = "connected"
	StatusUnreachable = "unreachable"
)

const inventoryPersistTimeout = 5 * time.Second

// Environment is one spoke the hub knows about.
type Environment struct {
	Name      string    `json:"name"`
	Namespace string    `json:"namespace,omitempty"`
	Status    string    `json:"status"`
	LastSeen  time.Time `json:"lastSeen,omitempty"`
	// Monitoring is true once the environment's last beat named a
	// MonitoringConfig. A database created before then has no monitoring.
	Monitoring bool `json:"monitoring"`
}

// Heartbeat is the observed state a spoke agent posts.
type Heartbeat struct {
	Environment string `json:"environment"`
	Namespace   string `json:"namespace"`
	// BackupStorage is the BackupStorage object this spoke has registered.
	// A kind spoke has MinIO and an EKS spoke has S3, so the hub cannot
	// stamp one global name into every environment's Instance manifests.
	BackupStorage string `json:"backupStorage,omitempty"`
	// MonitoringConfig is the MonitoringConfig this spoke has for its local
	// PMM, or empty when it has none. The hub adds the pmm-client to a new
	// database only when there is one to report to.
	MonitoringConfig string `json:"monitoringConfig,omitempty"`
	// MonitoringAddress is the address the cloud gave monitoring's load
	// balancer, or empty while it has none. It is read from the Service, so
	// it is the address a customer can open, not a setting the hub wrote.
	MonitoringAddress string            `json:"monitoringAddress,omitempty"`
	At                time.Time         `json:"at"`
	Instances         []engine.Instance `json:"instances"`
	Backups           []engine.Backup   `json:"backups"`
	// OmitInstances means listing failed. The hub keeps the last observed
	// instances and backups so a restart during an OpenEverest outage cannot
	// look like every database vanished. A successful empty list leaves this
	// false and replaces the snapshot.
	OmitInstances bool `json:"omitInstances,omitempty"`
	// OmitBackups means the backup list is the last successful snapshot, not
	// a current listing. The hub must not treat missing pending backups as
	// retries against that stale list.
	OmitBackups bool `json:"omitBackups,omitempty"`
	// Results acknowledges the work the previous reply carried. The hub
	// holds an item until it hears back, so this is what stops a delete
	// from being handed out a second time.
	Results []WorkResult `json:"results,omitempty"`
	// Engines are the engines this agent serves, which is the engines the
	// environment's chart installed a provider for. An agent that predates the
	// field reports none, so the hub refuses an engine that arrived later than
	// the field and keeps serving MySQL and PostgreSQL.
	Engines []string `json:"engines,omitempty"`
	// PrivateAccess marks an agent that tells a private load balancer from
	// a public one. An older agent reads every load balancer as public and
	// would hand over a private database's password as a public one, so a
	// beat or a stored snapshot without the mark keeps no password at all.
	PrivateAccess bool `json:"privateAccess,omitempty"`
	// AlertRules marks an agent that a join command of the release that
	// brought the alert rules installed. That command also gives the platform
	// Kustomization the right to apply the rules ConfigMap, which no hub
	// action can grant, so the mark is how the hub knows the ConfigMap will
	// apply.
	AlertRules bool `json:"alertRules,omitempty"`
	// Alerts is the firing alerts of this environment's own PMM, read on this
	// beat. Nil means the agent predates the field, which is not the same as
	// an empty list: no alerts is a reading, an absent field is no reading.
	Alerts *HeartbeatAlerts `json:"alerts,omitempty"`
	// Events are Kubernetes events for database pods since the previous beat.
	Events []HeartbeatEvent `json:"events,omitempty"`
	// Usage is at most one sample per database per five minutes.
	Usage []HeartbeatUsage `json:"usage,omitempty"`
	// Support is what the agent holds for support access. Nil means the agent
	// predates it, which says nothing about its windows. The hub reads it and
	// drops it before the beat is kept.
	Support *HeartbeatSupport `json:"support,omitempty"`
	// SecretResults answers the secret reads the previous reply asked for. The
	// handler takes them off the beat before anything else sees it, because
	// they carry credentials.
	SecretResults []SecretResult `json:"secretResults,omitempty"`
}

// Alert severities after the agent maps the severity label.
const (
	AlertCritical = "critical"
	AlertWarning  = "warning"
	AlertInfo     = "info"
)

// HeartbeatAlerts is one reading of the environment's firing alerts. Pending,
// when set, is a monitoring code saying why PMM could not be read, and Items is
// then empty. Total counts every firing alert PMM held, so Total above
// len(Items) means the agent cut the list.
type HeartbeatAlerts struct {
	At      time.Time        `json:"at"`
	Pending string           `json:"pending,omitempty"`
	Total   int              `json:"total"`
	Items   []HeartbeatAlert `json:"items"`
}

// HeartbeatAlert is one firing alert. Items arrive ordered by severity, then
// longest firing. Labels hold only the keys the agent allows.
type HeartbeatAlert struct {
	Fingerprint string            `json:"fingerprint"`
	Rule        string            `json:"rule"`
	Severity    string            `json:"severity"`
	Summary     string            `json:"summary"`
	StartsAt    time.Time         `json:"startsAt"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// HeartbeatEvent is one Kubernetes event a spoke attaches to a beat.
type HeartbeatEvent struct {
	DatabaseID string    `json:"databaseId"`
	At         time.Time `json:"at"`
	Type       string    `json:"type,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Message    string    `json:"message,omitempty"`
	Count      int32     `json:"count,omitempty"`
}

// HeartbeatUsage is one resource sample. Nil processor fields mean
// metrics-server did not answer.
type HeartbeatUsage struct {
	DatabaseID    string    `json:"databaseId"`
	At            time.Time `json:"at"`
	CPUAvg        *float64  `json:"cpuAvg,omitempty"`
	CPUMax        *float64  `json:"cpuMax,omitempty"`
	MemoryAvg     *float64  `json:"memoryAvg,omitempty"`
	MemoryMax     *float64  `json:"memoryMax,omitempty"`
	StorageBytes  *int64    `json:"storageBytes,omitempty"`
	MetricsReason string    `json:"metricsReason,omitempty"`
	// Monitoring is the reading from the spoke's own PMM taken with this
	// sample. Nil means the agent has no PMM to read from.
	Monitoring *HeartbeatMonitoring `json:"monitoring,omitempty"`
}

// HeartbeatMonitoring is the newest value of each series the Metrics tab
// charts. A nil value is a series PMM had nothing for. Pending, when set, says
// why there is no reading at all, and the values are then empty.
type HeartbeatMonitoring struct {
	CPU            *float64 `json:"cpu,omitempty"`
	Memory         *float64 `json:"memory,omitempty"`
	Connections    *float64 `json:"connections,omitempty"`
	ReplicationLag *float64 `json:"replicationLag,omitempty"`
	Disk           *float64 `json:"disk,omitempty"`
	Pending        string   `json:"pending,omitempty"`
}

// Inventory is the hub's view of spokes and their last heartbeat.
type Inventory struct {
	mu           sync.Mutex
	staleAfter   time.Duration
	environments map[string]registered
	persist      InventoryPersist
	// lastPersist paces the snapshot write. Every spoke rewrites the whole
	// row on every beat, and with twenty spokes that is a read and an
	// update per object several times a second, so Update conflicts become
	// routine and the heartbeat handler pays for two round trips.
	lastPersist map[string]time.Time
}

// persistEvery is the floor between two snapshot writes for one environment.
// Anything that changes what a restart has to remember bypasses it.
const persistEvery = time.Minute

type registered struct {
	namespace string
	storage   string
	// repo is the Git repository the environment's desired state lives in. It is
	// set once, when the environment is connected, and never changes.
	repo    string
	last    Heartbeat
	seen    bool
	pending []PendingBackup
}

// PendingBackup is an on-demand backup the hub accepted that the spoke has
// not yet reported as succeeded or failed. A given-up backup stays here
// with Status failed so listings still show it.
type PendingBackup struct {
	Database string        `json:"database"`
	Backup   engine.Backup `json:"backup"`
	// Attempts counts the beats that went out asking for this and came
	// back without it. A backup the spoke refuses outright, for example an
	// id longer than a label, never reaches a terminal state, so without a
	// cap it is asked for on every beat and shows Pending forever.
	Attempts int `json:"attempts,omitempty"`
}

// pendingBackupAttempts is how many beats a pending backup is asked for
// before the hub marks it failed, keeps that record, and stops asking.
const pendingBackupAttempts = 10

// NewInventory tracks spokes. A heartbeat older than staleAfter is unreachable.
func NewInventory(staleAfter time.Duration) *Inventory {
	if staleAfter <= 0 {
		staleAfter = 90 * time.Second
	}

	return &Inventory{
		staleAfter:   staleAfter,
		environments: map[string]registered{},
		lastPersist:  map[string]time.Time{},
	}
}

// SetPersist writes inventory rows after a join or heartbeat. A nil persist
// keeps the list in memory only.
func (i *Inventory) SetPersist(p InventoryPersist) {
	if i == nil {
		return
	}
	i.persist = p
}

// Load hydrates spokes from persist. Stale lastSeen stays unreachable until a
// new heartbeat. Git inventory should be loaded first; this overlays snapshots.
func (i *Inventory) Load(ctx context.Context) error {
	if i == nil || i.persist == nil {
		return nil
	}
	items, err := i.persist.Load(ctx)
	if err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	for name, rec := range items {
		if !ValidEnvironmentName(name) {
			continue
		}
		cur := i.environments[name]
		if rec.Namespace != "" {
			cur.namespace = rec.Namespace
		} else if rec.Heartbeat.Namespace != "" {
			cur.namespace = rec.Heartbeat.Namespace
		}
		if rec.BackupStorage != "" {
			cur.storage = rec.BackupStorage
		} else if rec.Heartbeat.BackupStorage != "" {
			cur.storage = rec.Heartbeat.BackupStorage
		}
		if rec.Repo != "" {
			cur.repo = rec.Repo
		}
		if rec.Seen {
			cur.last = persistableHeartbeat(rec.Heartbeat)
			cur.seen = true
		}
		if len(rec.PendingBackups) > 0 {
			cur.pending = append([]PendingBackup(nil), rec.PendingBackups...)
		}
		i.environments[name] = cur
	}

	return nil
}

// Repo is the Git repository stored on an environment's record, or blank for
// one that joined before records carried it.
func (i *Inventory) Repo(name string) string {
	if i == nil {
		return ""
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.environments[name].repo
}

// Register adds a spoke from Git inventory. It stays unreachable until a beat
// and does not write persist, so a boot seed cannot wipe a stored snapshot.
func (i *Inventory) Register(name, namespace string) {
	i.mu.Lock()
	defer i.mu.Unlock()

	cur := i.environments[name]
	cur.namespace = namespace
	i.environments[name] = cur
}

// remember registers a spoke and writes persist so a minted join survives
// restart before the first heartbeat. A persist failure is returned so
// IssueJoin can roll back instead of handing out a token the hub will reject.
func (i *Inventory) remember(name, namespace, repo string) error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	cur := i.environments[name]
	cur.namespace = namespace
	if cur.repo == "" {
		cur.repo = repo
	}
	i.environments[name] = cur
	rec := i.recordLocked(name, cur)
	i.mu.Unlock()

	return i.saveRecord(name, rec)
}

func (i *Inventory) saveRecord(name string, rec persistedEnvironment) error {
	if i == nil || i.persist == nil || name == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), inventoryPersistTimeout)
	defer cancel()
	if err := i.persist.Save(ctx, name, rec); err != nil {
		slog.Warn("hub inventory persist failed", slog.String("environment", name), slog.String("err", err.Error()))

		return err
	}

	return nil
}

// ApplyHeartbeat records observed state. An unknown environment is registered,
// so the name has to be one this store can read back and one Kubernetes
// accepts as a ConfigMap key.
func (i *Inventory) ApplyHeartbeat(beat Heartbeat) {
	_ = i.applyHeartbeat(beat, false)
}

func (i *Inventory) applyHeartbeat(beat Heartbeat, urgent bool) error {
	if !ValidEnvironmentName(beat.Environment) {
		return nil
	}
	beat = stripHeartbeat(beat)
	i.mu.Lock()
	cur := i.environments[beat.Environment]
	if beat.OmitInstances {
		beat.Instances = cur.last.Instances
		beat.Backups = cur.last.Backups
		beat.OmitInstances = false
		beat.OmitBackups = false
	} else if beat.OmitBackups {
		beat.Backups = cur.last.Backups
		beat.OmitBackups = false
	}
	if beat.Namespace != "" {
		cur.namespace = beat.Namespace
	}
	if beat.BackupStorage != "" {
		cur.storage = beat.BackupStorage
	}
	cur.last = beat
	cur.seen = true
	i.environments[beat.Environment] = cur
	rec := i.recordLocked(beat.Environment, cur)
	i.mu.Unlock()

	return i.persistRecordAt(beat.Environment, rec, urgent)
}

// List returns every registered environment, sorted by name.
func (i *Inventory) List(now time.Time) []Environment {
	i.mu.Lock()
	defer i.mu.Unlock()

	out := make([]Environment, 0, len(i.environments))
	for name, cur := range i.environments {
		out = append(out, i.view(name, cur, now))
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })

	return out
}

// Get returns one environment.
func (i *Inventory) Get(name string, now time.Time) (Environment, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()

	cur, ok := i.environments[name]
	if !ok {
		return Environment{}, false
	}

	return i.view(name, cur, now), true
}

// Namespace returns the namespace a spoke reported, or empty when it has
// never said. The hub falls back to its own configured one.
func (i *Inventory) Namespace(name string) string {
	if i == nil {
		return ""
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.environments[name].namespace
}

// BackupStorage returns the BackupStorage name a spoke reported, or empty
// when it has never said. The caller falls back to the hub default.
func (i *Inventory) BackupStorage(name string) string {
	if i == nil {
		return ""
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.environments[name].storage
}

// MonitoringConfig returns the MonitoringConfig a spoke reported on its last
// beat, or empty when it has none.
func (i *Inventory) MonitoringConfig(name string) string {
	if i == nil {
		return ""
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.environments[name].last.MonitoringConfig
}

// ServesEngine reports whether a spoke's last beat named the engine.
func (i *Inventory) ServesEngine(name, engineID string) bool {
	if i == nil {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	return slices.Contains(i.environments[name].last.Engines, engineID)
}

// PMMReport says what a spoke's last beat reported about monitoring: whether
// it has any, and whether its agent carries the alert rules mark.
func (i *Inventory) PMMReport(name string) (monitoring, alertRules bool) {
	if i == nil {
		return false, false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	last := i.environments[name].last

	return last.MonitoringConfig != "", last.AlertRules
}

// MonitoringAddress returns the address a spoke reported for monitoring on its
// last beat, or empty when it has none.
func (i *Inventory) MonitoringAddress(name string) string {
	if i == nil {
		return ""
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.environments[name].last.MonitoringAddress
}

// Forget drops an environment. A row appears the moment a token is minted, so
// every typo and every abandoned attempt is one, and nothing else removes it.
func (i *Inventory) Forget(ctx context.Context, name string) error {
	if i == nil || name == "" {
		return nil
	}
	i.mu.Lock()
	delete(i.environments, name)
	delete(i.lastPersist, name)
	i.mu.Unlock()

	if i.persist == nil {
		return nil
	}

	return i.persist.Remove(ctx, name)
}

// Snapshot returns the last heartbeat for an environment.
func (i *Inventory) Snapshot(name string) (Heartbeat, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()

	cur, ok := i.environments[name]
	if !ok || !cur.seen {
		return Heartbeat{}, false
	}

	return cur.last, true
}

func (i *Inventory) snapshot(name string) (persistedEnvironment, bool) {
	if i == nil || name == "" {
		return persistedEnvironment{}, false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	cur, ok := i.environments[name]
	if !ok {
		return persistedEnvironment{}, false
	}

	return i.recordLocked(name, cur), true
}

func (i *Inventory) restore(name string, rec persistedEnvironment) error {
	if i == nil || name == "" {
		return nil
	}
	i.mu.Lock()
	cur := i.environments[name]
	if rec.Namespace != "" {
		cur.namespace = rec.Namespace
	}
	if rec.BackupStorage != "" {
		cur.storage = rec.BackupStorage
	}
	if rec.Repo != "" {
		cur.repo = rec.Repo
	}
	cur.seen = rec.Seen
	cur.last = rec.Heartbeat
	cur.pending = append([]PendingBackup(nil), rec.PendingBackups...)
	i.environments[name] = cur
	i.mu.Unlock()

	return i.saveRecord(name, rec)
}

func (i *Inventory) view(name string, cur registered, now time.Time) Environment {
	c := Environment{Name: name, Namespace: cur.namespace, Status: StatusUnreachable}
	if !cur.seen {
		return c
	}
	c.LastSeen = cur.last.At
	c.Monitoring = cur.last.MonitoringConfig != ""
	if now.Sub(cur.last.At) <= i.staleAfter {
		c.Status = StatusConnected
	}

	return c
}

func (i *Inventory) recordLocked(name string, cur registered) persistedEnvironment {
	rec := persistedEnvironment{
		Namespace:      cur.namespace,
		BackupStorage:  cur.storage,
		Repo:           cur.repo,
		Seen:           cur.seen,
		Heartbeat:      cur.last,
		PendingBackups: append([]PendingBackup(nil), cur.pending...),
	}
	if rec.Heartbeat.Environment == "" {
		rec.Heartbeat.Environment = name
	}
	return rec
}

// persistRecord writes one environment's row. urgent skips the pacing: losing a
// pending backup or a restore intent to a restart is a lost operation, while
// losing a few seconds of lastSeen is not.
func (i *Inventory) persistRecord(name string, rec persistedEnvironment) {
	_ = i.persistRecordAt(name, rec, true)
}

func (i *Inventory) persistRecordAt(name string, rec persistedEnvironment, urgent bool) error {
	if i == nil || i.persist == nil || name == "" {
		return nil
	}
	if !urgent {
		now := time.Now()
		i.mu.Lock()
		last, seen := i.lastPersist[name]
		if seen && now.Sub(last) < persistEvery {
			i.mu.Unlock()

			return nil
		}
		i.lastPersist[name] = now
		i.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(context.Background(), inventoryPersistTimeout)
	defer cancel()
	if err := i.persist.Save(ctx, name, rec); err != nil {
		slog.Warn("hub inventory persist failed", slog.String("environment", name), slog.String("err", err.Error()))

		return err
	}

	return nil
}

func stripHeartbeat(beat Heartbeat) Heartbeat {
	// Events and samples are recorded into history by the caller. The
	// inventory snapshot is the databases, not the log.
	beat.Events = nil
	beat.Usage = nil
	beat = bindHeartbeat(beat)
	if len(beat.Instances) == 0 {
		beat.Instances = nil

		return beat
	}
	out := make([]engine.Instance, len(beat.Instances))
	copy(out, beat.Instances)
	for i := range out {
		// A public database keeps its connection string in memory so the hub
		// can hand it over on copy. Every other password, a private
		// database's included, stays on the environment.
		if out[i].Connection == nil || (beat.PrivateAccess && out[i].Exposure.PubliclyReachable()) {
			continue
		}
		conn := *out[i].Connection
		conn.URI = ""
		out[i].Connection = &conn
	}
	beat.Instances = out

	return beat
}

// persistableHeartbeat is the snapshot as it may be stored. A connection
// string carries a password, so none is written to the database or the
// inventory ConfigMap: a public database's lives in memory until the next
// beat, and the copy route answers that it is not ready after a restart.
func persistableHeartbeat(beat Heartbeat) Heartbeat {
	beat = stripHeartbeat(beat)
	if len(beat.Instances) == 0 {
		return beat
	}
	out := make([]engine.Instance, len(beat.Instances))
	copy(out, beat.Instances)
	for i := range out {
		if out[i].Connection == nil || out[i].Connection.URI == "" {
			continue
		}
		conn := *out[i].Connection
		conn.URI = ""
		out[i].Connection = &conn
	}
	beat.Instances = out

	return beat
}

// bindHeartbeat rebuilds every instance ID and Environment from the authenticated
// spoke name. The body is otherwise trusted, and a token scoped to one environment
// could plant another environment's IDs into lists and later operator actions.
func bindHeartbeat(beat Heartbeat) Heartbeat {
	environment := strings.TrimSpace(beat.Environment)
	if !ValidEnvironmentName(environment) {
		beat.Instances = nil

		return beat
	}
	beat.Environment = environment
	ns := strings.TrimSpace(beat.Namespace)
	if ns != "" && !ValidName(ns) {
		ns = ""
		beat.Namespace = ""
	} else {
		beat.Namespace = ns
	}
	if len(beat.Instances) == 0 {
		beat.Instances = nil

		return beat
	}
	out := make([]engine.Instance, 0, len(beat.Instances))
	for _, inst := range beat.Instances {
		name := strings.TrimSpace(inst.Name)
		if !ValidName(name) {
			continue
		}
		instNS := strings.TrimSpace(inst.Namespace)
		if instNS == "" {
			instNS = ns
		}
		if !ValidName(instNS) {
			continue
		}
		inst.Name = name
		inst.Namespace = instNS
		inst.Environment = environment
		inst.ID = JoinID(environment, instNS, name)
		out = append(out, inst)
	}
	beat.Instances = out

	return beat
}

// AddPendingBackup records a backup the hub accepted until a heartbeat
// reports succeeded or failed.
func (i *Inventory) AddPendingBackup(environment, database string, backup engine.Backup) {
	if i == nil || environment == "" || backup.ID == "" {
		return
	}
	i.mu.Lock()
	cur := i.environments[environment]
	for _, p := range cur.pending {
		if p.Backup.ID == backup.ID {
			i.mu.Unlock()

			return
		}
	}
	cur.pending = append(cur.pending, PendingBackup{Database: database, Backup: backup})
	i.environments[environment] = cur
	rec := i.recordLocked(environment, cur)
	i.mu.Unlock()
	i.persistRecord(environment, rec)
}

// PendingBackups returns on-demand backups the hub still lists, including
// ones it marked failed after the retry cap.
func (i *Inventory) PendingBackups(environment string) []PendingBackup {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	cur, ok := i.environments[environment]
	if !ok || len(cur.pending) == 0 {
		return nil
	}

	return append([]PendingBackup(nil), cur.pending...)
}

// ReconcilePendingBackups drops backups the spoke reported as succeeded or
// failed and returns pending ids still missing from the snapshot so the hub
// can re-queue work after a restart. A backup that exceeds the retry cap is
// kept as failed so GET /backups still lists it.
func (i *Inventory) ReconcilePendingBackups(environment string, observed []engine.Backup) []PendingBackup {
	if i == nil || environment == "" {
		return nil
	}
	seen := map[string]engine.Backup{}
	for _, b := range observed {
		seen[b.ID] = b
	}

	i.mu.Lock()
	cur := i.environments[environment]
	var keep []PendingBackup
	var missing []PendingBackup
	for _, p := range cur.pending {
		if p.Backup.Status == engine.BackupFailed {
			keep = append(keep, p)
			continue
		}
		got, ok := seen[p.Backup.ID]
		if ok && (got.Status == engine.BackupSucceeded || got.Status == engine.BackupFailed) {
			continue
		}
		if !ok {
			p.Attempts++
			if p.Attempts > pendingBackupAttempts {
				slog.Warn("hub gave up on a pending backup",
					slog.String("environment", environment),
					slog.String("database", p.Database),
					slog.String("backup", p.Backup.ID),
				)
				p.Backup.Status = engine.BackupFailed
				p.Backup.Message = "The environment has not reported this backup yet."
				keep = append(keep, p)

				continue
			}
			missing = append(missing, p)
		}
		keep = append(keep, p)
	}
	changed := pendingChanged(cur.pending, keep)
	cur.pending = keep
	i.environments[environment] = cur
	rec := i.recordLocked(environment, cur)
	i.mu.Unlock()
	if changed {
		i.persistRecord(environment, rec)
	}

	return missing
}

func pendingChanged(before, after []PendingBackup) bool {
	if len(before) != len(after) {
		return true
	}
	for i := range before {
		if before[i].Database != after[i].Database ||
			before[i].Attempts != after[i].Attempts ||
			before[i].Backup.ID != after[i].Backup.ID ||
			before[i].Backup.Status != after[i].Backup.Status {
			return true
		}
	}

	return false
}
