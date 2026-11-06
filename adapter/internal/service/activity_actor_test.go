package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/service"
)

// userNames answers for the Users that still exist. An id it does not hold is
// a User who was removed.
type userNames map[uint]string

func (n userNames) UserNames(_ context.Context, ids []uint) (map[uint]string, error) {
	out := map[uint]string{}
	for _, id := range ids {
		if name, ok := n[id]; ok {
			out[id] = name
		}
	}

	return out, nil
}

func actorActivity(t *testing.T, names userNames) (*service.Activity, *service.MemoryBook) {
	t.Helper()

	book := service.NewMemoryBook(nil)
	activity := service.NewActivity(nil, service.ActivityOptions{})
	activity.UseBook(book)
	activity.UseActors(names)

	return activity, book
}

func asUser(userID uint, client auth.Client) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{
		Subject: "login-subject",
		Client:  client,
		Scope: auth.Scope{
			UserID:         userID,
			OrganizationID: 1,
			Role:           auth.RoleUser,
			Team:           auth.Team{ID: 4, Name: "default"},
		},
	})
}

func TestActivityStoresTheUserAndTeamNotAName(t *testing.T) {
	activity, book := actorActivity(t, userNames{7: "Ann Lee"})
	ctx := asUser(7, auth.Client{})

	activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseCreate, Target: "orders"})

	stored, err := book.ListEntries(ctx, service.HistoryQuery{Kind: service.HistoryAction})
	require.NoError(t, err)
	require.Len(t, stored.Entries, 1)
	assert.Equal(t, uint(7), stored.Entries[0].UserID)
	assert.Equal(t, uint(4), stored.Entries[0].TeamID)
	assert.Empty(t, stored.Entries[0].Actor, "a name or login is read at display time, never stored")
}

func TestActivityShowsTheActingUser(t *testing.T) {
	activity, _ := actorActivity(t, userNames{7: "Ann Lee"})
	ctx := asUser(7, auth.Client{})

	activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseCreate, Target: "orders"})

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, "Ann Lee", page.Entries[0].Actor)

	got, err := activity.Get(ctx, page.Entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "Ann Lee", got.Actor)
}

func TestActivityNamesTheUserAnAgentActsFor(t *testing.T) {
	activity, _ := actorActivity(t, userNames{7: "Ann Lee"})
	ctx := asUser(7, auth.Client{Kind: auth.ClientMCP})

	activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseScale, Target: "orders"})

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, "Ann Lee", page.Entries[0].Actor)
	assert.Equal(t, auth.ClientMCP, page.Entries[0].Client)
}

func TestActivityRendersARemovedUser(t *testing.T) {
	activity, _ := actorActivity(t, userNames{})
	ctx := asUser(9, auth.Client{})

	activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseDelete, Target: "orders"})

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, service.ActorRemovedUser, page.Entries[0].Actor)
}

func TestActivityKeepsAFixedActorWithNoUser(t *testing.T) {
	activity, book := actorActivity(t, userNames{7: "Ann Lee"})
	ctx := asUser(7, auth.Client{})

	activity.Record(ctx, service.ActivityEvent{Action: service.ActionConnectionUpdate, Actor: "support"})

	stored, err := book.ListEntries(ctx, service.HistoryQuery{Kind: service.HistoryAction})
	require.NoError(t, err)
	require.Len(t, stored.Entries, 1)
	assert.Zero(t, stored.Entries[0].UserID)
	assert.Equal(t, "support", stored.Entries[0].Actor)

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	assert.Equal(t, "support", page.Entries[0].Actor)
}

func TestActivityWithoutAScopeKeepsTheSubject(t *testing.T) {
	activity, _ := actorActivity(t, userNames{})
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "operator", Scope: auth.Scope{Team: auth.Team{ID: 1}}})

	activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseBackup})

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, "operator", page.Entries[0].Actor)
}

func TestActivityRecordsTheChatAndTheAgentFromTheCaller(t *testing.T) {
	activity, book := actorActivity(t, userNames{7: "Ann Lee"})
	ctx := asUser(7, auth.Client{Kind: auth.ClientChat, Agent: "Anthropic"})

	activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseScale, DatabaseID: "ns.orders", Target: "orders"})

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, auth.ClientChat, page.Entries[0].Client)
	assert.Equal(t, "Anthropic", page.Entries[0].Agent)

	got, err := activity.Get(ctx, page.Entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "Anthropic", got.Agent)

	stored, err := book.ListEntries(ctx, service.HistoryQuery{Kind: service.HistoryAction})
	require.NoError(t, err)
	require.Len(t, stored.Entries, 1)
	assert.Equal(t, "Anthropic", stored.Entries[0].Agent, "the database history carries it too")
}

func TestActivityKeepsAnAgentTheCallSiteNamed(t *testing.T) {
	activity, _ := actorActivity(t, userNames{7: "Ann Lee"})
	ctx := asUser(7, auth.Client{Kind: auth.ClientMCP, Agent: "Cursor"})

	activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseScale, Target: "orders", Agent: "Claude Code"})

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, "Claude Code", page.Entries[0].Agent)
}

func TestActivityLeavesTheAgentEmptyForAPerson(t *testing.T) {
	activity, _ := actorActivity(t, userNames{7: "Ann Lee"})
	ctx := asUser(7, auth.Client{})

	activity.Record(ctx, service.ActivityEvent{Action: service.ActionDatabaseScale, Target: "orders"})

	page, err := activity.List(ctx, service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Empty(t, page.Entries[0].Client)
	assert.Empty(t, page.Entries[0].Agent)
}
