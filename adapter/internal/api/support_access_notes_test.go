package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/client/tailnet/tailnettest"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/service"
)

const noCaseFourHours = `{"level":"readOnly","hours":4}`

// addressHost is the DNS name the support address is served under.
func addressHost(address string) string {
	return strings.TrimPrefix(address, "svc:") + "." + tailnettest.Domain
}

// caseNotes are the comments posted to the fake case, in order.
func (f *fakeServiceNow) caseNotes() []string {
	var notes []string
	for _, req := range f.seen() {
		if comment, ok := req.Body["comments"].(string); ok {
			notes = append(notes, comment)
		}
	}

	return notes
}

func newNotedWorld(t *testing.T) (*tailnetWorld, *fakeServiceNow) {
	t.Helper()

	fake := newFakeServiceNow(t)
	world := newTailnetWorld(t, func(deps *api.Deps) {
		deps.Support = service.NewSupport(fake.client(t), service.WithSupportGuideURL(guideURL))
	})

	return world, fake
}

const guideURL = "https://guide.example/support-access"

func TestAnInstallConfirmationNotesTheCaseWithTheAddressAndWhatWasGranted(t *testing.T) {
	world, fake := newNotedWorld(t)
	windowID, work := world.openWindow(t)
	assert.Empty(t, fake.caseNotes(), "the window is still starting, so access is not open yet")

	world.agentBeats(t, world.installed(windowID, work))

	notes := fake.caseNotes()
	require.Len(t, notes, 1)
	stored := world.record(t, "spoke-a")
	assert.Contains(t, notes[0], addressHost(stored.Address))
	assert.Contains(t, notes[0], "Steps for the engineer: "+guideURL, "the note points at the steps rather than repeating them")
	assert.Contains(t, notes[0], "spoke-a")
	assert.Contains(t, notes[0], "Read only")
	assert.Contains(t, notes[0], "2026-09-08 16:00 UTC")
	assert.Contains(t, notes[0], "Reported by the Percona Database Service control plane.")
	assert.NotContains(t, notes[0], work.Key, "a key is never written to a case")
	assert.NotContains(t, notes[0], stored.Window.KeyID)
}

func TestARepeatedInstallConfirmationNotesTheCaseOnce(t *testing.T) {
	world, fake := newNotedWorld(t)
	windowID, work := world.openWindow(t)

	for range 3 {
		world.agentBeats(t, world.installed(windowID, work))
	}

	assert.Len(t, fake.caseNotes(), 1)
}

func TestEndingAWindowNotesTheCaseWhenItEnded(t *testing.T) {
	world, fake := newNotedWorld(t)
	windowID, work := world.openWindow(t)
	world.agentBeats(t, world.installed(windowID, work))

	require.Equal(t, http.StatusNoContent, world.end(t).Code)

	notes := fake.caseNotes()
	require.Len(t, notes, 2)
	assert.Contains(t, notes[1], "ended")
	assert.Contains(t, notes[1], "spoke-a")
	assert.Contains(t, notes[1], "Reported by the Percona Database Service control plane.")
	assert.NotContains(t, notes[1], addressHost(world.record(t, "spoke-a").Address))
	assert.NotContains(t, notes[1], work.Key)
}

func TestEndingAWindowThatIsAlreadyClosedNotesNothingMore(t *testing.T) {
	world, fake := newNotedWorld(t)
	windowID, work := world.openWindow(t)
	world.agentBeats(t, world.installed(windowID, work))
	require.Equal(t, http.StatusNoContent, world.end(t).Code)

	require.Equal(t, http.StatusNoContent, world.end(t).Code)

	assert.Len(t, fake.caseNotes(), 2)
}

func TestAWindowWithNoCaseReferenceNotesNothing(t *testing.T) {
	world, fake := newNotedWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, noCaseFourHours).Code)
	opens := supportWork(world.agentBeats(t, agentBeat{}), hub.WorkSupportOpen)
	require.Len(t, opens, 1)
	windowID := world.record(t, "spoke-a").Window.ID
	world.agentBeats(t, world.installed(windowID, opens[0]))

	require.Equal(t, http.StatusNoContent, world.end(t).Code)

	assert.Empty(t, fake.seen(), "no case reference, so ServiceNow is not called")
}

