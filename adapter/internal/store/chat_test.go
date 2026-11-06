package store_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/store"
)

func newChat(t *testing.T, hub *store.HubState, title string) store.ChatConversation {
	t.Helper()

	conv, err := hub.CreateChatConversation(t.Context(), store.ChatConversation{
		Subject:  "acme",
		Title:    title,
		Provider: "anthropic",
		Model:    "claude-sonnet-4",
	})
	require.NoError(t, err)

	return conv
}

func TestChatConversationRoundTrip(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()

	created, err := hub.CreateChatConversation(ctx, store.ChatConversation{
		Subject:    "acme",
		Title:      "Why is orders-dev slow?",
		Provider:   "anthropic",
		Model:      "claude-sonnet-4",
		DatabaseID: "pds.orders-dev",
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)

	got, err := hub.ChatConversation(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "Why is orders-dev slow?", got.Title)
	assert.Equal(t, "anthropic", got.Provider)
	assert.Equal(t, "claude-sonnet-4", got.Model)
	assert.Equal(t, "pds.orders-dev", got.DatabaseID)
	assert.Equal(t, "acme", got.Subject)
	assert.False(t, got.UpdatedAt.IsZero())
}

func TestChatMessagesKeepTheirOrderAndActions(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()
	conv := newChat(t, hub, "Backups")

	first, err := hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "Are backups running?"})
	require.NoError(t, err)
	second, err := hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{
		Role:    "assistant",
		Content: "The last backup failed.",
		Actions: `[{"label":"Retry backup","kind":"backup","id":"pds.orders-dev"}]`,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, first.Sequence)
	assert.Equal(t, 2, second.Sequence)

	got, err := hub.ChatMessages(ctx, conv.ID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "user", got[0].Role)
	assert.Equal(t, "Are backups running?", got[0].Content)
	assert.Equal(t, "assistant", got[1].Role)
	assert.JSONEq(t, `[{"label":"Retry backup","kind":"backup","id":"pds.orders-dev"}]`, got[1].Actions)
}

func TestAppendChatMessageMovesTheConversationToTheTop(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()
	older := newChat(t, hub, "Older")
	newChat(t, hub, "Newer")

	_, err := hub.AppendChatMessage(ctx, older.ID, store.ChatMessage{Role: "user", Content: "still here"})
	require.NoError(t, err)

	page, err := hub.ListChatConversations(ctx, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Conversations, 2)
	assert.Equal(t, "Older", page.Conversations[0].Title, "a new message makes a conversation the newest")
}

func TestChatContentIsSealedInTheRow(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(51))
	require.NoError(t, err)
	team := nextTeam(t, db)
	hub := db.Hub(team, cipher)
	ctx := t.Context()

	conv := newChat(t, hub, "secret title about orders-dev")
	_, err = hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{
		Role:    "assistant",
		Content: "secret content about orders-dev",
		Actions: `[{"label":"secret action","kind":"status","id":"pds.orders-dev"}]`,
	})
	require.NoError(t, err)

	var title string
	require.NoError(t, db.Gorm().WithContext(ctx).Model(&store.HubChatConversation{}).
		Where("team_id = ? AND id = ?", team, conv.ID).Select("title").Scan(&title).Error)
	assert.True(t, store.Sealed(title), "the title has to be sealed")
	assert.NotContains(t, title, "secret")

	var rows []store.HubChatMessage
	require.NoError(t, db.Gorm().WithContext(ctx).Where("team_id = ?", team).Find(&rows).Error)
	require.Len(t, rows, 1)
	for _, value := range []string{rows[0].Content, rows[0].Actions} {
		assert.True(t, store.Sealed(value), "message content and actions have to be sealed")
		assert.NotContains(t, value, "secret")
	}

	other, err := store.NewCipher(testKey(52))
	require.NoError(t, err)
	_, err = db.Hub(team, other).ChatMessages(ctx, conv.ID)
	require.ErrorIs(t, err, store.ErrUnsealable, "another key must not open the rows")
}

