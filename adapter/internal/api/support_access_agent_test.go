package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/client/tailnet/tailnettest"
	"github.com/percona/pds/adapter/internal/hub"
)

// rawAgentReason is what an agent says went wrong. It names things a customer
// must never read, so the tests can tell where it travels.
const rawAgentReason = "pull ghcr.io/internal/proxy: 10.4.7.2 refused the connection"

// agentBeat is one heartbeat an environment's agent sends: what its last
// reply's work produced, the support windows whose workload it holds, which of
// those have a proxy holding its certificate, and what it did to them since the
// last beat.
type agentBeat struct {
	results       []hub.WorkResult
	held          []string
	serving       []string
	confirmations []hub.SupportConfirmation
}

// agentBeats plays the agent for spoke-a and returns the hub's reply.
func (w *tailnetWorld) agentBeats(t *testing.T, in agentBeat) hub.HeartbeatReply {
	t.Helper()

	held := in.held
	if held == nil {
		held = []string{}
	}
	payload, err := json.Marshal(map[string]any{
		"environment": "spoke-a",
		"namespace":   "pds",
		"at":          *w.clock,
		"results":     in.results,
		"support":     map[string]any{"held": held, "serving": in.serving, "confirmations": in.confirmations},
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var reply hub.HeartbeatReply
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &reply))

	return reply
}

func supportWork(reply hub.HeartbeatReply, kind string) []hub.Work {
	var out []hub.Work
	for _, item := range reply.Work {
		if item.Kind == kind {
			out = append(out, item)
		}
	}

	return out
}

// openWindow opens a window, takes its work on a beat and returns both.
func (w *tailnetWorld) openWindow(t *testing.T) (windowID string, work hub.Work) {
	t.Helper()

	w.connect(t)
	require.Equal(t, http.StatusCreated, w.open(t, openFourHours).Code)
	windowID = w.record(t, "spoke-a").Window.ID
	opens := supportWork(w.agentBeats(t, agentBeat{}), hub.WorkSupportOpen)
	require.Len(t, opens, 1)

	return windowID, opens[0]
}

func TestTheOpenWorkNamesTheHostTheProxyKeepsItsCertificateUnder(t *testing.T) {
	world := newTailnetWorld(t)

	_, work := world.openWindow(t)

	assert.True(t, strings.HasPrefix(work.AddressHost, "pds-"), work.AddressHost)
	assert.True(t, strings.HasSuffix(work.AddressHost, ".tail1234.ts.net"), work.AddressHost)
	assert.Equal(t, strings.TrimPrefix(work.Address, "svc:"), strings.TrimSuffix(work.AddressHost, "."+tailnettest.Domain))
}

// installed plays a window's proxy coming up and the agent confirming it: the
// device joins under the window's id, the network routes the address to it, and
// the proxy holds its certificate.
func (w *tailnetWorld) installed(windowID string, work hub.Work) agentBeat {
	w.proxyUp(windowID)
	beat := w.confirmed(windowID, work)
	beat.serving = []string{windowID}

	return beat
}

// proxyUp joins the window's device, if it has not, and has the policy approve
// it to host the environment's address.
func (w *tailnetWorld) proxyUp(windowID string) {
	deviceID := "d-" + windowID
	if !slices.ContainsFunc(w.fake.Devices(), func(d tailnettest.Device) bool { return d.ID == deviceID }) {
		w.fake.AddDevice(deviceID, windowID, testEnvironmentTag)
	}
	w.fake.Approve(w.record(w.t, "spoke-a").Address, deviceID, tailnettest.Approval{AutoApproved: true})
}

// confirmed is only the agent's word that it created the workload, whatever the
// network does with it.
func (w *tailnetWorld) confirmed(windowID string, work hub.Work) agentBeat {
	return agentBeat{
		results:       []hub.WorkResult{{ID: work.ID}},
		held:          []string{windowID},
		confirmations: []hub.SupportConfirmation{{Window: windowID, Event: hub.SupportInstalled}},
	}
}

func (w *tailnetWorld) entryActions(t *testing.T) []string {
	t.Helper()

	var actions []string
	for _, entry := range supportEntries(t, do(t, w.srv, http.MethodGet, "/api/environments/spoke-a/history", nil)) {
		actions = append(actions, entry["action"].(string))
	}

	return actions
}

