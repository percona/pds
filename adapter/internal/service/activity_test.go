package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/service"
)

// fakeActivityStore is a ConfigMapStore backed by a map, so the ring can be
// inspected as the bytes that would reach the Kubernetes cluster.
type fakeActivityStore struct {
	mu     sync.Mutex
	data   map[string]string
	getErr error
	putErr error
	puts   int
}

func newFakeActivityStore() *fakeActivityStore {
	return &fakeActivityStore{data: map[string]string{}}
}

func (f *fakeActivityStore) GetConfigMap(_ context.Context, _, _ string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.getErr != nil {
		return nil, f.getErr
	}
	if len(f.data) == 0 {
		return nil, kube.ErrConfigMapNotFound
	}
	out := make(map[string]string, len(f.data))
	for k, v := range f.data {
		out[k] = v
	}

	return out, nil
}

func (f *fakeActivityStore) PutConfigMap(_ context.Context, _, _ string, values map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.putErr != nil {
		return f.putErr
	}
	f.puts++
	for k, v := range values {
		f.data[k] = v
	}

	return nil
}

func (f *fakeActivityStore) raw() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.data["data.json"]
}

func newActivity(store service.ConfigMapStore) *service.Activity {
	return service.NewActivity(store, service.ActivityOptions{})
}

func TestRecordAssignsSequentialIDs(t *testing.T) {
	t.Parallel()

	store := newFakeActivityStore()
	activity := newActivity(store)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		activity.Record(ctx, service.ActivityEvent{
			Action:  service.ActionDatabaseCreate,
			Target:  fmt.Sprintf("db-%d", i),
			Outcome: service.OutcomeSucceeded,
		})
	}

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 3)

	// Newest first, so the highest sequence leads.
	assert.Equal(t, []string{"00000000000000000003", "00000000000000000002", "00000000000000000001"}, ids(page.Entries))
	assert.Equal(t, "00000000000000000003", page.Cursor)
	assert.Equal(t, "db-2", page.Entries[0].Target)
}

func TestRecordFillsDefaults(t *testing.T) {
	t.Parallel()

	activity := newActivity(newFakeActivityStore())
	ctx := context.Background()

	activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseScale, Target: "orders"})

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)

	got := page.Entries[0]
	assert.Equal(t, service.ActorUnknown, got.Actor, "no OIDC subject means the actor is unknown, not blank")
	assert.Equal(t, service.OutcomeSucceeded, got.Outcome, "an event without an error succeeded")
	assert.False(t, got.At.IsZero(), "the recorder stamps the time")
}

func TestRecordKeepsTheSessionClientThroughTheRing(t *testing.T) {
	t.Parallel()

	ring := newFakeActivityStore()
	ctx := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: "acme", Client: auth.Client{Kind: auth.ClientMCP},
	})
	newActivity(ring).Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseScale, Target: "orders"})

	page, err := newActivity(ring).List(context.Background(), service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, auth.ClientMCP, page.Entries[0].Client)
	assert.Equal(t, "acme", page.Entries[0].Actor)
}

func TestRecordMarksFailureAndKeepsTheReason(t *testing.T) {
	t.Parallel()

	activity := newActivity(newFakeActivityStore())
	ctx := context.Background()

	activity.Record(ctx, service.ActivityEvent{
		Action:  service.ActionDatabaseBackup,
		Target:  "orders",
		Outcome: service.OutcomeFailed,
		Error:   "bucket permissions rejected the write",
	})

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, service.OutcomeFailed, page.Entries[0].Outcome)
	assert.Equal(t, "bucket permissions rejected the write", page.Entries[0].Error)
}

// The test that matters most: a credential must not survive Record, whichever
// field it arrives in.
func TestRecordSanitizesCredentials(t *testing.T) {
	t.Parallel()

	store := newFakeActivityStore()
	activity := newActivity(store)

	activity.Record(context.Background(), service.ActivityEvent{
		Action:  service.ActionConnectionUpdate,
		Target:  "servicenow",
		Outcome: service.OutcomeFailed,
		Detail:  "SN_API_KEY=abcd1234efgh5678 client_token: zzzz9999",
		Error:   "dial mysql://appuser:hunter2@orders.svc:3306/orders refused",
	})

	stored := store.raw()
	require.NotEmpty(t, stored)

	for _, secret := range []string{"abcd1234efgh5678", "zzzz9999", "hunter2", "appuser:hunter2"} {
		assert.NotContains(t, stored, secret, "a credential reached the stored document")
	}
	assert.Contains(t, stored, "[redacted]")
}

