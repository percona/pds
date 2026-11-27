package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"regexp"
	"sync"
	"time"
)

// environmentNamePattern is a DNS-1123 label: the name the hub UI shows.
var environmentNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidName reports whether name can be a Kubernetes object name. Environment
// names and database names are both DNS-1123 labels, and both end up as a
// path segment in the desired-state tree.
func ValidName(name string) bool {
	return environmentNamePattern.MatchString(name)
}

// ValidDatabaseName reports whether name can be a new database. The operators
// name Services after the database, and a Service name must start with a
// letter, so a name that starts with a digit is accepted by Flux and then
// never becomes a database.
func ValidDatabaseName(name string) bool {
	return ValidName(name) && name[0] >= 'a' && name[0] <= 'z'
}

// reservedEnvironmentNames are names the hub UI routes to pages of its own, so a
// environment called one of them has a row that links away from its databases.
var reservedEnvironmentNames = map[string]struct{}{
	"connect": {},
	"new":     {},
}

// ValidEnvironmentName reports whether name can be a spoke environment.
func ValidEnvironmentName(name string) bool {
	if ReservedEnvironmentName(name) {
		return false
	}

	return ValidName(name)
}

// ReservedEnvironmentName reports whether the hub UI already routes this name.
func ReservedEnvironmentName(name string) bool {
	_, reserved := reservedEnvironmentNames[name]

	return reserved
}

const (
	joinTokenPrefix  = "jnt_"
	agentTokenPrefix = "agt_"
	grantRoleAgent   = "agent"
)

// Grant is what a token hash buys: one environment, until it expires. A zero
// Expires never expires, which is what a token minted before this carried.
//
// A freshly issued join token is an invitation. Enrollment mints a separate
// agent credential (Role agent) that the spoke presents on every heartbeat,
// and the first of those beats drops the invitation. Redeemed is the older
// path: a heartbeat that used the invitation itself stamps it so the TTL
// cannot take a working environment offline. After either drop or redemption the
// only way to end the credential is to revoke it, which is what disconnecting
// an environment does.
type Grant struct {
	Environment string    `json:"environment"`
	Issued      time.Time `json:"issued,omitzero"`
	Expires     time.Time `json:"expires,omitzero"`
	Redeemed    time.Time `json:"redeemed,omitzero"`
	Role        string    `json:"role,omitempty"`
	// Invitation is the join-token hash this agent credential replaced.
	// The first heartbeat drops that invitation so a pasted command cannot
	// enroll after the spoke has joined, while a failed bootstrap can still
	// rerun with the same token.
	Invitation string `json:"invitation,omitempty"`
}

// invitation reports whether this grant is still a one-time join token.
// Agent credentials, redeemed heartbeat tokens, and legacy Secret values
// that were a bare environment name are lasting credentials, not invitations.
func (g Grant) invitation() bool {
	if g.Role == grantRoleAgent || !g.Redeemed.IsZero() {
		return false
	}
	if g.Issued.IsZero() && g.Expires.IsZero() {
		return false
	}

	return true
}

func (g Grant) expired(now time.Time) bool {
	if !g.Redeemed.IsZero() {
		return false
	}

	return !g.Expires.IsZero() && now.After(g.Expires)
}

// TokenPersist stores join-token grants so a hub restart can still authorize
// spokes. Implementations must never write the plaintext token.
type TokenPersist interface {
	Load(ctx context.Context) (map[string]Grant, error)
	Save(ctx context.Context, hash string, grant Grant) error
	// Remove drops hashes. Revoking is the other half of minting: without
	// it a decommissioned environment's token stays valid for as long as the
	// Secret exists.
	Remove(ctx context.Context, hashes []string) error
}

// TokenStore issues per-environment join tokens the spoke agent presents on
// heartbeat. The shared hub agent token still works for lab spokes.
type TokenStore struct {
	mu         sync.Mutex
	grants     map[string]Grant
	persist    TokenPersist
	ttl        time.Duration
	now        func() time.Time
	lastReload time.Time
	// gen counts Issue and Revoke. A persist reload that started before one
	// of those finished must not replace the map and drop the write.
	gen uint64
}

// reloadEvery is the floor between two persist reloads. Hits and misses share
// it so a stream of bad tokens cannot turn into a stream of Secret reads, and
// a revoke still becomes visible on every replica.
const reloadEvery = 5 * time.Second

// reloadAttempts is how many persist reads a reload will retry when Issue or
// Revoke ran during the previous read.
const reloadAttempts = 8

