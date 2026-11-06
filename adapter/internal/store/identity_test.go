package store_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

// identityNow is the instant the login tests reason from. A fixed clock keeps
// an expiry assertion from depending on how long the suite took to get here.
var identityNow = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

func newIdentity(t *testing.T) (*store.Identity, string) {
	t.Helper()

	db := openLive(t)

	return db.Identity(), nextLogin(t)
}

func TestEnsureLoginIsCreatedOnceAndIsNotSignedInYet(t *testing.T) {
	identity, login := newIdentity(t)
	ctx := t.Context()

	created, err := identity.EnsureLogin(ctx, login, identityNow)
	require.NoError(t, err)
	assert.True(t, created, "the first call creates the login")

	created, err = identity.EnsureLogin(ctx, login, identityNow)
	require.NoError(t, err)
	assert.False(t, created, "a second call finds the login already there")

	credential, err := identity.PasswordLogin(ctx, login)
	require.NoError(t, err)
	assert.Empty(t, credential.PasswordHash,
		"a login that has not claimed its invite holds no hash")
	assert.Zero(t, credential.Failures)
}

func TestPasswordLoginIsUnknownForANameNobodyCreated(t *testing.T) {
	identity, login := newIdentity(t)

	_, err := identity.PasswordLogin(t.Context(), login)

	require.ErrorIs(t, err, store.ErrLoginUnknown)
}

func TestClaimInviteSetsThePasswordAndSpendsTheLink(t *testing.T) {
	identity, login := newIdentity(t)
	ctx := t.Context()

	require.NoError(t, seedLogin(t, identity, login))
	require.NoError(t, identity.CreateInvite(ctx, login, hashFor(login, "claim"), identityNow.Add(time.Hour), identityNow))

	claimed, err := identity.ClaimInvite(ctx, hashFor(login, "claim"), "bcrypt-hash", identityNow)
	require.NoError(t, err)
	assert.Equal(t, login, claimed, "claiming returns the login the link belongs to")

	credential, err := identity.PasswordLogin(ctx, login)
	require.NoError(t, err)
	assert.Equal(t, "bcrypt-hash", credential.PasswordHash)

	_, err = identity.ClaimInvite(ctx, hashFor(login, "claim"), "second-hash", identityNow)
	require.ErrorIs(t, err, store.ErrInviteSpent, "a spent link is refused")

	credential, err = identity.PasswordLogin(ctx, login)
	require.NoError(t, err)
	assert.Equal(t, "bcrypt-hash", credential.PasswordHash,
		"the refused replay left the first password in place")
}

func TestClaimInviteRefusesALinkThatIsNotClaimable(t *testing.T) {
	identity, login := newIdentity(t)
	ctx := t.Context()

	require.NoError(t, seedLogin(t, identity, login))
	require.NoError(t, identity.CreateInvite(ctx, login, hashFor(login, "expired"), identityNow.Add(-time.Second), identityNow))

	tests := []struct {
		name string
		hash string
		want error
	}{
		{"expired", hashFor(login, "expired"), store.ErrInviteExpired},
		{"never minted", hashFor(login, "nobody-minted"), store.ErrInviteUnknown},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := identity.ClaimInvite(ctx, tc.hash, "bcrypt-hash", identityNow)

			require.ErrorIs(t, err, tc.want)
		})
	}
}

// TestClaimInviteIsSpentOnceUnderConcurrentClaims is the single-use guarantee
// where it actually has to hold. Two browsers opening the same link at once
// both read an unclaimed row, so the spend has to be a conditional update
// rather than a read followed by a write.
func TestClaimInviteIsSpentOnceUnderConcurrentClaims(t *testing.T) {
	identity, login := newIdentity(t)
	ctx := t.Context()

	require.NoError(t, seedLogin(t, identity, login))
	require.NoError(t, identity.CreateInvite(ctx, login, hashFor(login, "race"), identityNow.Add(time.Hour), identityNow))

	const claimers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		wins    int
		refused int
	)

	wg.Add(claimers)
	for range claimers {
		go func() {
			defer wg.Done()
			_, err := identity.ClaimInvite(ctx, hashFor(login, "race"), "bcrypt-hash", identityNow)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++

				return
			}
			if errors.Is(err, store.ErrInviteSpent) {
				refused++
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, wins, "exactly one claim spends the link")
	assert.Equal(t, claimers-1, refused, "every other claim is refused as spent")
}

func TestInviteReportsWhoTheLinkIsForWithoutSpendingIt(t *testing.T) {
	identity, login := newIdentity(t)
	ctx := t.Context()

	require.NoError(t, seedLogin(t, identity, login))
	require.NoError(t, identity.CreateInvite(ctx, login, hashFor(login, "peek"), identityNow.Add(time.Hour), identityNow))

	got, err := identity.Invite(ctx, hashFor(login, "peek"), identityNow)
	require.NoError(t, err)
	assert.Equal(t, login, got)

	// The peek must not spend the link, or opening the page twice would break
	// the flow it exists to start.
	_, err = identity.ClaimInvite(ctx, hashFor(login, "peek"), "bcrypt-hash", identityNow)
	require.NoError(t, err)
}

