package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/percona/pds/adapter/internal/client/kube"
)

// Work kinds the hub asks a spoke agent to run locally on the next heartbeat.
const (
	WorkBackup = "backup"
	WorkDelete = "delete"
	WorkScale  = "scale"
	// WorkSeed asks the agent to create the PostgreSQL database Name, ready to
	// restore the named backup of Database when its Instance appears.
	WorkSeed = "seed"
	// WorkCredentials asks the agent to write a new MySQL database's users
	// Secret. The hub keeps the database out of the environment's index until
	// the agent answers, so the Secret exists before the operator looks.
	WorkCredentials = "credentials"
	// WorkSupportOpen asks the agent to install a support access window's
	// workload, WorkSupportExtend to move its expiry and WorkSupportClose to
	// remove it.
	WorkSupportOpen   = "support_open"
	WorkSupportExtend = "support_extend"
	WorkSupportClose  = "support_close"
)

// workRetryAfter is how long an item stays in flight before the hub hands it
// out again. A spoke evicted between reading a reply and applying it never
// acknowledges, and without this the item is simply gone.
const workRetryAfter = 2 * time.Minute

// Work is one action for a spoke that can only dial out.
type Work struct {
	// ID is what the spoke acknowledges. The hub keeps an item in flight
	// until it hears back, so a lost reply is retried rather than dropped.
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Database string `json:"database"`
	BackupID string `json:"backupId,omitempty"`
	// Name is the database a seed creates. Database is the one it seeds from.
	Name string `json:"name,omitempty"`
	// Instance is the manifest a seed releases once the agent has run it. It
	// travels with the item so a hub restart does not lose it.
	Instance  string `json:"instance,omitempty"`
	CPUCores  *int   `json:"cpu,omitempty"`
	MemoryGB  *int   `json:"memoryGb,omitempty"`
	StorageGB *int   `json:"storageGb,omitempty"`
	Replicas  *int   `json:"replicas,omitempty"`
	// Window is the support access window a support item is about. The rest of
	// what the agent needs to start one rides with it, and the key rides only
	// on the reply: it is put on a copy at pickup and is never in a queued item.
	Window    string    `json:"window,omitempty"`
	Level     string    `json:"level,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	Address   string    `json:"address,omitempty"`
	// AddressHost is the address's full DNS name, which names the Secret the
	// access workload keeps its certificate in.
	AddressHost string `json:"addressHost,omitempty"`
	Key         string `json:"key,omitempty"`
}

// Why a seed failed, as the agent reports it. The hub switches on these and
// never on the error text, which is for the log.
const (
	// SeedBackupUnusable means the backup is missing or did not finish.
	SeedBackupUnusable = "backup"
	// SeedSourceGone means the database the backup was taken from is gone.
	SeedSourceGone = "source"
	// SeedUnavailable means the environment cannot seed a database at all.
	SeedUnavailable = "unavailable"
	// SeedOther is any other failure.
	SeedOther = "other"
)

// WorkResult is a spoke reporting what it did with one item.
type WorkResult struct {
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`
	// Reason is why a seed failed, one of the Seed constants. An agent that
	// predates it sends none.
	Reason string `json:"reason,omitempty"`
}

// HeartbeatReply is what the hub returns to a successful beat.
type HeartbeatReply struct {
	Work []Work `json:"work"`
	// Secrets are reads a browser is waiting on. They are not Work: nothing
	// persists them and nothing retries them.
	Secrets []SecretRequest `json:"secrets,omitempty"`
}

// WorkQueue holds outbound actions until the matching spoke heartbeats, and
// holds them again until that spoke says what happened.
type WorkQueue struct {
	mu       sync.Mutex
	items    map[string][]Work
	inflight map[string][]inflight
	now      func() time.Time
	persist  WorkPersist
}

type inflight struct {
	work Work
	sent time.Time
}

// NewWorkQueue starts empty.
func NewWorkQueue() *WorkQueue {
	return &WorkQueue{
		items:    map[string][]Work{},
		inflight: map[string][]inflight{},
		now:      time.Now,
	}
}

// SetClock replaces the queue's clock, so a test can reach a retry without
// waiting for one.
func (q *WorkQueue) SetClock(now func() time.Time) {
	if q == nil || now == nil {
		return
	}
	q.mu.Lock()
	q.now = now
	q.mu.Unlock()
}

func (q *WorkQueue) clock() time.Time {
	if q.now == nil {
		return time.Now()
	}

	return q.now()
}

// Push records work for one environment. A duplicate of an item already queued,
// or already out with a spoke, is ignored so a heartbeat that re-pushes
// pending backups does not double up. A persist failure rolls the item
// back so callers cannot treat it as accepted.
func (q *WorkQueue) Push(environment string, item Work) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, existing := range q.items[environment] {
		if sameWork(existing, item) {
			return nil
		}
	}
	for _, existing := range q.inflight[environment] {
		if sameWork(existing.work, item) {
			return nil
		}
	}
	if item.ID == "" {
		id, err := mintWorkID()
		if err != nil {
			return err
		}
		item.ID = id
	}
	q.items[environment] = append(q.items[environment], item)
	if err := q.saveLocked(environment); err != nil {
		q.items[environment] = q.items[environment][:len(q.items[environment])-1]
		if len(q.items[environment]) == 0 {
			delete(q.items, environment)
		}

		return err
	}

	return nil
}

