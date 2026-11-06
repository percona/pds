package store

import (
	"context"
	"errors"
	"time"
)

// ErrNotConnected is what every call returns before the connection lands. The
// process serves throughout, reporting not ready, so callers see this rather
// than a nil dereference during the window a first install spends waiting for
// its database.
var ErrNotConnected = errors.New("the adapter database is not connected yet")

// lazyPoll is how often Wait looks for the connection. The connection attempt
// has its own backoff; this only bounds how long after it lands the hub waits
// to notice.
const lazyPoll = 250 * time.Millisecond

// Lazy is the team scoped hub state, resolved when the database connects.
//
// It has the same surface as HubState, so everything that stores state can be
// wired at start in one pass rather than waiting for the connection. A call
// that arrives first reports that the database is not connected, which is the
// same thing readiness is already saying.
type Lazy struct {
	starter *Starter
	team    uint
	cipher  *Cipher
}

// NewLazy returns the hub state for one Team, resolved on first use.
func NewLazy(starter *Starter, team uint, cipher *Cipher) *Lazy {
	return &Lazy{starter: starter, team: team, cipher: cipher}
}

// Team is the Team this state is scoped to.
func (l *Lazy) Team() uint {
	if l == nil {
		return 0
	}

	return l.team
}

// For returns the same state scoped to another team, so one process can serve
// several without holding a resolved connection per team.
//
// Team zero returns a state that reads and writes nothing, because HubState
// refuses a blank team. A request that carried no team has to be refused rather
// than served somebody's rows, and answering with the team already in hand would
// do exactly that on a deployment pinned to one. A caller that wants a default
// names it.
func (l *Lazy) For(team uint) *Lazy {
	if l == nil {
		return nil
	}
	if team == l.team {
		return l
	}

	return &Lazy{starter: l.starter, team: team, cipher: l.cipher}
}

// State returns the resolved state, or nil before the connection lands.
func (l *Lazy) State() *HubState {
	if l == nil || l.starter == nil {
		return nil
	}
	db := l.starter.Store()
	if db == nil {
		return nil
	}

	return db.Hub(l.team, l.cipher)
}