func TestSessionRoundTripAndDelete(t *testing.T) {
	identity, login := newIdentity(t)
	ctx := t.Context()

	require.NoError(t, seedLogin(t, identity, login))
	require.NoError(t, identity.CreateSession(ctx, store.Session{
		Hash:       hashFor(login, "session"),
		UserID:     userID(t, identity, login),
		CreatedAt:  identityNow,
		LastSeenAt: identityNow,
		ExpiresAt:  identityNow.Add(30 * 24 * time.Hour),
	}))

	got, err := identity.Session(ctx, hashFor(login, "session"), identityNow, 7*24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, userID(t, identity, login), got.UserID)

	require.NoError(t, identity.DeleteSession(ctx, hashFor(login, "session")))

	_, err = identity.Session(ctx, hashFor(login, "session"), identityNow, 7*24*time.Hour)
	require.ErrorIs(t, err, store.ErrSessionUnknown, "logout ends the session at once")
}

func TestSessionIsRefusedOnceItIsPastItsLimits(t *testing.T) {
	identity, login := newIdentity(t)
	ctx := t.Context()

	require.NoError(t, seedLogin(t, identity, login))

	tests := []struct {
		name    string
		hash    string
		session store.Session
		at      time.Time
	}{
		{
			name: "past the absolute expiry",
			hash: hashFor(login, "absolute"),
			session: store.Session{
				CreatedAt:  identityNow.Add(-31 * 24 * time.Hour),
				LastSeenAt: identityNow,
				ExpiresAt:  identityNow.Add(-time.Second),
			},
			at: identityNow,
		},
		{
			name: "idle for longer than the window",
			hash: hashFor(login, "idle"),
			session: store.Session{
				CreatedAt:  identityNow,
				LastSeenAt: identityNow,
				ExpiresAt:  identityNow.Add(30 * 24 * time.Hour),
			},
			at: identityNow.Add(8 * 24 * time.Hour),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			session := tc.session
			session.Hash = tc.hash
			session.UserID = userID(t, identity, login)
			require.NoError(t, identity.CreateSession(ctx, session))

			_, err := identity.Session(ctx, tc.hash, tc.at, 7*24*time.Hour)

			require.ErrorIs(t, err, store.ErrSessionUnknown)
		})
	}
}

// TestSessionTouchesLastSeenSoTheIdleWindowSlides is why the row carries a
// last seen at all: a tab used every day must not be signed out by the idle
// window it keeps resetting.
func TestSessionTouchesLastSeenSoTheIdleWindowSlides(t *testing.T) {
	identity, login := newIdentity(t)
	ctx := t.Context()

	require.NoError(t, seedLogin(t, identity, login))
	require.NoError(t, identity.CreateSession(ctx, store.Session{
		Hash:       hashFor(login, "sliding"),
		UserID:     userID(t, identity, login),
		CreatedAt:  identityNow,
		LastSeenAt: identityNow,
		ExpiresAt:  identityNow.Add(30 * 24 * time.Hour),
	}))

	idle := 7 * 24 * time.Hour
	sixDaysOn := identityNow.Add(6 * 24 * time.Hour)
	_, err := identity.Session(ctx, hashFor(login, "sliding"), sixDaysOn, idle)
	require.NoError(t, err)

	// Six days after that visit is twelve days after sign in, which is past
	// the idle window measured from sign in and inside it measured from the
	// visit.
	_, err = identity.Session(ctx, hashFor(login, "sliding"), sixDaysOn.Add(6*24*time.Hour), idle)
	require.NoError(t, err)
}

func TestSetPasswordEndsEveryLiveSessionForThatLogin(t *testing.T) {
	identity, login := newIdentity(t)
	other := login + "-neighbour"
	ctx := t.Context()

	require.NoError(t, seedLogin(t, identity, login))
	require.NoError(t, seedLogin(t, identity, other))
	require.NoError(t, identity.CreateSession(ctx, newTestSession(t, identity, hashFor(login, "mine"), login)))
	require.NoError(t, identity.CreateSession(ctx, newTestSession(t, identity, hashFor(login, "theirs"), other)))

	require.NoError(t, identity.SetPassword(ctx, login, "new-hash", identityNow))

	_, err := identity.Session(ctx, hashFor(login, "mine"), identityNow, 7*24*time.Hour)
	require.ErrorIs(t, err, store.ErrSessionUnknown,
		"a password change ends the sessions it was signed in with")

	_, err = identity.Session(ctx, hashFor(login, "theirs"), identityNow, 7*24*time.Hour)
	require.NoError(t, err, "another login's sessions are untouched")
}

