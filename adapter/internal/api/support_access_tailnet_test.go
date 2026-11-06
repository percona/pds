package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/client/tailnet"
	"github.com/percona/pds/adapter/internal/client/tailnet/tailnettest"
	"github.com/percona/pds/adapter/internal/hub"
)

const (
	supportPathB       = "/api/environments/spoke-b/support-access"
	testEnvironmentTag = "tag:pds-environment"
	testServiceTag     = "tag:pds-support"
)

// tailnetWorld is a support world whose hub reaches a fake tailnet through the
// real client.
type tailnetWorld struct {
	*supportWorld
	fake *tailnettest.Server
	// t is the test the world was made for, which helpers that play the
	// environment's side use to read the hub's record.
	t *testing.T
}

func newTailnetWorld(t *testing.T, with ...func(*api.Deps)) *tailnetWorld {
	t.Helper()

	fake := tailnettest.New(t)
	client, err := tailnet.New(tailnet.Options{
		BaseURL:             fake.URL,
		Tailnet:             tailnettest.Tailnet,
		Domain:              tailnettest.Domain,
		KeyClientID:         tailnettest.KeyClientID,
		KeyClientSecret:     tailnettest.KeyClientSecret,
		ServiceClientID:     tailnettest.ServiceClientID,
		ServiceClientSecret: tailnettest.ServiceClientSecret,
		EnvironmentTag:      testEnvironmentTag,
		ServiceTag:          testServiceTag,
	})
	require.NoError(t, err)
	rows := hub.NewMemorySupportPersist()
	world := buildSupportWorld(t, rows, client, with...)
	world.rows = rows

	return &tailnetWorld{supportWorld: world, fake: fake, t: t}
}

func beatEnvironment(t *testing.T, srv http.Handler, environment string) {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"environment": environment,
		"namespace":   "pds",
		"at":          time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/"+environment+"/heartbeat",
		bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// record is what the hub keeps for an environment.
func (w *tailnetWorld) record(t *testing.T, environment string) hub.SupportRecord {
	t.Helper()

	rows, err := w.rows.Load(t.Context())
	require.NoError(t, err)

	return rows[environment]
}

func TestTheFirstOpenCreatesOneAddressAndLaterOpensReuseIt(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)

	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
	first := world.record(t, "spoke-a").Address
	require.Equal(t, http.StatusNoContent, world.end(t).Code)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)

	assert.NotEmpty(t, first)
	assert.Equal(t, first, world.record(t, "spoke-a").Address, "an environment keeps its address")
	require.Len(t, world.fake.Services(), 1)
	assert.Equal(t, first, world.fake.Services()[0].Name)
}

func TestAnAddressIsNamedFromARandomIDAndNeverSharedBetweenEnvironments(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)
	beatEnvironment(t, world.srv, "spoke-b")

	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
	rec := do(t, world.srv, http.MethodPost, supportPathB, bytes.NewReader([]byte(openFourHours)))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	a, b := world.record(t, "spoke-a").Address, world.record(t, "spoke-b").Address
	assert.NotEqual(t, a, b)
	assert.NotContains(t, a, "spoke-a")
	assert.NotContains(t, b, "spoke-b")
	assert.Len(t, world.fake.Services(), 2)
}

