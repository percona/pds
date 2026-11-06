package store_test

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

// nameSeq keeps every test in this file on its own Team. The tables are shared
// with whatever else ran against this server, and the Team id is exactly what is
// supposed to keep those apart.
//
// runID makes a login name unique per run as well as per test. A server outlives
// one go test, and a name reused across runs would find the rows the previous
// run left.
var (
	nameSeq atomic.Int64
	runID   = newRunID()
)

func newRunID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		panic("could not name this test run: " + err.Error())
	}

	return hex.EncodeToString(raw)
}

func newHubState(t *testing.T) *store.HubState {
	t.Helper()

	db := openLive(t)
	cipher, err := store.NewCipher(testKey(42))
	require.NoError(t, err)

	return db.Hub(nextTeam(t, db), cipher)
}

// nextLogin is a login name no other test, in this run or an earlier one, used.
func nextLogin(t *testing.T) string {
	t.Helper()

	return fmt.Sprintf("test-%s-%d", runID, nameSeq.Add(1))
}

// nextTeam makes an Organization with one Team and returns the Team's id, so a
// test has rows of its own to read and write.
func nextTeam(t *testing.T, db *store.Store) uint {
	t.Helper()

	now := time.Now().UTC()
	org := store.Organization{Name: nextLogin(t), CreatedAt: now}
	require.NoError(t, db.Gorm().WithContext(t.Context()).Create(&org).Error)
	team := store.Team{OrganizationID: org.ID, Name: "default", CreatedAt: now}
	require.NoError(t, db.Gorm().WithContext(t.Context()).Create(&team).Error)

	return team.ID
}

// organizationOf is the Organization a Team belongs to.
func organizationOf(t *testing.T, db *store.Store, team uint) uint {
	t.Helper()

	var row store.Team
	require.NoError(t, db.Gorm().WithContext(t.Context()).Take(&row, team).Error)

	return row.OrganizationID
}

