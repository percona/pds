package api_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
)

const (
	supportPath = "/api/environments/spoke-a/support-access"
	// supportAddress and supportKeyID are what the hub keeps in a window's row
	// and must never say to a customer.
	supportAddress = "t7q2-private.tail1234.example"
	supportKeyID   = "key-8f3a1c"
)

// supportWorld is a hub with a clock the test moves, the row store it can seed
// and read, and a history book, so a route test sees what a customer would.
type supportWorld struct {
	srv   *api.Server
	rows  *hub.MemorySupportPersist
	clock *time.Time
	hub   *hub.Service
	logs  bytes.Buffer
}

func newSupportWorld(t *testing.T) *supportWorld {
	t.Helper()

	rows := hub.NewMemorySupportPersist()
	require.NoError(t, rows.Save(t.Context(), "spoke-a", hub.SupportRecord{Address: supportAddress}))
	world := buildSupportWorld(t, rows, nil)
	world.rows = rows

	return world
}

// failingSupportPersist is a database that is down.
type failingSupportPersist struct{ err error }

func (f failingSupportPersist) Load(context.Context) (map[string]hub.SupportRecord, error) {
	return nil, f.err
}

func (f failingSupportPersist) Save(context.Context, string, hub.SupportRecord) error { return f.err }

func (f failingSupportPersist) Remove(context.Context, string) error { return f.err }

func buildSupportWorld(t *testing.T, rows hub.SupportPersist, tn hub.Tailnet, with ...func(*api.Deps)) *supportWorld {
	t.Helper()

	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	world := &supportWorld{clock: &now}

	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	inv.Register("spoke-b", "pds")
	hubSvc := hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
		Tailnet:       tn,
		Namespace:     testEverestNamespace,
		BackupStorage: testBackupStorage,
		Now:           func() time.Time { return *world.clock },
		Tokens:        hub.NewTokenStore(nil, time.Hour),
		SupportAccess: rows,
	})

	activity := service.NewActivity(nil, service.ActivityOptions{})
	activity.UseBook(service.NewMemoryBook(fixedTeam(1)))
	databases := service.NewDatabase(engine.NewRegistry(), service.WithHub(hubSvc), service.WithRecorder(activity))

	cfg := testConfig()
	cfg.HubAgentToken = "secret"
	cfg.HubAgentEnvironments = []string{"spoke-a", "spoke-b"}
	world.hub = hubSvc
	deps := api.Deps{
		Config:          cfg,
		Logger:          log.New(&world.logs, log.FormatJSON, slog.LevelDebug),
		Auth:            auth.NewStub(),
		Databases:       databases,
		Activity:        activity,
		ActivityDetails: service.NewActivityDetails(activity, databases),
	}
	for _, apply := range with {
		apply(&deps)
	}
	world.srv = api.NewServer(deps)

	return world
}

func (w *supportWorld) connect(t *testing.T) {
	t.Helper()

	beatHub(t, w.srv, "secret")
}

func (w *supportWorld) advance(d time.Duration) { *w.clock = w.clock.Add(d) }

func (w *supportWorld) open(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, w.srv, http.MethodPost, supportPath, strings.NewReader(body))
}

func (w *supportWorld) read(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, w.srv, http.MethodGet, supportPath, nil)
}

func (w *supportWorld) end(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, w.srv, http.MethodDelete, supportPath, nil)
}

// window is the support access window a read answered with, nil for none.
func window(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	got, _ := decodeBody(t, rec)["window"].(map[string]any)

	return got
}

// supportEntries are the activity entries a route answered with, newest first.
func supportEntries(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var out []map[string]any
	for _, entry := range activityEntries(t, decodeBody(t, rec)) {
		if strings.HasPrefix(entry["action"].(string), "support_access.") {
			out = append(out, entry)
		}
	}

	return out
}

const openFourHours = `{"level":"readOnly","hours":4,"caseReference":"CS0012345"}`

func TestReadingAnEnvironmentWithNoWindowAnswersNone(t *testing.T) {
	world := newSupportWorld(t)

	assert.Nil(t, window(t, world.read(t)))
}

