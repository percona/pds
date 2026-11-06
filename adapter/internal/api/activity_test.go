package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
)

const activityPath = "/api/activity"

// newActivityServer builds the router with an in memory ring, which is the
// same code path a laptop run takes.
func newActivityServer(t *testing.T) (*api.Server, *service.Activity) {
	t.Helper()

	activity := service.NewActivity(nil, service.ActivityOptions{})
	srv := api.NewServer(api.Deps{
		Config:          testConfig(),
		Logger:          log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:            auth.NewStub(),
		Activity:        activity,
		ActivityDetails: service.NewActivityDetails(activity, nil),
	})

	return srv, activity
}

func activityEntries(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()

	raw, ok := body["entries"].([]any)
	require.True(t, ok, "entries is not a list: %v", body["entries"])

	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		typed, ok := entry.(map[string]any)
		require.True(t, ok)

		out = append(out, typed)
	}

	return out
}

func TestActivityIsNotImplementedWithoutARing(t *testing.T) {
	srv := api.NewServer(api.Deps{
		Config: testConfig(),
		Logger: log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:   auth.NewStub(),
	})

	rec := do(t, srv, http.MethodGet, activityPath, nil)

	assert.Equal(t, http.StatusNotImplemented, rec.Code)
	assert.Equal(t, "ErrNotImplemented", decodeBody(t, rec)["code"])
}

func TestActivityIsAnEmptyListBeforeAnythingHappens(t *testing.T) {
	srv, _ := newActivityServer(t)

	rec := do(t, srv, http.MethodGet, activityPath, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	body := decodeBody(t, rec)
	assert.Empty(t, activityEntries(t, body))
	assert.Equal(t, "", body["cursor"])
	assert.Equal(t, false, body["truncated"])
}

func TestActivityReturnsNewestFirst(t *testing.T) {
	srv, activity := newActivityServer(t)
	ctx := context.Background()

	activity.Record(ctx, service.ActivityEvent{
		Action: service.ActionDatabaseCreate, DatabaseID: "ns.orders", Target: "orders",
	})
	activity.Record(ctx, service.ActivityEvent{
		Action: service.ActionDatabaseBackup, DatabaseID: "ns.orders", Target: "orders",
	})

	rec := do(t, srv, http.MethodGet, activityPath, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	body := decodeBody(t, rec)
	entries := activityEntries(t, body)
	require.Len(t, entries, 2)

	assert.Equal(t, service.ActionDatabaseBackup, entries[0]["action"])
	assert.Equal(t, service.ActionDatabaseCreate, entries[1]["action"])
	assert.Equal(t, "00000000000000000002", body["cursor"])

	first := entries[0]
	assert.Equal(t, "orders", first["target"])
	assert.Equal(t, "ns.orders", first["databaseId"])
	assert.Equal(t, service.ActorUnknown, first["actor"])
	assert.Equal(t, service.OutcomeSucceeded, first["outcome"])
	assert.NotEmpty(t, first["at"])
}

func TestActivityFiltersByDatabase(t *testing.T) {
	srv, activity := newActivityServer(t)
	ctx := context.Background()

	activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseCreate, DatabaseID: "ns.orders", Target: "orders"})
	activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseCreate, DatabaseID: "ns.billing", Target: "billing"})

	rec := do(t, srv, http.MethodGet, activityPath+"?databaseId=ns.billing", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	entries := activityEntries(t, decodeBody(t, rec))
	require.Len(t, entries, 1)
	assert.Equal(t, "billing", entries[0]["target"])
}

func TestActivitySinceReturnsOnlyWhatIsNew(t *testing.T) {
	srv, activity := newActivityServer(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseBackup, Target: "orders"})
	}

	rec := do(t, srv, http.MethodGet, activityPath+"?since=00000000000000000002", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	entries := activityEntries(t, decodeBody(t, rec))
	require.Len(t, entries, 1)
	assert.Equal(t, "00000000000000000003", entries[0]["id"])

	// What a live poll sees while nothing is happening.
	quiet := do(t, srv, http.MethodGet, activityPath+"?since=00000000000000000003", nil)
	require.Equal(t, http.StatusOK, quiet.Code)

	body := decodeBody(t, quiet)
	assert.Empty(t, activityEntries(t, body))
	assert.Equal(t, "00000000000000000003", body["cursor"])
}

func TestActivityLimitIsHonouredAndReportsTruncation(t *testing.T) {
	srv, activity := newActivityServer(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseBackup, Target: "orders"})
	}

	rec := do(t, srv, http.MethodGet, activityPath+"?limit=2", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	body := decodeBody(t, rec)
	assert.Len(t, activityEntries(t, body), 2)
	assert.Equal(t, true, body["truncated"])
}

func TestActivityRejectsAMalformedLimit(t *testing.T) {
	srv, _ := newActivityServer(t)

	for _, raw := range []string{"abc", "0", "-4"} {
		rec := do(t, srv, http.MethodGet, activityPath+"?limit="+raw, nil)

		assert.Equal(t, http.StatusBadRequest, rec.Code, "limit=%s", raw)
		assert.Equal(t, "ErrValidation", decodeBody(t, rec)["code"])
	}
}