func openLive(t *testing.T) *store.Store {
	t.Helper()

	db, err := store.Open(t.Context(), store.Options{URL: liveURL(t)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Migrate(t.Context()))

	return db
}

func TestEnvironmentsRoundTrip(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()

	require.NoError(t, hub.SaveEnvironment(ctx, "eks-prod", `{"namespace":"pds"}`))
	require.NoError(t, hub.SaveEnvironment(ctx, "kind-lab", `{"namespace":"lab"}`))

	got, err := hub.Environments(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.JSONEq(t, `{"namespace":"pds"}`, got["eks-prod"])
	assert.JSONEq(t, `{"namespace":"lab"}`, got["kind-lab"])
}

func TestSaveEnvironmentReplacesTheRow(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()

	require.NoError(t, hub.SaveEnvironment(ctx, "eks-prod", `{"seen":false}`))
	require.NoError(t, hub.SaveEnvironment(ctx, "eks-prod", `{"seen":true}`))

	got, err := hub.Environments(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `{"seen":true}`, got["eks-prod"])
}

func TestRemoveEnvironmentDropsOnlyThatRow(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()

	require.NoError(t, hub.SaveEnvironment(ctx, "eks-prod", `{"seen":true}`))
	require.NoError(t, hub.SaveEnvironment(ctx, "kind-lab", `{"seen":true}`))
	require.NoError(t, hub.RemoveEnvironment(ctx, "eks-prod"))

	got, err := hub.Environments(ctx)
	require.NoError(t, err)
	assert.NotContains(t, got, "eks-prod")
	assert.Contains(t, got, "kind-lab")
}

func TestOneTeamCannotSeeAnother(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(43))
	require.NoError(t, err)

	first := db.Hub(nextTeam(t, db), cipher)
	second := db.Hub(nextTeam(t, db), cipher)
	ctx := t.Context()

	require.NoError(t, first.SaveEnvironment(ctx, "shared-name", `{"owner":"first"}`))
	require.NoError(t, second.SaveEnvironment(ctx, "shared-name", `{"owner":"second"}`))
	require.NoError(t, first.SaveToken(ctx, "hash-a", `{"environment":"shared-name"}`))
	require.NoError(t, first.SaveWork(ctx, "shared-name", `[{"id":"w1"}]`))
	require.NoError(t, first.SaveConnection(ctx, "servicenow", map[string]string{"SN_API_KEY": "first-key"}))

	firstEnvironments, err := first.Environments(ctx)
	require.NoError(t, err)
	secondEnvironments, err := second.Environments(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `{"owner":"first"}`, firstEnvironments["shared-name"])
	assert.JSONEq(t, `{"owner":"second"}`, secondEnvironments["shared-name"])

	secondTokens, err := second.Tokens(ctx)
	require.NoError(t, err)
	assert.Empty(t, secondTokens)

	secondWork, err := second.Work(ctx)
	require.NoError(t, err)
	assert.Empty(t, secondWork)

	secondConn, err := second.Connection(ctx, "servicenow")
	require.NoError(t, err)
	assert.Empty(t, secondConn)
}

func TestTokensRoundTrip(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()

	require.NoError(t, hub.SaveToken(ctx, "hash-a", `{"environment":"eks-prod"}`))
	require.NoError(t, hub.SaveToken(ctx, "hash-b", `{"environment":"kind-lab"}`))

	got, err := hub.Tokens(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.JSONEq(t, `{"environment":"eks-prod"}`, got["hash-a"])
	assert.JSONEq(t, `{"environment":"kind-lab"}`, got["hash-b"])
}

func TestRemoveTokensDropsTheNamedHashes(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()

	require.NoError(t, hub.SaveToken(ctx, "hash-a", `{"environment":"eks-prod"}`))
	require.NoError(t, hub.SaveToken(ctx, "hash-b", `{"environment":"kind-lab"}`))
	require.NoError(t, hub.RemoveTokens(ctx, []string{"hash-a"}))

	got, err := hub.Tokens(ctx)
	require.NoError(t, err)
	assert.NotContains(t, got, "hash-a")
	assert.Contains(t, got, "hash-b")
}

func TestRemoveTokensWithNoHashesIsNotAnError(t *testing.T) {
	hub := newHubState(t)

	require.NoError(t, hub.RemoveTokens(t.Context(), nil))
}

func TestWorkRoundTrip(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()

	require.NoError(t, hub.SaveWork(ctx, "eks-prod", `[{"id":"w1","kind":"backup"}]`))

	got, err := hub.Work(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.JSONEq(t, `[{"id":"w1","kind":"backup"}]`, got["eks-prod"])
}

func TestSaveWorkKeepsAnEmptyQueueApartFromNoQueue(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()

	require.NoError(t, hub.SaveWork(ctx, "eks-prod", `[]`))

	got, err := hub.Work(ctx)
	require.NoError(t, err)
	require.Contains(t, got, "eks-prod", "a drained queue is a row, not a missing one")
	assert.JSONEq(t, `[]`, got["eks-prod"])
}

func TestConnectionValuesAreSealedInTheRow(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(44))
	require.NoError(t, err)
	team := nextTeam(t, db)
	hub := db.Hub(team, cipher)
	ctx := t.Context()

	require.NoError(t, hub.SaveConnection(ctx, "servicenow", map[string]string{
		"SN_API_KEY":      "sn_live_abc123",
		"SN_CLIENT_TOKEN": "ct_live_xyz789",
	}))

	var stored []string
	require.NoError(t, db.Gorm().WithContext(ctx).
		Model(&store.HubConnection{}).
		Where("organization_id = ?", organizationOf(t, db, team)).
		Pluck("sealed", &stored).Error)

	require.Len(t, stored, 2)
	for _, value := range stored {
		assert.NotContains(t, value, "sn_live_abc123")
		assert.NotContains(t, value, "ct_live_xyz789")
		assert.True(t, store.Sealed(value), "every stored credential has to be sealed")
	}
}

func TestConnectionRoundTrip(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()

	require.NoError(t, hub.SaveConnection(ctx, "servicenow", map[string]string{
		"SN_API_KEY":      "sn_live_abc123",
		"SN_CLIENT_TOKEN": "ct_live_xyz789",
	}))

	got, err := hub.Connection(ctx, "servicenow")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"SN_API_KEY":      "sn_live_abc123",
		"SN_CLIENT_TOKEN": "ct_live_xyz789",
	}, got)
}