func TestOpeningAWindowStartsItOpeningWithItsExpiryAndFixedChoices(t *testing.T) {
	world := newSupportWorld(t)
	world.connect(t)

	rec := world.open(t, `{"level":"admin","hours":24,"caseReference":"  CS0012345  "}`)

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	opened, _ := decodeBody(t, rec)["window"].(map[string]any)
	assert.Equal(t, "opening", opened["state"])
	assert.Equal(t, "admin", opened["level"])
	assert.Equal(t, "CS0012345", opened["caseReference"], "the case reference is trimmed")
	assert.Equal(t, "2026-09-09T12:00:00Z", opened["expiresAt"])
	assert.Equal(t, opened, window(t, world.read(t)), "a read answers with what the open did")
}

func TestOpeningRefusesValuesOutsideTheChoices(t *testing.T) {
	world := newSupportWorld(t)
	world.connect(t)

	for name, body := range map[string]string{
		"an unknown level":       `{"level":"root","hours":4}`,
		"no level":               `{"hours":4}`,
		"hours not on offer":     `{"level":"readOnly","hours":2}`,
		"no hours":               `{"level":"readOnly"}`,
		"a long case reference":  `{"level":"readOnly","hours":4,"caseReference":"` + strings.Repeat("9", 65) + `"}`,
		"a long padded case ref": `{"level":"readOnly","hours":4,"caseReference":"` + strings.Repeat(" ", 80) + strings.Repeat("9", 65) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := world.open(t, body)

			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
			assert.Equal(t, api.CodeValidation, decodeBody(t, rec)["code"])
			assert.Nil(t, window(t, world.read(t)), "a refused open leaves no window")
		})
	}
}

func TestOpeningKeepsACaseReferenceThatFillsTheLimitOnceTrimmed(t *testing.T) {
	world := newSupportWorld(t)
	world.connect(t)

	rec := world.open(t, `{"level":"readOnly","hours":1,"caseReference":"  `+strings.Repeat("9", 64)+`  "}`)

	assert.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
}

func TestOpeningRefusesABodyThatIsNotTheDocumentedShape(t *testing.T) {
	world := newSupportWorld(t)
	world.connect(t)

	for name, body := range map[string]string{
		"not JSON":       `level=readOnly`,
		"an extra field": `{"level":"readOnly","hours":4,"address":"anywhere"}`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, http.StatusBadRequest, world.open(t, body).Code)
		})
	}
}

func TestOpeningOnAnEnvironmentThatIsNotConnectedIsRefused(t *testing.T) {
	world := newSupportWorld(t)

	rec := world.open(t, openFourHours)

	assert.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, api.CodeConflict, decodeBody(t, rec)["code"])
	assert.Nil(t, window(t, world.read(t)))
}

func TestOpeningOnAnEnvironmentTheTeamDoesNotHaveIsNotFound(t *testing.T) {
	world := newSupportWorld(t)
	world.connect(t)

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		rec := do(t, world.srv, method, "/api/environments/no-such-env/support-access", strings.NewReader(openFourHours))

		assert.Equal(t, http.StatusNotFound, rec.Code, method)
	}
}

func TestASecondOpenWhileAWindowIsActiveIsRefusedAndChangesNothing(t *testing.T) {
	world := newSupportWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
	before := window(t, world.read(t))

	rec := world.open(t, `{"level":"admin","hours":72,"caseReference":"CS0099999"}`)

	assert.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, before, window(t, world.read(t)), "level, case reference and expiry stay as approved")
}

func TestAWindowThatRanOutReadsClosedAsExpiredAndAnotherMayOpen(t *testing.T) {
	world := newSupportWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, `{"level":"readOnly","hours":1}`).Code)

	world.advance(time.Hour)
	expired := window(t, world.read(t))

	assert.Equal(t, "closed", expired["state"])
	assert.Equal(t, "expired", expired["closedReason"])
	world.connect(t)
	assert.Equal(t, http.StatusCreated, world.open(t, `{"level":"admin","hours":1}`).Code)
}

func TestEndingClosesTheWindowWithItsReasonAndAnotherMayOpen(t *testing.T) {
	world := newSupportWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)

	rec := world.end(t)

	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
	ended := window(t, world.read(t))
	assert.Equal(t, "closed", ended["state"])
	assert.Equal(t, "ended", ended["closedReason"])
	assert.Equal(t, http.StatusCreated, world.open(t, `{"level":"admin","hours":1}`).Code)
}