func TestChatContentIsBoundToItsRow(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(53))
	require.NoError(t, err)
	team := nextTeam(t, db)
	hub := db.Hub(team, cipher)
	ctx := t.Context()

	conv := newChat(t, hub, "Bound")
	_, err = hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "first"})
	require.NoError(t, err)
	_, err = hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "assistant", Content: "second"})
	require.NoError(t, err)

	// Copy the first message's sealed content over the second's. Same team,
	// same key, same conversation: only the sequence differs.
	var first store.HubChatMessage
	require.NoError(t, db.Gorm().WithContext(ctx).
		Where("team_id = ? AND conversation_id = ? AND sequence = 1", team, conv.ID).Take(&first).Error)
	require.NoError(t, db.Gorm().WithContext(ctx).Model(&store.HubChatMessage{}).
		Where("team_id = ? AND conversation_id = ? AND sequence = 2", team, conv.ID).
		Update("content", first.Content).Error)

	_, err = hub.ChatMessages(ctx, conv.ID)
	require.ErrorIs(t, err, store.ErrUnsealable, "content moved to another sequence must not open")

	// A title copied onto another conversation must not open there either.
	target := newChat(t, hub, "Target")
	var sealed string
	require.NoError(t, db.Gorm().WithContext(ctx).Model(&store.HubChatConversation{}).
		Where("id = ?", conv.ID).Select("title").Scan(&sealed).Error)
	require.NoError(t, db.Gorm().WithContext(ctx).Model(&store.HubChatConversation{}).
		Where("id = ?", target.ID).Update("title", sealed).Error)

	_, err = hub.ChatConversation(ctx, target.ID)
	require.ErrorIs(t, err, store.ErrUnsealable)
}

func TestChatConversationsAreScopedToTheirTeam(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(54))
	require.NoError(t, err)
	first := db.Hub(nextTeam(t, db), cipher)
	second := db.Hub(nextTeam(t, db), cipher)
	ctx := t.Context()

	conv := newChat(t, first, "First team only")
	_, err = first.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "hello"})
	require.NoError(t, err)

	page, err := second.ListChatConversations(ctx, "", 10)
	require.NoError(t, err)
	assert.Empty(t, page.Conversations)

	_, err = second.ChatConversation(ctx, conv.ID)
	require.ErrorIs(t, err, store.ErrChatNotFound)
	_, err = second.ChatMessages(ctx, conv.ID)
	require.ErrorIs(t, err, store.ErrChatNotFound)
	_, err = second.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "intrude"})
	require.ErrorIs(t, err, store.ErrChatNotFound)
	require.ErrorIs(t, second.DeleteChatConversation(ctx, conv.ID), store.ErrChatNotFound)

	still, err := first.ChatMessages(ctx, conv.ID)
	require.NoError(t, err)
	assert.Len(t, still, 1, "the other team's delete must not reach this conversation")
}

func TestListChatConversationsPagesNewestFirst(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()

	for i := range 5 {
		newChat(t, hub, fmt.Sprintf("chat %d", i))
		// Distinct timestamps keep the expected order readable. Ties are
		// broken by id, which the paging below also has to survive.
		time.Sleep(2 * time.Millisecond)
	}

	var titles []string
	cursor := ""
	for range 3 {
		page, err := hub.ListChatConversations(ctx, cursor, 2)
		require.NoError(t, err)
		for _, conv := range page.Conversations {
			titles = append(titles, conv.Title)
		}
		cursor = page.Next
		if cursor == "" {
			break
		}
	}

	assert.Equal(t, []string{"chat 4", "chat 3", "chat 2", "chat 1", "chat 0"}, titles)
	assert.Empty(t, cursor, "the last page carries no cursor")
}