func TestRecordTrimsToTheRingSize(t *testing.T) {
	t.Parallel()

	activity := newActivity(newFakeActivityStore())
	ctx := context.Background()

	total := service.MaxActivityEvents + 25
	for i := 0; i < total; i++ {
		activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseBackup, Target: "orders"})
	}

	page, err := activity.List(ctx, service.ActivityQuery{Limit: service.MaxActivityEvents})
	require.NoError(t, err)
	assert.Len(t, page.Entries, service.MaxActivityEvents, "the ring is bounded")

	// The oldest fall off first, so the newest sequence is still the total.
	assert.Equal(t, fmt.Sprintf("%020d", total), page.Entries[0].ID)
	assert.Equal(t, fmt.Sprintf("%020d", total-service.MaxActivityEvents+1), page.Entries[len(page.Entries)-1].ID)
}

func TestListFiltersByDatabase(t *testing.T) {
	t.Parallel()

	activity := newActivity(newFakeActivityStore())
	ctx := context.Background()

	activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseCreate, DatabaseID: "db-1", Target: "orders"})
	activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseCreate, DatabaseID: "db-2", Target: "billing"})
	activity.Record(ctx, service.ActivityEvent{Action: service.ActionConnectionUpdate, Target: "servicenow"})

	page, err := activity.List(ctx, service.ActivityQuery{DatabaseID: "db-2"})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, "billing", page.Entries[0].Target)

	// The cursor tracks the whole store, not the filtered page, so a poll that
	// matches nothing still moves forward.
	assert.Equal(t, "00000000000000000003", page.Cursor)
}

func TestListSinceReturnsOnlyNewerEvents(t *testing.T) {
	t.Parallel()

	activity := newActivity(newFakeActivityStore())
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseBackup, Target: "orders"})
	}

	page, err := activity.List(ctx, service.ActivityQuery{Since: "00000000000000000002"})
	require.NoError(t, err)
	assert.Equal(t, []string{"00000000000000000004", "00000000000000000003"}, ids(page.Entries))

	// The steady state of a live poll: nothing new, and the cursor stands.
	quiet, err := activity.List(ctx, service.ActivityQuery{Since: page.Cursor})
	require.NoError(t, err)
	assert.Empty(t, quiet.Entries)
	assert.Equal(t, "00000000000000000004", quiet.Cursor)
}

func TestListClampsTheLimitAndReportsTruncation(t *testing.T) {
	t.Parallel()

	activity := newActivity(newFakeActivityStore())
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseBackup, Target: "orders"})
	}

	page, err := activity.List(ctx, service.ActivityQuery{Limit: 4})
	require.NoError(t, err)
	assert.Len(t, page.Entries, 4)
	assert.True(t, page.Truncated, "more matched than were returned")
	assert.Equal(t, "00000000000000000010", page.Entries[0].ID, "truncation drops the oldest, never the newest")

	full, err := activity.List(ctx, service.ActivityQuery{Limit: 50})
	require.NoError(t, err)
	assert.Len(t, full.Entries, 10)
	assert.False(t, full.Truncated)
}

func TestListDefaultsTheLimit(t *testing.T) {
	t.Parallel()

	activity := newActivity(newFakeActivityStore())
	ctx := context.Background()

	for i := 0; i < service.DefaultActivityLimit+5; i++ {
		activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseBackup, Target: "orders"})
	}

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	assert.Len(t, page.Entries, service.DefaultActivityLimit)
}

func TestNilStoreKeepsAnInProcessRing(t *testing.T) {
	t.Parallel()

	activity := newActivity(nil)
	ctx := context.Background()

	activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseCreate, Target: "orders"})

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1, "a laptop run still shows a feed")
	assert.Equal(t, "orders", page.Entries[0].Target)
}