// NewTokenStore starts empty. A nil persist keeps tokens in memory only. A
// zero ttl mints tokens that never expire.
func NewTokenStore(persist TokenPersist, ttl time.Duration) *TokenStore {
	return &TokenStore{
		grants:  map[string]Grant{},
		persist: persist,
		ttl:     ttl,
		now:     time.Now,
	}
}

// Load hydrates hashes from persist. A missing Secret is an empty store.
func (t *TokenStore) Load(ctx context.Context) error {
	if t == nil || t.persist == nil {
		return nil
	}
	loaded, err := t.persist.Load(ctx)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.replaceGrantsLocked(loaded)

	return nil
}

// Issue returns a new invitation bound to one environment name. The plaintext is
// returned once; only its hash is retained and persisted.
func (t *TokenStore) Issue(ctx context.Context, environment string) (string, error) {
	return t.mint(ctx, environment, joinTokenPrefix, "", t.ttl, "")
}

func (t *TokenStore) mint(ctx context.Context, environment, prefix, role string, ttl time.Duration, invitation string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := prefix + hex.EncodeToString(raw)
	hash := hashToken(token)

	issued := t.clock()
	grant := Grant{Environment: environment, Issued: issued, Role: role, Invitation: invitation}
	if role == grantRoleAgent {
		grant.Redeemed = issued
	} else if ttl > 0 {
		grant.Expires = issued.Add(ttl)
	}

	if t.persist != nil {
		if err := t.persist.Save(ctx, hash, grant); err != nil {
			return "", err
		}
	}

	t.mu.Lock()
	t.grants[hash] = grant
	t.gen++
	t.mu.Unlock()

	return token, nil
}

// SetClock replaces the store's clock. Tests use it to reach an expiry
// without sleeping.
func (t *TokenStore) SetClock(now func() time.Time) {
	if t == nil || now == nil {
		return
	}
	t.mu.Lock()
	t.now = now
	t.mu.Unlock()
}