func TestListChatConversationsRefusesAForgedCursor(t *testing.T) {
	hub := newHubState(t)

	_, err := hub.ListChatConversations(t.Context(), "not-a-cursor", 10)
	require.ErrorIs(t, err, store.ErrChatCursor)
}

func TestDeleteChatConversationRemovesItsMessages(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(55))
	require.NoError(t, err)
	team := nextTeam(t, db)
	hub := db.Hub(team, cipher)
	ctx := t.Context()

	conv := newChat(t, hub, "Doomed")
	for i := range 3 {
		_, err := hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "user", Content: fmt.Sprintf("m%d", i)})
		require.NoError(t, err)
	}
	keep := newChat(t, hub, "Kept")

	require.NoError(t, hub.DeleteChatConversation(ctx, conv.ID))

	var conversations, messages int64
	require.NoError(t, db.Gorm().WithContext(ctx).Model(&store.HubChatConversation{}).
		Where("id = ?", conv.ID).Count(&conversations).Error)
	require.NoError(t, db.Gorm().WithContext(ctx).Model(&store.HubChatMessage{}).
		Where("conversation_id = ?", conv.ID).Count(&messages).Error)
	assert.Zero(t, conversations, "the conversation row has to be gone, not flagged")
	assert.Zero(t, messages, "its messages have to be gone with it")

	_, err = hub.ChatConversation(ctx, keep.ID)
	require.NoError(t, err, "deleting one conversation leaves the others")
	require.ErrorIs(t, hub.DeleteChatConversation(ctx, conv.ID), store.ErrChatNotFound)
}

func TestChatNeedsAKey(t *testing.T) {
	db := openLive(t)
	hub := db.Hub(nextTeam(t, db), nil)

	_, err := hub.CreateChatConversation(t.Context(), store.ChatConversation{
		Title: "no key", Provider: "anthropic", Model: "m",
	})
	require.Error(t, err, "chat content is never written in the clear")
}

func TestChatRoleIsBoundToTheContent(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(56))
	require.NoError(t, err)
	team := nextTeam(t, db)
	hub := db.Hub(team, cipher)
	ctx := t.Context()

	conv := newChat(t, hub, "Relabel")
	_, err = hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "restart it"})
	require.NoError(t, err)
	require.NoError(t, db.Gorm().WithContext(ctx).Model(&store.HubChatMessage{}).
		Where("team_id = ? AND conversation_id = ?", team, conv.ID).
		Update("role", "assistant").Error)

	_, err = hub.ChatMessages(ctx, conv.ID)
	require.ErrorIs(t, err, store.ErrUnsealable, "a user turn relabelled as the assistant's must not open")
}

func TestListChatConversationsBreaksTiesOnID(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(57))
	require.NoError(t, err)
	team := nextTeam(t, db)
	hub := db.Hub(team, cipher)
	ctx := t.Context()

	for i := range 3 {
		newChat(t, hub, fmt.Sprintf("tied %d", i))
	}
	same := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	require.NoError(t, db.Gorm().WithContext(ctx).Model(&store.HubChatConversation{}).
		Where("team_id = ?", team).Update("updated_at", same).Error)

	seen := map[string]bool{}
	cursor := ""
	for range 4 {
		page, err := hub.ListChatConversations(ctx, cursor, 1)
		require.NoError(t, err)
		for _, conv := range page.Conversations {
			assert.False(t, seen[conv.ID], "a tied row must not repeat across pages")
			seen[conv.ID] = true
		}
		cursor = page.Next
		if cursor == "" {
			break
		}
	}
	assert.Len(t, seen, 3, "every tied row shows up exactly once")
}

func TestAppendChatMessageTakesTheNextSequenceUnderConcurrency(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()
	conv := newChat(t, hub, "Busy")

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "user", Content: fmt.Sprintf("m%d", i)})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	got, err := hub.ChatMessages(ctx, conv.ID)
	require.NoError(t, err)
	require.Len(t, got, writers)
	for i, msg := range got {
		assert.Equal(t, i+1, msg.Sequence, "sequences are dense and in order")
	}
}

