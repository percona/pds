package hub_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/hub"
)

func tokenStoreWithTTL(t *testing.T, ttl time.Duration) (*hub.TokenStore, hub.TokenPersist) {
	t.Helper()

	kv := &memKV{}
	persist := hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens")

	return hub.NewTokenStore(persist, ttl), persist
}

// An invitation nobody used is exactly what the TTL is for.
func TestAnUnredeemedTokenStillExpires(t *testing.T) {
	ctx := context.Background()
	store, _ := tokenStoreWithTTL(t, time.Hour)

	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	token, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	now = now.Add(2 * time.Hour)

	_, ok := store.Environment(ctx, token)
	assert.False(t, ok, "an invitation nobody redeemed must not outlive its TTL")
}

// The token is also the spoke's beat credential, so expiring it after the
// environment joined takes a working environment offline. Redeeming on first use is
// what separates a pending invitation from a live credential.
func TestARedeemedTokenOutlivesItsTTL(t *testing.T) {
	ctx := context.Background()
	store, _ := tokenStoreWithTTL(t, time.Hour)

	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	token, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	name, ok := store.Environment(ctx, token)
	require.True(t, ok)
	require.Equal(t, "spoke-a", name)

	now = now.Add(30 * 24 * time.Hour)

	name, ok = store.Environment(ctx, token)
	require.True(t, ok, "an environment that joined must keep beating")
	assert.Equal(t, "spoke-a", name)
}

// Redemption has to be durable, or a hub restart re-arms the TTL and the
// environment goes quiet an hour later anyway.
func TestRedemptionSurvivesAHubRestart(t *testing.T) {
	ctx := context.Background()
	store, persist := tokenStoreWithTTL(t, time.Hour)

	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	token, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)
	_, ok := store.Environment(ctx, token)
	require.True(t, ok)

	restarted := hub.NewTokenStore(persist, time.Hour)
	restarted.SetClock(func() time.Time { return now.Add(30 * 24 * time.Hour) })
	require.NoError(t, restarted.Load(ctx))

	name, ok := restarted.Environment(ctx, token)
	require.True(t, ok, "redemption must be persisted, not just remembered")
	assert.Equal(t, "spoke-a", name)
}

// Redemption removes the timer, so revocation is the only way to end a
// credential. It has to still work.
func TestRevocationBeatsRedemption(t *testing.T) {
	ctx := context.Background()
	store, _ := tokenStoreWithTTL(t, time.Hour)

	token, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)
	_, ok := store.Environment(ctx, token)
	require.True(t, ok)

	require.NoError(t, store.RevokeEnvironment(ctx, "spoke-a"))

	_, ok = store.Environment(ctx, token)
	assert.False(t, ok, "disconnect must end a redeemed credential")
}

// A token minted before this change has a zero Redeemed and a zero Expires,
// and must keep working rather than being read as an expired invitation.
func TestATokenFromBeforeRedemptionStillWorks(t *testing.T) {
	ctx := context.Background()
	store, _ := tokenStoreWithTTL(t, 0)

	token, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	name, ok := store.Environment(ctx, token)
	require.True(t, ok)
	assert.Equal(t, "spoke-a", name)
}

// A hub upgraded to the redemption fix still holds grants minted before the
// stamp existed. Their environments joined and are beating, but the grant looks
// like an invitation nobody used, so the fix alone leaves them refused. This is
// the live case that took percona-environment-for-dbs offline: issued
// 2026-09-11T15:08:50Z, last accepted beat 2026-09-12T15:08:41Z, nine seconds
// before its TTL, then two days of 401s while the agent kept beating.
func TestAnOldGrantIsAdoptedWhenItsEnvironmentHasJoined(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens")
	store := hub.NewTokenStore(persist, time.Hour)

	issued := time.Date(2026, time.September, 11, 15, 8, 50, 0, time.UTC)
	store.SetClock(func() time.Time { return issued })
	token, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	// Two days later the grant has expired and the spoke is still beating.
	now := issued.Add(48 * time.Hour)
	store.SetClock(func() time.Time { return now })
	_, ok := store.Environment(ctx, token)
	require.False(t, ok, "fixture must start refused")

	lastSeen := issued.Add(24 * time.Hour)
	require.NoError(t, store.AdoptRedeemed(ctx, map[string]time.Time{"spoke-a": lastSeen}))

	name, ok := store.Environment(ctx, token)
	require.True(t, ok, "an environment that demonstrably joined must be adopted")
	assert.Equal(t, "spoke-a", name)
}

