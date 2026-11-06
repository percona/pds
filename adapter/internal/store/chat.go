package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrChatNotFound means the conversation is not there for this team. A
// conversation another team holds answers the same, so an id is no way to
// find out whether somebody else has one.
var ErrChatNotFound = errors.New("chat conversation not found")

// ErrChatCursor means a list cursor is not one this store handed out.
var ErrChatCursor = errors.New("chat cursor is not valid")

// ErrChatRequestSeen means a conversation was already opened with this
// request key, so the message asking for a new one is a retry.
var ErrChatRequestSeen = errors.New("chat request was already received")

// ErrChatBusy means the conversation's newest message is a question still
// being answered, so another question now would be answered without it.
var ErrChatBusy = errors.New("chat conversation is still answering")

// maxChatPage bounds one list read.
const maxChatPage = 100

// ChatConversation is one saved chat, opened.
type ChatConversation struct {
	ID         string
	Subject    string
	Title      string
	Provider   string
	Model      string
	DatabaseID string
	RequestKey string
	// AgentID is the Cursor cloud agent the conversation runs on, empty when
	// it has none.
	AgentID   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ChatMessage is one turn, opened. Actions is the JSON the caller stored, so
// this package does not have to know the shape of a chat action.
type ChatMessage struct {
	Sequence  int
	Role      string
	Content   string
	Actions   string
	CreatedAt time.Time
}

// ChatConversationPage is one page of a list, newest first. Next is empty on
// the last page.
type ChatConversationPage struct {
	Conversations []ChatConversation
	Next          string
}

// CreateChatConversation starts a conversation with no messages. The id is
// minted here, so a caller cannot pick one that collides with another team's.
func (h *HubState) CreateChatConversation(ctx context.Context, conv ChatConversation) (ChatConversation, error) {
	if _, err := h.scope(ctx); err != nil {
		return ChatConversation{}, err
	}

	row, err := h.newConversationRow(conv)
	if err != nil {
		return ChatConversation{}, err
	}
	if err := h.db.WithContext(ctx).Create(&row).Error; err != nil {
		return ChatConversation{}, conversationInsertError(err, row)
	}

	return opened(conv, row), nil
}

// StartChatConversation opens a conversation and saves its first question in
// one transaction, so a process that goes away between the two leaves neither
// rather than an empty conversation in the list.
func (h *HubState) StartChatConversation(ctx context.Context, conv ChatConversation, question ChatMessage) (ChatConversation, ChatMessage, error) {
	if _, err := h.scope(ctx); err != nil {
		return ChatConversation{}, ChatMessage{}, err
	}

	row, err := h.newConversationRow(conv)
	if err != nil {
		return ChatConversation{}, ChatMessage{}, err
	}
	question.Sequence = 1
	err = h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return conversationInsertError(err, row)
		}

		return h.insertChatMessage(tx, row.ID, question, row.CreatedAt)
	})
	if errors.Is(err, ErrChatRequestSeen) {
		return ChatConversation{}, ChatMessage{}, ErrChatRequestSeen
	}
	if err != nil {
		return ChatConversation{}, ChatMessage{}, fmt.Errorf("start chat conversation: %w", scrub(err))
	}

	question.CreatedAt = row.CreatedAt

	return opened(conv, row), question, nil
}

// newConversationRow mints the id and seals the title for a conversation
// about to be written.
func (h *HubState) newConversationRow(conv ChatConversation) (HubChatConversation, error) {
	id, err := newChatID()
	if err != nil {
		return HubChatConversation{}, err
	}
	title, err := h.seal(chatTitleBinding(h.team, id), conv.Title)
	if err != nil {
		return HubChatConversation{}, err
	}

	// PostgreSQL keeps microseconds. Truncating here makes the value returned
	// the same one a later read sees, which the list cursor depends on.
	now := time.Now().UTC().Truncate(time.Microsecond)

	return HubChatConversation{
		ID:         id,
		TeamID:     h.team,
		Subject:    strings.TrimSpace(conv.Subject),
		Title:      title,
		Provider:   conv.Provider,
		Model:      conv.Model,
		DatabaseID: strings.TrimSpace(conv.DatabaseID),
		RequestKey: strings.TrimSpace(conv.RequestKey),
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

func conversationInsertError(err error, row HubChatConversation) error {
	if errors.Is(err, gorm.ErrDuplicatedKey) && row.RequestKey != "" {
		return ErrChatRequestSeen
	}

	return fmt.Errorf("create chat conversation: %w", scrub(err))
}

// opened is conv as written, with the fields the store filled in.
func opened(conv ChatConversation, row HubChatConversation) ChatConversation {
	conv.ID = row.ID
	conv.Subject = row.Subject
	conv.DatabaseID = row.DatabaseID
	conv.RequestKey = row.RequestKey
	conv.CreatedAt = row.CreatedAt
	conv.UpdatedAt = row.UpdatedAt

	return conv
}

// ChatConversation returns one conversation without its messages.
func (h *HubState) ChatConversation(ctx context.Context, id string) (ChatConversation, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return ChatConversation{}, err
	}

	var row HubChatConversation
	if err := tx.Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ChatConversation{}, ErrChatNotFound
		}

		return ChatConversation{}, fmt.Errorf("read chat conversation: %w", scrub(err))
	}

	return h.openConversation(row)
}