func TestRecordSurvivesAStoreFailure(t *testing.T) {
	t.Parallel()

	store := newFakeActivityStore()
	store.putErr = kube.ErrForbidden
	activity := newActivity(store)

	// A rejected write must not panic and must not take the caller down with
	// it: losing an activity row is better than failing the user's backup.
	assert.NotPanics(t, func() {
		activity.Record(context.Background(), service.ActivityEvent{Action: service.ActionDatabaseBackup, Target: "orders"})
	})
}

func TestListSurfacesAStoreFailure(t *testing.T) {
	t.Parallel()

	store := newFakeActivityStore()
	store.getErr = errors.New("configmaps is forbidden")
	activity := newActivity(store)

	_, err := activity.List(context.Background(), service.ActivityQuery{})
	require.Error(t, err, "a read failure is a real error, unlike a write failure")
}

func TestRecordIsSafeUnderConcurrency(t *testing.T) {
	t.Parallel()

	activity := newActivity(newFakeActivityStore())
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseBackup, Target: "orders"})
		}()
	}
	wg.Wait()

	page, err := activity.List(ctx, service.ActivityQuery{Limit: 100})
	require.NoError(t, err)
	assert.Len(t, page.Entries, 25)

	seen := map[string]struct{}{}
	for _, e := range page.Entries {
		_, duplicate := seen[e.ID]
		assert.False(t, duplicate, "sequence %s was handed out twice", e.ID)
		seen[e.ID] = struct{}{}
	}
}

func TestStoredDocumentIsReadableJSON(t *testing.T) {
	t.Parallel()

	store := newFakeActivityStore()
	activity := newActivity(store)

	activity.Record(context.Background(), service.ActivityEvent{
		Action:     service.ActionDatabaseScale,
		DatabaseID: "db-1",
		Target:     "orders",
		Detail:     "cpu 2, memory 8 GB",
	})

	var doc struct {
		NextSeq int `json:"nextSeq"`
		Events  []struct {
			ID         string `json:"id"`
			Action     string `json:"action"`
			DatabaseID string `json:"databaseId"`
			Detail     string `json:"detail"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal([]byte(store.raw()), &doc))
	require.Len(t, doc.Events, 1)
	assert.Equal(t, 2, doc.NextSeq)
	assert.Equal(t, "00000000000000000001", doc.Events[0].ID)
	assert.Equal(t, "db-1", doc.Events[0].DatabaseID)
	assert.Equal(t, "cpu 2, memory 8 GB", doc.Events[0].Detail)
}

func TestActivityDefaultsNameTheConfigMap(t *testing.T) {
	t.Parallel()

	activity := service.NewActivity(newFakeActivityStore(), service.ActivityOptions{})
	namespace, name := activity.Location()

	assert.Equal(t, "everest-system", namespace)
	assert.Equal(t, "pds-activity", name)
}

func TestRecordIgnoresABlankAction(t *testing.T) {
	t.Parallel()

	activity := newActivity(newFakeActivityStore())
	ctx := context.Background()

	activity.Record(ctx, service.ActivityEvent{Target: "orders"})

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	assert.Empty(t, page.Entries, "an event with no action is a programming slip, not a row")
}

func TestRecordTruncatesAnOverlongDetail(t *testing.T) {
	t.Parallel()

	activity := newActivity(newFakeActivityStore())
	ctx := context.Background()

	activity.Record(ctx, service.ActivityEvent{
		Action: service.ActionDatabaseCreate,
		Target: "orders",
		Detail: strings.Repeat("x", service.MaxActivityText*3),
	})

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.LessOrEqual(t, len(page.Entries[0].Detail), service.MaxActivityText,
		"one runaway error string must not eat the ring")
}

func TestRecordStampsTheTimeItIsGiven(t *testing.T) {
	t.Parallel()

	activity := newActivity(newFakeActivityStore())
	at := time.Date(2026, 3, 9, 10, 30, 0, 0, time.UTC)

	activity.Record(context.Background(), service.ActivityEvent{
		Action: service.ActionDatabaseDelete,
		Target: "orders",
		At:     at,
	})

	page, err := activity.List(context.Background(), service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.True(t, at.Equal(page.Entries[0].At))
}

func ids(entries []service.ActivityEvent) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ID)
	}

	return out
}