// TestClaimInviteEndsEveryLiveSession is the reset path: Percona mints a fresh
// link for a login that has already signed in, and claiming it has to end the
// sessions held under the password it replaces.
func TestClaimInviteEndsEveryLiveSession(t *testing.T) {
	identity, login := newIdentity(t)
	ctx := t.Context()

	require.NoError(t, seedLogin(t, identity, login))
	require.NoError(t, identity.CreateSession(ctx, newTestSession(t, identity, hashFor(login, "before-reset"), login)))
	require.NoError(t, identity.CreateInvite(ctx, login, hashFor(login, "reset"), identityNow.Add(time.Hour), identityNow))

	_, err := identity.ClaimInvite(ctx, hashFor(login, "reset"), "reset-hash", identityNow)
	require.NoError(t, err)

	_, err = identity.Session(ctx, hashFor(login, "before-reset"), identityNow, 7*24*time.Hour)
	require.ErrorIs(t, err, store.ErrSessionUnknown)
}

func TestFailuresCountUpAndClearOnSuccess(t *testing.T) {
	identity, login := newIdentity(t)
	ctx := t.Context()

	require.NoError(t, seedLogin(t, identity, login))

	first, err := identity.RecordFailure(ctx, login, identityNow)
	require.NoError(t, err)
	assert.Equal(t, 1, first)

	second, err := identity.RecordFailure(ctx, login, identityNow)
	require.NoError(t, err)
	assert.Equal(t, 2, second, "the count is read back from the row, not from memory")

	require.NoError(t, identity.ClearFailures(ctx, login, identityNow))

	credential, err := identity.PasswordLogin(ctx, login)
	require.NoError(t, err)
	assert.Zero(t, credential.Failures)
}

func TestPurgeExpiredSessionsDropsOnlyTheDeadRows(t *testing.T) {
	identity, login := newIdentity(t)
	ctx := t.Context()

	require.NoError(t, seedLogin(t, identity, login))
	live := newTestSession(t, identity, hashFor(login, "live"), login)
	dead := newTestSession(t, identity, hashFor(login, "dead"), login)
	dead.ExpiresAt = identityNow.Add(-time.Hour)
	require.NoError(t, identity.CreateSession(ctx, live))
	require.NoError(t, identity.CreateSession(ctx, dead))

	removed, err := identity.PurgeExpiredSessions(ctx, identityNow)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, removed, int64(1))

	_, err = identity.Session(ctx, hashFor(login, "live"), identityNow, 7*24*time.Hour)
	require.NoError(t, err)
}

// TestSessionKeyIsStableAcrossReads is what keeps a cookie working after a
// restart and across replicas: the key is a row, not a per process value.
func TestSessionKeyIsStableAcrossReads(t *testing.T) {
	db := openLive(t)
	ctx := t.Context()

	first, err := db.Identity().SessionKey(ctx, identityNow)
	require.NoError(t, err)
	assert.Len(t, first, 32)

	second, err := db.Identity().SessionKey(ctx, identityNow)
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

func TestLoginsListsWhoCanSignIn(t *testing.T) {
	identity, login := newIdentity(t)
	ctx := t.Context()

	require.NoError(t, seedLogin(t, identity, login))
	require.NoError(t, identity.SetPassword(ctx, login, "hash", identityNow))

	rows, err := identity.Logins(ctx)
	require.NoError(t, err)

	var found *store.PasswordLogin
	for i := range rows {
		if rows[i].Username == login {
			found = &rows[i]

			break
		}
	}
	require.NotNil(t, found, "the login this test created is listed")
	assert.Equal(t, "hash", found.PasswordHash)
}

func seedLogin(t *testing.T, identity *store.Identity, login string) error {
	t.Helper()

	_, err := identity.EnsureLogin(t.Context(), login, identityNow)

	return err
}

// hashFor names one row for one test. Invites and sessions are keyed by hash
// alone, and the tables outlive a go test run, so a fixed string would collide
// with whatever the last run left behind.
func hashFor(login, label string) string {
	sum := sha256.Sum256([]byte(login + "/" + label))

	return hex.EncodeToString(sum[:])
}

func newTestSession(t *testing.T, identity *store.Identity, hash, login string) store.Session {
	t.Helper()

	return store.Session{
		Hash:       hash,
		UserID:     userID(t, identity, login),
		CreatedAt:  identityNow,
		LastSeenAt: identityNow,
		ExpiresAt:  identityNow.Add(30 * 24 * time.Hour),
	}
}

// userID is the User behind a password login the test created.
func userID(t *testing.T, identity *store.Identity, login string) uint {
	t.Helper()

	placement, err := identity.Placement(t.Context(), login)
	require.NoError(t, err)

	return placement.UserID
}