func TestAnAddressThatDoesNotMatchThePolicyRefusesTheOpenAndAlertsTheHub(t *testing.T) {
	cases := map[string]tailnettest.Service{
		"a changed tag":  {Tags: []string{testServiceTag, "tag:broader"}, Ports: []string{"tcp:443"}},
		"a changed port": {Tags: []string{testServiceTag}, Ports: []string{"tcp:443", "tcp:22"}},
	}
	for name, edited := range cases {
		t.Run(name, func(t *testing.T) {
			world := newTailnetWorld(t)
			world.connect(t)
			require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
			require.Equal(t, http.StatusNoContent, world.end(t).Code)
			edited.Name = world.record(t, "spoke-a").Address
			world.fake.SetService(edited)
			world.connect(t)
			world.logs.Reset()

			rec := world.open(t, openFourHours)

			assert.Equal(t, http.StatusBadGateway, rec.Code, "body: %s", rec.Body.String())
			assert.Equal(t, "Contact Percona support.", decodeBody(t, rec)["message"])
			assert.NotContains(t, strings.ToLower(rec.Body.String()), "tailscale")
			assert.Contains(t, world.logs.String(), "does not match the policy", "the hub is told")
			assert.Equal(t, "closed", window(t, world.read(t))["state"], "a refused open starts no window")
		})
	}
}

func TestAnUnreachableTailnetRefusesTheOpenWithTheSafeSentence(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)
	world.fake.Fail()

	rec := world.open(t, openFourHours)

	assert.Equal(t, http.StatusBadGateway, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "Contact Percona support.", decodeBody(t, rec)["message"])
	assert.Nil(t, window(t, world.read(t)))
}

func TestEndingAWindowRevokesItsKeyByIDAndDeletesItsDevice(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
	_, err := world.hub.MintSupportKey(t.Context(), "spoke-a")
	require.NoError(t, err)
	windowID := world.record(t, "spoke-a").Window.ID
	world.fake.AddDevice("d-mine", windowID, testEnvironmentTag)
	world.fake.AddDevice("d-other", "win_someone_else", testEnvironmentTag)

	require.Equal(t, http.StatusNoContent, world.end(t).Code)

	require.Len(t, world.fake.Keys(), 1)
	assert.True(t, world.fake.Keys()[0].Revoked)
	assert.Equal(t, []tailnettest.Device{{ID: "d-other", Hostname: "win_someone_else", Tags: []string{testEnvironmentTag}}}, world.fake.Devices())
	assert.Equal(t, "closed", window(t, world.read(t))["state"])
	assert.Empty(t, world.record(t, "spoke-a").Window.KeyID, "no key id outlives its window")
}

func TestEndingAClosedWindowTouchesNothingOnTheTailnet(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
	require.Equal(t, http.StatusNoContent, world.end(t).Code)
	calls := len(world.fake.Requests())

	require.Equal(t, http.StatusNoContent, world.end(t).Code)

	assert.Len(t, world.fake.Requests(), calls)
}

func TestAWindowStaysOpenWhenItsAccessCannotBeTakenAway(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
	world.fake.Fail()

	rec := world.end(t)

	assert.Equal(t, http.StatusBadGateway, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "Contact Percona support.", decodeBody(t, rec)["message"])
	assert.Equal(t, "opening", window(t, world.read(t))["state"], "the customer is not told it ended")
}

func TestNeitherTheAddressNorAKeyReachesAResponseALogOrAStoredRow(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)
	opened := world.open(t, openFourHours)
	require.Equal(t, http.StatusCreated, opened.Code)
	secret, err := world.hub.MintSupportKey(t.Context(), "spoke-a")
	require.NoError(t, err)
	stored := world.record(t, "spoke-a")

	seen := opened.Body.String() + world.read(t).Body.String() +
		do(t, world.srv, http.MethodGet, "/api/environments", nil).Body.String() +
		do(t, world.srv, http.MethodGet, "/api/environments/spoke-a/history", nil).Body.String() +
		world.logs.String()
	row, err := json.Marshal(stored)
	require.NoError(t, err)

	for _, hidden := range []string{stored.Address, stored.Window.KeyID, secret} {
		assert.NotEmpty(t, hidden)
		assert.NotContains(t, seen, hidden)
	}
	assert.NotContains(t, string(row), secret, "the key is relayed once and never stored")
}

func (w *tailnetWorld) remove(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, w.srv, http.MethodDelete, "/api/environments/spoke-a", nil)
}

