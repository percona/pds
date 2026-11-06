package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/mcp"
)

// chatToolFailed is what the model reads when a call could not be made at all,
// such as a tool the Chat's tier does not include.
const chatToolFailed = "That tool is not available. Answer with what you already have."

// chatToolStopped is what the model reads when the turn ended while a call ran.
const chatToolStopped = "The call was cut off because the turn ran out of time. Answer with what you already have."

// maxChatToolResult bounds one result the model reads, so a long list cannot
// fill the context.
const maxChatToolResult = 20000

// chatToolResultCut ends a result that was longer than maxChatToolResult.
const chatToolResultCut = "\n[The result was cut off. Ask for less, for example with a limit.]"

// WithTools gives the Chat the registry of tools it offers the model. The Chat
// defines none of its own: it is a client of the registry, as an MCP client is.
// Without one a turn has no tools.
func WithTools(registry *mcp.Registry) ChatOption {
	return func(ch *Chat) {
		ch.tools = registry
	}
}

// WithAccessLimits gives the Chat the Organization settings that cap what its
// tools may do. Without them the Chat's tools only read.
func WithAccessLimits(limits AccessLimits) ChatOption {
	return func(ch *Chat) {
		ch.limits = limits
	}
}

// chatTools is one turn's connection to the registry: the tools it lists, the
// instructions it gives, and the way each call is made.
type chatTools struct {
	session      *mcp.Session
	defs         []claude.Tool
	instructions string
	// changes are the offered tools that change something, by name. A call to
	// one is put to the person before it is made.
	changes map[string]changeTool
	// approve asks the person. A turn that cannot ask has none, and its
	// changes are refused.
	approve approver
	// slots bounds the calls being made at once.
	slots chan struct{}
	// ended is how the turn was cut short by an approval that expired or was
	// abandoned, or empty.
	ended atomic.Value
}

// connectTools opens the turn's session. The caller is the person who asked,
// so every call is scoped and recorded as theirs, and the provider is the
// agent that made it. The tools and instructions are the registry's for the
// Chat's tier, the same on every turn.
func (c *Chat) connectTools(ctx context.Context, provider string) (*chatTools, error) {
	if c.tools == nil {
		return &chatTools{}, nil
	}
	session, err := c.tools.Connect(ctx, chatCaller(ctx, provider, c.access(ctx)))
	if err != nil {
		return nil, err
	}

	defs, changes, err := toolDefs(ctx, session)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("list the tools: %w", err), session.Close())
	}

	return &chatTools{
		session:      session,
		defs:         defs,
		changes:      changes,
		slots:        make(chan struct{}, maxParallelChatTools),
		instructions: strings.TrimSpace(session.InitializeResult().Instructions),
	}, nil
}

// toolDefs is the registry's tool list in the form the model clients take,
// and which of its tools change something. A tool is a read only when it says
// so, so a tool that says nothing is asked about.
func toolDefs(ctx context.Context, session *mcp.Session) ([]claude.Tool, map[string]changeTool, error) {
	var defs []claude.Tool
	changes := map[string]changeTool{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, nil, err
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return nil, nil, fmt.Errorf("tool %s schema: %w", tool.Name, err)
		}
		defs = append(defs, claude.Tool{Name: tool.Name, Description: tool.Description, InputSchema: schema})
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			destructive := tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint
			changes[tool.Name] = changeTool{title: tool.Title, destructive: destructive}
		}
	}

	return defs, changes, nil
}

// chatCaller is who the registry's tools act as: the signed in person, in
// their Team, as the Chat, run by the provider the conversation uses.
func chatCaller(ctx context.Context, provider string, access auth.Access) auth.Caller {
	scope, _ := auth.ScopeFromContext(ctx)
	subject, _ := auth.SubjectFromContext(ctx)
	caller := auth.Caller{
		Identity: auth.Identity{Subject: subject, Scope: scope, Client: auth.Client{Kind: auth.ClientChat}},
		Access:   access,
	}
	if agent := chatAgent(provider); auth.ValidAgent(agent) {
		caller.Agent = agent
	}

	return caller
}

// access is the tier this turn's tools are served at: the lowest of what the
// Organization allows agents and what the person's role allows. It is read on
// every turn, so a lowered limit applies to the next question. A limit that
// cannot be read leaves the Chat at read.
func (c *Chat) access(ctx context.Context) auth.Access {
	scope, ok := auth.ScopeFromContext(ctx)
	if c.limits == nil || !ok {
		return auth.AccessRead
	}
	settings, err := c.limits.AI(ctx, scope.OrganizationID)
	if err != nil {
		log.FromContext(ctx).WarnContext(ctx, "the Organization's AI settings could not be read, so the Chat's tools only read",
			slog.String("cause", log.RedactText(err.Error())))

		return auth.AccessRead
	}

	return CallerAccess(auth.Access(settings.Access), scope.Role)
}