// Adoption is evidence-based. A token nobody ever used belongs to no environment in
// the inventory, and it stays expired.
func TestAnOldGrantIsNotAdoptedWhenNobodyEverJoined(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	store := hub.NewTokenStore(hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens"), time.Hour)

	issued := time.Date(2026, time.September, 11, 15, 8, 50, 0, time.UTC)
	store.SetClock(func() time.Time { return issued })
	token, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	store.SetClock(func() time.Time { return issued.Add(48 * time.Hour) })
	require.NoError(t, store.AdoptRedeemed(ctx, map[string]time.Time{"spoke-b": issued}))

	_, ok := store.Environment(ctx, token)
	assert.False(t, ok, "an invitation nobody used must stay expired")
}

// Adoption has to reach the Secret, or the next hub restart refuses again.
func TestAdoptionIsPersisted(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens")
	store := hub.NewTokenStore(persist, time.Hour)

	issued := time.Date(2026, time.September, 11, 15, 8, 50, 0, time.UTC)
	store.SetClock(func() time.Time { return issued })
	token, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	now := issued.Add(48 * time.Hour)
	store.SetClock(func() time.Time { return now })
	require.NoError(t, store.AdoptRedeemed(ctx, map[string]time.Time{"spoke-a": issued.Add(24 * time.Hour)}))

	restarted := hub.NewTokenStore(persist, time.Hour)
	restarted.SetClock(func() time.Time { return now })
	require.NoError(t, restarted.Load(ctx))

	_, ok := restarted.Environment(ctx, token)
	assert.True(t, ok, "adoption must survive a restart")
}

// A repair invitation for an environment that is already beating is not evidence
// that invitation was used. Adopting it would make an unused secret last
// forever.
func TestAdoptionDoesNotRedeemALiveInvitation(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	persist := hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens")
	store := hub.NewTokenStore(persist, time.Hour)

	joinedAt := time.Date(2026, time.September, 11, 15, 8, 50, 0, time.UTC)
	store.SetClock(func() time.Time { return joinedAt })
	used, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)
	_, ok := store.Environment(ctx, used)
	require.True(t, ok)

	repairAt := joinedAt.Add(30 * time.Minute)
	store.SetClock(func() time.Time { return repairAt })
	repair, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	// The environment is still beating, and the repair token is still inside its
	// TTL. Adopting now would make that unused invitation last forever.
	later := repairAt.Add(20 * time.Minute)
	store.SetClock(func() time.Time { return later })
	require.NoError(t, store.AdoptRedeemed(ctx, map[string]time.Time{"spoke-a": later}))

	store.SetClock(func() time.Time { return repairAt.Add(2 * time.Hour) })
	_, ok = store.Environment(ctx, repair)
	assert.False(t, ok, "an unused invitation must still expire")
	_, ok = store.Environment(ctx, used)
	assert.True(t, ok, "the token that actually joined must keep working")
}

