package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
)

// State is the team scoped database the hub keeps its state in.
//
// Documents in and out are the same JSON the ConfigMaps and Secrets held, so
// a hub whose state was imported reads back exactly what it wrote before.
// Every method is already scoped to one Team, which is why none of them
// takes one: two customers hold two sets of rows rather than sharing an object.
type State interface {
	Environments(ctx context.Context) (map[string]string, error)
	SaveEnvironment(ctx context.Context, name, document string) error
	RemoveEnvironment(ctx context.Context, name string) error

	Tokens(ctx context.Context) (map[string]string, error)
	SaveToken(ctx context.Context, hash, document string) error
	RemoveTokens(ctx context.Context, hashes []string) error

	Work(ctx context.Context) (map[string]string, error)
	SaveWork(ctx context.Context, environment, document string) error

	CloneKeys(ctx context.Context) (map[string]string, error)
	SaveCloneKey(ctx context.Context, environment, document string) error
	RemoveCloneKey(ctx context.Context, environment string) error
}

// errNoState is what every persist returns without a database. It is a
// programming error rather than an operator one: the wiring only attaches
// these once the connection has landed.
var errNoState = errors.New("the hub has no database to persist through")

// DBInventoryPersist stores the environment list and last heartbeat snapshot as
// one row per environment.
//
// The ConfigMap this replaced was capped at 1 MiB and sharded across sixteen
// objects to stay under it. A row per environment has no such ceiling, so the
// sharding is gone with it.
type DBInventoryPersist struct {
	state State
}

// NewDBInventoryPersist stores inventory rows in the database.
func NewDBInventoryPersist(state State) *DBInventoryPersist {
	return &DBInventoryPersist{state: state}
}

// Load returns every environment this Team holds.
func (p *DBInventoryPersist) Load(ctx context.Context) (map[string]persistedEnvironment, error) {
	if p == nil || p.state == nil {
		return nil, errNoState
	}

	rows, err := p.state.Environments(ctx)
	if err != nil {
		return nil, err
	}

	out := make(map[string]persistedEnvironment, len(rows))
	for name, document := range rows {
		if !ValidEnvironmentName(name) {
			continue
		}
		rec, ok := decodePersistedEnvironment(document)
		if !ok {
			// One unreadable environment must not cost every other environment its
			// row. Log the skip so it is visible in the hub logs rather than
			// only as a fleet that came back short.
			slog.ErrorContext(ctx, "hub environment skipped, the stored document is not valid JSON",
				slog.String("environment", name))

			continue
		}
		rec.Heartbeat = persistableHeartbeat(rec.Heartbeat)
		out[name] = rec
	}

	return out, nil
}

// Save writes one environment snapshot.
func (p *DBInventoryPersist) Save(ctx context.Context, name string, rec persistedEnvironment) error {
	if p == nil || p.state == nil {
		return errNoState
	}

	rec.Heartbeat = persistableHeartbeat(rec.Heartbeat)
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}

	return p.state.SaveEnvironment(ctx, name, string(body))
}

// Remove drops one environment.
func (p *DBInventoryPersist) Remove(ctx context.Context, name string) error {
	if p == nil || p.state == nil {
		return errNoState
	}

	return p.state.RemoveEnvironment(ctx, name)
}

// DBTokenPersist stores join and agent grants as one row per token hash.
// Plaintext tokens are returned once when they are minted and never stored,
// so the table cannot hand anyone a working credential.
type DBTokenPersist struct {
	state State
}

// NewDBTokenPersist stores grants in the database.
func NewDBTokenPersist(state State) *DBTokenPersist {
	return &DBTokenPersist{state: state}
}

// Load returns hash to grant.
func (p *DBTokenPersist) Load(ctx context.Context) (map[string]Grant, error) {
	if p == nil || p.state == nil {
		return nil, errNoState
	}

	rows, err := p.state.Tokens(ctx)
	if err != nil {
		return nil, err
	}

	out := make(map[string]Grant, len(rows))
	for hash, document := range rows {
		if hash == "" {
			continue
		}
		grant, ok := decodeGrant(document)
		if !ok {
			continue
		}
		out[hash] = grant
	}

	return out, nil
}

// Save writes one grant against its hash.
func (p *DBTokenPersist) Save(ctx context.Context, hash string, grant Grant) error {
	if p == nil || p.state == nil {
		return errNoState
	}

	value, err := encodeGrant(grant)
	if err != nil {
		return err
	}

	return p.state.SaveToken(ctx, hash, value)
}

// Remove drops hashes and leaves the rest.
func (p *DBTokenPersist) Remove(ctx context.Context, hashes []string) error {
	if p == nil || p.state == nil {
		return errNoState
	}
	if len(hashes) == 0 {
		return nil
	}

	return p.state.RemoveTokens(ctx, hashes)
}

// DBWorkPersist stores the outbound queue as one row per environment.
type DBWorkPersist struct {
	state State
}

// NewDBWorkPersist stores the work queue in the database.
func NewDBWorkPersist(state State) *DBWorkPersist {
	return &DBWorkPersist{state: state}
}

