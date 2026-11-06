package service

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/client/cursor"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/store"
)

// cursorAgentBudget bounds one call that keeps or deletes a conversation's
// agent. Like the take back it has a budget of its own, because the turn's may
// be what just ran out.
const cursorAgentBudget = 10 * time.Second

// CursorAgent is a Cursor client that runs on one cloud agent.
type CursorAgent interface {
	claude.Completer
	// AgentID is the agent the client's runs go to. After a call it is the one
	// the conversation has to keep: the agent the client was opened on, a new
	// one it created, or the one that replaced an agent that refused a run.
	AgentID() string
}

// CursorAgents is Cursor Cloud Agents as the Chat uses them: a client for a
// conversation, and the deletion of an agent once its conversation is gone.
type CursorAgents interface {
	// Open returns a client on agentID, or one that creates an agent on its
	// first call when agentID is empty.
	Open(ctx context.Context, model, agentID string) (CursorAgent, error)
	Delete(ctx context.Context, agentID string) error
}

// WithCursorAgents replaces the connection to Cursor, for tests.
func WithCursorAgents(agents CursorAgents) ChatOption {
	return func(ch *Chat) {
		ch.cursorAgents = agents
	}
}

// cursorAPI reaches Cursor with the key the Organization pasted in.
type cursorAPI struct {
	connections *Connections
}

func (a cursorAPI) client(ctx context.Context, model, agentID string) (*cursor.Client, error) {
	creds, err := a.connections.Cursor(ctx)
	if err != nil {
		return nil, err
	}

	return cursor.New(cursor.Options{APIKey: creds.APIKey, Model: model, AgentID: agentID})
}

func (a cursorAPI) Open(ctx context.Context, model, agentID string) (CursorAgent, error) {
	client, err := a.client(ctx, model, agentID)
	if err != nil {
		return nil, err
	}

	return client, nil
}

func (a cursorAPI) Delete(ctx context.Context, agentID string) error {
	client, err := a.client(ctx, "", "")
	if err != nil {
		return err
	}

	return client.DeleteAgent(ctx, agentID)
}

// refreshCompleter reads the conversation again and, when another turn has
// replaced its agent since req's turn read it, opens the client on the agent it
// has now. Two turns do not run at once, but the first can end between the
// other's read and its question being let in, and the replacement it kept would
// otherwise be overwritten by this turn's and left at Cursor.
func (c *Chat) refreshCompleter(ctx context.Context, chats ChatStore, conv store.ChatConversation, completer claude.Completer, req ChatRequest) (store.ChatConversation, claude.Completer, error) {
	if _, ok := completer.(CursorAgent); !ok {
		return conv, completer, nil
	}
	current, err := chats.ChatConversation(ctx, conv.ID)
	if err != nil {
		return conv, nil, chatStoreError(err)
	}
	if current.AgentID == conv.AgentID {
		return conv, completer, nil
	}
	reopened, err := c.completerFor(ctx, strings.TrimSpace(req.Provider), req.Model, current.AgentID)
	if err != nil {
		return conv, nil, err
	}

	return current, reopened, nil
}

// keepAgent saves the agent a turn ended on with its conversation, so the
// next turn runs on it. An agent the conversation cannot keep is deleted, so
// it does not stay at Cursor holding a conversation nobody can reach: the
// turn failed and takes the conversation it had just started with it, or the
// id could not be saved.
func (c *Chat) keepAgent(ctx context.Context, chats ChatStore, conv store.ChatConversation, completer claude.Completer, started bool, turnErr error) {
	agent, ok := completer.(CursorAgent)
	if !ok || agent.AgentID() == "" || agent.AgentID() == conv.AgentID {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cursorAgentBudget)
	defer cancel()

	if turnErr != nil && started {
		c.deleteAgent(ctx, agent.AgentID())

		return
	}
	if err := chats.SetChatAgent(ctx, conv.ID, agent.AgentID()); err != nil {
		log.FromContext(ctx).WarnContext(ctx, "a chat conversation could not keep its Cursor agent",
			slog.String("agent", agent.AgentID()),
			slog.String("cause", log.RedactText(err.Error())))
		c.deleteAgent(ctx, agent.AgentID())
	}
}

// deleteAgent asks Cursor to delete an agent. It is best effort: a conversation
// is deleted whether or not Cursor answers, and the agent that stays is only
// something the person can delete in Cursor.
func (c *Chat) deleteAgent(ctx context.Context, agentID string) {
	if agentID == "" {
		return
	}
	if err := c.cursorAgents.Delete(ctx, agentID); err != nil {
		log.FromContext(ctx).WarnContext(ctx, "a Cursor agent was not deleted",
			slog.String("agent", agentID),
			slog.String("cause", log.RedactText(err.Error())))
	}
}

// withGrounding puts what Cursor is told about the databases at the head of the
// newest message, the question. A kept agent is sent only the question, so the
// list is current for every turn, where one in the system prompt would be as
// old as the agent.
func withGrounding(messages []claude.Message, grounding string) []claude.Message {
	out := slices.Clone(messages)
	last := &out[len(out)-1]
	blocks := slices.Clone(last.Content)
	for i := range blocks {
		if blocks[i].Type == "text" {
			blocks[i].Text = grounding + "\n\n" + blocks[i].Text

			break
		}
	}
	last.Content = blocks

	return out
}
