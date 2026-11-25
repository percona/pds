package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/percona/pds/adapter/internal/client/pmm"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/service"
)

// SpokeAPI is the local database API the agent drives from hub work.
type SpokeAPI interface {
	List(ctx context.Context) ([]engine.Instance, error)
	Backups(ctx context.Context, id string) ([]engine.Backup, error)
	Backup(ctx context.Context, id string) (*engine.Backup, error)
	BackupNamed(ctx context.Context, id, backupID string) (*engine.Backup, error)
	Delete(ctx context.Context, id string) error
	Scale(ctx context.Context, id string, in engine.ScaleInput) error
}

var _ SpokeAPI = (*service.Database)(nil)

// SecretReader reads a secret on this environment for the hub, which asked for it
// by kind. The hub never holds these values, so each read answers one
// browser request and is not kept.
type SecretReader interface {
	Read(ctx context.Context, kind string) (string, error)
}

// Options is everything the spoke heartbeat loop needs.
type Options struct {
	HubURL      string
	Token       string
	Environment string
	Namespace   string
	// Storage is the BackupStorage name this environment has registered, so the
	// hub writes manifests that name an object this spoke actually has.
	Storage   string
	Interval  time.Duration
	Databases SpokeAPI
	// Secrets answers the secret reads the hub asks for. Nil means this agent
	// cannot, and says so rather than leaving the request to time out.
	Secrets SecretReader
	HTTP    *http.Client
	Logger  *slog.Logger
	// Collect reads events and usage for the beat. Nil skips both.
	Collect func(ctx context.Context, namespace string, items []engine.Instance) ([]hub.HeartbeatEvent, []hub.HeartbeatUsage)
	// Monitor reads the local PMM for one database. It runs only for the
	// databases whose usage sample is due, so PMM is queried once per sample
	// rather than once per beat. Nil means this environment has no PMM.
	Monitor func(ctx context.Context, item engine.Instance) *hub.HeartbeatMonitoring
	// Alerts reads the firing alerts of this environment's own PMM. Nil means
	// there is no PMM to read, and the beat says so rather than omit the field.
	Alerts func(ctx context.Context) (pmm.AlertList, error)
	// MonitoringConfig is the MonitoringConfig new databases on this environment
	// report to. The hub names it in the manifests it writes here.
	MonitoringConfig string
	// MonitoringAddress reads the address the cloud gave monitoring's load
	// balancer, empty when it has none. Nil means this agent does not report
	// one.
	MonitoringAddress func(ctx context.Context) (string, error)
	// MonitoringExists reports whether MonitoringConfig is on the environment.
	// The beat names the config only once it is confirmed, so the hub never
	// writes a manifest pointing at one the hook has not created or that was
	// removed. An error is no answer and keeps the last confirmed one, so a
	// platform outage does not turn monitoring off for new databases. Nil
	// trusts MonitoringConfig as configured.
	MonitoringExists func(ctx context.Context) (bool, error)
	// Cleanup removes the Secrets the database platform leaves behind for a
	// database that is gone, and returns how many it removed. Nil means this
	// agent cannot, and leaves them.
	Cleanup func(ctx context.Context, namespace, name string) (int, error)
	// Credentials writes a new MySQL database's users Secret before the
	// operator generates one, so its passwords hold nothing the monitoring
	// exporter misreads. Nil means this agent cannot, and says so: the hub
	// then releases the database to the operator's own passwords.
	Credentials func(ctx context.Context, namespace, name string) error
	// Support runs support access windows on this environment. Nil means this
	// agent cannot, and says nothing about them.
	Support SupportWorkloads
	// supportConfirmations is what this agent did to windows that the hub has
	// not yet taken.
	supportConfirmations *supportConfirmations
	// cleanup remembers the PostgreSQL databases seen and the cleanups still
	// owed.
	cleanup *cleanupState
	// monitoring is the last answer from MonitoringExists and when it came.
	monitoring *monitoringSeen
	// MonitorTimeout bounds one beat's whole PMM pass, so a PMM that hangs
	// delays the heartbeat by this much at most. Zero means monitorTimeout.
	MonitorTimeout time.Duration
	// lastMonitor is the reading taken for each database's pending sample.
	// A beat the hub refused sends the same sample again, and reuses the
	// reading rather than asking PMM again.
	lastMonitor *map[string]monitorReading
	// lastUsage is the last time a sample was sent per database. The pointer
	// is shared across beats the way lastBackups is.
	lastUsage *map[string]time.Time
	// secretResults holds the answers to secret reads the hub asked for, until
	// the next beat carries them. A beat takes them out when it sends, so a
	// credential is posted once and never retried or kept.
	secretResults *[]hub.SecretResult
	// lastAddress is the last address read successfully, so one failed read
	// does not turn a working address back into none.
	lastAddress *string

	// lastBackups is the last backup list that listed without error, including
	// an empty list. backupsListed is whether that snapshot is valid. A later
	// list failure still posts a beat, and this snapshot is what goes on it so
	// the hub does not treat a probe failure as every backup vanishing.
	lastBackups   *[]engine.Backup
	backupsListed *bool
	// Ready reports whether this agent has reached the hub. Serve exposes
	// it on /readyz. It starts false and stays false until the first
	// successful heartbeat, so a misconfigured hub URL cannot pass a
	// rollout. After that, unreadyAfter consecutive failures take it down.
	Ready *atomic.Bool
}