// Enrollment is the one-time join. The invitation is consumed when the agent
// credential is minted so a copied command cannot mint a second lasting
// credential. Bootstrap retries use the enrollment JSON saved on the first
// success.
func TestIssueAgentConsumesTheInvitation(t *testing.T) {
	ctx := context.Background()
	store, persist := tokenStoreWithTTL(t, time.Hour)

	join, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	environment, agent, err := store.IssueAgent(ctx, join)
	require.NoError(t, err)
	assert.Equal(t, "spoke-a", environment)
	require.True(t, strings.HasPrefix(agent, "agt_"))
	assert.NotEqual(t, join, agent)

	_, _, err = store.IssueAgent(ctx, join)
	require.ErrorIs(t, err, hub.ErrJoinToken)

	_, _, err = store.IssueAgent(ctx, agent)
	require.ErrorIs(t, err, hub.ErrJoinToken)

	name, ok := store.Environment(ctx, agent)
	require.True(t, ok)
	assert.Equal(t, "spoke-a", name)
	_, ok = store.Environment(ctx, join)
	assert.False(t, ok, "the pasted invitation must not keep beating after enrollment")

	restarted := hub.NewTokenStore(persist, time.Hour)
	require.NoError(t, restarted.Load(ctx))
	_, ok = restarted.Environment(ctx, join)
	assert.False(t, ok)
	name, ok = restarted.Environment(ctx, agent)
	require.True(t, ok, "the agent credential must survive a restart")
	assert.Equal(t, "spoke-a", name)
}

func TestIssueAgentRevokesTheAgentWhenInvitationDropFails(t *testing.T) {
	ctx := context.Background()
	_, inner := tokenStoreWithTTL(t, time.Hour)
	persist := &failFirstRemove{inner: inner}
	store := hub.NewTokenStore(persist, time.Hour)

	join, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)

	_, agent, err := store.IssueAgent(ctx, join)
	require.Error(t, err)
	assert.Empty(t, agent)

	name, ok := store.InvitationEnvironment(ctx, join)
	require.True(t, ok, "the invitation must stay usable after a failed consume")
	assert.Equal(t, "spoke-a", name)

	environment, agent, err := store.IssueAgent(ctx, join)
	require.NoError(t, err)
	assert.Equal(t, "spoke-a", environment)
	require.True(t, strings.HasPrefix(agent, "agt_"))
	_, _, err = store.IssueAgent(ctx, join)
	require.ErrorIs(t, err, hub.ErrJoinToken)
}

type failFirstRemove struct {
	inner hub.TokenPersist
	n     int
}

func (f *failFirstRemove) Load(ctx context.Context) (map[string]hub.Grant, error) {
	return f.inner.Load(ctx)
}

func (f *failFirstRemove) Save(ctx context.Context, hash string, grant hub.Grant) error {
	return f.inner.Save(ctx, hash, grant)
}

func (f *failFirstRemove) Remove(ctx context.Context, hashes []string) error {
	f.n++
	if f.n == 1 {
		return assert.AnError
	}

	return f.inner.Remove(ctx, hashes)
}

func TestIssueAgentRejectsARedeemedHeartbeatToken(t *testing.T) {
	ctx := context.Background()
	store, _ := tokenStoreWithTTL(t, time.Hour)

	join, err := store.Issue(ctx, "spoke-a")
	require.NoError(t, err)
	_, ok := store.Environment(ctx, join)
	require.True(t, ok)

	_, _, err = store.IssueAgent(ctx, join)
	require.ErrorIs(t, err, hub.ErrJoinToken)
}

func TestIssueAgentRejectsALegacyLastingCredential(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	// sha256 of "jnt_legacy".
	hash := "39dc8ac9639c1e6fee80e2eef8e40a2ae55d411a0db8c367fa9c24e65f77abcb"
	require.NoError(t, kv.PutSecret(ctx, "everest-system", "pds-join-tokens",
		map[string]string{hash: "spoke-legacy"}))
	persist := hub.NewSecretTokenPersist(kv, "everest-system", "pds-join-tokens")
	store := hub.NewTokenStore(persist, time.Hour)
	require.NoError(t, store.Load(ctx))

	_, _, err := store.IssueAgent(ctx, "jnt_legacy")
	require.ErrorIs(t, err, hub.ErrJoinToken)
	name, ok := store.Environment(ctx, "jnt_legacy")
	require.True(t, ok)
	assert.Equal(t, "spoke-legacy", name)
}