func TestDeleteChatMessageRemovesOneTurn(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()
	conv := newChat(t, hub, "Take back")

	kept, err := hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "kept"})
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)
	unanswered, err := hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "unanswered"})
	require.NoError(t, err)

	require.NoError(t, hub.DeleteChatMessage(ctx, conv.ID, unanswered))
	got, err := hub.ChatMessages(ctx, conv.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "kept", got[0].Content)

	after, err := hub.ChatConversation(ctx, conv.ID)
	require.NoError(t, err)
	assert.True(t, kept.CreatedAt.Equal(after.UpdatedAt),
		"the list orders by the last message still there, not the one taken back: got %s, want %s",
		after.UpdatedAt, kept.CreatedAt)
	require.ErrorIs(t, hub.DeleteChatMessage(ctx, conv.ID, unanswered), store.ErrChatNotFound)

	other := newHubState(t)
	require.ErrorIs(t, other.DeleteChatMessage(ctx, conv.ID, unanswered), store.ErrChatNotFound,
		"another team cannot take a turn out")
}

func TestAppendChatQuestionRefusesABusyConversation(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()
	conv := newChat(t, hub, "Busy")
	since := time.Now().Add(-time.Minute)

	_, err := hub.AppendChatQuestion(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "first"}, since)
	require.NoError(t, err)

	_, err = hub.AppendChatQuestion(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "second"}, since)
	require.ErrorIs(t, err, store.ErrChatBusy, "a question while the last one is unanswered is refused")

	_, err = hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "assistant", Content: "answer"})
	require.NoError(t, err)
	_, err = hub.AppendChatQuestion(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "second"}, since)
	require.NoError(t, err, "once answered, the next question goes in")
}

func TestAppendChatQuestionReplacesAStaleUnansweredTurn(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()
	conv := newChat(t, hub, "Stale")

	_, err := hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "answered"})
	require.NoError(t, err)
	_, err = hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "assistant", Content: "reply"})
	require.NoError(t, err)
	_, err = hub.AppendChatQuestion(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "orphan"}, time.Now().Add(-time.Minute))
	require.NoError(t, err)

	// The process that was answering "orphan" went away. Once it is older than
	// since, the next question takes its place rather than stacking behind it,
	// so the thread never keeps a question with no reply.
	next, err := hub.AppendChatQuestion(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "next"}, time.Now().Add(time.Minute))
	require.NoError(t, err)

	got, err := hub.ChatMessages(ctx, conv.ID)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"answered", "reply", "next"}, []string{got[0].Content, got[1].Content, got[2].Content})
	assert.Equal(t, got[2].Sequence, next.Sequence)
}

func TestAppendChatQuestionLetsOneOfTwoConcurrentTurnsIn(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()
	conv := newChat(t, hub, "Race")
	since := time.Now().Add(-time.Minute)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := hub.AppendChatQuestion(ctx, conv.ID, store.ChatMessage{Role: "user", Content: fmt.Sprintf("tab %d", i)}, since)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	var accepted, busy int
	for err := range errs {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, store.ErrChatBusy):
			busy++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	assert.Equal(t, 1, accepted)
	assert.Equal(t, 1, busy)
}

func TestRecentChatMessagesReadsOnlyTheNewest(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()
	conv := newChat(t, hub, "Long")
	for i := range 6 {
		_, err := hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "user", Content: fmt.Sprintf("m%d", i)})
		require.NoError(t, err)
	}

	got, err := hub.RecentChatMessages(ctx, conv.ID, 3)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"m3", "m4", "m5"}, []string{got[0].Content, got[1].Content, got[2].Content},
		"the newest messages, oldest first")

	other := newHubState(t)
	_, err = other.RecentChatMessages(ctx, conv.ID, 3)
	require.ErrorIs(t, err, store.ErrChatNotFound)
}