func TestSaveConnectionDropsFieldsThatAreGone(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()

	require.NoError(t, hub.SaveConnection(ctx, "servicenow", map[string]string{
		"SN_API_KEY":      "key",
		"SN_CLIENT_TOKEN": "token",
		"SN_URL":          "https://example.service-now.com",
	}))
	require.NoError(t, hub.SaveConnection(ctx, "servicenow", map[string]string{
		"SN_API_KEY":      "key",
		"SN_CLIENT_TOKEN": "token",
	}))

	got, err := hub.Connection(ctx, "servicenow")
	require.NoError(t, err)
	assert.NotContains(t, got, "SN_URL", "a field the caller stopped sending must not survive as a stale value")
}

func TestRemoveConnectionDropsEveryField(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()

	require.NoError(t, hub.SaveConnection(ctx, "servicenow", map[string]string{"SN_API_KEY": "key"}))
	require.NoError(t, hub.SaveConnection(ctx, "cursor", map[string]string{"CURSOR_API_KEY": "key"}))
	require.NoError(t, hub.RemoveConnection(ctx, "servicenow"))

	gone, err := hub.Connection(ctx, "servicenow")
	require.NoError(t, err)
	assert.Empty(t, gone)

	kept, err := hub.Connection(ctx, "cursor")
	require.NoError(t, err)
	assert.NotEmpty(t, kept)
}

func TestConnectionReportsAValueItCannotOpen(t *testing.T) {
	db := openLive(t)
	writer, err := store.NewCipher(testKey(45))
	require.NoError(t, err)
	team := nextTeam(t, db)
	ctx := t.Context()

	require.NoError(t, db.Hub(team, writer).
		SaveConnection(ctx, "servicenow", map[string]string{"SN_API_KEY": "sn_live_abc123"}))

	other, err := store.NewCipher(testKey(46))
	require.NoError(t, err)

	_, err = db.Hub(team, other).Connection(ctx, "servicenow")

	require.Error(t, err, "a wrong key has to fail loudly rather than read as no credential")
	assert.NotContains(t, err.Error(), "sn_live_abc123")
}

func TestCloneKeysAreSealedAndRoundTrip(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(47))
	require.NoError(t, err)
	team := nextTeam(t, db)
	hub := db.Hub(team, cipher)
	ctx := t.Context()

	identity := "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNza\n-----END OPENSSH PRIVATE KEY-----"
	require.NoError(t, hub.SaveCloneKey(ctx, "eks-prod", identity))

	var stored []string
	require.NoError(t, db.Gorm().WithContext(ctx).
		Model(&store.HubCloneKey{}).
		Where("team_id = ?", team).
		Pluck("sealed", &stored).Error)
	require.Len(t, stored, 1)
	assert.NotContains(t, stored[0], "BEGIN OPENSSH PRIVATE KEY")

	got, err := hub.CloneKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"eks-prod": identity}, got)

	require.NoError(t, hub.RemoveCloneKey(ctx, "eks-prod"))
	after, err := hub.CloneKeys(ctx)
	require.NoError(t, err)
	assert.Empty(t, after)
}

func TestHubStateRejectsNoTeam(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(49))
	require.NoError(t, err)

	hub := db.Hub(0, cipher)

	_, err = hub.Environments(t.Context())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "team"))
}

func TestOneOrganizationCannotOpenAnothersSealedRows(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(71))
	require.NoError(t, err)
	first, second := nextTeam(t, db), nextTeam(t, db)
	ctx := t.Context()

	require.NoError(t, db.Hub(first, cipher).
		SaveConnection(ctx, "servicenow", map[string]string{"SN_API_KEY": "sn_live_abc123"}))
	require.NoError(t, db.Hub(first, cipher).SaveCloneKey(ctx, "eks-prod", "identity"))

	// Both Teams share one key, so only the binding stops a row being moved.
	require.NoError(t, db.Gorm().WithContext(ctx).Exec(
		`INSERT INTO hub_connections (organization_id, name, field, sealed, updated_at)
		 SELECT ?, name, field, sealed, updated_at FROM hub_connections WHERE organization_id = ?`,
		organizationOf(t, db, second), organizationOf(t, db, first)).Error)
	require.NoError(t, db.Gorm().WithContext(ctx).Exec(
		`INSERT INTO hub_clone_keys (team_id, environment, sealed, updated_at)
		 SELECT ?, environment, sealed, updated_at FROM hub_clone_keys WHERE team_id = ?`,
		second, first).Error)

	_, err = db.Hub(second, cipher).Connection(ctx, "servicenow")
	require.ErrorIs(t, err, store.ErrUnsealable,
		"a credential copied into another organization's row must not open there")

	_, err = db.Hub(second, cipher).CloneKeys(ctx)
	require.ErrorIs(t, err, store.ErrUnsealable,
		"a clone identity copied into another team's row must not open there")
}