// ChatConversationByRequestKey returns the conversation a request key opened.
func (h *HubState) ChatConversationByRequestKey(ctx context.Context, key string) (ChatConversation, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return ChatConversation{}, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ChatConversation{}, ErrChatNotFound
	}

	var row HubChatConversation
	if err := tx.Where("request_key = ?", key).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ChatConversation{}, ErrChatNotFound
		}

		return ChatConversation{}, fmt.Errorf("read chat conversation: %w", scrub(err))
	}

	return h.openConversation(row)
}

// ListChatConversations returns conversations newest first, by the time of
// their last message. cursor is empty for the first page.
func (h *HubState) ListChatConversations(ctx context.Context, cursor string, limit int) (ChatConversationPage, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return ChatConversationPage{}, err
	}
	if limit <= 0 || limit > maxChatPage {
		limit = maxChatPage
	}

	if cursor != "" {
		at, id, err := decodeChatCursor(cursor)
		if err != nil {
			return ChatConversationPage{}, err
		}
		tx = tx.Where("(updated_at < ?) OR (updated_at = ? AND id < ?)", at, at, id)
	}

	var rows []HubChatConversation
	if err := tx.Order("updated_at DESC").Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return ChatConversationPage{}, fmt.Errorf("list chat conversations: %w", scrub(err))
	}

	page := ChatConversationPage{Conversations: make([]ChatConversation, 0, min(len(rows), limit))}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.Next = encodeChatCursor(last.UpdatedAt, last.ID)
	}
	for _, row := range rows {
		conv, err := h.openConversation(row)
		if err != nil {
			return ChatConversationPage{}, err
		}
		page.Conversations = append(page.Conversations, conv)
	}

	return page, nil
}

// ChatMessages returns every message of one conversation, oldest first.
func (h *HubState) ChatMessages(ctx context.Context, id string) ([]ChatMessage, error) {
	return h.chatMessages(ctx, id, 0)
}

// RecentChatMessages returns the newest limit messages of one conversation,
// oldest first. A turn reads this rather than the whole thread, so what it
// costs stays bounded however long the conversation grows.
func (h *HubState) RecentChatMessages(ctx context.Context, id string, limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		return []ChatMessage{}, nil
	}

	return h.chatMessages(ctx, id, limit)
}

// FirstChatMessages returns the oldest limit messages of one conversation,
// oldest first: the question that opened it and what followed.
func (h *HubState) FirstChatMessages(ctx context.Context, id string, limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		return []ChatMessage{}, nil
	}

	return h.chatMessages(ctx, id, -limit)
}

// chatMessages reads messages oldest first. A limit above zero keeps only the
// newest that many, and one below zero only the oldest that many.
func (h *HubState) chatMessages(ctx context.Context, id string, limit int) ([]ChatMessage, error) {
	tx, err := h.scope(ctx)
	if err != nil {
		return nil, err
	}

	var found int64
	if err := tx.Model(&HubChatConversation{}).Where("id = ?", id).Count(&found).Error; err != nil {
		return nil, fmt.Errorf("read chat conversation: %w", scrub(err))
	}
	if found == 0 {
		return nil, ErrChatNotFound
	}

	var rows []HubChatMessage
	q := h.db.WithContext(ctx).Where("team_id = ? AND conversation_id = ?", h.team, id)
	switch {
	case limit > 0:
		q = q.Order("sequence DESC").Limit(limit)
	case limit < 0:
		q = q.Order("sequence ASC").Limit(-limit)
	default:
		q = q.Order("sequence ASC")
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read chat messages: %w", scrub(err))
	}
	if limit > 0 {
		slices.Reverse(rows)
	}

	out := make([]ChatMessage, 0, len(rows))
	for _, row := range rows {
		content, err := h.open(chatMessageBinding(h.team, id, row.Sequence, row.Role, "content"), row.Content)
		if err != nil {
			return nil, fmt.Errorf("read chat message %d: %w", row.Sequence, err)
		}
		actions, err := h.open(chatMessageBinding(h.team, id, row.Sequence, row.Role, "actions"), row.Actions)
		if err != nil {
			return nil, fmt.Errorf("read chat message %d actions: %w", row.Sequence, err)
		}
		out = append(out, ChatMessage{
			Sequence:  row.Sequence,
			Role:      row.Role,
			Content:   content,
			Actions:   actions,
			CreatedAt: row.CreatedAt,
		})
	}

	return out, nil
}

