package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/oe"

	"sigs.k8s.io/yaml"
)

// ErrUnreachable means the spoke has not heartbeated inside the stale window.
var ErrUnreachable = errors.New("environment is unreachable")

// ErrEnvironmentRequired means a hub create omitted the environment field.
var ErrEnvironmentRequired = errors.New("environment is required")

// ErrNotSupported means hub mode has no path for that action, because the
// hub does not own the manifest the action would have to change.
var ErrNotSupported = errors.New("that action is not available in hub mode")

// ErrEngineNotSupported means the hub cannot write a manifest for that engine.
var ErrEngineNotSupported = errors.New("that engine is not available in hub mode")

// ErrAccessNotAvailable means a create asked for access from outside the
// Kubernetes cluster for an engine that has none yet.
var ErrAccessNotAvailable = errors.New("access from outside is not available for that engine")

// ErrEngineNotOnEnvironment means the environment has not reported a provider
// for the engine, so it needs an update before it can run one.
var ErrEngineNotOnEnvironment = errors.New("the environment does not run that engine")

// ErrRestoreNotSupported means restoring a backup into a new database is not
// available for the source's engine.
var ErrRestoreNotSupported = errors.New("restore to a new database is not available for that engine")

// ErrRestoring means the database is a PostgreSQL copy the agent is still
// seeding, or one whose seed ran and whose committed Instance no heartbeat
// lists yet. Deleting it in either state would orphan the seeded database.
var ErrRestoring = errors.New("database is still being restored")

// ErrInvalidEnvironment means the join name is not a DNS label, or is a name
// the hub UI already routes to a page of its own.
var ErrInvalidEnvironment = errors.New("environment name is not a DNS label")

// ErrPersist means hashed join tokens, inventory, or queued work could not
// be written.
var ErrPersist = errors.New("hub state could not be saved")

// ErrGitHost means GitHub could not create or update the environment repository.
var ErrGitHost = errors.New("github could not create or update that repository")

// ErrJoinToken means the bearer is not a live join token.
var ErrJoinToken = errors.New("join token is not valid")

func wrapPersist(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%w: %w", ErrPersist, err)
}

// SpokeBucket is the S3 bucket Connect creates for one spoke.
func SpokeBucket(environment string) string {
	return "pds-" + environment
}

// Options configure a hub Service.
type Options struct {
	Namespace     string
	BackupStorage string
	Now           func() time.Time
	Tokens        *TokenStore
	GitHost       GitHost
	CloneKeys     CloneKeyPersist
	// SupportAccess keeps each environment's support access record. Nil keeps
	// them in memory.
	SupportAccess SupportPersist
	// Tailnet is the private network support access runs over. Nil leaves
	// addresses and keys to be made by hand, as an environment joined by hand does.
	Tailnet Tailnet
	// OnSupportExpired is told, once per window, that the sweep closed it
	// because its time ran out, and which window it was. The hub has no
	// activity log of its own.
	OnSupportExpired func(ctx context.Context, environment string, window SupportWindow)
	// PMMChartVersion is the percona/pmm chart written into each spoke's PMM
	// definition. Empty uses DefaultPMMChartVersion.
	PMMChartVersion string
	// Context is the process's own, which ends at shutdown and parents the
	// background work a heartbeat starts. Nil means work that is never
	// cancelled.
	Context context.Context
}

// Service is the hub-mode database backend.
type Service struct {
	// life coordinates enrollment, disconnect, and the clone-key map.
	// Heartbeats do not take it, so a join that talks to GitHub cannot
	// mark unrelated spokes unreachable.
	life sync.Mutex
	// spokes serializes the write-then-rewrite-kustomization sequence for
	// one environment, and the heartbeat that must not recreate an environment
	// Disconnect just forgot. Two creates that interleave leave one
	// instance file committed but absent from resources, so Flux never
	// applies it and the hub shows it pending forever.
	spokes *spokeLocks
	// secrets holds the secret reads browsers are waiting on. It is memory
	// only and apart from the work queue, which is persisted.
	secrets          secretRelay
	store            Store
	inv              *Inventory
	ns               string
	storage          string
	now              func() time.Time
	tokens           *TokenStore
	work             *WorkQueue
	git              GitHost
	clonePersist     CloneKeyPersist
	support          SupportPersist
	tailnet          Tailnet
	supportExpired   func(ctx context.Context, environment string, window SupportWindow)
	cloneKeys        map[string]CloneKey
	cloneUnpersisted map[string]bool
	pmmVersion       string
	rulesGate        *alertRulesGate
	// restoreFailed holds the PostgreSQL copies whose seed failed, by
	// environment and name, with the reason the agent gave, so the developer
	// is told instead of waiting on a database that will not appear. A hub
	// restart forgets them.
	failMu        sync.Mutex
	restoreFailed map[string]string
	// supportEvents are what beats did to support access windows, until the
	// heartbeat route takes them to write up.
	supportEventsMu sync.Mutex
	supportEvents   map[string][]SupportEvent
	rulesWork       sync.WaitGroup
	lifetime        context.Context
}