// Wait blocks until the database is connected, or ctx ends.
func (l *Lazy) Wait(ctx context.Context) (*HubState, error) {
	for {
		if state := l.State(); state != nil {
			return state, nil
		}

		timer := time.NewTimer(lazyPoll)
		select {
		case <-ctx.Done():
			timer.Stop()

			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *Lazy) resolve() (*HubState, error) {
	state := l.State()
	if state == nil {
		return nil, ErrNotConnected
	}

	return state, nil
}

// Environments returns every environment document this team holds.
func (l *Lazy) Environments(ctx context.Context) (map[string]string, error) {
	state, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return state.Environments(ctx)
}

// SaveEnvironment replaces one environment document.
func (l *Lazy) SaveEnvironment(ctx context.Context, name, document string) error {
	state, err := l.resolve()
	if err != nil {
		return err
	}

	return state.SaveEnvironment(ctx, name, document)
}

// RemoveEnvironment drops one environment.
func (l *Lazy) RemoveEnvironment(ctx context.Context, name string) error {
	state, err := l.resolve()
	if err != nil {
		return err
	}

	return state.RemoveEnvironment(ctx, name)
}

// Tokens returns every grant this team holds.
func (l *Lazy) Tokens(ctx context.Context) (map[string]string, error) {
	state, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return state.Tokens(ctx)
}

// SaveToken writes one grant against its hash.
func (l *Lazy) SaveToken(ctx context.Context, hash, document string) error {
	state, err := l.resolve()
	if err != nil {
		return err
	}

	return state.SaveToken(ctx, hash, document)
}

// RemoveTokens drops the named hashes.
func (l *Lazy) RemoveTokens(ctx context.Context, hashes []string) error {
	state, err := l.resolve()
	if err != nil {
		return err
	}

	return state.RemoveTokens(ctx, hashes)
}

// Work returns every environment's outbound queue.
func (l *Lazy) Work(ctx context.Context) (map[string]string, error) {
	state, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return state.Work(ctx)
}

// SaveWork replaces one environment's queue.
func (l *Lazy) SaveWork(ctx context.Context, environment, document string) error {
	state, err := l.resolve()
	if err != nil {
		return err
	}

	return state.SaveWork(ctx, environment, document)
}

// SupportAccess returns every environment's support access document.
func (l *Lazy) SupportAccess(ctx context.Context) (map[string]string, error) {
	state, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return state.SupportAccess(ctx)
}

// SaveSupportAccess replaces one environment's support access document.
func (l *Lazy) SaveSupportAccess(ctx context.Context, environment, document string) error {
	state, err := l.resolve()
	if err != nil {
		return err
	}

	return state.SaveSupportAccess(ctx, environment, document)
}

// RemoveSupportAccess drops one environment's support access document.
func (l *Lazy) RemoveSupportAccess(ctx context.Context, environment string) error {
	state, err := l.resolve()
	if err != nil {
		return err
	}

	return state.RemoveSupportAccess(ctx, environment)
}

// AllSupportAccess returns the support access document of every Team's
// environments. It ignores the Team this state is scoped to, because the private
// network it describes is shared.
func (l *Lazy) AllSupportAccess(ctx context.Context) ([]string, error) {
	if l == nil {
		return nil, ErrNotConnected
	}

	return l.starter.AllSupportAccess(ctx)
}

// CloneKeys returns every clone identity this team holds.
func (l *Lazy) CloneKeys(ctx context.Context) (map[string]string, error) {
	state, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return state.CloneKeys(ctx)
}

// SaveCloneKey writes one clone identity.
func (l *Lazy) SaveCloneKey(ctx context.Context, environment, document string) error {
	state, err := l.resolve()
	if err != nil {
		return err
	}

	return state.SaveCloneKey(ctx, environment, document)
}

// RemoveCloneKey drops one clone identity.
func (l *Lazy) RemoveCloneKey(ctx context.Context, environment string) error {
	state, err := l.resolve()
	if err != nil {
		return err
	}

	return state.RemoveCloneKey(ctx, environment)
}

// Connection returns one credential's fields.
func (l *Lazy) Connection(ctx context.Context, name string) (map[string]string, error) {
	state, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return state.Connection(ctx, name)
}

// SaveConnection replaces one credential's fields.
func (l *Lazy) SaveConnection(ctx context.Context, name string, values map[string]string) error {
	state, err := l.resolve()
	if err != nil {
		return err
	}

	return state.SaveConnection(ctx, name, values)
}

// RemoveConnection drops every field of one credential.
func (l *Lazy) RemoveConnection(ctx context.Context, name string) error {
	state, err := l.resolve()
	if err != nil {
		return err
	}

	return state.RemoveConnection(ctx, name)
}

// UserNames reads the display name of Users by id.
func (l *Lazy) UserNames(ctx context.Context, ids []uint) (map[uint]string, error) {
	state, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return state.UserNames(ctx, ids)
}

// CreateChatConversation starts one conversation.
func (l *Lazy) CreateChatConversation(ctx context.Context, conv ChatConversation) (ChatConversation, error) {
	state, err := l.resolve()
	if err != nil {
		return ChatConversation{}, err
	}

	return state.CreateChatConversation(ctx, conv)
}

// ChatConversation returns one conversation without its messages.
func (l *Lazy) ChatConversation(ctx context.Context, id string) (ChatConversation, error) {
	state, err := l.resolve()
	if err != nil {
		return ChatConversation{}, err
	}

	return state.ChatConversation(ctx, id)
}

// ListChatConversations returns one page of conversations, newest first.
func (l *Lazy) ListChatConversations(ctx context.Context, cursor string, limit int) (ChatConversationPage, error) {
	state, err := l.resolve()
	if err != nil {
		return ChatConversationPage{}, err
	}

	return state.ListChatConversations(ctx, cursor, limit)
}

// ChatMessages returns every message of one conversation, oldest first.
func (l *Lazy) ChatMessages(ctx context.Context, id string) ([]ChatMessage, error) {
	state, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return state.ChatMessages(ctx, id)
}

// AppendChatMessage adds one message at the end of a conversation.
func (l *Lazy) AppendChatMessage(ctx context.Context, id string, msg ChatMessage) (ChatMessage, error) {
	state, err := l.resolve()
	if err != nil {
		return ChatMessage{}, err
	}

	return state.AppendChatMessage(ctx, id, msg)
}

// DeleteChatConversation removes a conversation and its messages.
func (l *Lazy) DeleteChatConversation(ctx context.Context, id string) error {
	state, err := l.resolve()
	if err != nil {
		return err
	}

	return state.DeleteChatConversation(ctx, id)
}

// SetChatAgent records the Cursor agent a conversation runs on.
func (l *Lazy) SetChatAgent(ctx context.Context, id, agentID string) error {
	state, err := l.resolve()
	if err != nil {
		return err
	}

	return state.SetChatAgent(ctx, id, agentID)
}

// DeleteChatMessage removes one message of a conversation.
func (l *Lazy) DeleteChatMessage(ctx context.Context, id string, msg ChatMessage) error {
	state, err := l.resolve()
	if err != nil {
		return err
	}

	return state.DeleteChatMessage(ctx, id, msg)
}

// AppendChatQuestion adds a user message unless the conversation is busy.
func (l *Lazy) AppendChatQuestion(ctx context.Context, id string, msg ChatMessage, since time.Time) (ChatMessage, error) {
	state, err := l.resolve()
	if err != nil {
		return ChatMessage{}, err
	}

	return state.AppendChatQuestion(ctx, id, msg, since)
}

// RecentChatMessages returns the newest messages of one conversation.
func (l *Lazy) RecentChatMessages(ctx context.Context, id string, limit int) ([]ChatMessage, error) {
	state, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return state.RecentChatMessages(ctx, id, limit)
}

// ChatConversationByRequestKey returns the conversation a request key opened.
func (l *Lazy) ChatConversationByRequestKey(ctx context.Context, key string) (ChatConversation, error) {
	state, err := l.resolve()
	if err != nil {
		return ChatConversation{}, err
	}

	return state.ChatConversationByRequestKey(ctx, key)
}

// FirstChatMessages returns the oldest messages of one conversation.
func (l *Lazy) FirstChatMessages(ctx context.Context, id string, limit int) ([]ChatMessage, error) {
	state, err := l.resolve()
	if err != nil {
		return nil, err
	}

	return state.FirstChatMessages(ctx, id, limit)
}

// StartChatConversation opens a conversation with its first question.
func (l *Lazy) StartChatConversation(ctx context.Context, conv ChatConversation, question ChatMessage) (ChatConversation, ChatMessage, error) {
	state, err := l.resolve()
	if err != nil {
		return ChatConversation{}, ChatMessage{}, err
	}

	return state.StartChatConversation(ctx, conv, question)
}

// Owner names the Organization and the Team this team's rows belong to.
func (l *Lazy) Owner(ctx context.Context) (organization, team string, err error) {
	if l == nil || l.starter == nil {
		return "", "", ErrNotConnected
	}
	db := l.starter.Store()
	if db == nil {
		return "", "", ErrNotConnected
	}

	return db.Identity().TeamOwner(ctx, l.team)
}

// ClaimRepo gives a repository name to an owner, across every team.
func (l *Lazy) ClaimRepo(ctx context.Context, repo, owner string) (won, fresh bool, err error) {
	if l == nil || l.starter == nil {
		return false, false, ErrNotConnected
	}
	db := l.starter.Store()
	if db == nil {
		return false, false, ErrNotConnected
	}

	return db.ClaimRepo(ctx, repo, owner)
}

// ReleaseRepo gives a repository name back to the claim table.
func (l *Lazy) ReleaseRepo(ctx context.Context, repo, owner string) error {
	if l == nil || l.starter == nil {
		return ErrNotConnected
	}
	db := l.starter.Store()
	if db == nil {
		return ErrNotConnected
	}

	return db.ReleaseRepo(ctx, repo, owner)
}