// The response must never carry a credential, whatever the recorder was given.
func TestActivityResponseIsSanitized(t *testing.T) {
	srv, activity := newActivityServer(t)

	activity.Record(context.Background(), service.ActivityEvent{
		Action:  service.ActionConnectionUpdate,
		Target:  "servicenow",
		Outcome: service.OutcomeFailed,
		Detail:  "SN_API_KEY=abcd1234efgh5678",
		Error:   "dial mysql://appuser:hunter2@orders.svc:3306/orders refused",
	})

	rec := do(t, srv, http.MethodGet, activityPath, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	raw := rec.Body.String()
	for _, secret := range []string{"abcd1234efgh5678", "hunter2"} {
		assert.NotContains(t, raw, secret)
	}
}

func TestActivityRecordsAMutationEndToEnd(t *testing.T) {
	activity := service.NewActivity(nil, service.ActivityOptions{})
	srv := buildHubTestServer(t, "secret", true, nil, activity)
	beatHub(t, srv, "secret")

	created := do(t, srv, http.MethodPost, "/api/databases",
		strings.NewReader(`{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a"}`))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	rec := do(t, srv, http.MethodGet, activityPath, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	entries := activityEntries(t, decodeBody(t, rec))
	require.Len(t, entries, 1, "creating a database writes exactly one row")
	assert.Equal(t, service.ActionDatabaseCreate, entries[0]["action"])
	assert.Equal(t, "orders-dev", entries[0]["target"])
	assert.Equal(t, service.OutcomeSucceeded, entries[0]["outcome"])
	assert.Contains(t, entries[0]["detail"], "dev")
}

func TestActivityDetailReturnsTheRecordedRequest(t *testing.T) {
	srv, activity := newActivityServer(t)

	activity.Record(context.Background(), service.ActivityEvent{
		Action:       service.ActionDatabaseBackup,
		DatabaseID:   "pds.orders",
		Target:       "orders",
		ResourceKind: service.ResourceBackup,
		ResourceName: "orders-20260831041203",
	})
	id := activityEntries(t, decodeBody(t, do(t, srv, http.MethodGet, activityPath, nil)))[0]["id"]

	rec := do(t, srv, http.MethodGet, activityPath+"/"+id.(string), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := decodeBody(t, rec)
	event, ok := body["event"].(map[string]any)
	require.True(t, ok, "the detail carries the recorded event")
	assert.Equal(t, service.ActionDatabaseBackup, event["action"])
	assert.Equal(t, "orders-20260831041203", event["resourceName"])

	timeline, ok := body["timeline"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, timeline, "the request itself always opens the timeline")

	first, ok := timeline[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, service.TimelineRequest, first["source"])
}

// TestActivityDetailOfAnAgedOutEntryIs404 is what a bookmarked link hits once
// the ring has rolled past the row it points at.
func TestActivityDetailOfAnAgedOutEntryIs404(t *testing.T) {
	srv, _ := newActivityServer(t)

	rec := do(t, srv, http.MethodGet, activityPath+"/00000000000000009999", nil)

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, api.CodeNotFound, decodeBody(t, rec)["code"])
}

func TestActivityDetailIsNotImplementedWithoutARing(t *testing.T) {
	srv := api.NewServer(api.Deps{
		Config: testConfig(),
		Logger: log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:   auth.NewStub(),
	})

	rec := do(t, srv, http.MethodGet, activityPath+"/00000000000000000001", nil)

	require.Equal(t, http.StatusNotImplemented, rec.Code)
}

func TestActivityEntriesCarryTheClientTheSessionDeclared(t *testing.T) {
	srv, activity := newActivityServer(t)

	mcp := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: "acme", Client: auth.Client{Kind: auth.ClientMCP},
	})
	person := auth.WithIdentity(context.Background(), auth.Identity{Subject: "acme"})
	activity.Record(mcp, service.ActivityEvent{Action: service.ActionDatabaseCreate, DatabaseID: "ns.agent", Target: "agent"})
	activity.Record(person, service.ActivityEvent{Action: service.ActionDatabaseCreate, DatabaseID: "ns.person", Target: "person"})

	rec := do(t, srv, http.MethodGet, activityPath, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	byTarget := map[string]map[string]any{}
	for _, entry := range activityEntries(t, decodeBody(t, rec)) {
		byTarget[entry["target"].(string)] = entry
	}

	assert.Equal(t, "acme", byTarget["agent"]["actor"], "the actor stays the login")
	assert.Equal(t, "mcp", byTarget["agent"]["client"])
	assert.NotContains(t, byTarget["person"], "client", "an entry with no declared client shows none")
}

func TestActivityEntriesCarryTheChatAndTheAgentWhenThereIsOne(t *testing.T) {
	srv, activity := newActivityServer(t)

	chat := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: "acme", Client: auth.Client{Kind: auth.ClientChat, Agent: "Anthropic"},
	})
	person := auth.WithIdentity(context.Background(), auth.Identity{Subject: "acme"})
	activity.Record(chat, service.ActivityEvent{Action: service.ActionDatabaseCreate, DatabaseID: "ns.chat", Target: "chat"})
	activity.Record(person, service.ActivityEvent{Action: service.ActionDatabaseCreate, DatabaseID: "ns.person", Target: "person"})

	list := do(t, srv, http.MethodGet, activityPath, nil)
	require.Equal(t, http.StatusOK, list.Code)
	byTarget := map[string]map[string]any{}
	for _, entry := range activityEntries(t, decodeBody(t, list)) {
		byTarget[entry["target"].(string)] = entry
	}

	assert.Equal(t, "chat", byTarget["chat"]["client"])
	assert.Equal(t, "Anthropic", byTarget["chat"]["agent"])
	assert.NotContains(t, byTarget["person"], "agent", "an entry with no agent shows none")

	for target, want := range map[string]any{"chat": "Anthropic", "person": nil} {
		detail := do(t, srv, http.MethodGet, activityPath+"/"+byTarget[target]["id"].(string), nil)
		require.Equal(t, http.StatusOK, detail.Code, detail.Body.String())
		event, ok := decodeBody(t, detail)["event"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, want, event["agent"], target)
		if want == nil {
			assert.NotContains(t, event, "agent")
		}
	}
}