// Serve listens for Kubernetes probes and heartbeats until ctx is done.
func Serve(ctx context.Context, cfg config.Config, logger *slog.Logger, opts Options) error {
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Logger == nil {
		opts.Logger = logger
	}
	addr := cfg.Addr
	if addr == "" {
		addr = ":8080"
	}

	// The process is alive whatever the hub is doing, so healthz is static.
	// readyz starts false: an agent that has never reached its hub must
	// not report Ready, or a rollout succeeds while the environment never
	// appears.
	ready := opts.Ready
	if ready == nil {
		ready = &atomic.Bool{}
		opts.Ready = ready
	}
	ready.Store(false)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"the hub is not answering"}`))

			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		opts.Logger.InfoContext(ctx, "spoke agent listening", slog.String("addr", addr), slog.String("environment", opts.Environment))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	runCh := make(chan error, 1)
	go func() {
		runCh <- Run(ctx, opts)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case err := <-runCh:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = httpServer.Shutdown(shutdownCtx)
		cancel()

		return err
	}
}

// Run posts observed database state to the hub until ctx is done.
func Run(ctx context.Context, opts Options) error {
	if opts.HubURL == "" || opts.Environment == "" {
		return fmt.Errorf("HUB_URL and ENVIRONMENT_NAME are required for the agent")
	}
	if opts.Interval <= 0 {
		opts.Interval = 15 * time.Second
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	if opts.lastBackups == nil {
		opts.lastBackups = new([]engine.Backup)
	}
	if opts.backupsListed == nil {
		opts.backupsListed = new(bool)
	}
	if opts.lastUsage == nil {
		opts.lastUsage = &map[string]time.Time{}
	}
	if opts.secretResults == nil {
		opts.secretResults = new([]hub.SecretResult)
	}
	if opts.lastAddress == nil {
		opts.lastAddress = new(string)
	}
	if opts.monitoring == nil {
		opts.monitoring = &monitoringSeen{}
	}
	if opts.cleanup == nil {
		opts.cleanup = newCleanupState()
	}
	if opts.supportConfirmations == nil {
		opts.supportConfirmations = &supportConfirmations{}
	}
	if opts.lastMonitor == nil {
		opts.lastMonitor = &map[string]monitorReading{}
	}
	if opts.MonitorTimeout <= 0 {
		opts.MonitorTimeout = monitorTimeout
	}

	// What the last beat's work produced. It rides along on the next beat,
	// and until the hub has it the hub keeps that work in flight.
	var results []hub.WorkResult
	var failures int
	joined := false

	for {
		expireSupport(ctx, opts, time.Now())
		var err error
		results, err = beat(ctx, opts, results)
		if err != nil {
			failures++
			// One warn line per interval for a hub that has been
			// unreachable for a week is tens of thousands of lines saying
			// the same thing, and a full inventory scan behind each one.
			if failures == 1 || failures%failureLogEvery == 0 {
				opts.Logger.WarnContext(ctx, "heartbeat failed",
					slog.Int("consecutive", failures),
					slog.String("err", err.Error()),
				)
			}
		} else {
			if joined && failures >= unreadyAfter && opts.Ready != nil {
				opts.Logger.InfoContext(ctx, "heartbeat recovered", slog.Int("afterFailures", failures))
			}
			failures = 0
			joined = true
		}
		if opts.Ready != nil {
			opts.Ready.Store(joined && failures < unreadyAfter)
		}

		wait := backoff(opts.Interval, failures)
		if failures == 0 && len(*opts.secretResults) > 0 {
			// A browser is waiting on these answers, so the next beat is not
			// left to the interval.
			wait = 0
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// Retry pacing. A failing hub is retried more slowly each time, up to a cap,
// so a spoke that cannot join does not scan its whole inventory every
// interval forever.
const (
	// workTimeout bounds one item, and workBudget bounds the whole batch, so
	// a wedged Everest call cannot outlast the hub's default 90s stale window
	// and mark a healthy spoke unreachable.
	workTimeout     = 20 * time.Second
	workBudget      = 60 * time.Second
	maxBackoff      = 5 * time.Minute
	failureLogEvery = 20
	// unreadyAfter is how many consecutive failures make a joined pod
	// report not ready. Below this a blip should not take the container
	// down. The threshold does not apply before the first success: a new
	// agent whose hub URL is wrong must stay unready.
	unreadyAfter = 4
)

func throttleUsage(last *map[string]time.Time, usage []hub.HeartbeatUsage, now time.Time) []hub.HeartbeatUsage {
	if last == nil || len(usage) == 0 {
		return usage
	}
	kept := make([]hub.HeartbeatUsage, 0, len(usage))
	for _, point := range usage {
		prev := (*last)[point.DatabaseID]
		if !prev.IsZero() && now.Sub(prev) < 5*time.Minute {
			continue
		}
		kept = append(kept, point)
	}

	return kept
}

// monitoringRecheck is how long an answer from MonitoringExists stands.
const monitoringRecheck = time.Minute

// monitoringSeen is the last MonitoringExists answer.
type monitoringSeen struct {
	at     time.Time
	exists bool
}

// reportedMonitoring is the MonitoringConfig this beat names, or empty.
func reportedMonitoring(ctx context.Context, opts Options, now time.Time) string {
	if opts.MonitoringConfig == "" || opts.MonitoringExists == nil {
		return opts.MonitoringConfig
	}
	seen := opts.monitoring
	if seen == nil {
		seen = &monitoringSeen{}
	}
	if seen.at.IsZero() || now.Sub(seen.at) >= monitoringRecheck {
		if exists, err := opts.MonitoringExists(ctx); err == nil {
			seen.exists = exists
		}
		seen.at = now
	}
	if !seen.exists {
		return ""
	}

	return opts.MonitoringConfig
}

// monitorTimeout is the default budget for one beat's PMM pass.
const monitorTimeout = 20 * time.Second

// monitorReading is a PMM reading and when it was taken.
type monitorReading struct {
	at      time.Time
	reading *hub.HeartbeatMonitoring
}

// monitorWorkers is how many databases one pass reads from PMM at once. Reads
// share the pass budget, and one at a time let a slow database early in the
// list use it up for every database after it, on every pass.
const monitorWorkers = 4

// monitorUsage attaches a PMM reading to each sample that is going out. A
// reading younger than the sample interval is reused, which is the case when
// the previous beat carrying it did not reach the hub. Reads run on
// monitorWorkers goroutines; each writes only its own sample, and the cache is
// written once they have all finished.
func monitorUsage(ctx context.Context, opts Options, items []engine.Instance, usage []hub.HeartbeatUsage, now time.Time) {
	if opts.Monitor == nil || len(usage) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, opts.MonitorTimeout)
	defer cancel()
	byID := make(map[string]engine.Instance, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}

	type read struct {
		index int
		item  engine.Instance
	}
	var reads []read
	for i := range usage {
		item, ok := byID[usage[i].DatabaseID]
		if !ok {
			continue
		}
		if opts.lastMonitor != nil {
			if cached, ok := (*opts.lastMonitor)[item.ID]; ok && now.Sub(cached.at) < 5*time.Minute {
				usage[i].Monitoring = cached.reading
				continue
			}
		}
		reads = append(reads, read{index: i, item: item})
	}

	queue := make(chan read)
	var wg sync.WaitGroup
	for range min(monitorWorkers, len(reads)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for next := range queue {
				usage[next.index].Monitoring = opts.Monitor(ctx, next.item)
			}
		}()
	}
	for _, next := range reads {
		queue <- next
	}
	close(queue)
	wg.Wait()

	if opts.lastMonitor != nil {
		for _, done := range reads {
			(*opts.lastMonitor)[done.item.ID] = monitorReading{at: now, reading: usage[done.index].Monitoring}
		}
	}
}

func commitUsage(last *map[string]time.Time, usage []hub.HeartbeatUsage, now time.Time) {
	if last == nil {
		return
	}
	for _, point := range usage {
		(*last)[point.DatabaseID] = now
	}
}

func backoff(interval time.Duration, failures int) time.Duration {
	if failures <= 0 {
		return interval
	}
	wait := interval
	for range failures - 1 {
		wait *= 2
		if wait >= maxBackoff {
			return maxBackoff
		}
	}
	if wait > maxBackoff {
		return maxBackoff
	}

	return wait
}

func beat(ctx context.Context, opts Options, pending []hub.WorkResult) ([]hub.WorkResult, error) {
	var instances []engine.Instance
	omitInstances := false
	if opts.Databases != nil {
		items, err := opts.Databases.List(ctx)
		if err != nil {
			// OpenEverest may still be coming up, and a restart can hit the
			// same outage. The environment still needs to register. An empty
			// list would replace the hub snapshot, so the beat omits
			// instances and the hub keeps what it already has.
			opts.Logger.WarnContext(ctx, "heartbeat list failed", slog.String("err", err.Error()))
			omitInstances = true
		} else {
			for _, item := range items {
				item.Environment = opts.Environment
				item.ID = hub.JoinID(opts.Environment, item.Namespace, item.Name)
				instances = append(instances, item)
			}
			sweepDeleted(ctx, opts, instances)
		}
	}

	var backups []engine.Backup
	backupFailed := false
	if opts.Databases != nil {
		if lister, ok := opts.Databases.(interface {
			NamespaceBackups(context.Context, string) ([]engine.Backup, error)
		}); ok && opts.Namespace != "" {
			listed, err := lister.NamespaceBackups(ctx, opts.Namespace)
			if err != nil {
				opts.Logger.WarnContext(ctx, "heartbeat backups failed", slog.String("err", err.Error()))
				backupFailed = true
			} else {
				allowed := map[string]struct{}{}
				for _, item := range instances {
					if item.Name != "" {
						allowed[item.Name] = struct{}{}
					}
				}
				for _, backup := range listed {
					if _, ok := allowed[backup.Instance]; ok {
						backups = append(backups, backup)
					}
				}
			}
		} else {
			for _, item := range instances {
				localID := item.Namespace + "." + item.Name
				itemBackups, err := opts.Databases.Backups(ctx, localID)
				if err != nil {
					opts.Logger.WarnContext(ctx, "heartbeat backups failed", slog.String("err", err.Error()))
					backupFailed = true
					continue
				}
				backups = append(backups, itemBackups...)
			}
		}
	}
	if omitInstances {
		// Instance list failed, so a "successful" backup list was filtered
		// against an empty allow set. Keep the last good cache.
		if opts.lastBackups != nil && opts.backupsListed != nil && *opts.backupsListed {
			backups = append([]engine.Backup(nil), (*opts.lastBackups)...)
		}
	} else if backupFailed {
		// A backup list that has never succeeded used to skip the beat
		// outright, so a spoke whose backup API was unavailable could never
		// register and the hub never saw the environment at all. The beat goes
		// out instead with OmitBackups set, which the hub reads as "keep the
		// snapshot you have" rather than "there are none", so nothing is
		// misreported and the environment still joins.
		if opts.lastBackups != nil && opts.backupsListed != nil && *opts.backupsListed {
			backups = append([]engine.Backup(nil), (*opts.lastBackups)...)
		} else {
			backups = nil
		}
	} else if opts.lastBackups != nil {
		stored := append([]engine.Backup(nil), backups...)
		*opts.lastBackups = stored
		if opts.backupsListed != nil {
			*opts.backupsListed = true
		}
	}

	now := time.Now().UTC()
	// The alert read shares the monitoring budget with the metric readings:
	// it runs beside them, so a hung PMM costs the beat one budget, not two.
	alertsDone := make(chan *hub.HeartbeatAlerts, 1)
	alertCtx, cancelAlerts := context.WithTimeout(ctx, opts.MonitorTimeout)
	defer cancelAlerts()
	go func() { alertsDone <- readAlerts(alertCtx, opts, now) }()

	var events []hub.HeartbeatEvent
	var usage []hub.HeartbeatUsage
	if opts.Collect != nil && !omitInstances {
		events, usage = opts.Collect(ctx, opts.Namespace, instances)
		usage = throttleUsage(opts.lastUsage, usage, now)
		monitorUsage(ctx, opts, instances, usage, now)
		markRefusedResizes(instances, events)
	}
	alerts := <-alertsDone
	secretResults := takeSecretResults(opts)
	support := supportReport(ctx, opts)
	payload, err := json.Marshal(hub.Heartbeat{
		Support:           support,
		SecretResults:     secretResults,
		MonitoringAddress: reportedAddress(ctx, opts),
		Environment:       opts.Environment,
		Namespace:         opts.Namespace,
		BackupStorage:     opts.Storage,
		MonitoringConfig:  reportedMonitoring(ctx, opts, now),
		Results:           pending,
		At:                now,
		Instances:         stripCredentials(instances),
		Backups:           backups,
		OmitInstances:     omitInstances,
		OmitBackups:       omitInstances || backupFailed,
		PrivateAccess:     true,
		AlertRules:        true,
		Alerts:            alerts,
		Events:            events,
		Usage:             usage,
	})
	if err != nil {
		return pending, err
	}

	url := opts.HubURL + "/api/internal/environments/" + opts.Environment + "/heartbeat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return pending, err
	}
	req.Header.Set("Content-Type", "application/json")
	if opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+opts.Token)
	}

	resp, err := opts.HTTP.Do(req)
	if err != nil {
		return pending, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return pending, err
	}
	// The hub took the results this beat carried, so they are settled even
	// if nothing else about this beat worked out. The usage throttle is
	// committed only after the hub accepts the beat, so a failed post
	// sends the same samples again.
	if resp.StatusCode < 300 {
		commitUsage(opts.lastUsage, usage, now)
		if support != nil {
			opts.supportConfirmations.sent(len(support.Confirmations))
		}
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode >= 300 {
		return pending, fmt.Errorf("heartbeat: hub answered %s", resp.Status)
	}
	var reply hub.HeartbeatReply
	if len(body) > 0 {
		if err := json.Unmarshal(body, &reply); err != nil {
			return nil, fmt.Errorf("heartbeat: hub body is not valid JSON: %w", err)
		}
	}

	answerSecrets(ctx, opts, reply.Secrets)

	var supportItems, databaseItems []hub.Work
	for _, item := range reply.Work {
		if isSupportWork(item) {
			supportItems = append(supportItems, item)
		} else {
			databaseItems = append(databaseItems, item)
		}
	}
	results := runSupportWork(ctx, opts, supportItems)
	if opts.Databases == nil {
		return results, nil
	}

	return append(results, applyWork(ctx, opts.Logger, opts.Databases, opts.Credentials, databaseItems)...), nil
}

// applyWork runs what the hub asked for and reports on each item, so the hub
// can stop holding it. An item nothing ran is left unreported and comes back.
// workDatabases is the slice of the database service the work loop calls. It
// exists so the loop can be tested without a live OpenEverest behind it.
type workDatabases interface {
	Backup(ctx context.Context, id string) (*engine.Backup, error)
	BackupNamed(ctx context.Context, id, backupID string) (*engine.Backup, error)
	Delete(ctx context.Context, id string) error
	Scale(ctx context.Context, id string, in engine.ScaleInput) error
}

func applyWork(
	ctx context.Context,
	logger *slog.Logger,
	databases workDatabases,
	credentials func(ctx context.Context, namespace, name string) error,
	items []hub.Work,
) []hub.WorkResult {
	if len(items) == 0 {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	results := make([]hub.WorkResult, 0, len(items))
	deadline := time.Now().Add(workBudget)
	for _, item := range items {
		remain := time.Until(deadline)
		if remain <= 0 {
			// Unreported items stay in flight on the hub and are handed
			// out again. Reporting them as errors would drop the work.
			break
		}
		timeout := workTimeout
		if remain < timeout {
			timeout = remain
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		var err error
		switch item.Kind {
		case hub.WorkBackup:
			if item.BackupID != "" {
				_, err = databases.BackupNamed(ctx, item.Database, item.BackupID)
			} else {
				_, err = databases.Backup(ctx, item.Database)
			}
		case hub.WorkDelete:
			err = databases.Delete(ctx, item.Database)
		case hub.WorkCredentials:
			err = prepareCredentials(ctx, credentials, item.Database)
		case hub.WorkScale:
			err = databases.Scale(ctx, item.Database, engine.ScaleInput{
				CPUCores:  item.CPUCores,
				MemoryGB:  item.MemoryGB,
				StorageGB: item.StorageGB,
				Replicas:  item.Replicas,
			})
		default:
			logger.WarnContext(ctx, "unknown hub work", slog.String("kind", item.Kind))
			results = append(results, hub.WorkResult{ID: item.ID, Error: "unknown kind " + item.Kind})
			cancel()

			continue
		}
		cancel()
		result := hub.WorkResult{ID: item.ID}
		if err != nil {
			logger.WarnContext(ctx, "hub work failed",
				slog.String("kind", item.Kind),
				slog.String("database", item.Database),
				slog.String("err", err.Error()),
			)
			result.Error = err.Error()
		}
		results = append(results, result)
	}

	return results
}

// prepareCredentials writes the users Secret for one "namespace.name"
// database.
func prepareCredentials(ctx context.Context, credentials func(context.Context, string, string) error, database string) error {
	if credentials == nil {
		return errors.New("this agent cannot write secrets on this Kubernetes cluster")
	}
	namespace, name, ok := strings.Cut(database, ".")
	if !ok || namespace == "" || name == "" {
		return fmt.Errorf("database %q is not namespace.name", database)
	}

	return credentials(ctx, namespace, name)
}

// stripCredentials drops the connection URI from every instance before the
// heartbeat leaves the environment, except a public database that already has its
// address.
//
// Host, port and username stay: the hub renders those and none of them is a
// secret. The URI carries the password. A beat that carried one for every
// database would put the whole fleet's credentials into hub memory. A public
// database is the exception, because the connection string is what a client
// outside the environment copies, and the hub has no other way to read the Secret.
// A private database is not: every MySQL database has one, and its address
// only answers inside the customer's VPC, where the Secret can be read.
// The caller's instances come from the engine and may be reused, so this
// copies rather than editing in place.
func stripCredentials(items []engine.Instance) []engine.Instance {
	if len(items) == 0 {
		return nil
	}

	out := make([]engine.Instance, len(items))
	copy(out, items)

	for i := range out {
		if out[i].Connection == nil || out[i].Exposure.PubliclyReachable() {
			continue
		}

		conn := *out[i].Connection
		conn.URI = ""
		out[i].Connection = &conn
	}

	return out
}

// secretReadTimeout bounds one secret read, so a slow API server cannot hold
// a beat past the hub's stale window.
const secretReadTimeout = 10 * time.Second

// takeSecretResults removes and returns the answers waiting for a beat. They
// are taken whether or not the post succeeds: the request they answer is
// short lived, and a credential has no business waiting around for a retry.
func takeSecretResults(opts Options) []hub.SecretResult {
	if opts.secretResults == nil || len(*opts.secretResults) == 0 {
		return nil
	}
	out := *opts.secretResults
	*opts.secretResults = nil

	return out
}

// answerSecrets reads what the hub asked for and queues the answers for the
// next beat. A failed read is an answer too, so the browser hears why instead
// of waiting out its timeout.
func answerSecrets(ctx context.Context, opts Options, requests []hub.SecretRequest) {
	if opts.secretResults == nil {
		return
	}
	for _, req := range requests {
		res := hub.SecretResult{ID: req.ID}
		if opts.Secrets == nil {
			res.Error = "this Kubernetes cluster cannot read secrets for the control plane"
		} else {
			readCtx, cancel := context.WithTimeout(ctx, secretReadTimeout)
			value, err := opts.Secrets.Read(readCtx, req.Kind)
			cancel()
			if err != nil {
				res.Error = err.Error()
			} else {
				res.Value = value
			}
		}
		*opts.secretResults = append(*opts.secretResults, res)
	}
}

// addressReadTimeout bounds the read of monitoring's Service, so a slow API
// server cannot hold a beat.
const addressReadTimeout = 5 * time.Second

// reportedAddress is the address this beat names for monitoring, or empty.
// An empty answer is believed, because it means the load balancer is gone. An
// error is not: the last good address is kept until a read succeeds.
func reportedAddress(ctx context.Context, opts Options) string {
	if opts.MonitoringAddress == nil {
		return ""
	}
	last := opts.lastAddress
	if last == nil {
		last = new(string)
	}

	readCtx, cancel := context.WithTimeout(ctx, addressReadTimeout)
	defer cancel()
	address, err := opts.MonitoringAddress(readCtx)
	if err != nil {
		return *last
	}
	*last = address

	return address
}
