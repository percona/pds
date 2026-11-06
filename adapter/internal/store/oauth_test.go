package store_test

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

var oauthNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func newOAuth(t *testing.T) *store.OAuth {
	t.Helper()

	return openLive(t).OAuth()
}

// uniqueHash keeps one test's rows apart from every other test's in a shared
// database.
func uniqueHash(t *testing.T, label string) string {
	t.Helper()

	return hashFor(t.Name(), strconv.FormatInt(oauthRun, 10)+label)
}

// oauthRun differs between runs, so a database that keeps rows from an
// earlier run does not collide with this one.
var oauthRun = time.Now().UnixNano()

func sampleCode(t *testing.T) store.OAuthCode {
	t.Helper()

	return store.OAuthCode{
		Hash: uniqueHash(t, "code"), UserID: 7, OrganizationID: 3, TeamID: 5, Method: store.SignInOIDC,
		Access: "change", Passwords: true, ClientID: "https://agent.example/c.json", ClientName: "Agent",
		ClientHost: "agent.example", ClientKind: "cimd", RedirectURI: "http://127.0.0.1:5/cb",
		CodeChallenge: "challenge", Resource: "https://pds.example/mcp",
		CreatedAt: oauthNow, ExpiresAt: oauthNow.Add(time.Minute),
	}
}

func sampleSession(t *testing.T, label string) store.AgentSession {
	t.Helper()

	return store.AgentSession{
		ID: uniqueHash(t, "session-"+label)[:32], UserID: 7, OrganizationID: 3, TeamID: 5, Method: store.SignInOIDC,
		Access: "change", ClientID: "https://agent.example/c.json", ClientName: "Agent", ClientHost: "agent.example",
		ClientKind: "cimd", Resource: "https://pds.example/mcp",
		CreatedAt: oauthNow, LastUsedAt: oauthNow, ExpiresAt: oauthNow.Add(30 * 24 * time.Hour),
	}
}

func tokensFor(t *testing.T, session store.AgentSession, label string, at time.Time) []store.OAuthToken {
	t.Helper()

	return []store.OAuthToken{
		{Hash: uniqueHash(t, "access-"+label), SessionID: session.ID, Kind: store.TokenAccess, ExpiresAt: at.Add(time.Hour)},
		{Hash: uniqueHash(t, "refresh-"+label), SessionID: session.ID, Kind: store.TokenRefresh, ExpiresAt: session.ExpiresAt},
	}
}

func TestARegisteredClientRoundTripsAndUnknownOnesAreRefused(t *testing.T) {
	oauth := newOAuth(t)
	ctx := t.Context()
	id := "dcr_" + uniqueHash(t, "client")[:20]

	_, err := oauth.Client(ctx, id)
	require.ErrorIs(t, err, store.ErrOAuthClientUnknown)

	require.NoError(t, oauth.CreateClient(ctx, store.OAuthClient{
		ID: id, Name: "Cursor", RedirectURIs: []string{"http://localhost:8787/callback"}, CreatedAt: oauthNow,
	}))

	got, err := oauth.Client(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "Cursor", got.Name)
	assert.Equal(t, []string{"http://localhost:8787/callback"}, got.RedirectURIs)
}

func TestClientsThatNeverGotAGrantArePurgedAndUsedOnesStay(t *testing.T) {
	oauth := newOAuth(t)
	ctx := t.Context()
	stale := "dcr_" + uniqueHash(t, "stale")[:20]
	used := "dcr_" + uniqueHash(t, "used")[:20]
	old := oauthNow.Add(-48 * time.Hour)

	for _, id := range []string{stale, used} {
		require.NoError(t, oauth.CreateClient(ctx, store.OAuthClient{
			ID: id, Name: "A", RedirectURIs: []string{"https://a.example/cb"}, CreatedAt: old,
		}))
	}
	require.NoError(t, oauth.MarkClientUsed(ctx, used))

	require.NoError(t, oauth.PurgeClients(ctx, oauthNow.Add(-24*time.Hour)))

	_, err := oauth.Client(ctx, stale)
	require.ErrorIs(t, err, store.ErrOAuthClientUnknown)
	_, err = oauth.Client(ctx, used)
	require.NoError(t, err)
}