// Load returns the queue for every environment.
func (p *DBWorkPersist) Load(ctx context.Context) (map[string][]Work, error) {
	if p == nil || p.state == nil {
		return nil, errNoState
	}

	rows, err := p.state.Work(ctx)
	if err != nil {
		return nil, err
	}

	out := make(map[string][]Work, len(rows))
	for environment, document := range rows {
		var items []Work
		// One unreadable environment must not cost every other environment its queue.
		if err := json.Unmarshal([]byte(document), &items); err != nil {
			slog.ErrorContext(ctx, "hub work queue environment skipped, the stored document is not valid JSON",
				slog.String("environment", environment),
				slog.String("err", err.Error()))

			continue
		}
		if len(items) > 0 {
			out[environment] = items
		}
	}

	return out, nil
}

// Save replaces one environment's queue. An empty queue writes an empty array
// rather than removing the row, so a later read cannot mistake drained for
// never seen.
func (p *DBWorkPersist) Save(ctx context.Context, environment string, items []Work) error {
	if p == nil || p.state == nil {
		return errNoState
	}
	if environment == "" {
		return nil
	}
	if items == nil {
		items = []Work{}
	}

	body, err := json.Marshal(items)
	if err != nil {
		return err
	}

	return p.state.SaveWork(ctx, environment, string(body))
}

// DBCloneKeyPersist stores clone identities as one row per environment. The
// identity is an OpenSSH private key, so the row holds it sealed.
type DBCloneKeyPersist struct {
	state State
}

// NewDBCloneKeyPersist stores clone identities in the database.
func NewDBCloneKeyPersist(state State) *DBCloneKeyPersist {
	return &DBCloneKeyPersist{state: state}
}

// Load returns environment to clone key.
func (p *DBCloneKeyPersist) Load(ctx context.Context) (map[string]CloneKey, error) {
	if p == nil || p.state == nil {
		return nil, errNoState
	}

	rows, err := p.state.CloneKeys(ctx)
	if err != nil {
		return nil, err
	}

	out := make(map[string]CloneKey, len(rows))
	for environment, document := range rows {
		var stored CloneKey
		if err := json.Unmarshal([]byte(document), &stored); err != nil {
			continue
		}
		if stored.Identity == "" || stored.Pub == "" {
			continue
		}
		out[environment] = stored
	}

	return out, nil
}

// Save writes one environment identity.
func (p *DBCloneKeyPersist) Save(ctx context.Context, environment string, key CloneKey) error {
	if p == nil || p.state == nil {
		return errNoState
	}
	if environment == "" {
		return nil
	}

	body, err := json.Marshal(key)
	if err != nil {
		return err
	}

	return p.state.SaveCloneKey(ctx, environment, string(body))
}

// Remove drops one environment identity.
func (p *DBCloneKeyPersist) Remove(ctx context.Context, environment string) error {
	if p == nil || p.state == nil {
		return errNoState
	}
	if environment == "" {
		return nil
	}

	return p.state.RemoveCloneKey(ctx, environment)
}

// SupportState is the part of the team scoped database the support access
// records live in.
type SupportState interface {
	SupportAccess(ctx context.Context) (map[string]string, error)
	SaveSupportAccess(ctx context.Context, environment, document string) error
	RemoveSupportAccess(ctx context.Context, environment string) error
}

// AllSupportState reads the support access documents of every Team.
type AllSupportState interface {
	AllSupportAccess(ctx context.Context) ([]string, error)
}

// NewDBSupportCensus reads every Team's support access records from the
// database. A document that cannot be read fails the census, because a record
// left out would make its window's device look stray.
func NewDBSupportCensus(state AllSupportState) SupportCensus {
	return func(ctx context.Context) ([]SupportRecord, error) {
		documents, err := state.AllSupportAccess(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]SupportRecord, 0, len(documents))
		for _, document := range documents {
			var rec SupportRecord
			if err := json.Unmarshal([]byte(document), &rec); err != nil {
				return nil, fmt.Errorf("a support access record is not valid JSON: %w", err)
			}
			out = append(out, rec)
		}

		return out, nil
	}
}

// DBSupportPersist stores support access as one row per environment.
type DBSupportPersist struct {
	state SupportState
}

// NewDBSupportPersist stores support access records in the database.
func NewDBSupportPersist(state SupportState) *DBSupportPersist {
	return &DBSupportPersist{state: state}
}

// Load returns the record of every environment this Team holds one for.
func (p *DBSupportPersist) Load(ctx context.Context) (map[string]SupportRecord, error) {
	if p == nil || p.state == nil {
		return nil, errNoState
	}

	rows, err := p.state.SupportAccess(ctx)
	if err != nil {
		return nil, err
	}

	out := make(map[string]SupportRecord, len(rows))
	for environment, document := range rows {
		var rec SupportRecord
		// A record that cannot be read is reported rather than dropped. Reading
		// it as none would let a second window open over one that is still there.
		if err := json.Unmarshal([]byte(document), &rec); err != nil {
			return nil, fmt.Errorf("support access record for %s is not valid JSON: %w", environment, err)
		}
		out[environment] = rec
	}

	return out, nil
}

// Save writes one environment's record.
func (p *DBSupportPersist) Save(ctx context.Context, environment string, rec SupportRecord) error {
	if p == nil || p.state == nil {
		return errNoState
	}

	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}

	return p.state.SaveSupportAccess(ctx, environment, string(body))
}

// Remove drops one environment's record.
func (p *DBSupportPersist) Remove(ctx context.Context, environment string) error {
	if p == nil || p.state == nil {
		return errNoState
	}

	return p.state.RemoveSupportAccess(ctx, environment)
}