func TestEndingAClosedWindowAnswersNoContentAndWritesNoSecondEntry(t *testing.T) {
	world := newSupportWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
	require.Equal(t, http.StatusNoContent, world.end(t).Code)

	again := world.end(t)

	assert.Equal(t, http.StatusNoContent, again.Code)
	entries := supportEntries(t, do(t, world.srv, http.MethodGet, activityPath, nil))
	assert.Len(t, entries, 2, "one entry for the open and one for the end")
}

func TestEndingWhereNothingWasOpenAnswersNoContentAndWritesNoEntry(t *testing.T) {
	world := newSupportWorld(t)

	rec := world.end(t)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, supportEntries(t, do(t, world.srv, http.MethodGet, activityPath, nil)))
}

func TestEndingAWindowThatAlreadyRanOutWritesNoEntry(t *testing.T) {
	world := newSupportWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, `{"level":"readOnly","hours":1}`).Code)
	world.advance(2 * time.Hour)

	assert.Equal(t, http.StatusNoContent, world.end(t).Code)

	entries := supportEntries(t, do(t, world.srv, http.MethodGet, activityPath, nil))
	assert.Len(t, entries, 1, "only the open")
}

func environmentRow(t *testing.T, world *supportWorld) map[string]any {
	t.Helper()

	rec := do(t, world.srv, http.MethodGet, "/api/environments", nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	for _, item := range decodeBody(t, rec)["environments"].([]any) {
		if row := item.(map[string]any); row["name"] == "spoke-a" {
			return row
		}
	}
	t.Fatal("the list has no spoke-a")

	return nil
}

func TestTheEnvironmentsListCarriesTheSummaryOnlyWhileAWindowIsActive(t *testing.T) {
	world := newSupportWorld(t)
	world.connect(t)
	assert.NotContains(t, environmentRow(t, world), "supportAccess", "no window yet")

	require.Equal(t, http.StatusCreated, world.open(t, `{"level":"admin","hours":4,"caseReference":"CS0012345"}`).Code)
	assert.Equal(t, map[string]any{"state": "opening", "level": "admin", "expiresAt": "2026-09-08T16:00:00Z"},
		environmentRow(t, world)["supportAccess"], "the summary says no more than state, level and expiry")

	require.Equal(t, http.StatusNoContent, world.end(t).Code)
	assert.NotContains(t, environmentRow(t, world), "supportAccess", "ended")

	require.Equal(t, http.StatusCreated, world.open(t, `{"level":"readOnly","hours":1}`).Code)
	world.advance(time.Hour)
	assert.NotContains(t, environmentRow(t, world), "supportAccess", "ran out")
}

func TestOpeningAndEndingAreInTheEnvironmentHistoryAndTheGlobalFeed(t *testing.T) {
	world := newSupportWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
	require.Equal(t, http.StatusNoContent, world.end(t).Code)

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"history": do(t, world.srv, http.MethodGet, "/api/environments/spoke-a/history", nil),
		"feed":    do(t, world.srv, http.MethodGet, activityPath, nil),
	} {
		entries := supportEntries(t, rec)

		require.Len(t, entries, 2, name)
		assert.Equal(t, "support_access.end", entries[0]["action"], name)
		assert.Equal(t, "support_access.open", entries[1]["action"], name)
		for _, entry := range entries {
			assert.Equal(t, "spoke-a", entry["target"], name)
			assert.Equal(t, "succeeded", entry["outcome"], name)
		}
		assert.Contains(t, entries[1]["detail"], "CS0012345", name)
	}
}

func TestAnotherEnvironmentsHistoryHoldsNoSupportAccessEntries(t *testing.T) {
	world := newSupportWorld(t)
	world.connect(t)
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)

	rec := do(t, world.srv, http.MethodGet, "/api/environments/eks-testing/history", nil)

	assert.Empty(t, supportEntries(t, rec))
}