// AppendChatMessage adds one message at the end of a conversation and moves
// the conversation to the top of the list.
//
// The conversation row is locked while the sequence is picked, so two turns
// saved at once take the next two numbers rather than colliding on one.
func (h *HubState) AppendChatMessage(ctx context.Context, id string, msg ChatMessage) (ChatMessage, error) {
	return h.appendChatMessage(ctx, id, msg, time.Time{})
}

// AppendChatQuestion adds a user message unless the conversation is busy:
// its newest message is a user message saved after since, which is a turn
// still being answered. The check runs under the same row lock as the append,
// so of two questions sent at once exactly one goes in.
//
// A question older than since belongs to a turn whose process went away
// before answering. It does not hold the conversation: the new question
// replaces it in the same transaction.
func (h *HubState) AppendChatQuestion(ctx context.Context, id string, msg ChatMessage, since time.Time) (ChatMessage, error) {
	return h.appendChatMessage(ctx, id, msg, since)
}

func (h *HubState) appendChatMessage(ctx context.Context, id string, msg ChatMessage, busySince time.Time) (ChatMessage, error) {
	if _, err := h.scope(ctx); err != nil {
		return ChatMessage{}, err
	}
	if h.cipher == nil {
		return ChatMessage{}, errors.New("no encryption key is configured, so this value cannot be stored")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var conv HubChatConversation
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("team_id = ? AND id = ?", h.team, id).Take(&conv).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrChatNotFound
		}
		if err != nil {
			return err
		}

		var last HubChatMessage
		err = tx.Where("team_id = ? AND conversation_id = ?", h.team, id).
			Order("sequence DESC").Limit(1).Take(&last).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			last = HubChatMessage{}
		case err != nil:
			return err
		}
		msg.Sequence = last.Sequence + 1
		if !busySince.IsZero() && last.Role == "user" {
			if last.CreatedAt.After(busySince) {
				return ErrChatBusy
			}
			// The turn that asked this was lost before it answered. The new
			// question takes its place, so the thread keeps no question
			// without a reply and the model is not sent two in a row.
			if err := tx.Delete(&HubChatMessage{}, last.ID).Error; err != nil {
				return err
			}
			msg.Sequence = last.Sequence
		}

		return h.insertChatMessage(tx, id, msg, now)
	})
	if errors.Is(err, ErrChatNotFound) || errors.Is(err, ErrChatBusy) {
		return ChatMessage{}, err
	}
	if err != nil {
		return ChatMessage{}, fmt.Errorf("save chat message: %w", scrub(err))
	}

	msg.CreatedAt = now

	return msg, nil
}

// insertChatMessage seals one message, writes it and moves its conversation
// to the top of the list, inside the caller's transaction.
func (h *HubState) insertChatMessage(tx *gorm.DB, id string, msg ChatMessage, now time.Time) error {
	content, err := h.seal(chatMessageBinding(h.team, id, msg.Sequence, msg.Role, "content"), msg.Content)
	if err != nil {
		return err
	}
	actions, err := h.seal(chatMessageBinding(h.team, id, msg.Sequence, msg.Role, "actions"), msg.Actions)
	if err != nil {
		return err
	}

	if err := tx.Create(&HubChatMessage{
		ConversationID: id,
		TeamID:         h.team,
		Sequence:       msg.Sequence,
		Role:           msg.Role,
		Content:        content,
		Actions:        actions,
		CreatedAt:      now,
	}).Error; err != nil {
		return err
	}

	return tx.Model(&HubChatConversation{}).
		Where("team_id = ? AND id = ?", h.team, id).
		Update("updated_at", now).Error
}

// DeleteChatConversation removes a conversation and every message in it. The
// rows are deleted rather than flagged: nothing reads a deleted conversation
// back, so keeping it would only keep what the customer asked to be rid of.
func (h *HubState) DeleteChatConversation(ctx context.Context, id string) error {
	if _, err := h.scope(ctx); err != nil {
		return err
	}

	err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("team_id = ? AND id = ?", h.team, id).Delete(&HubChatConversation{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrChatNotFound
		}

		return tx.Where("team_id = ? AND conversation_id = ?", h.team, id).Delete(&HubChatMessage{}).Error
	})
	if errors.Is(err, ErrChatNotFound) {
		return ErrChatNotFound
	}
	if err != nil {
		return fmt.Errorf("delete chat conversation: %w", scrub(err))
	}

	return nil
}