// NewService wires Git desired state and the heartbeat cache.
func NewService(store Store, inv *Inventory, opts Options) *Service {
	ns := strings.TrimSpace(opts.Namespace)
	if ns == "" {
		ns = "pds"
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	tokens := opts.Tokens
	if tokens == nil {
		tokens = NewTokenStore(nil, 0)
	}

	lifetime := opts.Context
	if lifetime == nil {
		lifetime = context.Background()
	}
	support := opts.SupportAccess
	if support == nil {
		support = NewMemorySupportPersist()
	}

	queue := NewWorkQueue()
	// The queue's retry wait follows the hub's clock, so one moved by a test or
	// a replay moves both.
	queue.SetClock(now)

	return &Service{
		lifetime:         lifetime,
		spokes:           newSpokeLocks(),
		store:            store,
		inv:              inv,
		ns:               ns,
		storage:          opts.BackupStorage,
		now:              now,
		tokens:           tokens,
		work:             queue,
		git:              opts.GitHost,
		clonePersist:     opts.CloneKeys,
		support:          support,
		tailnet:          opts.Tailnet,
		supportExpired:   opts.OnSupportExpired,
		cloneKeys:        map[string]CloneKey{},
		cloneUnpersisted: map[string]bool{},
		pmmVersion:       strings.TrimSpace(opts.PMMChartVersion),
		rulesGate:        newAlertRulesGate(),
		restoreFailed:    map[string]string{},
		supportEvents:    map[string][]SupportEvent{},
	}
}

// namespaceFor is the namespace to write into an environment's manifests. A spoke
// installed with its own EVEREST_NAMESPACE reports it, and writing the hub's
// instead put the Instance somewhere OpenEverest on that environment does not
// manage, where it never reconciles.
func (s *Service) namespaceFor(environment string) string {
	if reported := strings.TrimSpace(s.inv.Namespace(environment)); reported != "" {
		return reported
	}

	return s.ns
}

// manifest reads a database's Instance manifest. A manifest path carries no
// namespace: it describes the database in the environment's own namespace, so
// an id in any other namespace has no manifest, and is reported as ErrNotFound
// rather than read by name.
func (s *Service) manifest(ctx context.Context, environment, namespace, name string) ([]byte, error) {
	if namespace != s.namespaceFor(environment) {
		return nil, ErrNotFound
	}

	return s.store.Get(ctx, instancePath(environment, name))
}

// storageFor is the BackupStorage name to stamp into an environment's manifests.
// A spoke reports its own on every heartbeat, because a kind spoke has MinIO
// where an EKS spoke has S3. The configured default covers a spoke that has
// not reported one yet.
func (s *Service) storageFor(environment string) string {
	if reported := strings.TrimSpace(s.inv.BackupStorage(environment)); reported != "" {
		return reported
	}

	return s.storage
}

// targetsFor is what a new manifest on this environment points at. The monitoring
// config is only the one the spoke reported: naming one the spoke does not
// have would leave the pmm-client with nowhere to report.
func (s *Service) targetsFor(environment string) environmentTargets {
	return environmentTargets{
		BackupStorage:    s.storageFor(environment),
		MonitoringConfig: strings.TrimSpace(s.inv.MonitoringConfig(environment)),
	}
}

// Create writes an Instance manifest for a connected environment.
func (s *Service) Create(ctx context.Context, in engine.CreateInput) (*engine.Instance, error) {
	environment := strings.TrimSpace(in.Environment)
	if environment == "" {
		return nil, ErrEnvironmentRequired
	}
	unlock := s.spokes.lock(environment)
	defer unlock()
	name := strings.TrimSpace(in.Name)
	flavor, err := flavorFor(in.Engine)
	if err != nil {
		return nil, err
	}
	// The HTTP role never reaches a plugin. An unchecked name becomes a path
	// segment in the desired-state tree and a metadata.name Flux has to apply.
	if !ValidName(name) {
		return nil, fmt.Errorf("%w: %q must be lowercase letters, numbers, and dashes", engine.ErrInvalidName, name)
	}
	if field, limit := createNameLimit(flavor); limit > 0 && len(name) > limit {
		return nil, &engine.BoundsError{
			Field: field, Unit: "characters", Value: len(name), Min: 1, Max: limit,
		}
	}
	if err := s.requireKnown(environment); err != nil {
		return nil, err
	}
	if err := s.requireEngine(environment, flavor); err != nil {
		return nil, err
	}
	path := instancePath(environment, name)
	if _, err := s.store.Get(ctx, path); err == nil {
		return nil, fmt.Errorf("%w: %q", engine.ErrExists, name)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	// Git is the hub's desired state, not the whole truth. A database the
	// spoke already runs without a file here would be silently resized to
	// this profile when Flux applied the new manifest over it.
	if s.seeding(environment, name) {
		return nil, fmt.Errorf("%w: %q on %s", engine.ErrExists, name, environment)
	}
	if s.observed(environment, s.namespaceFor(environment), name, JoinID(environment, s.namespaceFor(environment), name)) {
		return nil, fmt.Errorf("%w: %q on %s", engine.ErrExists, name, environment)
	}

	exposure, err := accessFor(flavor, in)
	if err != nil {
		return nil, err
	}

	ns := s.namespaceFor(environment)
	body, p, err := instanceYAML(name, ns, environment, flavor, strings.ToLower(strings.TrimSpace(in.Profile)),
		s.targetsFor(environment), in.Overrides, exposure)
	if err != nil {
		return nil, err
	}
	if err := s.store.Write(ctx, map[string][]byte{path: body}, "create "+name); err != nil {
		return nil, err
	}
	// The MySQL operator generates the users Secret the moment the database
	// exists, with symbols the exporter cannot read in the monitor password. A
	// MySQL Instance stays out of the index until the agent has written that
	// Secret first, so the item is owed before the index is rewritten. It is
	// queued after the manifest is written, so a hub that stops in between
	// never asks for a Secret for a database that does not exist. No other
	// engine reads that Secret, and the PostgreSQL delete removes only Secrets
	// named for the PostgreSQL database, so a PostgreSQL Instance is listed at once.
	credentials := Work{Kind: WorkCredentials, Database: ns + "." + name}
	if flavor.ID == oe.MySQL.ID {
		if err := wrapPersist(s.work.Push(environment, credentials)); err != nil {
			if rollback := s.store.Delete(ctx, []string{path}, "roll back "+name); rollback != nil {
				slog.Warn("hub create rollback failed",
					slog.String("path", path), slog.String("err", rollback.Error()))
			}

			return nil, err
		}
	}
	if err := s.rewriteKustomize(ctx, environment); err != nil {
		// The manifest is written but nothing references it, and the
		// duplicate check would match it on the retry, so a create that
		// exists nowhere would answer 409 forever.
		if rollback := s.store.Delete(ctx, []string{path}, "roll back "+name); rollback != nil {
			slog.Warn("hub create rollback failed",
				slog.String("path", path), slog.String("err", rollback.Error()))
		}
		s.cancelCredentials(environment, credentials.Database)

		return nil, err
	}
	// The database that now holds the name replaces a restore of it that
	// failed, which would otherwise come back once this one is deleted.
	s.forgetRestoreFailure(environment, name)

	out := engine.Instance{
		ID:          JoinID(environment, ns, name),
		Name:        name,
		Engine:      flavor.ID,
		Profile:     p.name,
		Namespace:   ns,
		Environment: environment,
		Status:      engine.Status{Phase: engine.PhasePending},
		CreatedAt:   s.now(),
		Sizing: engine.Sizing{
			Members:   int(p.members),
			CPUCores:  float64(p.cpu),
			MemoryGB:  float64(p.memoryGB),
			StorageGB: float64(p.storageGB),
		},
		Exposure: exposure,
	}

	return &out, nil
}

// Delete removes the Instance file so Flux can prune it, or asks the spoke
// agent to delete an instance that only exists in the heartbeat cache.
func (s *Service) Delete(ctx context.Context, id string) error {
	environment, ns, name, err := SplitID(id)
	if err != nil {
		return err
	}
	unlock := s.spokes.lock(environment)
	defer unlock()
	if err := s.requireKnown(environment); err != nil {
		return err
	}

	path := instancePath(environment, name)
	body, gitErr := s.manifest(ctx, environment, ns, name)
	if gitErr == nil && restoredFromYAML(body) && !s.observed(environment, ns, name, id) {
		// The seed already created the copy's database. Removing the manifest
		// before an Instance is listed would leave it running with no Instance
		// to delete it.
		return fmt.Errorf("%w: %q", ErrRestoring, id)
	}
	if errors.Is(gitErr, ErrNotFound) {
		if !s.observed(environment, ns, name, id) {
			// A copy still seeding is refused rather than cancelled: the agent
			// may already be creating its database, which nothing would adopt.
			if ns == s.namespaceFor(environment) && s.seeding(environment, name) {
				return fmt.Errorf("%w: %q", ErrRestoring, id)
			}
			// A copy whose seed failed has nothing on the environment, and
			// deleting it is how the developer clears the failure.
			if ns == s.namespaceFor(environment) && s.forgetRestoreFailure(environment, name) {
				return nil
			}

			return fmt.Errorf("%w: %q", engine.ErrNotFound, id)
		}
		return wrapPersist(s.work.Push(environment, Work{Kind: WorkDelete, Database: ns + "." + name}))
	}
	if gitErr != nil {
		return gitErr
	}
	if err := s.store.Delete(ctx, []string{path}, "delete "+name); err != nil {
		return err
	}
	if err := s.rewriteKustomizeAfterDelete(ctx, environment); err != nil {
		if rollback := s.store.Write(ctx, map[string][]byte{path: body}, "roll back delete "+name); rollback != nil {
			slog.Warn("hub delete rollback failed",
				slog.String("path", path), slog.String("err", rollback.Error()))
		}

		return err
	}
	// A database deleted while held never reaches the environment, and a
	// Secret written for it there would hold passwords nothing uses.
	s.cancelCredentials(environment, ns+"."+name)

	return nil
}

// cancelCredentials drops the credentials item for a database that will not
// be created after all. A failure leaves at worst an unused Secret on the
// environment, so it is logged rather than returned.
func (s *Service) cancelCredentials(environment, database string) {
	if err := s.work.Cancel(environment, WorkCredentials, database); err != nil {
		slog.Warn("hub could not drop the credentials work of a database",
			slog.String("environment", environment), slog.String("database", database),
			slog.String("err", err.Error()))
	}
}

// Backup asks the spoke to take an on-demand backup. The minted id is kept on
// the inventory record so GET /backups still lists it until the next
// heartbeat reports succeeded or failed. Backup CRs are not written to Git.
func (s *Service) Backup(ctx context.Context, id string) (*engine.Backup, error) {
	environment, ns, name, err := SplitID(id)
	if err != nil {
		return nil, err
	}
	unlock := s.spokes.lock(environment)
	defer unlock()
	if err := s.requireConnected(environment); err != nil {
		return nil, err
	}

	_, gitErr := s.manifest(ctx, environment, ns, name)
	if errors.Is(gitErr, ErrNotFound) {
		if !s.observed(environment, ns, name, id) {
			return nil, fmt.Errorf("%w: %q", engine.ErrNotFound, id)
		}
	} else if gitErr != nil {
		return nil, gitErr
	}

	// Second resolution meant a double click produced one id, so the second
	// request answered 202 for a backup that was never asked for.
	backupID := backupWorkID(name, s.now())
	pending := engine.Backup{ID: backupID, Status: engine.BackupPending, CreatedAt: s.now(), Instance: name}
	database := ns + "." + name
	if err := s.work.Push(environment, Work{Kind: WorkBackup, Database: database, BackupID: backupID}); err != nil {
		return nil, wrapPersist(err)
	}
	s.inv.AddPendingBackup(environment, database, pending)

	return &pending, nil
}

// RestoreIssued is the database a restore asked for.
type RestoreIssued struct {
	Environment string
	Name        string
	ID          string
}

// maxRestoreCopyName is the longest name a restored copy may carry.
//
// The provider names its restore object <copy>-datasource, and the PXC
// operator names the job restore-job-<restore>-<environment>, so the label it
// builds is 2*len(copy) + 24 characters. Kubernetes refuses a label over 63.
const maxRestoreCopyName = (63 - len("restore-job-") - len("-datasource") - 1) / 2

// maxPostgreSQLName is the longest name a PostgreSQL database may carry. The
// operator labels every member with the controller revision hash of its
// instance set, <name>-instance1-<4 characters>-<up to 10 characters>, and
// Kubernetes refuses a label over 63.
const maxPostgreSQLName = 63 - len("-instance1-xxxx-xxxxxxxxxx")

// MaxMongoDBName is the longest name a MongoDB database may carry. The provider
// derives the replica set's object names from it and refuses a longer one with
// a Failed phase and no message, so the limit is enforced before a manifest is
// written.
const MaxMongoDBName = 22

// createNameLimit is the longest name a create may carry for an engine, with
// the words that name the limit in the refusal. Zero means only the DNS label
// rule applies.
func createNameLimit(flavor oe.Flavor) (field string, limit int) {
	switch flavor.ID {
	case oe.PostgreSQL.ID:
		return "A PostgreSQL database name", maxPostgreSQLName
	case oe.MongoDB.ID:
		return "A MongoDB database name", MaxMongoDBName
	}

	return "", 0
}

// accessFor is the exposure a create writes. A flavor that carries the engine
// component alone has no load balancer to put it behind, so a request that
// names an access or an allow list is refused rather than ignored.
func accessFor(flavor oe.Flavor, in engine.CreateInput) (engine.Exposure, error) {
	if !flavor.EngineOnly {
		return engine.CheckAccess(in)
	}
	if in.Access != "" || len(in.SourceRanges) > 0 {
		return engine.Exposure{}, fmt.Errorf("%w: %s", ErrAccessNotAvailable, flavor.ID)
	}

	return engine.Exposure{}, nil
}

// requireEngine refuses an engine the environment's last beat did not name.
// MySQL and PostgreSQL ship in every release, so an agent that predates the
// field still runs them.
func (s *Service) requireEngine(environment string, flavor oe.Flavor) error {
	if flavor.ID == oe.MySQL.ID || flavor.ID == oe.PostgreSQL.ID {
		return nil
	}
	if !s.inv.ServesEngine(environment, flavor.ID) {
		return fmt.Errorf("%w: %s on %s", ErrEngineNotOnEnvironment, flavor.ID, environment)
	}

	return nil
}

// RestoreToNewDatabase writes a second Instance on the environment the source runs
// on, provisioned from the named backup. The source database is left as it is,
// which is why the restored copy needs a name of its own.
func (s *Service) RestoreToNewDatabase(ctx context.Context, id, backupID, name string) (*RestoreIssued, error) {
	environment, _, source, err := SplitID(id)
	if err != nil {
		return nil, err
	}
	unlock := s.spokes.lock(environment)
	defer unlock()
	name = strings.TrimSpace(name)
	// The name becomes a path segment in the desired-state tree and a
	// metadata.name Flux applies, so it is checked before anything is read.
	if !ValidName(name) {
		return nil, fmt.Errorf("%w: %q must be lowercase letters, numbers, and dashes", engine.ErrInvalidName, name)
	}
	if err := s.requireConnected(environment); err != nil {
		return nil, err
	}
	src, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// The copy is the source's engine: a PostgreSQL backup restored as a MySQL
	// database could not be consumed. An engine the catalog does not know is
	// refused before the backup, because no backup makes it work.
	flavor, err := flavorFor(src.Engine)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %w", ErrRestoreNotSupported, src.Engine, err)
	}
	postgres := flavor.ID == oe.PostgreSQL.ID
	// A MySQL copy is provisioned from a backup by a restore job whose name is
	// built from this one twice over, and a PostgreSQL copy gives every member
	// a label built from it. A label over 63 characters is refused, so a longer
	// name commits a manifest that comes up as an empty database minutes later.
	if postgres && len(name) > maxPostgreSQLName {
		return nil, &engine.RestoreNameTooLongError{
			Name: name, Limit: maxPostgreSQLName, Reason: engine.RestoreNameMembers,
		}
	}
	if !postgres && len(name) > maxRestoreCopyName {
		return nil, &engine.RestoreNameTooLongError{Name: name, Limit: maxRestoreCopyName}
	}
	// The source is one of the names this refuses, which is the point: a
	// restore that lands on it overwrites the database it was taken from.
	if err := s.requireAbsent(ctx, environment, name); err != nil {
		return nil, err
	}

	state := s.backupState(environment, source, backupID)
	if state != engine.BackupSucceeded {
		return nil, &engine.BackupSourceError{BackupID: backupID, State: state}
	}
	// A heartbeat-only instance has no catalog profile. Falling back to dev
	// restored a 250 GB database into a 20Gi volume, so rebuild it at the
	// size the spoke reported instead, and only give up when there is no
	// size either.
	profileName := strings.ToLower(strings.TrimSpace(src.Profile))
	dest, err := resolveProfile(flavor, profileName, engine.ResourceOverrides{})
	if err != nil {
		sized, ok := profileFromSizing(src.Sizing)
		if !ok {
			return nil, fmt.Errorf("%w: %s reports profile %q and no size, so there is nothing to restore it at",
				engine.ErrUnknownProfile, source, src.Profile)
		}
		dest = sized
		dest.flavor = flavor
	}

	// The copy gets no load balancer, even though a create would and even
	// when its source has one. A second load balancer is a second bill in the
	// customer's account, and a restore does not ask for one.
	ns := s.namespaceFor(environment)
	restoreFrom := backupID
	if postgres {
		// The provider refuses a dataSource on a PostgreSQL Instance, so the
		// agent creates the database with its dataSource first and the Instance
		// adopts it. The provider drops a backup storage that has no schedule,
		// and the copy must archive to a repository of its own afterwards.
		restoreFrom = ""
		dest.daily = true
	}
	body, _, err := buildInstanceYAML(name, ns, environment, dest, s.targetsFor(environment), restoreFrom, engine.Exposure{})
	if err != nil {
		return nil, err
	}
	if postgres {
		if body, err = markRestored(body, source); err != nil {
			return nil, err
		}
		// The Instance is committed when the agent reports the seed run, not
		// now. Flux applies a commit on its own schedule, and a database the
		// provider creates before the seed reaches it is bootstrapped empty.
		seed := Work{
			Kind: WorkSeed, Database: ns + "." + source, BackupID: backupID,
			Name: name, Instance: string(body),
		}
		if err := s.work.Push(environment, seed); err != nil {
			return nil, wrapPersist(err)
		}
		s.forgetRestoreFailure(environment, name)

		return &RestoreIssued{Environment: environment, Name: name, ID: JoinID(environment, ns, name)}, nil
	}
	if err := s.commitInstance(ctx, environment, name, body, "restore "+source+" into "+name); err != nil {
		return nil, err
	}

	return &RestoreIssued{Environment: environment, Name: name, ID: JoinID(environment, ns, name)}, nil
}

// commitInstance writes an Instance manifest and lists it in the index Flux
// reads. A manifest no index lists is a database that never appears, and
// leaving it behind also takes the name for good: the absent check matches it
// on the retry, so the same request would answer 409 from then on.
func (s *Service) commitInstance(ctx context.Context, environment, name string, body []byte, message string) error {
	path := instancePath(environment, name)
	if err := s.store.Write(ctx, map[string][]byte{path: body}, message); err != nil {
		return err
	}
	if err := s.rewriteKustomize(ctx, environment); err != nil {
		if rollback := s.store.Delete(ctx, []string{path}, "roll back "+name); rollback != nil {
			slog.Warn("hub restore rollback failed",
				slog.String("path", path), slog.String("err", rollback.Error()))
		}

		return err
	}

	return nil
}

// settleSeeds acts on the results of PostgreSQL seeds, and returns the results
// that may be acknowledged. A seed that ran releases the Instance it was
// holding. One that failed commits nothing, since the provider would create a
// healthy empty database under the copy's name, and is remembered so the
// developer sees the failure. A seed whose Instance cannot be committed is left
// unacknowledged, so it is handed out again and the commit retried. The beat
// itself is still recorded, as it is when a credentials release cannot write,
// so a Git outage does not make the environment look unreachable.
func (s *Service) settleSeeds(environment string, results []WorkResult) []WorkResult {
	owed := s.work.owed(environment, WorkSeed)
	if len(owed) == 0 {
		return results
	}
	ctx, cancel := context.WithTimeout(s.lifetime, releaseTimeout)
	defer cancel()
	kept := make([]WorkResult, 0, len(results))
	for _, result := range results {
		item, ok := owed[result.ID]
		if !ok {
			kept = append(kept, result)

			continue
		}
		if result.Error != "" {
			slog.Warn("hub seed failed",
				slog.String("environment", environment),
				slog.String("database", item.Name),
				slog.String("reason", result.Reason),
				slog.String("err", result.Error))
			s.failMu.Lock()
			s.restoreFailed[environment+"/"+item.Name] = result.Reason
			s.failMu.Unlock()
			kept = append(kept, result)

			continue
		}
		_, source, _ := strings.Cut(item.Database, ".")
		if err := s.commitInstance(ctx, environment, item.Name, []byte(item.Instance), "restore "+source+" into "+item.Name); err != nil {
			slog.Warn("hub could not commit a seeded database, so its seed is handed out again",
				slog.String("environment", environment),
				slog.String("database", item.Name),
				slog.String("err", err.Error()))

			continue
		}
		kept = append(kept, result)
	}

	return kept
}

// forgetRestoreFailure drops the failed seed of this name, and reports whether
// there was one.
func (s *Service) forgetRestoreFailure(environment, name string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()

	key := environment + "/" + name
	_, failed := s.restoreFailed[key]
	delete(s.restoreFailed, key)

	return failed
}

// forgetRestoreFailures drops every failed seed of one environment.
func (s *Service) forgetRestoreFailures(environment string) {
	s.failMu.Lock()
	defer s.failMu.Unlock()

	for key := range s.restoreFailed {
		if env, _, _ := strings.Cut(key, "/"); env == environment {
			delete(s.restoreFailed, key)
		}
	}
}

// restoreCopies is the PostgreSQL copies an environment has no Instance for
// yet: the ones waiting on their seed and the ones whose seed failed.
func (s *Service) restoreCopies(environment string) []engine.Instance {
	var out []engine.Instance
	for _, seed := range s.work.owed(environment, WorkSeed) {
		inst := s.pendingInstance(environment, seed.Name)
		inst.Engine = oe.PostgreSQL.ID
		if profile := profileFromInstanceYAML([]byte(seed.Instance)); profile != "" {
			inst.Profile = profile
		}
		out = append(out, inst)
	}
	s.failMu.Lock()
	defer s.failMu.Unlock()
	for key, reason := range s.restoreFailed {
		env, name, _ := strings.Cut(key, "/")
		if env != environment {
			continue
		}
		inst := s.pendingInstance(environment, name)
		inst.Engine = oe.PostgreSQL.ID
		inst.Status = failedRestoreStatus(reason)
		out = append(out, inst)
	}

	return out
}

// failedRestoreStatus is what a copy whose seed failed shows. A reason the hub
// does not know, or none because the agent predates it, keeps the sentence that
// covers every cause.
func failedRestoreStatus(reason string) engine.Status {
	status := engine.Status{Phase: engine.PhaseFailed}
	switch reason {
	case SeedBackupUnusable:
		status.Message = "The backup is missing or did not finish. Pick a finished backup and restore again."
	case SeedSourceGone:
		status.Message = "The database this backup was taken from no longer exists. Restore a backup of a database that still exists."
	case SeedUnavailable:
		status.Message = "This environment cannot restore a PostgreSQL backup yet. Contact Percona support."
	case SeedOther:
		status.Message = "The restore could not start. Contact Percona support."
	default:
		status.Message = "The restore could not start from that backup. Check that the backup finished and its source database still exists, then restore again."
	}

	return status
}

// List returns Git desired instances overlaid with the last heartbeat snapshot.
func (s *Service) List(ctx context.Context, environment string) ([]engine.Instance, error) {
	byID := map[string]engine.Instance{}
	for _, c := range s.inv.List(s.now()) {
		if environment != "" && c.Name != environment {
			continue
		}
		desired, err := s.desiredInstances(ctx, c.Name)
		if err != nil {
			return nil, err
		}
		for _, inst := range s.restoreCopies(c.Name) {
			byID[inst.ID] = inst
		}
		for _, inst := range desired {
			byID[inst.ID] = inst
		}
		snap, ok := s.inv.Snapshot(c.Name)
		if !ok {
			continue
		}
		for _, inst := range snap.Instances {
			if prev, ok := byID[inst.ID]; ok {
				if strings.TrimSpace(inst.Profile) == "" && prev.Profile != "" {
					inst.Profile = prev.Profile
				}
				keepDesiredExposure(&inst, prev.Exposure)
			}
			overlayLastBackup(&inst, snap.Backups)
			byID[inst.ID] = inst
		}
	}

	out := make([]engine.Instance, 0, len(byID))
	for _, inst := range byID {
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out, nil
}

// Get returns one instance from the heartbeat cache, or a pending Git object.
func (s *Service) Get(ctx context.Context, id string) (*engine.Instance, error) {
	environment, namespace, name, err := SplitID(id)
	if err != nil {
		return nil, err
	}
	snap, ok := s.inv.Snapshot(environment)
	if ok {
		for i := range snap.Instances {
			// A same-named database in another namespace is not this one, and
			// must not shadow a pending manifest for the requested id.
			other := snap.Instances[i].Namespace != "" && snap.Instances[i].Namespace != namespace
			if (snap.Instances[i].Name == name && !other) || snap.Instances[i].ID == id {
				inst := snap.Instances[i]
				// An agent older than external access reports no exposure, and
				// one older than private access reads a private load balancer
				// as public, so anything short of private is checked against
				// the manifest.
				if strings.TrimSpace(inst.Profile) == "" || !inst.Exposure.Private {
					if body, err := s.manifest(ctx, environment, namespace, name); err == nil {
						if profile := profileFromInstanceYAML(body); profile != "" && strings.TrimSpace(inst.Profile) == "" {
							inst.Profile = profile
						}
						keepDesiredExposure(&inst, exposureFromInstanceYAML(body))
					}
				}
				if strings.TrimSpace(inst.Profile) == "" {
					inst.Profile = "custom"
				}
				overlayLastBackup(&inst, snap.Backups)

				return &inst, nil
			}
		}
	}
	body, err := s.manifest(ctx, environment, namespace, name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			for _, inst := range s.restoreCopies(environment) {
				if inst.ID == id {
					return &inst, nil
				}
			}

			return nil, fmt.Errorf("%w: %q", engine.ErrNotFound, id)
		}

		return nil, err
	}
	inst := s.pendingInstance(environment, name)
	inst.Engine = engineFromInstanceYAML(body)
	if profile := profileFromInstanceYAML(body); profile != "" {
		inst.Profile = profile
	}
	inst.Exposure = exposureFromInstanceYAML(body)

	return &inst, nil
}

// Backups returns heartbeat backups for one instance, union still-pending ids
// the hub accepted that the spoke has not yet reported as succeeded or failed.
func (s *Service) Backups(ctx context.Context, id string) ([]engine.Backup, error) {
	environment, namespace, name, err := SplitID(id)
	if err != nil {
		return nil, err
	}
	byID := map[string]engine.Backup{}
	snap, ok := s.inv.Snapshot(environment)
	known := false
	elsewhere := false
	if ok {
		for _, inst := range snap.Instances {
			// A reported database with this name in another namespace is a
			// different database, and so are its backups.
			if inst.ID == id || (inst.Name == name && (inst.Namespace == "" || inst.Namespace == namespace)) {
				known = true
				break
			}
			if inst.Name == name {
				elsewhere = true
			}
		}
		for _, b := range snap.Backups {
			if (!elsewhere || known) && backupBelongsTo(b, name) {
				byID[b.ID] = b
			}
		}
	}
	for _, p := range s.inv.PendingBackups(environment) {
		if !backupBelongsTo(p.Backup, name) || p.Database != namespace+"."+name {
			continue
		}
		if _, exists := byID[p.Backup.ID]; exists {
			continue
		}
		byID[p.Backup.ID] = p.Backup
	}
	if !ok && len(byID) == 0 {
		return nil, fmt.Errorf("%w: %q", engine.ErrNotFound, id)
	}
	if !known {
		if _, err := s.manifest(ctx, environment, namespace, name); err != nil {
			if errors.Is(err, ErrNotFound) && len(byID) == 0 {
				return nil, fmt.Errorf("%w: %q", engine.ErrNotFound, id)
			}
			if !errors.Is(err, ErrNotFound) {
				return nil, err
			}
		}
	}

	out := make([]engine.Backup, 0, len(byID))
	for _, b := range byID {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })

	return out, nil
}