// SetPersist attaches durable storage. Without one the queue is memory only,
// which is what a process with nowhere to persist it and most tests use.
func (q *WorkQueue) SetPersist(p WorkPersist) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.persist = p
}

// Load restores every environment's queue. Anything that was in flight when the
// hub stopped comes back as queued: handing work to a spoke is not the same as
// the spoke running it, and the spoke acknowledges by id, so the worst case is
// the item is offered again.
func (q *WorkQueue) Load(ctx context.Context) error {
	q.mu.Lock()
	persist := q.persist
	q.mu.Unlock()

	if persist == nil {
		return nil
	}

	loaded, err := persist.Load(ctx)
	if err != nil {
		// An older Role that cannot read the work ConfigMap will refuse
		// every write too. Detach that store so the hub still starts.
		// A timeout or API blip is not that case: keep persist attached
		// and fail Load so startup stays retryable.
		if errors.Is(err, kube.ErrForbidden) {
			q.mu.Lock()
			q.persist = nil
			q.mu.Unlock()
			slog.Warn("hub work queue is memory only, so a restart will drop queued work",
				slog.String("reason", err.Error()))

			return nil
		}

		return err
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	for environment, items := range loaded {
		if len(items) == 0 {
			continue
		}
		q.items[environment] = items
	}

	return nil
}

// mintWorkID returns an id that stays unique after a restart with an empty
// queue. A counter reconstructed from outstanding items reused wrk_1 as soon
// as the last item was acknowledged, so a delayed ack from that item could
// drop later work minted under the same id.
func mintWorkID() (string, error) { return mintID("wrk_") }

// mintID returns a random id with a prefix that says what it names.
func mintID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}

	return prefix + hex.EncodeToString(b[:]), nil
}

// saveLocked writes one environment's outstanding work. The caller holds the lock.
// Queued and in-flight items are stored together, because both are still owed
// to the spoke and a restart cannot tell them apart usefully.
func (q *WorkQueue) saveLocked(environment string) error {
	if q.persist == nil {
		return nil
	}

	owed := make([]Work, 0, len(q.items[environment])+len(q.inflight[environment]))
	owed = append(owed, q.items[environment]...)
	for _, item := range q.inflight[environment] {
		owed = append(owed, item.work)
	}

	ctx, cancel := context.WithTimeout(context.Background(), inventoryPersistTimeout)
	defer cancel()
	// A stalled ConfigMap write must not hold the queue mutex forever.
	if err := q.persist.Save(ctx, environment, owed); err != nil {
		slog.Warn("hub work queue not persisted", slog.String("environment", environment))

		return err
	}

	return nil
}

// Take hands one environment its queued work, plus anything it was handed before
// and never acknowledged. Items stay in flight until Ack or the next retry.
func (q *WorkQueue) Take(environment string) []Work {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := q.clock()
	out := make([]Work, 0, len(q.items[environment]))

	kept := q.inflight[environment][:0]
	for _, item := range q.inflight[environment] {
		if now.Sub(item.sent) < workRetryAfter {
			kept = append(kept, item)

			continue
		}
		item.sent = now
		kept = append(kept, item)
		out = append(out, item.work)
	}
	q.inflight[environment] = kept

	for _, item := range q.items[environment] {
		out = append(out, item)
		q.inflight[environment] = append(q.inflight[environment], inflight{work: item, sent: now})
	}
	delete(q.items, environment)

	return out
}