func TestAnOpeningWindowReachesTheAgentWithItsFixedChoicesAndAOneTimeKey(t *testing.T) {
	world := newTailnetWorld(t)

	windowID, work := world.openWindow(t)

	stored := world.record(t, "spoke-a")
	assert.Equal(t, windowID, work.Window)
	assert.Equal(t, "readOnly", work.Level)
	assert.Equal(t, stored.Window.ExpiresAt, work.ExpiresAt)
	assert.Equal(t, stored.Address, work.Address, "the agent serves through the environment's address")
	assert.NotEmpty(t, work.Key)
	require.Len(t, world.fake.Keys(), 1)
	assert.Equal(t, world.fake.Keys()[0].ID, stored.Window.KeyID, "only the key's id is kept")
}

func TestAnOpenHandedOutAgainCarriesAFreshKeyNotAStoredOne(t *testing.T) {
	world := newTailnetWorld(t)
	_, work := world.openWindow(t)

	assert.Empty(t, supportWork(world.agentBeats(t, agentBeat{}), hub.WorkSupportOpen),
		"work in flight is not handed out again at once")
	world.advance(3 * time.Minute)
	again := supportWork(world.agentBeats(t, agentBeat{}), hub.WorkSupportOpen)

	require.Len(t, again, 1, "an item the agent never acknowledged comes back")
	assert.NotEmpty(t, again[0].Key)
	assert.NotEqual(t, work.Key, again[0].Key, "the first key was relayed once and is not kept")
	assert.True(t, world.fake.Keys()[0].Revoked)
}

func TestAnInstallConfirmationOpensTheWindowAndWritesOneEntryHoweverOftenItIsSent(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)

	for range 3 {
		world.agentBeats(t, world.installed(windowID, work))
	}

	assert.Equal(t, "open", window(t, world.read(t))["state"])
	assert.Equal(t, 1, countOf(world.entryActions(t), "support_access.install"))
}

func countOf(items []string, want string) int {
	n := 0
	for _, item := range items {
		if item == want {
			n++
		}
	}

	return n
}

func TestNoInstallConfirmationRevokesTheOldKeyAndMintsANewOne(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	first := world.record(t, "spoke-a").Window.KeyID

	reply := world.agentBeats(t, agentBeat{
		results: []hub.WorkResult{{ID: work.ID, Error: rawAgentReason}},
		held:    nil,
		confirmations: []hub.SupportConfirmation{
			{Window: windowID, Event: hub.SupportFailed, Reason: rawAgentReason},
		},
	})

	opens := supportWork(reply, hub.WorkSupportOpen)
	require.Len(t, opens, 1, "the open is handed out again")
	assert.NotEmpty(t, opens[0].Key)
	assert.NotEqual(t, work.Key, opens[0].Key)
	keys := world.fake.Keys()
	require.Len(t, keys, 2)
	assert.True(t, keys[0].Revoked)
	assert.False(t, keys[1].Revoked)
	assert.NotEqual(t, first, world.record(t, "spoke-a").Window.KeyID)
	assert.Equal(t, "opening", window(t, world.read(t))["state"])
}

func TestAWindowThatDoesNotStartInFiveMinutesClosesAsInstallFailed(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	world.fake.AddDevice("d-half", windowID, testEnvironmentTag)
	world.advance(5 * time.Minute)

	reply := world.agentBeats(t, agentBeat{
		results: []hub.WorkResult{{ID: work.ID}},
		confirmations: []hub.SupportConfirmation{
			{Window: windowID, Event: hub.SupportFailed, Reason: rawAgentReason},
		},
	})

	got := window(t, world.read(t))
	assert.Equal(t, "closed", got["state"])
	assert.Equal(t, "failed", got["closedReason"])
	assert.Empty(t, supportWork(reply, hub.WorkSupportOpen), "no new key after the limit")
	assert.Empty(t, world.fake.Devices(), "whatever joined is taken off the network")
	for _, key := range world.fake.Keys() {
		assert.True(t, key.Revoked)
	}
	assert.Equal(t, 1, countOf(world.entryActions(t), "support_access.fail"))
}