// backupBelongsTo decides which database a backup in a heartbeat list is for.
// The spoke reports the owner, and that is the only answer. A backup with no
// owner is ignored so an older spoke cannot attach a sibling's backup to this
// database during a rolling upgrade.
func backupBelongsTo(b engine.Backup, instance string) bool {
	if b.Instance == "" {
		return false
	}

	return b.Instance == instance
}

// requireKnown allows a write for an environment that has checked in at least once,
// reachable right now or not.
//
// The Git-backed operations are desired state. A create, a scale, a schedule
// and a delete are all a commit, the commit succeeds whether or not the spoke
// is listening, and Flux applies it when the spoke comes back. Refusing them
// while an environment is briefly unreachable throws away the main thing putting Git
// in the loop bought.
//
// An environment that has never beaten is still refused, and the zero LastSeen is
// what tells the two apart. A row can exist because the Git inventory lists it
// while no spoke has ever run there, and a manifest written for that environment
// sits in a tree nothing pulls, with nothing in the UI to explain it.
func (s *Service) requireKnown(environment string) error {
	got, ok := s.inv.Get(environment, s.now())
	if !ok || got.LastSeen.IsZero() {
		return fmt.Errorf("%w: %s", ErrUnreachable, environment)
	}

	return nil
}

// requireConnected demands a live spoke. It guards the operations that ride the
// heartbeat rather than Git: an on-demand backup and a restore are events, and
// there is no honest answer for an event nobody can carry.
func (s *Service) requireConnected(environment string) error {
	got, ok := s.inv.Get(environment, s.now())
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnreachable, environment)
	}
	if got.Status != StatusConnected {
		return fmt.Errorf("%w: %s", ErrUnreachable, environment)
	}

	return nil
}