func TestRemovingAnEnvironmentClosesItsWindowAndDeletesItsDeviceAndAddress(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
	_, err := world.hub.MintSupportKey(t.Context(), "spoke-a")
	require.NoError(t, err)
	windowID := world.record(t, "spoke-a").Window.ID
	world.fake.AddDevice("d-mine", windowID, testEnvironmentTag)
	world.fake.AddDevice("d-other", "win_someone_else", testEnvironmentTag)

	rec := world.remove(t)

	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, []tailnettest.Device{{ID: "d-other", Hostname: "win_someone_else", Tags: []string{testEnvironmentTag}}}, world.fake.Devices())
	require.Len(t, world.fake.Keys(), 1)
	assert.True(t, world.fake.Keys()[0].Revoked)
	assert.Empty(t, world.fake.Services(), "the address goes with the environment")
	assert.Equal(t, hub.SupportRecord{}, world.record(t, "spoke-a"), "no record outlives the environment")
	removals := supportEntriesNamed(t, world, "support_access.remove")
	require.Len(t, removals, 1)
	assert.Equal(t, "spoke-a", removals[0]["target"])
	assert.Equal(t, "Access ended because the environment was removed.", removals[0]["detail"])
}

func TestRemovingAnEnvironmentWithNoWindowDeletesTheAddressMadeEarlier(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
	require.Equal(t, http.StatusNoContent, world.end(t).Code)
	require.Len(t, world.fake.Services(), 1)

	require.Equal(t, http.StatusNoContent, world.remove(t).Code)

	assert.Empty(t, world.fake.Services())
	assert.Equal(t, hub.SupportRecord{}, world.record(t, "spoke-a"))
	assert.Len(t, supportEntriesNamed(t, world, "support_access.remove"), 1)
}

func TestRemovingAnEnvironmentThatNeverHadSupportAccessWritesNoEntry(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)

	require.Equal(t, http.StatusNoContent, world.remove(t).Code)

	assert.Empty(t, supportEntriesNamed(t, world, "support_access.remove"))
}

func TestRemovingAnEnvironmentCompletesWhenTheTailnetFailsAndTheSweeperCleansUpLater(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
	windowID := world.record(t, "spoke-a").Window.ID
	world.fake.AddDevice("d-mine", windowID, testEnvironmentTag)
	world.fake.Fail()

	rec := world.remove(t)

	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, hub.SupportRecord{}, world.record(t, "spoke-a"))
	world.fake.Recover()
	client, err := tailnet.New(tailnet.Options{
		BaseURL:             world.fake.URL,
		Tailnet:             tailnettest.Tailnet,
		Domain:              tailnettest.Domain,
		KeyClientID:         tailnettest.KeyClientID,
		KeyClientSecret:     tailnettest.KeyClientSecret,
		ServiceClientID:     tailnettest.ServiceClientID,
		ServiceClientSecret: tailnettest.ServiceClientSecret,
		EnvironmentTag:      testEnvironmentTag,
		ServiceTag:          testServiceTag,
	})
	require.NoError(t, err)
	census := func(ctx context.Context) ([]hub.SupportRecord, error) {
		rows, err := world.rows.Load(ctx)
		out := make([]hub.SupportRecord, 0, len(rows))
		for _, row := range rows {
			out = append(out, row)
		}

		return out, err
	}

	require.NoError(t, hub.NewStraySupportSweeper(client, census, func() time.Time { return *world.clock }).Sweep(t.Context()))

	assert.Empty(t, world.fake.Devices())
	assert.Empty(t, world.fake.Services())
}

// supportEntriesNamed are the activity entries of one support access action.
func supportEntriesNamed(t *testing.T, world *tailnetWorld, action string) []map[string]any {
	t.Helper()

	var out []map[string]any
	for _, entry := range supportEntries(t, do(t, world.srv, http.MethodGet, activityPath, nil)) {
		if entry["action"] == action {
			out = append(out, entry)
		}
	}

	return out
}