// DeleteChatMessage removes one message. It is how a turn the model never
// answered is taken back out, so the thread does not keep a question with no
// reply.
//
// The conversation's position in the list goes back with it: UpdatedAt is set
// to the newest message still there, or to when the conversation started, so
// the list stays ordered by what the thread actually holds.
//
// The message is matched on its sequence and the time it was saved, not the
// sequence alone. A question that replaced a stale one reuses its sequence,
// so a late take back of the stale one must not remove its replacement.
func (h *HubState) DeleteChatMessage(ctx context.Context, id string, msg ChatMessage) error {
	if _, err := h.scope(ctx); err != nil {
		return err
	}

	err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("team_id = ? AND conversation_id = ? AND sequence = ? AND created_at = ?",
			h.team, id, msg.Sequence, msg.CreatedAt).Delete(&HubChatMessage{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrChatNotFound
		}

		return tx.Exec(`UPDATE hub_chat_conversations SET updated_at = COALESCE(
			(SELECT MAX(created_at) FROM hub_chat_messages WHERE team_id = ? AND conversation_id = ?),
			created_at) WHERE team_id = ? AND id = ?`, h.team, id, h.team, id).Error
	})
	if errors.Is(err, ErrChatNotFound) {
		return ErrChatNotFound
	}
	if err != nil {
		return fmt.Errorf("delete chat message: %w", scrub(err))
	}

	return nil
}

func (h *HubState) openConversation(row HubChatConversation) (ChatConversation, error) {
	title, err := h.open(chatTitleBinding(h.team, row.ID), row.Title)
	if err != nil {
		return ChatConversation{}, fmt.Errorf("read chat title: %w", err)
	}

	return ChatConversation{
		ID:         row.ID,
		Subject:    row.Subject,
		Title:      title,
		Provider:   row.Provider,
		Model:      row.Model,
		DatabaseID: row.DatabaseID,
		RequestKey: row.RequestKey,
		AgentID:    row.AgentID,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}, nil
}

// SetChatAgent records the Cursor agent a conversation runs on, replacing the
// one it had. The conversation does not move in the list: an agent is not a
// message, so UpdateColumn leaves updated_at alone.
func (h *HubState) SetChatAgent(ctx context.Context, id, agentID string) error {
	if _, err := h.scope(ctx); err != nil {
		return err
	}

	result := h.db.WithContext(ctx).Model(&HubChatConversation{}).
		Where("team_id = ? AND id = ?", h.team, id).
		UpdateColumn("agent_id", strings.TrimSpace(agentID))
	if result.Error != nil {
		return fmt.Errorf("keep chat agent: %w", scrub(result.Error))
	}
	if result.RowsAffected == 0 {
		return ErrChatNotFound
	}

	return nil
}

// chatTitleBinding and chatMessageBinding name the row a sealed chat value
// belongs to. The sequence is part of a message's binding, so a turn copied
// over another turn of the same conversation does not open there. The role is
// part of it, so a user turn relabelled as the assistant's no longer opens,
// and the field is, so content and actions cannot trade places.
func chatTitleBinding(team uint, id string) string {
	return "hub_chat_conversations\x1f" + strconv.FormatUint(uint64(team), 10) + "\x1f" + id + "\x1ftitle"
}

func chatMessageBinding(team uint, id string, sequence int, role, field string) string {
	return "hub_chat_messages\x1f" + strconv.FormatUint(uint64(team), 10) + "\x1f" + id + "\x1f" + strconv.Itoa(sequence) +
		"\x1f" + role + "\x1f" + field
}

func newChatID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("chat conversation id: %w", err)
	}

	return hex.EncodeToString(raw), nil
}

// The cursor is the last row's position, not an offset, so a conversation that
// moves to the top between two pages does not shift the next page under it.
func encodeChatCursor(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at.UTC().UnixMicro(), 10) + "." + id))
}

func decodeChatCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", ErrChatCursor
	}
	micros, id, ok := strings.Cut(string(raw), ".")
	if !ok || id == "" {
		return time.Time{}, "", ErrChatCursor
	}
	at, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return time.Time{}, "", ErrChatCursor
	}

	return time.UnixMicro(at).UTC(), id, nil
}