func TestDeleteChatMessageLeavesAQuestionThatReplacedIt(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()
	conv := newChat(t, hub, "Replaced")

	stale, err := hub.AppendChatQuestion(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "stale"}, time.Now().Add(-time.Minute))
	require.NoError(t, err)
	replacement, err := hub.AppendChatQuestion(ctx, conv.ID, store.ChatMessage{Role: "user", Content: "replacement"}, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, stale.Sequence, replacement.Sequence, "the replacement reuses the stale question's place")

	// The turn that asked "stale" fails late and takes its question back. It
	// must not take the replacement with it.
	require.ErrorIs(t, hub.DeleteChatMessage(ctx, conv.ID, stale), store.ErrChatNotFound)

	got, err := hub.ChatMessages(ctx, conv.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "replacement", got[0].Content)
}

func TestCreateChatConversationRefusesARequestKeySeenBefore(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()
	key := "0123456789abcdef0123456789abcdef"

	first, err := hub.CreateChatConversation(ctx, store.ChatConversation{
		Title: "first", Provider: "anthropic", Model: "m", RequestKey: key,
	})
	require.NoError(t, err)

	_, err = hub.CreateChatConversation(ctx, store.ChatConversation{
		Title: "retry", Provider: "anthropic", Model: "m", RequestKey: key,
	})
	require.ErrorIs(t, err, store.ErrChatRequestSeen, "a retry of the same message opens no second chat")

	found, err := hub.ChatConversationByRequestKey(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, first.ID, found.ID)
	assert.Equal(t, "first", found.Title)

	// Conversations without a key never collide with each other.
	for range 2 {
		_, err := hub.CreateChatConversation(ctx, store.ChatConversation{Title: "no key", Provider: "anthropic", Model: "m"})
		require.NoError(t, err)
	}

	// The key is scoped to the team.
	other := newHubState(t)
	_, err = other.ChatConversationByRequestKey(ctx, key)
	require.ErrorIs(t, err, store.ErrChatNotFound)
	_, err = other.CreateChatConversation(ctx, store.ChatConversation{
		Title: "theirs", Provider: "anthropic", Model: "m", RequestKey: key,
	})
	require.NoError(t, err, "another team may use the same key")
}

func TestFirstChatMessagesReadsOnlyTheOpening(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()
	conv := newChat(t, hub, "Opening")
	for i := range 4 {
		_, err := hub.AppendChatMessage(ctx, conv.ID, store.ChatMessage{Role: "user", Content: fmt.Sprintf("m%d", i)})
		require.NoError(t, err)
	}

	got, err := hub.FirstChatMessages(ctx, conv.ID, 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []string{"m0", "m1"}, []string{got[0].Content, got[1].Content})
}

func TestStartChatConversationSavesTheConversationAndItsQuestionTogether(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(58))
	require.NoError(t, err)
	team := nextTeam(t, db)
	hub := db.Hub(team, cipher)
	ctx := t.Context()

	conv, asked, err := hub.StartChatConversation(ctx,
		store.ChatConversation{Title: "opening", Provider: "anthropic", Model: "m"},
		store.ChatMessage{Role: "user", Content: "opening", Actions: "[]"})
	require.NoError(t, err)
	assert.Equal(t, 1, asked.Sequence)
	got, err := hub.ChatMessages(ctx, conv.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "opening", got[0].Content)

	// A question the database refuses leaves no conversation behind: the two
	// rows commit together or not at all.
	_, _, err = hub.StartChatConversation(ctx,
		store.ChatConversation{Title: "refused", Provider: "anthropic", Model: "m"},
		store.ChatMessage{Role: "a role far longer than the column allows", Content: "refused", Actions: "[]"})
	require.Error(t, err)

	var conversations int64
	require.NoError(t, db.Gorm().WithContext(ctx).Model(&store.HubChatConversation{}).
		Where("team_id = ?", team).Count(&conversations).Error)
	assert.Equal(t, int64(1), conversations, "the refused start left no empty conversation")
}