func (t *TokenStore) clock() time.Time {
	if t == nil {
		return time.Now()
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.clockLocked()
}

// Environment reports the spoke a token is allowed to register. The shared lab
// token is not stored here. Persist is the source of truth: a miss reloads
// so another replica can authorize a token it did not mint, and a hit
// reloads on the same floor so a revoke on another replica becomes visible
// instead of living in this process's map until restart.
func (t *TokenStore) Environment(ctx context.Context, token string) (string, bool) {
	grant, ok := t.lookup(ctx, token)
	if !ok {
		return "", false
	}
	if grant.Role == grantRoleAgent {
		_ = t.dropInvitation(ctx, hashToken(token), grant)
	} else {
		t.redeem(ctx, hashToken(token), grant, t.clock())
	}

	return grant.Environment, true
}

// InvitationEnvironment reports the spoke a still-unused join token may enroll.
// Redeemed heartbeat credentials and agent tokens are not invitations.
func (t *TokenStore) InvitationEnvironment(ctx context.Context, token string) (string, bool) {
	grant, ok := t.lookup(ctx, token)
	if !ok || !grant.invitation() {
		return "", false
	}

	return grant.Environment, true
}

func (t *TokenStore) lookup(ctx context.Context, token string) (Grant, bool) {
	if t == nil || token == "" {
		return Grant{}, false
	}
	hash := hashToken(token)
	now := t.clock()
	t.refreshFromPersist(ctx, now)

	t.mu.Lock()
	grant, ok := t.grants[hash]
	t.mu.Unlock()
	if !ok || grant.expired(now) {
		return Grant{}, false
	}

	return grant, true
}

// IssueAgent mints a lasting heartbeat credential for a still-unused join
// token and drops the invitation so a second enrollment cannot mint another
// agent. A failed bootstrap retries from the enrollment JSON saved next to
// the join command, not by asking the hub to mint again.
func (t *TokenStore) IssueAgent(ctx context.Context, token string) (environment, agent string, err error) {
	grant, ok := t.lookup(ctx, token)
	if !ok || !grant.invitation() {
		return "", "", ErrJoinToken
	}
	agent, err = t.mint(ctx, grant.Environment, agentTokenPrefix, grantRoleAgent, 0, hashToken(token))
	if err != nil {
		return "", "", err
	}
	agentGrant, ok := t.lookup(ctx, agent)
	if !ok {
		t.forgetHash(ctx, hashToken(agent))

		return "", "", ErrJoinToken
	}
	if err := t.dropInvitation(ctx, hashToken(agent), agentGrant); err != nil {
		t.forgetHash(ctx, hashToken(agent))

		return "", "", err
	}

	return grant.Environment, agent, nil
}

// dropInvitation removes the join token an agent credential replaced. A
// persist failure leaves the invitation in place so the next enroll or beat
// retries, and the caller must not treat the new agent as issued.
func (t *TokenStore) dropInvitation(ctx context.Context, agentHash string, grant Grant) error {
	inv := grant.Invitation
	if inv == "" {
		return nil
	}
	if t.persist != nil {
		if err := t.persist.Remove(ctx, []string{inv}); err != nil {
			slog.Warn("hub join token invitation not dropped", slog.String("environment", grant.Environment))

			return err
		}
		grant.Invitation = ""
		if err := t.persist.Save(ctx, agentHash, grant); err != nil {
			slog.Warn("hub agent token invitation ref not cleared", slog.String("environment", grant.Environment))
		}
	} else {
		grant.Invitation = ""
	}
	t.mu.Lock()
	delete(t.grants, inv)
	t.grants[agentHash] = grant
	t.gen++
	t.mu.Unlock()

	return nil
}

// forgetHash drops a minted grant from persist and memory. Enrollment uses
// it to take back an agent credential whose invitation could not be consumed.
func (t *TokenStore) forgetHash(ctx context.Context, hash string) {
	if hash == "" {
		return
	}
	if t.persist != nil {
		if err := t.persist.Remove(ctx, []string{hash}); err != nil {
			slog.Warn("hub could not revoke a minted agent token after a failed enrollment")
		}
	}
	t.mu.Lock()
	delete(t.grants, hash)
	t.gen++
	t.mu.Unlock()
}

// refreshFromPersist replaces the in-memory map from persist. It drops hashes
// that are no longer stored, which is how disconnect on one replica revokes
// a token another replica already cached. A failed read leaves memory alone
// so a Secret blip does not take every spoke offline.
func (t *TokenStore) refreshFromPersist(ctx context.Context, now time.Time) {
	if t.persist == nil || !t.allowReload(now) {
		return
	}
	for range reloadAttempts {
		t.mu.Lock()
		gen := t.gen
		t.mu.Unlock()
		loaded, err := t.persist.Load(ctx)
		if err != nil {
			slog.Warn("hub join token reload failed")

			return
		}

		t.mu.Lock()
		if t.gen != gen {
			t.mu.Unlock()
			continue
		}
		t.replaceGrantsLocked(loaded)
		t.mu.Unlock()

		return
	}
}

func (t *TokenStore) replaceGrantsLocked(loaded map[string]Grant) {
	next := make(map[string]Grant, len(loaded))
	for hash, grant := range loaded {
		if hash == "" || grant.Environment == "" {
			continue
		}
		next[hash] = grant
	}
	t.grants = next
	t.lastReload = t.clockLocked()
}

func (t *TokenStore) clockLocked() time.Time {
	if t == nil || t.now == nil {
		return time.Now()
	}

	return t.now()
}

// AdoptRedeemed stamps expired grants for environments the hub can prove joined.
//
// The redemption fix on its own does not rescue an already-running fleet. A
// grant written by an earlier build has no stamp, so after its TTL it reads as
// an invitation nobody used, and the spoke that has been beating keeps getting
// 401 with no way back. seen maps an environment to when it was last heard from.
//
// A still-valid invitation is not that case. Adopting it would turn an unused
// repair token into a lasting credential. A token nobody used, or one issued
// after the last beat, stays expired.
func (t *TokenStore) AdoptRedeemed(ctx context.Context, seen map[string]time.Time) error {
	if t == nil || len(seen) == 0 {
		return nil
	}

	type adoption struct {
		hash  string
		grant Grant
	}

	var pending []adoption

	t.mu.Lock()
	now := t.clockLocked()
	for hash, grant := range t.grants {
		if !grant.Redeemed.IsZero() {
			continue
		}
		at, ok := seen[grant.Environment]
		if !ok || at.IsZero() {
			continue
		}
		if grant.Expires.IsZero() || !now.After(grant.Expires) {
			continue
		}
		if !grant.Issued.IsZero() && grant.Issued.After(at) {
			continue
		}
		grant.Redeemed = at
		t.grants[hash] = grant
		pending = append(pending, adoption{hash: hash, grant: grant})
	}
	t.mu.Unlock()

	if t.persist == nil {
		return nil
	}
	for _, item := range pending {
		if err := t.persist.Save(ctx, item.hash, item.grant); err != nil {
			return err
		}
		slog.Info("hub adopted a join token for an environment that had already joined",
			slog.String("environment", item.grant.Environment))
	}

	return nil
}

// redeem stamps the first use of a token and writes it through, so a hub
// restart does not re-arm the TTL under an environment that already joined. It runs
// on every heartbeat and does nothing after the first, so the common path is
// one map read under the lock.
//
// A failed write leaves the grant redeemed in memory. Refusing the beat instead
// would take the environment offline for a Secret write it cannot influence, and
// the worst case here is that a restart re-arms a TTL that the next beat
// immediately clears again.
func (t *TokenStore) redeem(ctx context.Context, hash string, grant Grant, now time.Time) {
	if !grant.Redeemed.IsZero() {
		return
	}

	t.mu.Lock()
	current, ok := t.grants[hash]
	if !ok || !current.Redeemed.IsZero() {
		t.mu.Unlock()

		return
	}
	current.Redeemed = now
	t.grants[hash] = current
	t.mu.Unlock()

	if t.persist == nil {
		return
	}
	if err := t.persist.Save(ctx, hash, current); err != nil {
		slog.Warn("hub join token redemption not persisted", slog.String("environment", current.Environment))
	}
}

// allowReload reports whether enough time has passed to read persist again.
func (t *TokenStore) allowReload(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.lastReload.IsZero() && now.Sub(t.lastReload) < reloadEvery {
		return false
	}
	t.lastReload = now

	return true
}

// encodeGrant is the Secret value for one hash. Plaintext never lands here.
func encodeGrant(grant Grant) (string, error) {
	body, err := json.Marshal(grant)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// decodeGrant reads a Secret value. Tokens minted before grants carried a
// bare environment name, and those keep working with no expiry.
func decodeGrant(value string) (Grant, bool) {
	if value == "" {
		return Grant{}, false
	}
	if value[0] != '{' {
		return Grant{Environment: value}, true
	}
	var grant Grant
	if err := json.Unmarshal([]byte(value), &grant); err != nil || grant.Environment == "" {
		return Grant{}, false
	}

	return grant, true
}

// RevokeEnvironment drops every token bound to one environment. Disconnecting a
// environment and leaving its token live would mean the row could be recreated by
// anyone still holding the command.
func (t *TokenStore) RevokeEnvironment(ctx context.Context, environment string) error {
	if t == nil || environment == "" {
		return nil
	}

	t.mu.Lock()
	var hashes []string
	for hash, grant := range t.grants {
		if grant.Environment == environment {
			hashes = append(hashes, hash)
			delete(t.grants, hash)
		}
	}
	t.gen++
	t.mu.Unlock()

	if t.persist == nil {
		return nil
	}
	// Another replica may hold hashes this one never saw.
	loaded, err := t.persist.Load(ctx)
	if err == nil {
		for hash, grant := range loaded {
			if grant.Environment == environment {
				hashes = append(hashes, hash)
			}
		}
	}
	if len(hashes) == 0 {
		return nil
	}

	return t.persist.Remove(ctx, hashes)
}

// snapshotEnvironment copies grants for one environment so disconnect can put them
// back if a later persist step fails after revocation.
func (t *TokenStore) snapshotEnvironment(environment string) map[string]Grant {
	if t == nil || environment == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]Grant{}
	for hash, grant := range t.grants {
		if grant.Environment == environment {
			out[hash] = grant
		}
	}

	return out
}

// restoreGrants writes hashes back to persist and memory. Disconnect uses it
// when a later cleanup step fails after RevokeEnvironment already dropped them.
func (t *TokenStore) restoreGrants(ctx context.Context, grants map[string]Grant) error {
	if t == nil || len(grants) == 0 {
		return nil
	}
	var first error
	for hash, grant := range grants {
		if hash == "" || grant.Environment == "" {
			continue
		}
		if t.persist != nil {
			if err := t.persist.Save(ctx, hash, grant); err != nil && first == nil {
				first = err
			}
		}
		t.mu.Lock()
		t.grants[hash] = grant
		t.gen++
		t.mu.Unlock()
	}

	return first
}

// HashToken is the SHA-256 hex a grant is stored under. The plaintext is
// returned once when the token is minted and never written, so every lookup
// has to hash the bearer the same way.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}

func hashToken(token string) string {
	return HashToken(token)
}