func TestTheRawAgentReasonReachesTheHubLogAndNoCustomer(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	world.logs.Reset()
	world.advance(5 * time.Minute)
	world.agentBeats(t, agentBeat{
		results: []hub.WorkResult{{ID: work.ID, Error: rawAgentReason}},
		confirmations: []hub.SupportConfirmation{
			{Window: windowID, Event: hub.SupportFailed, Reason: rawAgentReason},
		},
	})

	seen := world.read(t).Body.String() +
		do(t, world.srv, http.MethodGet, "/api/environments/spoke-a/history", nil).Body.String() +
		do(t, world.srv, http.MethodGet, "/api/environments", nil).Body.String()

	assert.NotContains(t, seen, "10.4.7.2")
	assert.NotContains(t, seen, "ghcr.io")
	assert.Contains(t, world.logs.String(), "10.4.7.2", "the hub is told what went wrong")
}

func TestEndingAWindowBeforeItStartsStopsTheOpenFromBeingHandedOut(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
	require.Equal(t, http.StatusNoContent, world.end(t).Code)

	reply := world.agentBeats(t, agentBeat{})

	assert.Empty(t, supportWork(reply, hub.WorkSupportOpen))
	assert.Len(t, world.fake.Keys(), 0, "no key is minted for a window that is over")
}

func TestEndingAWindowAsksTheAgentHoldingItsWorkloadToRemoveIt(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	world.agentBeats(t, world.installed(windowID, work))
	require.Equal(t, http.StatusNoContent, world.end(t).Code)

	reply := world.agentBeats(t, agentBeat{held: []string{windowID}})

	closes := supportWork(reply, hub.WorkSupportClose)
	require.Len(t, closes, 1)
	assert.Equal(t, windowID, closes[0].Window)
	assert.Empty(t, closes[0].Key)
}

func TestAWorkloadForAWindowTheHubDoesNotKnowIsClosed(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)

	reply := world.agentBeats(t, agentBeat{held: []string{"win_left_over"}})

	closes := supportWork(reply, hub.WorkSupportClose)
	require.Len(t, closes, 1)
	assert.Equal(t, "win_left_over", closes[0].Window)
}

func TestAWorkloadIsKeptWhileItsWindowIsOpen(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)

	reply := world.agentBeats(t, world.installed(windowID, work))

	assert.Empty(t, supportWork(reply, hub.WorkSupportClose))
}

func TestARemovalConfirmationWritesOneEntryHoweverOftenItIsSent(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	world.agentBeats(t, world.installed(windowID, work))
	require.Equal(t, http.StatusNoContent, world.end(t).Code)
	closeWork := supportWork(world.agentBeats(t, agentBeat{held: []string{windowID}}), hub.WorkSupportClose)
	require.Len(t, closeWork, 1)

	for range 3 {
		world.agentBeats(t, agentBeat{
			results:       []hub.WorkResult{{ID: closeWork[0].ID}},
			confirmations: []hub.SupportConfirmation{{Window: windowID, Event: hub.SupportRemoved}},
		})
	}

	assert.Equal(t, 1, countOf(world.entryActions(t), "support_access.uninstall"))
}

func TestAnAgentThatReportsNoSupportStateLeavesWindowsAlone(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)

	beatEnvironment(t, world.srv, "spoke-a")

	assert.Equal(t, "opening", window(t, world.read(t))["state"])
}

func TestAWorkloadThatComesUpAfterTheWindowEndedWritesNoOpenEntry(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	require.Equal(t, http.StatusNoContent, world.end(t).Code)

	world.agentBeats(t, world.installed(windowID, work))

	assert.Zero(t, countOf(world.entryActions(t), "support_access.install"))
	assert.Equal(t, "closed", window(t, world.read(t))["state"])
}

func (w *tailnetWorld) extend(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, w.srv, http.MethodPatch, supportPath, strings.NewReader(body))
}

// openedWindow opens a window and has the agent confirm it, so it reads open.
func (w *tailnetWorld) openedWindow(t *testing.T) string {
	t.Helper()

	windowID, work := w.openWindow(t)
	w.agentBeats(t, w.installed(windowID, work))
	require.Equal(t, "open", window(t, w.read(t))["state"])

	return windowID
}