func TestAParkedRequestIsTakenOnceAndNotAfterItExpires(t *testing.T) {
	oauth := newOAuth(t)
	ctx := t.Context()
	row := store.OAuthRequest{
		Hash: uniqueHash(t, "request"), UserID: 7, ClientID: "c", ClientName: "A", ClientKind: "dcr",
		RedirectURI: "https://a.example/cb", Scope: "pds:read", State: "s", CodeChallenge: "ch",
		Resource: "https://pds.example/mcp", CreatedAt: oauthNow, ExpiresAt: oauthNow.Add(10 * time.Minute),
	}
	require.NoError(t, oauth.CreateRequest(ctx, row))

	peek, err := oauth.Request(ctx, row.Hash, oauthNow.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, "s", peek.State)

	taken, err := oauth.TakeRequest(ctx, row.Hash, oauthNow.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, row.RedirectURI, taken.RedirectURI)

	_, err = oauth.TakeRequest(ctx, row.Hash, oauthNow.Add(time.Minute))
	require.ErrorIs(t, err, store.ErrOAuthRequestUnknown, "a second take finds nothing")

	row.Hash = uniqueHash(t, "late")
	require.NoError(t, oauth.CreateRequest(ctx, row))
	_, err = oauth.Request(ctx, row.Hash, oauthNow.Add(11*time.Minute))
	require.ErrorIs(t, err, store.ErrOAuthRequestUnknown, "an expired request reads as unknown")
	_, err = oauth.TakeRequest(ctx, row.Hash, oauthNow.Add(11*time.Minute))
	require.ErrorIs(t, err, store.ErrOAuthRequestUnknown)
}

func TestACodeIsUsedOnceEvenUnderConcurrentRequests(t *testing.T) {
	oauth := newOAuth(t)
	ctx := t.Context()
	code := sampleCode(t)
	require.NoError(t, oauth.CreateCode(ctx, code))

	var wins, reuses atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := oauth.UseCode(ctx, code.Hash, oauthNow.Add(time.Second))
			if err == nil {
				wins.Add(1)

				return
			}
			if assert.ErrorIs(t, err, store.ErrOAuthCodeUsed) {
				reuses.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), wins.Load(), "exactly one request gets the code")
	assert.Equal(t, int32(7), reuses.Load())
}

func TestAnExpiredOrUnknownCodeIsUnknown(t *testing.T) {
	oauth := newOAuth(t)
	ctx := t.Context()
	code := sampleCode(t)
	require.NoError(t, oauth.CreateCode(ctx, code))

	_, err := oauth.UseCode(ctx, code.Hash, oauthNow.Add(2*time.Minute))
	require.ErrorIs(t, err, store.ErrOAuthCodeUnknown)

	_, err = oauth.UseCode(ctx, uniqueHash(t, "nothing"), oauthNow)
	require.ErrorIs(t, err, store.ErrOAuthCodeUnknown)
}

func TestAReusedCodeReportsTheSessionItBought(t *testing.T) {
	oauth := newOAuth(t)
	ctx := t.Context()
	code := sampleCode(t)
	require.NoError(t, oauth.CreateCode(ctx, code))
	_, err := oauth.UseCode(ctx, code.Hash, oauthNow)
	require.NoError(t, err)

	session := sampleSession(t, "a")
	require.NoError(t, oauth.CreateAgentSession(ctx, session, tokensFor(t, session, "a", oauthNow), code.Hash))

	again, err := oauth.UseCode(ctx, code.Hash, oauthNow.Add(time.Second))
	require.ErrorIs(t, err, store.ErrOAuthCodeUsed)
	assert.Equal(t, session.ID, again.SessionID)
}

func TestATokenFindsItsSessionAndUnknownOnesDoNot(t *testing.T) {
	oauth := newOAuth(t)
	ctx := t.Context()
	session := sampleSession(t, "a")
	tokens := tokensFor(t, session, "a", oauthNow)
	require.NoError(t, oauth.CreateAgentSession(ctx, session, tokens, ""))

	token, found, err := oauth.TokenSession(ctx, tokens[0].Hash)
	require.NoError(t, err)
	assert.Equal(t, store.TokenAccess, token.Kind)
	assert.Equal(t, session.ID, found.ID)
	assert.Equal(t, uint(5), found.TeamID)
	assert.Equal(t, "change", found.Access)

	_, _, err = oauth.TokenSession(ctx, uniqueHash(t, "nope"))
	require.ErrorIs(t, err, store.ErrOAuthTokenUnknown)
}

func TestRotatingARefreshTokenRetiresItAndIssuesTheNext(t *testing.T) {
	oauth := newOAuth(t)
	ctx := t.Context()
	session := sampleSession(t, "a")
	first := tokensFor(t, session, "first", oauthNow)
	require.NoError(t, oauth.CreateAgentSession(ctx, session, first, ""))

	later := oauthNow.Add(2 * time.Hour)
	second := tokensFor(t, session, "second", later)
	require.NoError(t, oauth.RotateRefresh(ctx, first[1].Hash, later, second))

	_, _, err := oauth.TokenSession(ctx, second[1].Hash)
	require.NoError(t, err, "the new refresh token works")
	_, found, err := oauth.TokenSession(ctx, second[0].Hash)
	require.NoError(t, err)
	assert.WithinDuration(t, later, found.LastUsedAt, time.Second, "a refresh counts as use")

	err = oauth.RotateRefresh(ctx, first[1].Hash, later, tokensFor(t, session, "third", later))
	require.ErrorIs(t, err, store.ErrOAuthTokenReused, "the retired token is spent")
}