// What the hub keeps to reach the environment is not the customer's to read, in
// any answer a window gives.
func TestNoAnswerCarriesTheSupportAddressOrKeyID(t *testing.T) {
	world := newSupportWorld(t)
	world.connect(t)
	require.NoError(t, world.rows.Save(t.Context(), "spoke-a", hub.SupportRecord{Address: supportAddress}))

	answers := map[string]*httptest.ResponseRecorder{}
	answers["open"] = world.open(t, openFourHours)
	answers["read"] = world.read(t)
	answers["list"] = do(t, world.srv, http.MethodGet, "/api/environments", nil)
	// A key is minted by a later change; the row holds it from then on.
	record, err := world.rows.Load(t.Context())
	require.NoError(t, err)
	row := record["spoke-a"]
	require.NotNil(t, row.Window, "the open made a window")
	row.Window.KeyID = supportKeyID
	require.NoError(t, world.rows.Save(t.Context(), "spoke-a", row))
	answers["read with a key"] = world.read(t)
	answers["refused open"] = world.open(t, openFourHours)
	answers["end"] = world.end(t)
	answers["read ended"] = world.read(t)
	answers["history"] = do(t, world.srv, http.MethodGet, "/api/environments/spoke-a/history", nil)
	answers["feed"] = do(t, world.srv, http.MethodGet, activityPath, nil)

	for name, rec := range answers {
		assert.NotContains(t, rec.Body.String(), supportAddress, name)
		assert.NotContains(t, rec.Body.String(), "tail1234", name)
		assert.NotContains(t, rec.Body.String(), supportKeyID, name)
	}
	kept, err := world.rows.Load(t.Context())
	require.NoError(t, err)
	assert.Equal(t, supportAddress, kept["spoke-a"].Address, "opening and ending keep the environment's address")
}

func TestARowThatCannotBeReadIsAnUnavailableControlPlane(t *testing.T) {
	world := buildSupportWorld(t, failingSupportPersist{err: errors.New("deadline exceeded talking to the database")}, nil)

	rec := world.read(t)

	assert.Equal(t, http.StatusBadGateway, rec.Code, "body: %s", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "database")
}

// Support access belongs to the environment, so its Team decides who can see it
// at all: a member of another Team is told the environment does not exist.
func TestSupportAccessIsScopedToTheEnvironmentsTeam(t *testing.T) {
	world := newScopeWorld(t, "")
	own := "/api/environments/acme-env/support-access"
	other := "/api/environments/pay-env/support-access"

	ann := world.as(t, "ann")
	read := world.call(t, ann, http.MethodGet, own, "")
	require.Equal(t, http.StatusOK, read.Code, "body: %s", read.Body.String())
	assert.Nil(t, decodeBody(t, read)["window"], "any member of the Team may read")

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		rec := world.call(t, ann, method, other, openFourHours)

		assert.Equal(t, http.StatusNotFound, rec.Code, method)
		assert.Equal(t, api.CodeNotFound, decodeBody(t, rec)["code"], method)
	}

	root := world.call(t, world.as(t, "root"), http.MethodGet, other, "")
	assert.Equal(t, http.StatusOK, root.Code, "an admin of the Organization acts in the environment's Team")
}

// Each refusal says what is wrong and the one thing to do about it.
func TestRefusalsSayWhatToDo(t *testing.T) {
	world := newSupportWorld(t)
	message := func(rec *httptest.ResponseRecorder) any { return decodeBody(t, rec)["message"] }

	assert.Equal(t, "Support access needs a connected environment. Try again once it reports in.",
		message(world.open(t, openFourHours)))
	world.connect(t)
	assert.Equal(t, "Choose read only or admin access.", message(world.open(t, `{"level":"root","hours":4}`)))
	assert.Equal(t, "Choose 1, 4, 24 or 72 hours.", message(world.open(t, `{"level":"admin","hours":2}`)))
	assert.Equal(t, "Shorten the case reference to 64 characters or fewer.",
		message(world.open(t, `{"level":"admin","hours":4,"caseReference":"`+strings.Repeat("9", 65)+`"}`)))
	require.Equal(t, http.StatusCreated, world.open(t, openFourHours).Code)
	assert.Equal(t, "Support access is already active on this environment. End it to open another.",
		message(world.open(t, openFourHours)))
	assert.Equal(t, "That environment does not exist. Check its name and try again.",
		message(do(t, world.srv, http.MethodGet, "/api/environments/no-such-env/support-access", nil)))
}