func TestAServiceNowFailureLeavesOpeningAndEndingUntouched(t *testing.T) {
	world, fake := newNotedWorld(t)
	fake.failStatus = http.StatusInternalServerError
	windowID, work := world.openWindow(t)

	world.agentBeats(t, world.installed(windowID, work))
	assert.Equal(t, "open", window(t, world.read(t))["state"])
	assert.Equal(t, http.StatusNoContent, world.end(t).Code)

	assert.Equal(t, "closed", window(t, world.read(t))["state"])
	assert.Contains(t, world.entryActions(t), "support_access.install")
	assert.Contains(t, world.logs.String(), "support access note")
}

func shareNote(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	note, _ := window(t, rec)["shareNote"].(string)

	return note
}

// readAsCaller reads the window as someone who may change support access.
func (w *tailnetWorld) readAsCaller(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, supportPath, nil)
	req.Header.Set("Authorization", "Bearer "+testHubOperatorToken)
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req)

	return rec
}

func TestAnOpenWindowGivesTheCallerTheSameTextTheCaseGets(t *testing.T) {
	world, fake := newNotedWorld(t)
	windowID, work := world.openWindow(t)
	world.agentBeats(t, world.installed(windowID, work))
	stored := world.record(t, "spoke-a")

	share := shareNote(t, world.readAsCaller(t))

	assert.Contains(t, share, addressHost(stored.Address))
	assert.Contains(t, share, "spoke-a")
	assert.Contains(t, share, "Read only")
	require.Len(t, fake.caseNotes(), 1)
	assert.Equal(t, share+"\nReported by the Percona Database Service control plane.", fake.caseNotes()[0],
		"the console and the case say the same thing")
}

func TestWithNoGuideLinkNeitherTheCaseNorTheCallerGetsAStepsLine(t *testing.T) {
	fake := newFakeServiceNow(t)
	world := newTailnetWorld(t, func(deps *api.Deps) {
		deps.Support = service.NewSupport(fake.client(t))
	})
	windowID, work := world.openWindow(t)
	world.agentBeats(t, world.installed(windowID, work))

	require.Len(t, fake.caseNotes(), 1)
	assert.NotContains(t, fake.caseNotes()[0], "Steps for the engineer")
	assert.NotContains(t, shareNote(t, world.readAsCaller(t)), "Steps for the engineer")
}

func TestAWindowWithNoServiceNowStillGivesTheCallerTheTextToShare(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)
	world.agentBeats(t, world.installed(windowID, work))

	assert.Contains(t, shareNote(t, world.readAsCaller(t)), addressHost(world.record(t, "spoke-a").Address))
}

func TestAReadWithNoCallerGetsNoTextToShare(t *testing.T) {
	world := newTailnetWorld(t)
	world.openedWindow(t)

	assert.Empty(t, shareNote(t, world.read(t)), "the address goes only to who may open and end the window")
}

func TestAWindowThatIsNotOpenGivesNoTextToShare(t *testing.T) {
	world := newTailnetWorld(t)
	windowID, work := world.openWindow(t)

	assert.Empty(t, shareNote(t, world.readAsCaller(t)), "a window still starting has no access to share")

	world.agentBeats(t, world.installed(windowID, work))
	require.Equal(t, http.StatusNoContent, world.end(t).Code)

	assert.Empty(t, shareNote(t, world.readAsCaller(t)), "an ended window has nothing to share")
}

func TestExtendingAWindowKeepsTheTextToShare(t *testing.T) {
	world := newTailnetWorld(t)
	world.openedWindow(t)

	rec := world.extend(t, `{"hours":24}`)

	assert.Contains(t, shareNote(t, rec), addressHost(world.record(t, "spoke-a").Address))
}

func TestTheAddressReachesOnlyTheOpenWindowResponse(t *testing.T) {
	world := newTailnetWorld(t)
	world.openedWindow(t)
	host := addressHost(world.record(t, "spoke-a").Address)
	assert.Contains(t, world.readAsCaller(t).Body.String(), host)

	elsewhere := do(t, world.srv, http.MethodGet, "/api/environments", nil).Body.String() +
		do(t, world.srv, http.MethodGet, "/api/environments/spoke-a/history", nil).Body.String() +
		world.logs.String()

	assert.NotContains(t, elsewhere, host)
}