// Ack drops the items a spoke reported on. An item the spoke failed is
// dropped too: the hub does not know whether retrying it is safe, and the
// heartbeat that carries the failure also carries the state it produced.
//
// After a restart, outstanding work is queued rather than in flight. The
// spoke still sends retained results first, so acknowledgement has to
// remove matching IDs from both lists. A persist failure restores both so
// an acknowledgement is not treated as durable until the ConfigMap update
// succeeds.
func (q *WorkQueue) Ack(environment string, results []WorkResult) error {
	if len(results) == 0 {
		return nil
	}
	done := make(map[string]struct{}, len(results))
	for _, result := range results {
		if result.ID != "" {
			done[result.ID] = struct{}{}
		}
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	prevFlying := append([]inflight(nil), q.inflight[environment]...)
	prevQueued := append([]Work(nil), q.items[environment]...)

	keptFlying := q.inflight[environment][:0]
	for _, item := range q.inflight[environment] {
		if _, ok := done[item.work.ID]; ok {
			continue
		}
		keptFlying = append(keptFlying, item)
	}
	if len(keptFlying) == 0 {
		delete(q.inflight, environment)
	} else {
		q.inflight[environment] = keptFlying
	}

	keptQueued := q.items[environment][:0]
	for _, item := range q.items[environment] {
		if _, ok := done[item.ID]; ok {
			continue
		}
		keptQueued = append(keptQueued, item)
	}
	if len(keptQueued) == 0 {
		delete(q.items, environment)
	} else {
		q.items[environment] = keptQueued
	}

	if err := q.saveLocked(environment); err != nil {
		if len(prevFlying) == 0 {
			delete(q.inflight, environment)
		} else {
			q.inflight[environment] = prevFlying
		}
		if len(prevQueued) == 0 {
			delete(q.items, environment)
		} else {
			q.items[environment] = prevQueued
		}

		return err
	}

	return nil
}

// Clear drops queued and in-flight work for one environment and persists the empty
// queue. Disconnecting without this leaves the next registration of the same
// name a stale delete or scale on its first heartbeat. A persist failure
// restores the in-memory queue so disconnect cannot forget work that is still
// on disk.
func (q *WorkQueue) Clear(environment string) error {
	if q == nil || environment == "" {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	queued := q.items[environment]
	flying := q.inflight[environment]
	delete(q.items, environment)
	delete(q.inflight, environment)
	if err := q.saveLocked(environment); err != nil {
		if len(queued) > 0 {
			q.items[environment] = queued
		}
		if len(flying) > 0 {
			q.inflight[environment] = flying
		}

		return err
	}

	return nil
}

func (q *WorkQueue) snapshot(environment string) (queued []Work, flying []inflight) {
	if q == nil || environment == "" {
		return nil, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	return append([]Work(nil), q.items[environment]...), append([]inflight(nil), q.inflight[environment]...)
}

func (q *WorkQueue) restore(environment string, queued []Work, flying []inflight) error {
	if q == nil || environment == "" {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(queued) > 0 {
		q.items[environment] = append([]Work(nil), queued...)
	} else {
		delete(q.items, environment)
	}
	if len(flying) > 0 {
		q.inflight[environment] = append([]inflight(nil), flying...)
	} else {
		delete(q.inflight, environment)
	}

	return q.saveLocked(environment)
}

// Cancel drops the queued or in-flight items of one kind for one database. A
// persist failure restores both lists, as Ack does.
func (q *WorkQueue) Cancel(environment, kind, database string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	prevFlying := append([]inflight(nil), q.inflight[environment]...)
	prevQueued := append([]Work(nil), q.items[environment]...)
	matches := func(item Work) bool { return item.Kind == kind && item.Database == database }

	keptFlying := make([]inflight, 0, len(prevFlying))
	for _, item := range prevFlying {
		if !matches(item.work) {
			keptFlying = append(keptFlying, item)
		}
	}
	keptQueued := make([]Work, 0, len(prevQueued))
	for _, item := range prevQueued {
		if !matches(item) {
			keptQueued = append(keptQueued, item)
		}
	}
	if len(keptFlying) == len(prevFlying) && len(keptQueued) == len(prevQueued) {
		return nil
	}
	q.setLocked(environment, keptQueued, keptFlying)
	if err := q.saveLocked(environment); err != nil {
		q.setLocked(environment, prevQueued, prevFlying)

		return err
	}

	return nil
}

func (q *WorkQueue) setLocked(environment string, queued []Work, flying []inflight) {
	if len(queued) == 0 {
		delete(q.items, environment)
	} else {
		q.items[environment] = queued
	}
	if len(flying) == 0 {
		delete(q.inflight, environment)
	} else {
		q.inflight[environment] = flying
	}
}

// owed returns the queued and in-flight items of one kind, by id.
func (q *WorkQueue) owed(environment, kind string) map[string]Work {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := map[string]Work{}
	for _, item := range q.items[environment] {
		if item.Kind == kind {
			out[item.ID] = item
		}
	}
	for _, item := range q.inflight[environment] {
		if item.work.Kind == kind {
			out[item.work.ID] = item.work
		}
	}

	return out
}

// Pending reports how many items are queued or waiting to be acknowledged.
func (q *WorkQueue) Pending(environment string) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	return len(q.items[environment]) + len(q.inflight[environment])
}

func sameWork(a, b Work) bool {
	if a.Kind != b.Kind || a.Database != b.Database || a.BackupID != b.BackupID || a.Name != b.Name ||
		a.Window != b.Window {
		return false
	}
	if a.Kind == WorkSupportExtend {
		return a.ExpiresAt.Equal(b.ExpiresAt)
	}
	if a.Kind != WorkScale {
		return true
	}

	return ptrIntEqual(a.CPUCores, b.CPUCores) &&
		ptrIntEqual(a.MemoryGB, b.MemoryGB) &&
		ptrIntEqual(a.StorageGB, b.StorageGB) &&
		ptrIntEqual(a.Replicas, b.Replicas)
}

func ptrIntEqual(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	return *a == *b
}