func TestCloneKeysAreScopedToOneTeam(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(72))
	require.NoError(t, err)
	first := db.Hub(nextTeam(t, db), cipher)
	second := db.Hub(nextTeam(t, db), cipher)
	ctx := t.Context()

	require.NoError(t, first.SaveCloneKey(ctx, "eks-prod", "identity"))

	got, err := second.CloneKeys(ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "clone identities are private keys, so the scoping matters most here")
}

func TestSupportAccessIsReplacedPerEnvironmentAndKeptApartPerTeam(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(44))
	require.NoError(t, err)
	first := db.Hub(nextTeam(t, db), cipher)
	second := db.Hub(nextTeam(t, db), cipher)
	ctx := t.Context()

	require.NoError(t, first.SaveSupportAccess(ctx, "eks-prod", `{"window":{"state":"opening"}}`))
	require.NoError(t, first.SaveSupportAccess(ctx, "eks-prod", `{"window":{"state":"closed"}}`))
	require.NoError(t, first.SaveSupportAccess(ctx, "eks-test", `{"address":"a"}`))

	got, err := first.SupportAccess(ctx)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.JSONEq(t, `{"window":{"state":"closed"}}`, got["eks-prod"], "the second save replaced the first")
	other, err := second.SupportAccess(ctx)
	require.NoError(t, err)
	assert.Empty(t, other, "another Team holds none")
}

func TestRemovingSupportAccessDropsOnlyThatEnvironmentsRowOfThatTeam(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(45))
	require.NoError(t, err)
	first := db.Hub(nextTeam(t, db), cipher)
	second := db.Hub(nextTeam(t, db), cipher)
	ctx := t.Context()
	require.NoError(t, first.SaveSupportAccess(ctx, "eks-prod", `{"address":"a"}`))
	require.NoError(t, first.SaveSupportAccess(ctx, "eks-test", `{"address":"b"}`))
	require.NoError(t, second.SaveSupportAccess(ctx, "eks-prod", `{"address":"c"}`))

	require.NoError(t, first.RemoveSupportAccess(ctx, "eks-prod"))

	got, err := first.SupportAccess(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.JSONEq(t, `{"address":"b"}`, got["eks-test"], "the other environment's document stays")
	kept, err := second.SupportAccess(ctx)
	require.NoError(t, err)
	assert.Len(t, kept, 1, "another Team's environment of the same name stays")
}

func TestEveryTeamsSupportAccessIsReadAcrossTeams(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(45))
	require.NoError(t, err)
	first := db.Hub(nextTeam(t, db), cipher)
	second := db.Hub(nextTeam(t, db), cipher)
	ctx := t.Context()
	require.NoError(t, first.SaveSupportAccess(ctx, "eks-prod", `{"address":"svc:pds-a"}`))
	require.NoError(t, second.SaveSupportAccess(ctx, "eks-prod", `{"address":"svc:pds-b"}`))

	got, err := db.AllSupportAccess(ctx)

	require.NoError(t, err)
	var addresses []string
	for _, document := range got {
		var rec struct {
			Address string `json:"address"`
		}
		require.NoError(t, json.Unmarshal([]byte(document), &rec))
		addresses = append(addresses, rec.Address)
	}
	assert.Contains(t, addresses, "svc:pds-a")
	assert.Contains(t, addresses, "svc:pds-b")
}

func TestSupportAccessNeedsAnEnvironment(t *testing.T) {
	hub := newHubState(t)

	require.Error(t, hub.SaveSupportAccess(t.Context(), " ", `{}`))
}