func TestStartChatConversationRefusesARequestKeySeenBefore(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()
	key := "fedcba9876543210fedcba9876543210"

	_, _, err := hub.StartChatConversation(ctx,
		store.ChatConversation{Title: "q", Provider: "anthropic", Model: "m", RequestKey: key},
		store.ChatMessage{Role: "user", Content: "q", Actions: "[]"})
	require.NoError(t, err)
	_, _, err = hub.StartChatConversation(ctx,
		store.ChatConversation{Title: "q", Provider: "anthropic", Model: "m", RequestKey: key},
		store.ChatMessage{Role: "user", Content: "q", Actions: "[]"})
	require.ErrorIs(t, err, store.ErrChatRequestSeen)
}

func TestChatConversationKeepsItsCursorAgent(t *testing.T) {
	hub := newHubState(t)
	ctx := t.Context()
	conv := newChat(t, hub, "Agent")
	assert.Empty(t, conv.AgentID, "a new conversation has no agent yet")

	require.NoError(t, hub.SetChatAgent(ctx, conv.ID, "bc-agent-1"))
	got, err := hub.ChatConversation(ctx, conv.ID)
	require.NoError(t, err)
	assert.Equal(t, "bc-agent-1", got.AgentID)
	assert.True(t, conv.UpdatedAt.Equal(got.UpdatedAt), "keeping an agent is not a message, so the list order stays")

	require.NoError(t, hub.SetChatAgent(ctx, conv.ID, "bc-agent-2"))
	got, err = hub.ChatConversation(ctx, conv.ID)
	require.NoError(t, err)
	assert.Equal(t, "bc-agent-2", got.AgentID, "a replacement agent takes the place of the old one")
}

func TestSetChatAgentStaysInsideItsTeam(t *testing.T) {
	db := openLive(t)
	cipher, err := store.NewCipher(testKey(56))
	require.NoError(t, err)
	owner := db.Hub(nextTeam(t, db), cipher)
	other := db.Hub(nextTeam(t, db), cipher)
	ctx := t.Context()
	conv := newChat(t, owner, "Mine")

	require.ErrorIs(t, other.SetChatAgent(ctx, conv.ID, "bc-agent-1"), store.ErrChatNotFound)
	require.ErrorIs(t, owner.SetChatAgent(ctx, "no-such-conversation", "bc-agent-1"), store.ErrChatNotFound)

	got, err := owner.ChatConversation(ctx, conv.ID)
	require.NoError(t, err)
	assert.Empty(t, got.AgentID, "another team's write must not reach this conversation")
}

// TestMigrateAddsTheAgentColumnToSavedConversations is the upgrade of a
// database whose conversations were saved before they kept an agent.
func TestMigrateAddsTheAgentColumnToSavedConversations(t *testing.T) {
	url := liveURL(t)
	dropSchema(t, url)

	db, err := store.Open(t.Context(), store.Options{URL: url})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	ctx := t.Context()
	gorm := db.Gorm().WithContext(ctx)

	require.NoError(t, db.Migrate(ctx))
	team := nextTeam(t, db)
	require.NoError(t, gorm.Exec(`ALTER TABLE hub_chat_conversations DROP COLUMN agent_id`).Error)
	require.NoError(t, gorm.Exec(
		`INSERT INTO hub_chat_conversations (id, team_id, title, provider, model, created_at, updated_at)
		 VALUES ('saved-before', ?, 'sealed', 'cursor', 'composer-2', now(), now())`, team).Error)

	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Migrate(ctx), "a second start finds the column there")

	assert.True(t, hasColumn(t, db, "hub_chat_conversations", "agent_id"))
	var row store.HubChatConversation
	require.NoError(t, gorm.Where("id = ?", "saved-before").Take(&row).Error)
	assert.Empty(t, row.AgentID, "a conversation saved before has no agent, and its next turn starts one")
}