func (w *tailnetWorld) extended(windowID string, until time.Time) agentBeat {
	return agentBeat{
		held:          []string{windowID},
		confirmations: []hub.SupportConfirmation{{Window: windowID, Event: hub.SupportExtended, ExpiresAt: until}},
	}
}

func TestAnExtensionWaitsForTheAgentAndTheWindowShowsWhereItIsGoing(t *testing.T) {
	world := newTailnetWorld(t)
	windowID := world.openedWindow(t)
	before := window(t, world.read(t))

	rec := world.extend(t, `{"hours":24}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	until := world.clock.Add(24 * time.Hour).UTC().Format(time.RFC3339)
	waiting := window(t, world.read(t))
	assert.Equal(t, "open", waiting["state"])
	assert.Equal(t, before["expiresAt"], waiting["expiresAt"], "the countdown keeps the time the environment has")
	assert.Equal(t, until, waiting["extendingTo"])
	assert.Equal(t, before["level"], waiting["level"])
	assert.Equal(t, before["caseReference"], waiting["caseReference"])

	works := supportWork(world.agentBeats(t, agentBeat{held: []string{windowID}}), hub.WorkSupportExtend)
	require.Len(t, works, 1)
	assert.Equal(t, windowID, works[0].Window)
	assert.Equal(t, until, works[0].ExpiresAt.UTC().Format(time.RFC3339))

	world.agentBeats(t, world.extended(windowID, works[0].ExpiresAt))
	done := window(t, world.read(t))
	assert.Equal(t, until, done["expiresAt"])
	assert.NotContains(t, done, "extendingTo")
}

func TestAnExtensionPastSeventyTwoHoursFromNowIsRefusedAndChangesNothing(t *testing.T) {
	world := newTailnetWorld(t)
	world.openedWindow(t)
	before := window(t, world.read(t))

	rec := world.extend(t, `{"hours":73}`)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Equal(t, api.CodeValidation, decodeBody(t, rec)["code"])
	assert.Equal(t, before, window(t, world.read(t)))
	assert.Equal(t, 0, countOf(world.entryActions(t), "support_access.extend"))
}

func TestAnExtensionThatDoesNotEndLaterThanTheWindowDoesIsRefused(t *testing.T) {
	world := newTailnetWorld(t)
	world.openedWindow(t)

	rec := world.extend(t, `{"hours":4}`)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}

func TestOnlyHoursCanBeSentToExtendAWindow(t *testing.T) {
	world := newTailnetWorld(t)
	world.openedWindow(t)

	rec := world.extend(t, `{"hours":24,"level":"admin"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "readOnly", window(t, world.read(t))["level"])
}

func TestAWindowThatIsNotOpenCannotBeExtended(t *testing.T) {
	world := newTailnetWorld(t)
	world.connect(t)

	assert.Equal(t, http.StatusConflict, world.extend(t, `{"hours":24}`).Code, "no window")

	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
	assert.Equal(t, http.StatusConflict, world.extend(t, `{"hours":24}`).Code, "still opening")

	require.Equal(t, http.StatusNoContent, world.end(t).Code)
	assert.Equal(t, http.StatusConflict, world.extend(t, `{"hours":24}`).Code, "ended")
}

func TestExtendingAnEnvironmentTheTeamDoesNotHaveIsNotFound(t *testing.T) {
	world := newTailnetWorld(t)

	rec := do(t, world.srv, http.MethodPatch, "/api/environments/no-such-env/support-access", strings.NewReader(`{"hours":24}`))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestEachExtensionWritesOneEntryAndTheAgentsRepeatedConfirmationWritesNone(t *testing.T) {
	world := newTailnetWorld(t)
	windowID := world.openedWindow(t)

	require.Equal(t, http.StatusOK, world.extend(t, `{"hours":24}`).Code)
	first := supportWork(world.agentBeats(t, agentBeat{held: []string{windowID}}), hub.WorkSupportExtend)[0]
	for range 3 {
		world.agentBeats(t, world.extended(windowID, first.ExpiresAt))
	}
	assert.Equal(t, 1, countOf(world.entryActions(t), "support_access.extend"))

	world.advance(time.Hour)
	require.Equal(t, http.StatusOK, world.extend(t, `{"hours":72}`).Code)
	second := supportWork(world.agentBeats(t, agentBeat{held: []string{windowID}}), hub.WorkSupportExtend)
	require.Len(t, second, 1, "a second extension is its own item")
	for range 2 {
		world.agentBeats(t, world.extended(windowID, second[0].ExpiresAt))
	}

	assert.Equal(t, 2, countOf(world.entryActions(t), "support_access.extend"))
	assert.Equal(t, second[0].ExpiresAt.UTC().Format(time.RFC3339), window(t, world.read(t))["expiresAt"])
}

func TestAnExtensionThatIsAskedForAgainBeforeTheAgentConfirmsReplacesTheFirst(t *testing.T) {
	world := newTailnetWorld(t)
	windowID := world.openedWindow(t)
	require.Equal(t, http.StatusOK, world.extend(t, `{"hours":24}`).Code)
	require.Len(t, supportWork(world.agentBeats(t, agentBeat{held: []string{windowID}}), hub.WorkSupportExtend), 1)
	require.Equal(t, http.StatusOK, world.extend(t, `{"hours":72}`).Code)

	works := supportWork(world.agentBeats(t, agentBeat{held: []string{windowID}}), hub.WorkSupportExtend)

	require.Len(t, works, 1)
	assert.Equal(t, world.clock.Add(72*time.Hour).UTC(), works[0].ExpiresAt.UTC())
}

func TestAConfirmationForAnExpiryTheHubIsNotWaitingForMovesNothing(t *testing.T) {
	world := newTailnetWorld(t)
	windowID := world.openedWindow(t)
	before := window(t, world.read(t))

	world.agentBeats(t, world.extended(windowID, world.clock.Add(48*time.Hour)))

	assert.Equal(t, before, window(t, world.read(t)))
}

func TestEndingAWindowDropsAnExtensionTheAgentHasNotRun(t *testing.T) {
	world := newTailnetWorld(t)
	windowID := world.openedWindow(t)
	require.Equal(t, http.StatusOK, world.extend(t, `{"hours":24}`).Code)
	require.Equal(t, http.StatusNoContent, world.end(t).Code)

	reply := world.agentBeats(t, agentBeat{held: []string{windowID}})

	assert.Empty(t, supportWork(reply, hub.WorkSupportExtend))
}

func TestAnInstalledWindowStaysOpeningUntilTheNetworkRoutesItsAddress(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)

	world.agentBeats(t, world.confirmed(windowID, work))

	assert.Equal(t, "opening", window(t, world.read(t))["state"], "a workload that exists is not access that works")
	assert.Zero(t, countOf(world.entryActions(t), "support_access.install"))

	world.proxyUp(windowID)
	world.agentBeats(t, agentBeat{held: []string{windowID}, serving: []string{windowID}})

	assert.Equal(t, "open", window(t, world.read(t))["state"])
	assert.Equal(t, 1, countOf(world.entryActions(t), "support_access.install"))
}

func TestAnApprovedWindowStaysOpeningUntilItsProxyHoldsACertificate(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	world.proxyUp(windowID)

	world.agentBeats(t, world.confirmed(windowID, work))

	assert.Equal(t, "opening", window(t, world.read(t))["state"], "an address with no certificate answers no one")
	assert.Zero(t, countOf(world.entryActions(t), "support_access.install"))

	world.agentBeats(t, agentBeat{held: []string{windowID}, serving: []string{windowID}})

	assert.Equal(t, "open", window(t, world.read(t))["state"])
}

func TestAnApprovedWindowWhoseProxyNeverGetsACertificateClosesAsInstallFailed(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	world.proxyUp(windowID)
	world.agentBeats(t, world.confirmed(windowID, work))

	world.advance(5 * time.Minute)
	world.agentBeats(t, agentBeat{held: []string{windowID}})

	got := window(t, world.read(t))
	assert.Equal(t, "closed", got["state"])
	assert.Equal(t, "failed", got["closedReason"])
}

func TestAWindowWhoseDeviceNobodyHasApprovedKeepsOpening(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	world.fake.AddDevice("d-"+windowID, windowID, testEnvironmentTag)

	world.agentBeats(t, world.confirmed(windowID, work))

	assert.Equal(t, "opening", window(t, world.read(t))["state"])
}

func TestAnOpenWindowCostsTheNetworkNoCallsOnLaterBeats(t *testing.T) {
	world := newTailnetWorld(t)
	windowID := world.openedWindow(t)
	before := len(world.fake.Requests())

	world.agentBeats(t, agentBeat{held: []string{windowID}})

	assert.Len(t, world.fake.Requests(), before, "once open, the window is not asked about again")
}

func TestAnInstalledWindowThatTheNetworkNeverServesClosesAsInstallFailed(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	world.fake.AddDevice("d-"+windowID, windowID, testEnvironmentTag)
	world.agentBeats(t, world.confirmed(windowID, work))

	world.advance(5 * time.Minute)
	reply := world.agentBeats(t, agentBeat{held: []string{windowID}})

	got := window(t, world.read(t))
	assert.Equal(t, "closed", got["state"])
	assert.Equal(t, "failed", got["closedReason"])
	assert.Empty(t, world.fake.Devices(), "the access that does not work is taken off the network")
	assert.Equal(t, 1, countOf(world.entryActions(t), "support_access.fail"))
	assert.NotEmpty(t, supportWork(reply, hub.WorkSupportClose), "the agent is told to remove what it installed")
}

func TestANetworkThatCannotAnswerLeavesAnOpenWindowOpen(t *testing.T) {
	world := newTailnetWorld(t)
	windowID := world.openedWindow(t)

	world.fake.Fail()
	world.agentBeats(t, agentBeat{held: []string{windowID}})

	assert.Equal(t, "open", window(t, world.read(t))["state"], "not knowing is not the same as not serving")
}

func TestANetworkThatCannotAnswerLeavesAnInstalledWindowOpening(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	world.proxyUp(windowID)

	world.fake.Fail()
	world.agentBeats(t, world.confirmed(windowID, work))

	assert.Equal(t, "opening", window(t, world.read(t))["state"])
}

// actorOf is who the environment's history says did the one support access
// entry with this action.
func (w *tailnetWorld) actorOf(t *testing.T, action string) string {
	t.Helper()

	var actors []string
	for _, entry := range supportEntries(t, do(t, w.srv, http.MethodGet, "/api/environments/spoke-a/history", nil)) {
		if entry["action"] == action {
			actors = append(actors, entry["actor"].(string))
		}
	}
	require.Len(t, actors, 1, action)

	return actors[0]
}

func TestAnInstallEntryReadsSystemAsItsActor(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	world.agentBeats(t, world.installed(windowID, work))

	assert.Equal(t, "system", world.actorOf(t, "support_access.install"))
}

func TestAnUninstallEntryReadsSystemAsItsActor(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	world.agentBeats(t, world.installed(windowID, work))
	require.Equal(t, http.StatusNoContent, world.end(t).Code)
	closeWork := supportWork(world.agentBeats(t, agentBeat{held: []string{windowID}}), hub.WorkSupportClose)
	require.Len(t, closeWork, 1)
	world.agentBeats(t, agentBeat{
		results:       []hub.WorkResult{{ID: closeWork[0].ID}},
		confirmations: []hub.SupportConfirmation{{Window: windowID, Event: hub.SupportRemoved}},
	})

	assert.Equal(t, "system", world.actorOf(t, "support_access.uninstall"))
}

func TestAStartFailedEntryReadsSystemAsItsActor(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	world.fake.AddDevice("d-half", windowID, testEnvironmentTag)
	world.advance(5 * time.Minute)
	world.agentBeats(t, agentBeat{
		results:       []hub.WorkResult{{ID: work.ID}},
		confirmations: []hub.SupportConfirmation{{Window: windowID, Event: hub.SupportFailed, Reason: rawAgentReason}},
	})

	assert.Equal(t, "system", world.actorOf(t, "support_access.fail"))
}

func TestASystemEntryReadsSystemAsItsActorInTheGlobalFeedToo(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	world.agentBeats(t, world.installed(windowID, work))

	entries := supportEntriesNamed(t, world, "support_access.install")

	require.Len(t, entries, 1)
	assert.Equal(t, "system", entries[0]["actor"])
}