// requireAbsent refuses a name the environment already runs, in Git or in its last
// heartbeat. A restore writes the manifest unconditionally, so a name already
// in use would be resized to the source profile and then restored over.
func (s *Service) requireAbsent(ctx context.Context, environment, name string) error {
	_, err := s.store.Get(ctx, instancePath(environment, name))
	if err == nil {
		return fmt.Errorf("%w: %q on %s", engine.ErrExists, name, environment)
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	if s.seeding(environment, name) ||
		s.observed(environment, s.namespaceFor(environment), name, JoinID(environment, s.namespaceFor(environment), name)) {
		return fmt.Errorf("%w: %q on %s", engine.ErrExists, name, environment)
	}

	return nil
}

// seeding reports whether a PostgreSQL copy of this name is waiting on its
// seed. It has no manifest yet, so the Git check cannot see it.
func (s *Service) seeding(environment, name string) bool {
	for _, seed := range s.work.owed(environment, WorkSeed) {
		if seed.Name == name {
			return true
		}
	}

	return false
}

// backupState is the state of one backup of one database. It takes the
// instance because a restore that matched an id anywhere on the environment would
// happily read another database's backup.
func (s *Service) backupState(environment, instance, backupID string) string {
	snap, ok := s.inv.Snapshot(environment)
	if ok {
		for _, b := range snap.Backups {
			if b.ID == backupID && backupBelongsTo(b, instance) {
				return b.Status
			}
		}
	}
	for _, p := range s.inv.PendingBackups(environment) {
		if p.Backup.ID == backupID && backupBelongsTo(p.Backup, instance) {
			return p.Backup.Status
		}
	}

	return ""
}

// SetWorkPersist gives the outbound queue durable storage, so a hub restart
// does not drop a delete or a scale the spoke had not collected yet, and loads
// whatever the previous process left owed.
func (s *Service) SetWorkPersist(ctx context.Context, p WorkPersist) error {
	s.work.SetPersist(p)

	return s.work.Load(ctx)
}

// releaseTimeout bounds the index write a credentials answer triggers. The
// agent gives up on a beat after 10s, and a reply it never reads strands the
// work handed out with it, so the write gets half of that. A release that runs
// over is not acknowledged and is tried again when the item is handed out
// again.
const releaseTimeout = 5 * time.Second

// releaseHeld lists the databases whose credentials the agent has answered
// for, and returns the results that may be acknowledged. An answer with an
// error releases the database too: the operator then generates the passwords
// itself, as it did before the hold existed, and a create never waits on an
// agent that cannot help. When the index write fails, those answers are left
// unacknowledged, so the item is handed out again and the release retried.
func (s *Service) releaseHeld(environment string, results []WorkResult) []WorkResult {
	owed := s.work.owed(environment, WorkCredentials)
	releasing := map[string]struct{}{}
	for _, result := range results {
		item, ok := owed[result.ID]
		if !ok {
			continue
		}
		_, name, _ := strings.Cut(item.Database, ".")
		releasing[name] = struct{}{}
		if result.Error != "" {
			slog.Warn("the agent could not prepare a database's credentials, so the operator generates them",
				slog.String("environment", environment), slog.String("database", item.Database),
				slog.String("cause", result.Error))
		}
	}
	if len(releasing) == 0 {
		return results
	}

	ctx, cancel := context.WithTimeout(s.lifetime, releaseTimeout)
	defer cancel()
	if err := s.writeKustomize(ctx, environment, false, releasing); err != nil {
		slog.Warn("hub could not list databases whose credentials are ready",
			slog.String("environment", environment), slog.String("err", err.Error()))
		kept := make([]WorkResult, 0, len(results))
		for _, result := range results {
			if _, ok := owed[result.ID]; !ok {
				kept = append(kept, result)
			}
		}

		return kept
	}

	return results
}

// Environments is the inventory list at the service clock.
func (s *Service) Environments() []Environment {
	return s.inv.List(s.now())
}

// GitRepo is the GitHub repository this environment's desired state lives in.
// Callers that mint a clone URL pass this rather than the environment name so a
// hub hands Flux the name it recorded for the environment.
func (s *Service) GitRepo(environment string) string {
	environment = strings.TrimSpace(environment)
	if s != nil {
		if namer, ok := s.git.(repoNamer); ok {
			return namer.Repo(environment)
		}
	}

	return environment
}

// repoNamer is a git host that names the repository of each environment, and
// repoAssigner one that names the repository of a new environment.
type (
	repoNamer interface {
		Repo(environment string) string
	}
	repoAssigner interface {
		Assign(ctx context.Context, environment string) error
	}
)

// recordedRepo is the repository to store on a record: the one the git host
// names for the environment, or blank when it has no names of its own.
func (s *Service) recordedRepo(environment string) string {
	if _, ok := s.git.(repoNamer); !ok {
		return ""
	}

	return s.GitRepo(environment)
}

// ApplyHeartbeat records a spoke snapshot and re-queues pending backups the
// beat did not yet include.
// LastInstances is the previous heartbeat's databases, copied so a later beat
// can diff phases without racing the inventory lock.
func (s *Service) LastInstances(name string) []engine.Instance {
	if s == nil || s.inv == nil {
		return nil
	}
	beat, ok := s.inv.Snapshot(name)
	if !ok {
		return nil
	}
	out := make([]engine.Instance, len(beat.Instances))
	copy(out, beat.Instances)

	return out
}

func (s *Service) ApplyHeartbeat(beat Heartbeat) {
	unlock := s.spokes.lock(beat.Environment)
	defer unlock()
	_ = s.applyHeartbeatLocked(beat)
}

// AcceptHeartbeat applies a beat and returns work under that environment's lock,
// which Disconnect also takes. authorize runs inside that lock so a token
// that was valid a moment ago cannot recreate an environment Disconnect just
// forgot.
// A failed acknowledgement persist is returned without handing more work,
// so the spoke retries those results instead of treating them as settled.
func (s *Service) AcceptHeartbeat(beat Heartbeat, authorize func() bool) ([]Work, bool, error) {
	unlock := s.spokes.lock(beat.Environment)
	defer unlock()
	if authorize != nil && !authorize() {
		return nil, false, nil
	}
	if _, known := s.inv.Get(beat.Environment, s.now()); !known {
		return nil, false, nil
	}
	if err := s.applyHeartbeatLocked(beat); err != nil {
		return nil, true, wrapPersist(err)
	}
	s.reconcileAlertRulesOnBeat(beat.Environment)

	return s.relaySupportKeys(beat.Environment, s.work.Take(beat.Environment)), true, nil
}

func (s *Service) applyHeartbeatLocked(beat Heartbeat) error {
	if _, known := s.inv.Get(beat.Environment, s.now()); !known {
		return nil
	}
	// Staleness is measured against this clock. A spoke with a fast clock
	// used to report a timestamp in the future, which made now.Sub(at)
	// negative and pinned a dead environment to connected for good.
	beat.At = s.now()
	// Settle what the last reply asked for before deciding what to ask next.
	settled := s.settleSeeds(beat.Environment, beat.Results)
	if err := s.work.Ack(beat.Environment, s.releaseHeld(beat.Environment, settled)); err != nil {
		return err
	}
	if err := s.reconcileSupport(&beat); err != nil {
		return err
	}
	// lastSeen-only beats use the one-minute persist floor. Pending backups
	// and restores still persist immediately through persistRecord.
	if err := s.inv.applyHeartbeat(beat, false); err != nil {
		return err
	}
	if beat.OmitInstances || beat.OmitBackups {
		return nil
	}
	for _, p := range s.inv.ReconcilePendingBackups(beat.Environment, beat.Backups) {
		if err := s.work.Push(beat.Environment, Work{Kind: WorkBackup, Database: p.Database, BackupID: p.Backup.ID}); err != nil {
			slog.Warn("hub could not persist a requeued backup",
				slog.String("environment", beat.Environment),
				slog.String("backup", p.Backup.ID),
			)
		}
	}

	return nil
}

// keepDesiredExposure fills exposure from the manifest when the spoke reported
// less than it asks for. An agent older than external access sends a zero
// Exposure, and one older than private access reads every load balancer as
// public. Flux applies the manifest whatever the agent says, so Git is the
// better answer. An address only the spoke can know is left as reported.
func keepDesiredExposure(inst *engine.Instance, desired engine.Exposure) {
	if !desired.External {
		return
	}
	if !inst.Exposure.External {
		inst.Exposure.External = true
		inst.Exposure.SourceRanges = desired.SourceRanges
	}
	if desired.Private {
		// The ranges an older agent read off a private load balancer are
		// the safety net, not an allow list anyone set.
		inst.Exposure.Private = true
		inst.Exposure.SourceRanges = nil
	}
}

func (s *Service) pendingInstance(environment, name string) engine.Instance {
	ns := s.namespaceFor(environment)

	return engine.Instance{
		ID:          JoinID(environment, ns, name),
		Name:        name,
		Engine:      defaultEngine,
		Profile:     "custom",
		Namespace:   ns,
		Environment: environment,
		Status:      engine.Status{Phase: engine.PhasePending},
	}
}

func (s *Service) desiredInstances(ctx context.Context, environment string) ([]engine.Instance, error) {
	paths, err := s.store.List(ctx, instanceDir(environment)+"/")
	if err != nil {
		return nil, err
	}
	var out []engine.Instance
	for _, path := range paths {
		base := pathBase(path)
		if base == "kustomization.yaml" || !strings.HasSuffix(base, ".yaml") {
			continue
		}
		name := strings.TrimSuffix(base, ".yaml")
		inst := s.pendingInstance(environment, name)
		body, err := s.store.Get(ctx, path)
		if err == nil {
			inst.Engine = engineFromInstanceYAML(body)
			if profile := profileFromInstanceYAML(body); profile != "" {
				inst.Profile = profile
			}
			inst.Exposure = exposureFromInstanceYAML(body)
		}
		out = append(out, inst)
	}

	return out, nil
}

// ErrIndexWouldEmpty is returned when rewriting a kustomize index would drop it
// from listing databases to listing none, outside a delete.
//
// Flux prunes what the rendered output no longer contains, so an empty index is
// an instruction to delete every database that Kustomization owns. A build
// failure aborts before prune, but an empty yet valid render does not, so this
// is the one wrong file the hub could write that destroys data.
var ErrIndexWouldEmpty = errors.New("refusing to empty a kustomize index that lists databases")

// rewriteKustomize regenerates an environment's instances index and refuses to empty
// one that currently lists something.
//
// The case it guards is not a bad commit, it is a blind read: a store that
// answers a listing with nothing and no error, which is what a hub whose Git
// volume failed to mount looks like from inside the process. Deleting is the
// one time emptying the index is correct, and that path calls
// rewriteKustomizeAfterDelete instead.
func (s *Service) rewriteKustomize(ctx context.Context, environment string) error {
	return s.writeKustomize(ctx, environment, false, nil)
}

// rewriteKustomizeAfterDelete is rewriteKustomize for the one caller allowed to
// produce an empty index, because removing the last database is exactly that.
func (s *Service) rewriteKustomizeAfterDelete(ctx context.Context, environment string) error {
	return s.writeKustomize(ctx, environment, true, nil)
}

// writeKustomize lists every manifest in the environment's instances directory
// except the databases still waiting on their credentials. releasing names
// databases whose credentials answer is being settled by this write, so they
// are listed even though their item is still owed. A database the index
// already lists is never held back: an item can stay owed after its release,
// when the acknowledgement was not saved, and Flux deletes what the index
// stops listing.
func (s *Service) writeKustomize(ctx context.Context, environment string, mayEmpty bool, releasing map[string]struct{}) error {
	dir := instanceDir(environment)
	paths, err := s.store.List(ctx, dir+"/")
	if err != nil {
		return err
	}
	held := map[string]struct{}{}
	for _, item := range s.work.owed(environment, WorkCredentials) {
		_, name, _ := strings.Cut(item.Database, ".")
		if _, ok := releasing[name]; !ok {
			held[name] = struct{}{}
		}
	}
	if len(held) > 0 {
		listed, err := s.indexResources(ctx, dir)
		if err != nil {
			return err
		}
		for _, resource := range listed {
			delete(held, strings.TrimSuffix(resource, ".yaml"))
		}
	}
	var resources []string
	for _, path := range paths {
		base := pathBase(path)
		if base == "kustomization.yaml" || !strings.HasSuffix(base, ".yaml") {
			continue
		}
		if _, ok := held[strings.TrimSuffix(base, ".yaml")]; ok {
			continue
		}
		resources = append(resources, base)
	}
	sort.Strings(resources)

	if len(resources) == 0 && !mayEmpty {
		listed, listErr := s.indexResources(ctx, dir)
		if listErr != nil {
			return listErr
		}
		if len(listed) > 0 {
			return fmt.Errorf("%w: %s lists %d", ErrIndexWouldEmpty, dir, len(listed))
		}
	}

	body, err := kustomizeYAML(resources)
	if err != nil {
		return err
	}

	return s.store.Write(ctx, map[string][]byte{dir + "/kustomization.yaml": body}, "kustomize "+dir)
}

// indexResources returns the resources the index on disk currently names. A
// missing index is a directory nothing has written yet, which names nothing
// rather than being an error.
func (s *Service) indexResources(ctx context.Context, dir string) ([]string, error) {
	body, err := s.store.Get(ctx, dir+"/kustomization.yaml")
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var doc struct {
		Resources []string `json:"resources"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		// An index we cannot read is not an index we may overwrite with an
		// empty one.
		return nil, fmt.Errorf("%w: %s is unreadable", ErrIndexWouldEmpty, dir)
	}

	return doc.Resources, nil
}

// Scale asks a spoke agent to resize an instance that only exists in the
// heartbeat cache. GitOps instances are still written as YAML elsewhere.
func (s *Service) Scale(ctx context.Context, id string, in engine.ScaleInput) error {
	environment, ns, name, err := SplitID(id)
	if err != nil {
		return err
	}
	unlock := s.spokes.lock(environment)
	defer unlock()
	if err := s.requireKnown(environment); err != nil {
		return err
	}

	path := instancePath(environment, name)
	body, gitErr := s.manifest(ctx, environment, ns, name)
	if gitErr != nil && !errors.Is(gitErr, ErrNotFound) {
		return gitErr
	}
	if errors.Is(gitErr, ErrNotFound) {
		if !s.observed(environment, ns, name, id) {
			// A copy still seeding is listed, so it is refused as Delete
			// refuses it rather than reported as missing.
			if ns == s.namespaceFor(environment) && s.seeding(environment, name) {
				return fmt.Errorf("%w: %q", ErrRestoring, id)
			}

			return fmt.Errorf("%w: %q", engine.ErrNotFound, id)
		}
		if err := s.checkHubScale(environment, name, id, nil, in); err != nil {
			return err
		}
		return wrapPersist(s.work.Push(environment, Work{
			Kind:      WorkScale,
			Database:  ns + "." + name,
			CPUCores:  in.CPUCores,
			MemoryGB:  in.MemoryGB,
			StorageGB: in.StorageGB,
			Replicas:  in.Replicas,
		}))
	}
	if err := s.checkHubScale(environment, name, id, body, in); err != nil {
		return err
	}

	// A hub-created instance lives in Git, so scaling it means rewriting
	// the manifest and letting Flux apply it. This used to answer 501 for
	// the only kind of instance the hub itself produces.
	updated, err := mutateInstanceYAML(body, func(spec *everest.InstanceSpec) error {
		return applyScale(spec, in)
	})
	if err != nil {
		return err
	}

	return s.store.Write(ctx, map[string][]byte{path: updated}, "scale "+name)
}

func (s *Service) checkHubScale(environment, name, id string, body []byte, in engine.ScaleInput) error {
	current, err := s.scaleStorageState(environment, name, id, body)
	if err != nil {
		return err
	}

	return oe.CheckScale(in, current, s.scaleBlockedByBackup(environment, name))
}

func (s *Service) scaleStorageState(environment, name, id string, body []byte) (oe.StorageState, error) {
	if len(body) > 0 {
		return storageStateFromYAML(body)
	}
	snap, ok := s.inv.Snapshot(environment)
	if !ok {
		return oe.StorageState{}, nil
	}
	for _, inst := range snap.Instances {
		if inst.Name == name || inst.ID == id {
			return oe.StorageState{Present: true, GB: inst.Sizing.StorageGB}, nil
		}
	}

	return oe.StorageState{}, nil
}

func (s *Service) scaleBlockedByBackup(environment, name string) bool {
	snap, ok := s.inv.Snapshot(environment)
	if ok {
		for _, b := range snap.Backups {
			if !backupBelongsTo(b, name) {
				continue
			}
			if b.Status == engine.BackupPending || b.Status == engine.BackupRunning {
				return true
			}
		}
	}
	for _, p := range s.inv.PendingBackups(environment) {
		if !backupBelongsTo(p.Backup, name) {
			continue
		}
		if p.Backup.Status == engine.BackupPending || p.Backup.Status == engine.BackupRunning {
			return true
		}
	}

	return false
}

// SetBackupSchedule writes the one schedule of a hub-created instance into its
// manifest. A nil in asks for the default daily schedule and leaves an already
// enabled schedule alone. A disabled in turns the schedule off and keeps its
// settings; with no schedule to turn off it writes nothing.
// An instance the hub did not create is not in Git, so there is nothing here
// to change and the caller is told so.
//
// The schedule returned is the one the manifest now asks for. The environment
// reports it back once it has applied the change.
func (s *Service) SetBackupSchedule(ctx context.Context, id string, in *engine.ScheduleInput) (*engine.BackupSchedule, error) {
	if in != nil {
		if err := in.Validate(); err != nil {
			return nil, err
		}
	}
	environment, ns, name, err := SplitID(id)
	if err != nil {
		return nil, err
	}
	unlock := s.spokes.lock(environment)
	defer unlock()
	if err := s.requireKnown(environment); err != nil {
		return nil, err
	}
	if ns != s.namespaceFor(environment) {
		return nil, fmt.Errorf("%w: %q", engine.ErrNotFound, id)
	}

	path := instancePath(environment, name)
	body, gitErr := s.store.Get(ctx, path)
	if errors.Is(gitErr, ErrNotFound) {
		return nil, fmt.Errorf("%w: %q is not managed from this hub", ErrNotSupported, name)
	}
	if gitErr != nil {
		return nil, gitErr
	}

	storage := s.storageFor(environment)
	var (
		result  *engine.BackupSchedule
		changed bool
	)
	updated, err := mutateInstanceYAML(body, func(spec *everest.InstanceSpec) error {
		existing := oe.ScheduleOf(spec.Backup)
		switch {
		case in == nil && existing != nil && !existing.Disabled:
			result = existing

			return nil

		case in != nil && !in.Enabled:
			backup, off := oe.DisableSchedule(spec.Backup)
			if !off {
				result = existing

				return nil
			}
			spec.Backup = backup

		default:
			want := engine.DefaultSchedule()
			if in != nil {
				want = *in
			}
			if err := applySchedule(spec, storage, want); err != nil {
				return err
			}
		}
		changed = true
		result = oe.ScheduleOf(spec.Backup)

		return nil
	})
	if err != nil {
		return nil, err
	}
	if !changed {
		return result, nil
	}

	if err := s.store.Write(ctx, map[string][]byte{path: updated}, "backup schedule "+name); err != nil {
		return nil, err
	}

	return result, nil
}

// IssueJoin mints a token bound to one environment name and writes empty Flux
// kustomize dirs so the spoke can pull before the first create. The environment
// is registered so a hub restart still lists it as unreachable until a beat.
func (s *Service) IssueJoin(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if !ValidEnvironmentName(name) {
		return "", fmt.Errorf("%w: %q", ErrInvalidEnvironment, name)
	}
	s.life.Lock()
	defer s.life.Unlock()
	unlock := s.spokes.lock(name)
	defer unlock()
	_, alreadyKnown := s.inv.Get(name, s.now())
	if !alreadyKnown {
		if assigner, ok := s.git.(repoAssigner); ok {
			if err := assigner.Assign(ctx, name); err != nil {
				return "", fmt.Errorf("%w: %w", ErrGitHost, err)
			}
		}
	}
	if s.git != nil {
		if err := s.git.EnsureRepo(ctx, name); err != nil {
			return "", fmt.Errorf("%w: %w", ErrGitHost, err)
		}
	}
	existing, err := s.store.List(ctx, "clusters/"+name+"/")
	if err != nil {
		return "", err
	}
	hadGit := len(existing) > 0
	if err := s.ensureSpokeGit(ctx, name); err != nil {
		return "", err
	}
	if err := s.inv.remember(name, s.ns, s.recordedRepo(name)); err != nil {
		s.abortJoin(ctx, name, alreadyKnown, hadGit)
		return "", wrapPersist(err)
	}
	token, err := s.tokens.Issue(ctx, name)
	if err != nil {
		s.abortJoin(ctx, name, alreadyKnown, hadGit)
		return "", wrapPersist(err)
	}

	return token, nil
}

// CloneIdentity returns the read-only SSH identity Flux on this environment
// should clone with. The org token is not part of it. A stored key is reused
// so a repair or hub restart does not drop the working GitHub deploy key.
func (s *Service) CloneIdentity(ctx context.Context, environment string) (identity, pub string, err error) {
	s.life.Lock()
	defer s.life.Unlock()

	return s.cloneIdentityLocked(ctx, environment)
}

// PreviewJoin reports the environment an unused invitation would enroll, without
// minting an agent or dropping the invitation. Dry-run uses this so a later
// join, including one on another machine, can still spend the token.
func (s *Service) PreviewJoin(ctx context.Context, token string) (environment string, err error) {
	if s == nil || s.tokens == nil {
		return "", ErrJoinToken
	}
	name, ok := s.tokens.InvitationEnvironment(ctx, token)
	if !ok {
		return "", ErrJoinToken
	}

	return name, nil
}

// Enroll authorizes a still-unused join token, mints the clone identity when
// asked, and issues an agent credential under the same lock Disconnect holds.
// The invitation is consumed here so a copied join command cannot mint a
// second lasting credential. The agent bearer cannot fetch clone keys.
func (s *Service) Enroll(ctx context.Context, token string, clone bool) (environment, identity, pub, agent string, err error) {
	s.life.Lock()
	defer s.life.Unlock()

	name, ok := s.tokens.InvitationEnvironment(ctx, token)
	if !ok {
		return "", "", "", "", ErrJoinToken
	}
	unlock := s.spokes.lock(name)
	defer unlock()
	if clone {
		identity, pub, err = s.cloneIdentityLocked(ctx, name)
		if err != nil {
			return name, "", "", "", err
		}
	}
	name, agent, err = s.tokens.IssueAgent(ctx, token)
	if err != nil {
		if errors.Is(err, ErrJoinToken) {
			return "", "", "", "", err
		}

		return name, "", "", "", wrapPersist(err)
	}

	return name, identity, pub, agent, nil
}

func (s *Service) cloneIdentityLocked(ctx context.Context, environment string) (identity, pub string, err error) {
	if s.git == nil {
		return "", "", fmt.Errorf("%w: no git host", ErrGitHost)
	}
	environment = strings.TrimSpace(environment)
	if environment == "" {
		return "", "", fmt.Errorf("%w: environment name is empty", ErrGitHost)
	}

	if stored, ok := s.cloneKeys[environment]; ok && stored.Identity != "" && stored.Pub != "" {
		if err := s.git.EnsureDeployKey(ctx, environment, stored.Pub); err != nil {
			return "", "", fmt.Errorf("%w: %w", ErrGitHost, err)
		}
		if s.cloneUnpersisted[environment] {
			if err := s.persistCloneLocked(ctx, environment, stored); err != nil {
				return "", "", err
			}
		}

		return stored.Identity, stored.Pub, nil
	}

	identity, pub, err = s.git.EnsureCloneKey(ctx, environment)
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrGitHost, err)
	}
	stored := CloneKey{Identity: identity, Pub: pub}
	s.cloneKeys[environment] = stored
	s.cloneUnpersisted[environment] = true
	if err := s.persistCloneLocked(ctx, environment, stored); err != nil {
		return "", "", err
	}

	return identity, pub, nil
}

func (s *Service) persistCloneLocked(ctx context.Context, environment string, stored CloneKey) error {
	if s.clonePersist == nil {
		delete(s.cloneUnpersisted, environment)

		return nil
	}
	if err := s.clonePersist.Save(ctx, environment, stored); err != nil {
		s.cloneUnpersisted[environment] = true

		return wrapPersist(err)
	}
	delete(s.cloneUnpersisted, environment)

	return nil
}

// LoadCloneKeys restores stored identities. A missing Secret is an empty map.
func (s *Service) LoadCloneKeys(ctx context.Context) error {
	if s == nil || s.clonePersist == nil {
		return nil
	}
	keys, err := s.clonePersist.Load(ctx)
	if err != nil {
		return wrapPersist(err)
	}
	s.life.Lock()
	defer s.life.Unlock()
	if s.cloneKeys == nil {
		s.cloneKeys = map[string]CloneKey{}
	}
	for environment, key := range keys {
		s.cloneKeys[environment] = key
	}

	return nil
}

func (s *Service) snapshotClone(environment string) (CloneKey, bool) {
	if s == nil || s.cloneKeys == nil {
		return CloneKey{}, false
	}
	key, ok := s.cloneKeys[environment]
	if !ok || key.Identity == "" || key.Pub == "" {
		return CloneKey{}, false
	}

	return key, true
}

func (s *Service) dropClone(ctx context.Context, environment string) error {
	if s.git != nil {
		if err := s.git.RevokeCloneKey(ctx, environment); err != nil {
			return fmt.Errorf("%w: %w", ErrGitHost, err)
		}
	}
	delete(s.cloneKeys, environment)
	delete(s.cloneUnpersisted, environment)
	if s.clonePersist != nil {
		if err := s.clonePersist.Remove(ctx, environment); err != nil {
			return wrapPersist(err)
		}
	}

	return nil
}

func (s *Service) restoreClone(ctx context.Context, environment string, key CloneKey) {
	if key.Identity == "" || key.Pub == "" {
		return
	}
	if s.cloneKeys == nil {
		s.cloneKeys = map[string]CloneKey{}
	}
	s.cloneKeys[environment] = key
	if s.clonePersist != nil {
		if err := s.clonePersist.Save(ctx, environment, key); err != nil {
			s.cloneUnpersisted[environment] = true
			slog.Warn("hub could not restore the clone identity after a failed disconnect",
				slog.String("environment", environment),
				slog.String("err", err.Error()),
			)
		} else {
			delete(s.cloneUnpersisted, environment)
		}
	}
	if s.git != nil {
		if err := s.git.EnsureDeployKey(ctx, environment, key.Pub); err != nil {
			slog.Warn("hub could not restore the GitHub deploy key after a failed disconnect",
				slog.String("environment", environment),
				slog.String("err", err.Error()),
			)
		}
	}
}

// abortJoin drops git and inventory this IssueJoin created. An environment that
// already had a tree or a row keeps both: reissuing a token is how an
// existing spoke is repaired, and deleting that tree would let Flux prune
// every database on it.
func (s *Service) abortJoin(ctx context.Context, environment string, alreadyKnown, hadGit bool) {
	if !hadGit {
		paths, err := s.store.List(ctx, "clusters/"+environment+"/")
		if err == nil && len(paths) > 0 {
			_ = s.store.Delete(ctx, paths, "abort join "+environment)
		}
	}
	if !alreadyKnown {
		_ = s.inv.Forget(ctx, environment)
	}
}

func (s *Service) ensureSpokeGit(ctx context.Context, environment string) error {
	body, err := yaml.Marshal(environmentFile{
		Name:      environment,
		Namespace: s.ns,
	})
	if err != nil {
		return err
	}
	if err := s.store.Write(ctx, map[string][]byte{
		environmentPath(environment): body,
	}, "seed environment metadata "+environment); err != nil {
		return err
	}
	// Only instances. Backups and restores travel the work queue and are
	// never written to Git, so seeding kustomize directories for them left
	// every spoke reconciling two trees that could only ever be empty.
	if err := s.rewriteKustomize(ctx, environment); err != nil {
		return err
	}

	return s.seedPMM(ctx, environment)
}

// seedPMM writes an environment's PMM definition the first time it joins. A rejoin
// leaves an existing one alone: the file may hold settings an operator chose
// since, and overwriting them with defaults would resize a running PMM.
func (s *Service) seedPMM(ctx context.Context, environment string) error {
	if _, err := s.store.Get(ctx, pmmDir(environment)+"/kustomization.yaml"); err == nil {
		return nil
	}
	files, err := pmmFiles(environment, DefaultPMMSettings(), s.pmmVersion, true)
	if err != nil {
		return err
	}

	return s.store.Write(ctx, files, "seed monitoring "+environment)
}

// Disconnect removes an environment from the hub: its inventory row, its tokens,
// the clone key Flux used to pull, and its support access window and address.
// A row appears the moment a token is minted, so every typo and every abandoned
// attempt was permanent, and the token stayed valid for as long as the Secret
// did.
//
// It reports whether the environment held any support access, even when a
// later step fails: that access is gone either way, and the caller writes the
// entry for it.
//
// It does not touch the environment itself. Whatever is running there keeps
// running, which is why the UI has to say so. Desired-state files stay in
// Git: deleting them while Flux still has prune: true, then restoring the
// deploy key on a later join of the same name, is an instruction to delete
// every Instance that Kustomization owned.
func (s *Service) Disconnect(ctx context.Context, environment string) (supportRemoved bool, err error) {
	environment = strings.TrimSpace(environment)
	if !ValidEnvironmentName(environment) {
		return false, fmt.Errorf("%w: %q", ErrInvalidEnvironment, environment)
	}
	s.life.Lock()
	defer s.life.Unlock()
	unlock := s.spokes.lock(environment)
	defer unlock()
	if _, known := s.inv.Get(environment, s.now()); !known {
		return false, fmt.Errorf("%w: %q", engine.ErrNotFound, environment)
	}

	// Access that points at the environment goes first, while the record still
	// says what to take back. Only a failure to read or drop the record stops
	// the removal.
	supportRemoved, err = s.removeSupportAccess(ctx, environment)
	if err != nil {
		return false, err
	}

	tokenSnap := s.tokens.snapshotEnvironment(environment)
	queued, flying := s.work.snapshot(environment)
	invSnap, hadInv := s.inv.snapshot(environment)
	cloneSnap, hadClone := s.snapshotClone(environment)
	restoreTokens := func() {
		if err := s.tokens.restoreGrants(ctx, tokenSnap); err != nil {
			slog.Warn("hub could not restore join tokens after a failed disconnect",
				slog.String("environment", environment),
				slog.String("err", err.Error()),
			)
		}
	}
	restoreClone := func() {
		if hadClone {
			s.restoreClone(ctx, environment, cloneSnap)
		}
	}

	if err := s.dropClone(ctx, environment); err != nil {
		restoreClone()

		return supportRemoved, err
	}

	if err := s.tokens.RevokeEnvironment(ctx, environment); err != nil {
		restoreClone()
		restoreTokens()

		return supportRemoved, wrapPersist(err)
	}

	if err := s.work.Clear(environment); err != nil {
		restoreClone()
		restoreTokens()

		return supportRemoved, wrapPersist(err)
	}

	if err := s.inv.Forget(ctx, environment); err != nil {
		restoreClone()
		restoreTokens()
		if err := s.work.restore(environment, queued, flying); err != nil {
			slog.Warn("hub could not restore queued work after a failed disconnect",
				slog.String("environment", environment),
				slog.String("err", err.Error()),
			)
		}
		if hadInv {
			if err := s.inv.restore(environment, invSnap); err != nil {
				slog.Warn("hub could not restore inventory after a failed disconnect",
					slog.String("environment", environment),
					slog.String("err", err.Error()),
				)
			}
		}

		return supportRemoved, wrapPersist(err)
	}
	s.forgetRestoreFailures(environment)

	return supportRemoved, nil
}

// JoinEnvironment reports the spoke a join token may register.
func (s *Service) JoinEnvironment(ctx context.Context, token string) (string, bool) {
	return s.tokens.Environment(ctx, token)
}

// SetWorkClock replaces the work queue's clock, so a test can reach a retry
// without waiting for one.
func (s *Service) SetWorkClock(now func() time.Time) {
	s.work.SetClock(now)
}

// MonitoringAddress is the address an environment's monitoring can be opened at, as
// that environment last reported it. Empty means none yet, or not exposed.
func (s *Service) MonitoringAddress(environment string) string {
	return strings.TrimSpace(s.inv.MonitoringAddress(environment))
}

// TakeWork drains outbound actions for one spoke.
func (s *Service) TakeWork(environment string) []Work {
	return s.work.Take(environment)
}

// observed is whether the environment's last heartbeat lists this database. A
// reported database with the same name in another namespace is a different one.
func (s *Service) observed(environment, namespace, name, id string) bool {
	snap, ok := s.inv.Snapshot(environment)
	if !ok {
		return false
	}
	for i := range snap.Instances {
		inst := snap.Instances[i]
		if inst.ID == id || (inst.Name == name && (inst.Namespace == "" || inst.Namespace == namespace)) {
			return true
		}
	}

	return false
}

func overlayLastBackup(inst *engine.Instance, backups []engine.Backup) {
	if inst == nil || inst.LastBackupAt != nil {
		return
	}
	var newest time.Time
	for _, b := range backups {
		if b.Status != engine.BackupSucceeded || !backupBelongsTo(b, inst.Name) {
			continue
		}
		at := b.FinishedAt
		if at.IsZero() {
			at = b.CreatedAt
		}
		if at.After(newest) {
			newest = at
		}
	}
	if newest.IsZero() {
		return
	}
	inst.LastBackupAt = &newest
}

// backupWorkID is the Backup CR name the spoke must create. Kubernetes
// labels cap that at 63 characters, so a 63-character database name cannot
// keep the whole timestamp suffix unless the name itself is truncated.
func backupWorkID(name string, now time.Time) string {
	suffix := strings.ReplaceAll(now.UTC().Format("20060102150405.000000000"), ".", "")
	const maxLabel = 63
	room := maxLabel - len(suffix) - 1
	if room < 1 {
		if len(suffix) > maxLabel {
			return suffix[:maxLabel]
		}

		return suffix
	}
	if len(name) > room {
		name = strings.TrimRight(name[:room], "-")
	}
	if name == "" {
		return suffix
	}

	return name + "-" + suffix
}

func pathBase(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return path
	}

	return path[i+1:]
}