// chatAgent is the provider's name as the activity log shows it.
func chatAgent(provider string) string {
	switch strings.TrimSpace(provider) {
	case ProviderAnthropic:
		return "Anthropic"
	case ProviderCursor:
		return "Cursor"
	default:
		return ""
	}
}

func (t *chatTools) close(ctx context.Context) {
	if t.session == nil {
		return
	}
	if err := t.session.Close(); err != nil {
		log.FromContext(ctx).DebugContext(ctx, "the Chat's tool session did not close cleanly",
			slog.String("cause", log.RedactText(err.Error())))
	}
}

// run makes the calls the model asked for in one round side by side, a few at
// a time so one round cannot flood monitoring. Results come back in the order
// asked. A change waits for the person, one at a time, without holding up a
// read.
func (t *chatTools) run(ctx context.Context, calls []claude.ContentBlock) []claude.ContentBlock {
	results := make([]claude.ContentBlock, len(calls))
	var wg sync.WaitGroup
	for i, block := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = t.call(ctx, block)
		}()
	}
	wg.Wait()

	return results
}

// endedBy is how an approval cut the turn short, or empty.
func (t *chatTools) endedBy() string {
	outcome, _ := t.ended.Load().(string)

	return outcome
}

// call makes one call and returns the tool result the model reads. A tool that
// failed is a result marked as an error, which the model reads and answers
// from. The registry recovers a tool's panic into the same kind of result.
//
// A tool that changes something is made only with the person's approval, and
// with exactly the arguments they were shown. A tool the tier does not include
// is not asked about: the registry refuses it, as it would any call above the
// tier, and nothing is put to the person.
func (t *chatTools) call(ctx context.Context, block claude.ContentBlock) claude.ContentBlock {
	result := claude.ContentBlock{Type: "tool_result", ToolUseID: block.ID}
	if t.session == nil {
		result.Content, result.IsError = chatToolFailed, true

		return result
	}

	if change, asks := t.changes[block.Name]; asks {
		asked := approval{refusal: chatToolNoAsk}
		if t.approve != nil {
			asked = t.approve(ctx, block, change)
		}
		if asked.ended != "" {
			t.ended.Store(asked.ended)
		}
		if asked.refusal != "" {
			result.Content, result.IsError = asked.refusal, true

			return result
		}
		block.Input = asked.input
	}

	select {
	case t.slots <- struct{}{}:
		defer func() { <-t.slots }()
	case <-ctx.Done():
		result.Content, result.IsError = chatToolStopped, true

		return result
	}
	answer, err := t.session.CallTool(ctx, &sdk.CallToolParams{Name: block.Name, Arguments: block.Input})
	if err != nil {
		log.FromContext(ctx).WarnContext(ctx, "a chat tool call was not made",
			slog.String("tool", block.Name),
			slog.String("cause", log.RedactText(err.Error())))
		result.Content, result.IsError = chatToolFailed, true
		if ctx.Err() != nil {
			result.Content = chatToolStopped
		}

		return result
	}
	result.Content, result.IsError = clipToolResult(redactCredentials(resultText(answer))), answer.IsError

	return result
}

// clipToolResult cuts a result at maxChatToolResult bytes, on a character
// boundary, and says so.
func clipToolResult(text string) string {
	if len(text) <= maxChatToolResult {
		return text
	}
	cut := maxChatToolResult
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}

	return text[:cut] + chatToolResultCut
}

// resultText is the text of a tool's answer. The registry answers with one text
// block that holds the same JSON as the structured content.
func resultText(answer *sdk.CallToolResult) string {
	var text strings.Builder
	for _, block := range answer.Content {
		if part, ok := block.(*sdk.TextContent); ok {
			text.WriteString(part.Text)
		}
	}

	return text.String()
}

// chatToolLabel is the progress line the user sees while a tool runs.
func chatToolLabel(name string) string {
	switch name {
	case "list_databases":
		return "Listing databases"
	case "get_database":
		return "Reading status"
	case "get_metrics":
		return "Reading metrics"
	case "list_backups":
		return "Reading backups"
	case "get_database_history":
		return "Reading history"
	case "get_database_usage":
		return "Reading usage"
	case "get_environment_history":
		return "Reading environment history"
	case "list_environments":
		return "Listing environments"
	case "list_activity", "get_activity":
		return "Reading activity"
	case "list_alerts", "get_database_alerts":
		return "Reading alerts"
	case "list_support_tickets":
		return "Listing tickets"
	case "get_support_ticket":
		return "Reading a ticket"
	case "wait_for_database", "wait_for_backup", "wait_for_environment":
		return "Waiting"
	default:
		return "Reading data"
	}
}