func TestOnlyOneOfTwoConcurrentRefreshesWins(t *testing.T) {
	oauth := newOAuth(t)
	ctx := t.Context()
	session := sampleSession(t, "a")
	first := tokensFor(t, session, "first", oauthNow)
	require.NoError(t, oauth.CreateAgentSession(ctx, session, first, ""))

	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			label := string(rune('a' + i))
			if err := oauth.RotateRefresh(ctx, first[1].Hash, oauthNow, tokensFor(t, session, label, oauthNow)); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), wins.Load())
}

func TestRefreshingARevokedSessionIsRefused(t *testing.T) {
	oauth := newOAuth(t)
	ctx := t.Context()
	session := sampleSession(t, "a")
	first := tokensFor(t, session, "first", oauthNow)
	require.NoError(t, oauth.CreateAgentSession(ctx, session, first, ""))

	require.NoError(t, oauth.DeleteAgentSession(ctx, session.ID))

	err := oauth.RotateRefresh(ctx, first[1].Hash, oauthNow, tokensFor(t, session, "second", oauthNow))
	require.Error(t, err)
	_, _, err = oauth.TokenSession(ctx, first[0].Hash)
	require.ErrorIs(t, err, store.ErrOAuthTokenUnknown, "deleting a session ends every token it held")
	_, _, err = oauth.TokenSession(ctx, first[1].Hash)
	require.ErrorIs(t, err, store.ErrOAuthTokenUnknown)
}

func TestTouchingASessionMovesItsLastUse(t *testing.T) {
	oauth := newOAuth(t)
	ctx := t.Context()
	session := sampleSession(t, "a")
	tokens := tokensFor(t, session, "a", oauthNow)
	require.NoError(t, oauth.CreateAgentSession(ctx, session, tokens, ""))

	require.NoError(t, oauth.TouchAgentSession(ctx, session.ID, oauthNow.Add(time.Hour)))

	_, found, err := oauth.TokenSession(ctx, tokens[0].Hash)
	require.NoError(t, err)
	assert.WithinDuration(t, oauthNow.Add(time.Hour), found.LastUsedAt, time.Second)
}

func TestPurgeDropsWhatHasExpiredAndKeepsWhatLives(t *testing.T) {
	oauth := newOAuth(t)
	ctx := t.Context()
	live := sampleSession(t, "live")
	dead := sampleSession(t, "dead")
	dead.ExpiresAt = oauthNow.Add(-time.Hour)
	liveTokens := tokensFor(t, live, "live", oauthNow)
	deadTokens := tokensFor(t, dead, "dead", oauthNow.Add(-2*time.Hour))
	require.NoError(t, oauth.CreateAgentSession(ctx, live, liveTokens, ""))
	require.NoError(t, oauth.CreateAgentSession(ctx, dead, deadTokens, ""))

	require.NoError(t, oauth.PurgeOAuth(ctx, oauthNow))

	_, _, err := oauth.TokenSession(ctx, liveTokens[1].Hash)
	require.NoError(t, err)
	_, _, err = oauth.TokenSession(ctx, deadTokens[1].Hash)
	require.ErrorIs(t, err, store.ErrOAuthTokenUnknown)
}

func TestAgentSessionsAreListedByOrganizationAndPersonAndNeverPastTheirExpiry(t *testing.T) {
	oauth := newOAuth(t)
	ctx := t.Context()
	// An Organization of its own, so rows other tests left in a shared database do not show.
	organization := uint(1_000_000 + oauthRun%1_000_000)

	mine, theirs, elsewhere, expired := sampleSession(t, "mine"), sampleSession(t, "theirs"), sampleSession(t, "elsewhere"), sampleSession(t, "expired")
	for _, s := range []*store.AgentSession{&mine, &theirs, &elsewhere, &expired} {
		s.OrganizationID = organization
	}
	theirs.UserID = 8
	elsewhere.OrganizationID = organization + 1
	mine.LastUsedAt = oauthNow.Add(-time.Hour)
	theirs.LastUsedAt = oauthNow
	expired.ExpiresAt = oauthNow.Add(-time.Second)
	for i, s := range []store.AgentSession{mine, theirs, elsewhere, expired} {
		require.NoError(t, oauth.CreateAgentSession(ctx, s, tokensFor(t, s, strconv.Itoa(i), oauthNow), ""))
	}

	all, err := oauth.AgentSessions(ctx, organization, 0, oauthNow)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, []string{theirs.ID, mine.ID}, []string{all[0].ID, all[1].ID}, "newest use first, and the expired and the other Organization's are left out")

	own, err := oauth.AgentSessions(ctx, organization, 7, oauthNow)
	require.NoError(t, err)
	require.Len(t, own, 1)
	assert.Equal(t, mine.ID, own[0].ID)
}
