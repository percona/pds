package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/mcp"
	"github.com/percona/pds/adapter/internal/service/skills"
	"github.com/percona/pds/adapter/internal/store"
)

const (
	ProviderAnthropic     = "anthropic"
	ProviderCursor        = "cursor"
	maxChatRounds         = 4
	maxChatMessages       = 32
	maxChatChars          = 8000
	chatBudget            = 165 * time.Second
	maxParallelChatTools  = 4
	maxCursorGroundingDBs = 20
	maxActionLabel        = 40
)

// ErrInvalidChat means the thread or provider is not acceptable.
var ErrInvalidChat = errors.New("invalid chat")

// ChatMessage is one turn in a chat thread.
type ChatMessage struct {
	Role    string
	Content string
}

// ChatRequest is one new message. The thread it continues is read from the
// store rather than taken from the caller, so a client cannot rewrite what the
// assistant said earlier.
type ChatRequest struct {
	// ConversationID is empty on the first message, which starts one.
	ConversationID string
	// DatabaseID is recorded when a conversation starts from a database page.
	// A later message ignores it.
	DatabaseID string
	// RequestKey is the id the browser minted for a first message. A retry of
	// that message carries it again and is answered from the conversation it
	// already opened. A later message ignores it.
	RequestKey string
	Provider   string
	Model      string
	Message    string
	// Progress, when set, hears what the turn is doing while it runs, so a
	// caller can show the user something before the model has answered. It is
	// called from the goroutine running Reply, never concurrently.
	Progress func(ChatProgress)
	// Text, when set, hears the answer's text as the model writes it, in
	// pieces that add up to the reply's text, so a caller can show it while the
	// rest is still coming. An answer that arrives whole, such as Cursor's, is
	// heard as one piece. The reply is still the whole answer: a caller that
	// showed the pieces replaces them with it, which matters when a refused
	// answer is replaced by a sentence. It is called from the goroutine running
	// Reply, never concurrently.
	Text func(string)
	// Started, when set, hears the conversation id once the message is saved
	// and before the model is asked, so a caller whose turn then fails still
	// knows which conversation holds the message.
	Started func(conversationID string)
	// Approval, when set, is how a change the model asks for is put to the
	// person: it hears the card to show, and the change is made only if the
	// person answers it through Decide. Without it no change is made. It is
	// called from the goroutine running Reply, never concurrently.
	Approval func(ChatApproval)
	// Settled hears how each approval ended, so the card can say so.
	Settled func(ChatApprovalEnd)
}

// ChatProgress is one step of a turn that is still running.
type ChatProgress struct {
	Label string `json:"label"`
}

// ChatAction is one button the UI can render from a chat reply.
type ChatAction struct {
	Label       string `json:"label"`
	Kind        string `json:"kind"`
	ID          string `json:"id,omitempty"`
	Metric      string `json:"metric,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Description string `json:"description,omitempty"`
}

// ChatReply is the adapter's answer to a chat turn, already saved.
type ChatReply struct {
	ConversationID string       `json:"conversationId"`
	Text           string       `json:"text"`
	Actions        []ChatAction `json:"actions"`
}

// chatDatabases is what the Chat reads itself: the list it gives Cursor with
// the question, and the databases its buttons name. Everything the model asks
// for goes through the registry's tools instead.
type chatDatabases interface {
	ListEnvironment(ctx context.Context, environment string) ([]engine.Instance, error)
	Lookup(ctx context.Context, id string) (*engine.Instance, error)
}

type chatPayload struct {
	Text    string       `json:"text"`
	Actions []ChatAction `json:"actions"`
}

// Chat answers questions about a person's databases. The model's tools are the
// registry's, which the Chat calls in process as that person.
type Chat struct {
	databases    chatDatabases
	connections  *Connections
	tools        *mcp.Registry
	completer    claude.Completer
	cursorAgents CursorAgents
	limits       AccessLimits
	stores       ChatStores
	approvals    *chatApprovals
	now          func() time.Time
	timer        startTimer
}

// ChatOption configures Chat.
type ChatOption func(*Chat)

// WithChatClock replaces the clock that decides when an unanswered question
// is too old to still be running, for tests.
func WithChatClock(now func() time.Time) ChatOption {
	return func(ch *Chat) {
		ch.now = now
	}
}

// WithChatCompleter injects a Completer, for tests.
func WithChatCompleter(c claude.Completer) ChatOption {
	return func(ch *Chat) {
		ch.completer = c
	}
}

// NewChat returns the Chat service.
func NewChat(databases chatDatabases, connections *Connections, opts ...ChatOption) *Chat {
	ch := &Chat{
		databases:   databases,
		connections: connections,
		approvals:   newChatApprovals(),
		now:         time.Now,
		timer:       systemTimer,
	}
	ch.cursorAgents = cursorAPI{connections: connections}
	for _, opt := range opts {
		opt(ch)
	}

	return ch
}

// Reply saves one message, answers it, and saves the answer with its actions.
//
// The turn runs on a context detached from the caller's and bounded by
// chatBudget, which does not run while the turn waits for an approval. A
// browser that goes away mid-reply, a closed tab or a dropped connection, still
// finds the reply in the conversation when it comes back. Only an approval
// still waiting notices: it is cancelled, and nothing is changed.
func (c *Chat) Reply(ctx context.Context, req ChatRequest) (*ChatReply, error) {
	if c == nil {
		return nil, ErrAssistantUnavailable
	}
	gone := ctx.Done()
	ctx, budget := newTurnBudget(context.WithoutCancel(ctx), chatBudget, c.timer, c.now)
	defer budget.end()

	reply, err := c.reply(ctx, req, budget, gone)
	if err != nil && budget.exhausted() {
		// The context says only that it was canceled, which a caller reads as
		// the person leaving.
		log.FromContext(ctx).DebugContext(ctx, "the chat turn ran out of time", slog.String("cause", log.RedactText(err.Error())))
		return nil, fmt.Errorf("the turn ran out of time: %w", context.DeadlineExceeded)
	}

	return reply, err
}

// reply is Reply's turn, run on the budget's context.
func (c *Chat) reply(ctx context.Context, req ChatRequest, budget *turnBudget, gone <-chan struct{}) (*ChatReply, error) {

	if err := validateChatRequest(req); err != nil {
		return nil, err
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = currentModel(req.Provider, req.Model)
	chats, err := c.chatStore(ctx)
	if err != nil {
		return nil, err
	}
	// A continued conversation is read before the key is checked, so a stale
	// id or a switched model is reported as that rather than as a missing key.
	conv, err := continueConversation(ctx, chats, req)
	if err != nil {
		return nil, err
	}
	// A question older than this is no longer being answered: its turn has
	// run out of budget, or the process running it went away.
	staleBefore := c.now().Add(-chatBudget - chatTakeBackBudget)

	// resumed is a retry taking over a first message a lost turn left
	// unanswered. The conversation holds nothing else, so it is treated as
	// just started: a failure takes the whole conversation back.
	resumed := false
	if conv.ID == "" && strings.TrimSpace(req.RequestKey) != "" {
		opened, err := chats.ChatConversationByRequestKey(ctx, req.RequestKey)
		switch {
		case err == nil:
			reply, resume, err := replayRequest(ctx, chats, req, opened, staleBefore)
			if !resume {
				return reply, err
			}
			conv, resumed = opened, true
		case !errors.Is(err, store.ErrChatNotFound):
			return nil, chatStoreError(err)
		}
	}

	// The key is checked before anything is written, so a provider with no key
	// does not leave an empty conversation in the list.
	completer, err := c.completerFor(ctx, strings.TrimSpace(req.Provider), req.Model, conv.AgentID)
	if err != nil {
		return nil, err
	}

	started := conv.ID == "" || resumed
	// The message is kept as the model will read it, so a secret pasted into
	// the chat is not written to the database either.
	question := store.ChatMessage{
		Role:    "user",
		Content: Sanitize(strings.TrimSpace(req.Message)),
		Actions: encodeChatActions(nil),
	}
	var asked store.ChatMessage
	if conv.ID == "" {
		// A first message opens its conversation and goes in with it in one
		// write, so nothing can leave an empty conversation in the list.
		conv, asked, err = startConversation(ctx, chats, req, question)
		if errors.Is(err, store.ErrChatRequestSeen) {
			// The same message arrived twice at once and the other copy
			// opened the conversation first.
			opened, lookupErr := chats.ChatConversationByRequestKey(ctx, req.RequestKey)
			if lookupErr != nil {
				return nil, chatStoreError(lookupErr)
			}
			// The other copy is the one running, so this one waits on it.
			reply, _, replayErr := replayRequest(ctx, chats, req, opened, time.Time{})

			return reply, replayErr
		}
		if err != nil {
			return nil, err
		}
	} else {
		// A conversation takes one turn at a time, since two questions
		// answered at once would each be answered without the other; a
		// question older than a whole turn is no longer running and is
		// replaced.
		asked, err = chats.AppendChatQuestion(ctx, conv.ID, question, staleBefore)
		if err != nil {
			return nil, chatStoreError(err)
		}
	}
	if req.Started != nil {
		req.Started(conv.ID)
	}

	thread, err := turnWindow(ctx, chats, conv.ID, asked, started)
	if err != nil {
		takeBack(ctx, chats, conv.ID, asked, started)

		return nil, err
	}
	if !started {
		// The conversation was read before the question was let in. A turn that
		// ran in between may have replaced its agent, so this one runs on the
		// agent the conversation has now.
		current, refreshed, err := c.refreshCompleter(ctx, chats, conv, completer, req)
		if err != nil {
			takeBack(ctx, chats, conv.ID, asked, started)

			return nil, err
		}
		conv, completer = current, refreshed
	}

	reply, err := c.answer(ctx, req, completer, thread, budget, gone)
	if err == nil {
		_, err = chats.AppendChatMessage(ctx, conv.ID, store.ChatMessage{
			Role:    "assistant",
			Content: reply.Text,
			Actions: encodeChatActions(reply.Actions),
		})
		if err != nil {
			err = fmt.Errorf("%w: save the reply: %w", ErrChatUnsaved, chatStoreError(err))
		}
	}
	// The agent is kept after the reply is saved, or deleted with the
	// conversation when the turn is taken back.
	c.keepAgent(ctx, chats, conv, completer, started, err)
	if err != nil {
		// The question goes too, the same as when the model does not answer, or
		// the conversation would read as still answering it.
		takeBack(ctx, chats, conv.ID, asked, started)

		return nil, err
	}
	reply.ConversationID = conv.ID

	return reply, nil
}

// answer runs the tool loop over a thread and returns the model's reply.
func (c *Chat) answer(ctx context.Context, req ChatRequest, completer claude.Completer, thread []ChatMessage, budget *turnBudget, gone <-chan struct{}) (*ChatReply, error) {
	report := func(label string) {
		if req.Progress != nil {
			req.Progress(ChatProgress{Label: label})
		}
	}
	report("Thinking")

	tools, err := c.connectTools(ctx, strings.TrimSpace(req.Provider))
	if err != nil {
		return nil, fmt.Errorf("connect the Chat's tools: %w", err)
	}
	defer tools.close(ctx)
	tools.approve = c.approverFor(ctx, req, budget, gone)

	messages := chatMessagesToClaude(thread)
	// Cursor does not stream and keeps the JSON reply and the tool protocol it
	// is taught here, so the two providers differ in how an answer is read.
	streamed := strings.TrimSpace(req.Provider) != ProviderCursor
	answerFormat := anthropicAnswerFormat
	if !streamed {
		answerFormat = cursorToolProtocol
		messages = withGrounding(messages, c.cursorGrounding(ctx))
	}
	system := chatSystemPrompt(tools.instructions, answerFormat)
	stream := newReplyStream(req.Text)

	for round := 0; round < maxChatRounds; round++ {
		if round > 0 {
			report("Thinking")
		}
		// The last round rules tools out, so the model has to answer with what
		// it has read rather than end the turn asking for more.
		call := claude.Request{
			System:    system,
			Model:     req.Model,
			Messages:  messages,
			Tools:     tools.defs,
			MaxTokens: claude.DefaultMaxTokens,
		}
		if round == maxChatRounds-1 {
			// The instruction goes after the tool results rather than into the
			// system prompt, which has to stay the same on every round for the
			// model to read it from the cache.
			call.Messages = withFinalRoundPrompt(messages)
			call.NoToolCalls = true
		}
		if streamed {
			stream.beginRound()
			call.OnText = stream.write
		}

		resp, err := completer.Complete(ctx, call)
		if err != nil {
			return nil, err
		}
		log.FromContext(ctx).DebugContext(ctx, "chat model call",
			slog.Int("round", round+1),
			slog.Int("input_tokens", resp.Usage.InputTokens),
			slog.Int("output_tokens", resp.Usage.OutputTokens),
			slog.Int("cache_creation_input_tokens", resp.Usage.CacheCreationInputTokens),
			slog.Int("cache_read_input_tokens", resp.Usage.CacheReadInputTokens),
		)

		if resp.StopReason == claude.StopRefusal {
			// Whatever was streamed before the refusal is replaced by the
			// sentence when the reply arrives, and none of it is saved.
			return &ChatReply{Text: claude.RefusalSentence}, nil
		}

		// The response goes back as it came, thinking blocks included, so the
		// next round continues the same reasoning.
		messages = append(messages, claude.Message{Role: "assistant", Content: resp.Content})
		if streamed {
			stream.endRound(contentText(resp.Content))
			if resp.StopReason == "max_tokens" {
				// A cut off answer used to fail to parse and say so. Now its
				// text reads as complete unless something says otherwise.
				stream.note(chatCutOffSentence)
			}
		}

		if resp.StopReason != "tool_use" {
			if streamed {
				return stream.reply(ctx, c), nil
			}
			reply, err := parseChatReply(ctx, c, resp.Content)
			if err == nil && req.Text != nil {
				req.Text(reply.Text)
			}

			return reply, err
		}

		var calls []claude.ContentBlock
		for _, block := range resp.Content {
			if block.Type == "tool_use" {
				calls = append(calls, block)
				report(chatToolLabel(block.Name))
			}
		}
		messages = append(messages, claude.Message{Role: "user", Content: tools.run(ctx, calls)})
		if outcome := tools.endedBy(); outcome != "" {
			// Nobody is there to read another answer, and the model has nothing
			// to add to nothing having changed.
			return approvalEndedReply(outcome), nil
		}
	}

	return &ChatReply{
		Text: "The assistant ran out of steps before it could finish.",
		Actions: []ChatAction{{
			Label: defaultChatActionLabel(skills.ActionSupport),
			Kind:  skills.ActionSupport,
		}},
	}, nil
}

// approvalEndedReply is the whole answer of a turn whose approval was not
// answered.
func approvalEndedReply(outcome string) *ChatReply {
	text := chatApprovalCancelled
	if outcome == OutcomeExpired {
		text = chatApprovalExpired
	}

	return &ChatReply{Text: text}
}

// withFinalRoundPrompt returns messages with the instruction to answer added
// to the last one, which is the user's: the question, or the tool results.
func withFinalRoundPrompt(messages []claude.Message) []claude.Message {
	out := slices.Clone(messages)
	last := &out[len(out)-1]
	last.Content = append(slices.Clone(last.Content), claude.ContentBlock{Type: "text", Text: chatFinalRoundPrompt})

	return out
}

// currentModel is the model a turn runs on: the one asked for, or the default
// when Anthropic has deprecated it.
func currentModel(provider, model string) string {
	model = strings.TrimSpace(model)
	if provider == ProviderAnthropic && claude.Deprecated(model) {
		return claude.DefaultModel
	}

	return model
}

func (c *Chat) completerFor(ctx context.Context, provider, model, agentID string) (claude.Completer, error) {
	if c.completer != nil {
		return c.completer, nil
	}

	switch provider {
	case ProviderAnthropic:
		if c.connections == nil {
			return nil, ErrAssistantUnavailable
		}
		creds, err := c.connections.Assistant(ctx)
		if err != nil {
			return nil, err
		}

		return claude.New(claude.Options{
			APIKey:  creds.APIKey,
			BaseURL: creds.BaseURL,
			Model:   model,
		})

	case ProviderCursor:
		agent, err := c.cursorAgents.Open(ctx, model, agentID)
		if err != nil {
			return nil, err
		}

		return agent, nil

	default:
		return nil, ErrUnknownProvider
	}
}

func validateChatRequest(req ChatRequest) error {
	provider := strings.TrimSpace(req.Provider)
	switch provider {
	case ProviderAnthropic, ProviderCursor:
	default:
		return ErrUnknownProvider
	}
	if strings.TrimSpace(req.Model) == "" {
		return ErrInvalidChat
	}
	content := strings.TrimSpace(req.Message)
	if content == "" || utf8.RuneCountInString(content) > maxChatChars {
		return ErrInvalidChat
	}

	return nil
}

func chatMessagesToClaude(messages []ChatMessage) []claude.Message {
	out := make([]claude.Message, 0, len(messages))
	for _, message := range messages {
		out = append(out, claude.Message{
			Role: strings.TrimSpace(message.Role),
			Content: []claude.ContentBlock{{
				Type: "text",
				Text: Sanitize(strings.TrimSpace(message.Content)),
			}},
		})
	}

	return out
}

// databaseRows renders instances as the short JSON list Cursor is given with
// the question. It is a summary, not a tool result: list_databases has more.
func databaseRows(instances []engine.Instance) string {
	type row struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Engine    string `json:"engine"`
		Status    string `json:"status"`
		Profile   string `json:"profile"`
		Namespace string `json:"namespace"`
	}
	rows := make([]row, 0, len(instances))
	for _, inst := range instances {
		rows = append(rows, row{
			ID:        inst.ID,
			Name:      inst.Name,
			Engine:    inst.Engine,
			Status:    inst.Status.Phase,
			Profile:   inst.Profile,
			Namespace: inst.Namespace,
		})
	}
	body, _ := json.Marshal(rows)

	return string(body)
}

// extractJSON returns the outermost object in text. A model sometimes wraps
// the payload in a sentence, and the decoder needs the object alone.
func extractJSON(text string) string {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return text
	}

	return text[start : end+1]
}

// contentText is the text of the blocks that are text, in order.
func contentText(content []claude.ContentBlock) string {
	var text strings.Builder
	for _, block := range content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}

	return text.String()
}

// parseChatReply reads a reply written as JSON, which is how Cursor answers.
func parseChatReply(ctx context.Context, c *Chat, content []claude.ContentBlock) (*ChatReply, error) {
	raw := extractJSON(contentText(content))
	var payload chatPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return &ChatReply{
			Text: "The assistant could not produce a usable reply.",
			Actions: []ChatAction{{
				Label: defaultChatActionLabel(skills.ActionSupport),
				Kind:  skills.ActionSupport,
			}},
		}, nil
	}

	return finishReply(ctx, c, Sanitize(strings.TrimSpace(payload.Text)), payload.Actions), nil
}

// finishReply makes the reply for an answer's text, which is already redacted,
// and the buttons it asked for. An answer with no text is replaced by a
// sentence, and a support button when it asked for none that passed.
func finishReply(ctx context.Context, c *Chat, text string, actions []ChatAction) *ChatReply {
	out := &ChatReply{Text: text}
	textWasBlank := text == ""
	if textWasBlank {
		out.Text = "The assistant could not produce a usable reply."
	}
	out.Actions = filterActions(ctx, c, out.Text, actions)
	if textWasBlank && len(out.Actions) == 0 {
		out.Actions = []ChatAction{{
			Label: defaultChatActionLabel(skills.ActionSupport),
			Kind:  skills.ActionSupport,
		}}
	}

	return out
}

func filterActions(ctx context.Context, c *Chat, replyText string, actions []ChatAction) []ChatAction {
	var out []ChatAction
	for _, action := range actions {
		if validated, ok := validateChatAction(ctx, c, replyText, action); ok {
			out = append(out, validated)
		}
	}

	return out
}

func validateChatAction(ctx context.Context, c *Chat, replyText string, action ChatAction) (ChatAction, bool) {
	kind := strings.ToLower(strings.TrimSpace(action.Kind))
	switch kind {
	case skills.ActionMetrics, skills.ActionStatus, skills.ActionSupport:
	default:
		return ChatAction{}, false
	}

	label := strings.TrimSpace(action.Label)
	if label == "" || len(label) > maxActionLabel {
		label = defaultChatActionLabel(kind)
	}
	if len(label) > maxActionLabel {
		label = label[:maxActionLabel]
	}

	out := ChatAction{
		Label: label,
		Kind:  kind,
		ID:    strings.TrimSpace(action.ID),
	}

	if kind != skills.ActionSupport {
		if out.ID == "" {
			return ChatAction{}, false
		}
		if c.databases == nil {
			return ChatAction{}, false
		}
		if _, err := c.databases.Lookup(ctx, out.ID); err != nil {
			return ChatAction{}, false
		}
	}

	switch kind {
	case skills.ActionMetrics:
		metric := strings.ToLower(strings.TrimSpace(action.Metric))
		switch metric {
		case "cpu", "memory", "connections", "replication-lag", "disk":
			out.Metric = metric
		default:
			return ChatAction{}, false
		}
	case skills.ActionSupport:
		subject := strings.TrimSpace(action.Subject)
		description := strings.TrimSpace(action.Description)
		if subject == "" {
			subject = replyText
		}
		if description == "" {
			description = replyText
		}
		out.Subject = Sanitize(subject)
		out.Description = Sanitize(description)
	case skills.ActionStatus:
	}

	return out, true
}

func defaultChatActionLabel(kind string) string {
	switch kind {
	case skills.ActionMetrics:
		return "View metrics"
	case skills.ActionStatus:
		return "View status"
	default:
		return "Contact support"
	}
}

// groundingDatabases lists at most maxCursorGroundingDBs databases, so a
// hub with many of them does not fill Cursor's prompt before the question.
// Cursor asks list_databases for the rest when it needs them.
func (c *Chat) groundingDatabases(ctx context.Context) (out string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.FromContext(ctx).ErrorContext(ctx, "chat grounding panicked",
				slog.String("panic", log.RedactText(fmt.Sprint(recovered))))
			out = "databases error"
		}
	}()
	if c.databases == nil {
		return "[]"
	}
	instances, err := c.databases.ListEnvironment(ctx, "")
	if err != nil {
		return "databases error"
	}
	if len(instances) <= maxCursorGroundingDBs {
		return databaseRows(instances)
	}

	return fmt.Sprintf("%s (%d more, call list_databases for them)",
		databaseRows(instances[:maxCursorGroundingDBs]), len(instances)-maxCursorGroundingDBs)
}

// cursorGrounding hands Cursor the database list up front, since it cannot
// call tools natively. The list already carries each status,
// so nothing is read per database here: Cursor asks for metrics or history
// through the tool protocol when a question needs them.
func (c *Chat) cursorGrounding(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("Adapter-executed reads:\n")
	b.WriteString("databases: ")
	b.WriteString(Sanitize(c.groundingDatabases(ctx)))

	return b.String()
}

// chatRolePrompt is what only the Chat tells its model: the job, and how a
// button works. What the tools are and how to use them is the registry's
// instructions, which follow it.
const chatRolePrompt = `You help platform engineers operate MySQL and PostgreSQL databases on Kubernetes. Ask for every tool you need in one step rather than one at a time. To make a change, call the change tool for it: before anything happens the person is asked to approve it, with the tool and its exact arguments in front of them, and a call they reject or let expire comes back as a refusal. Say a change was made only when its result says it was accepted, and when a call is refused, say that nothing changed. Buttons only open something, they never change anything. A button has a label, a kind that is one of metrics, status, support, and the id of the database it opens, copied exactly as a tool returned it. For metrics, add metric.`

// anthropicAnswerFormat is how Anthropic writes the answer and the buttons.
const anthropicAnswerFormat = `Write the answer as plain text with no markdown, and do not wrap it in JSON. When a button would help, end the answer with one line of the form ` + actionsOpen + `[{"label":"View status","kind":"status","id":"environment/namespace.name"}]` + actionsClose + `, and write nothing after it. Leave the line out when no button helps.`

// chatSystemPrompt is the whole system prompt of a turn: the Chat's role, the
// registry's instructions for the Chat's tier, and how the provider answers.
// None of it depends on the question, the person or the round, so the model
// reads it from the cache.
func chatSystemPrompt(instructions, answerFormat string) string {
	parts := []string{chatRolePrompt}
	if instructions != "" {
		parts = append(parts, instructions)
	}

	return strings.Join(append(parts, answerFormat), "\n\n")
}

// chatCutOffSentence ends an answer the model ran out of room to finish.
const chatCutOffSentence = "The answer was cut off. Ask again to get the rest."

const chatFinalRoundPrompt = `Answer now with what the tools have returned. No more tools are available in this turn.`

const cursorToolProtocol = `Cursor Cloud Agents cannot call these tools. You have no MCP, no kubectl, and no network to the environment. The adapter runs every tool. To call tools, reply with JSON only of the form {"tool_calls":[{"id":"1","name":"list_databases","input":{}}]}. After tool_results, either emit more tool_calls or the final {"text":"...","actions":[...]}. A button looks like {"label":"View status","kind":"status","id":"environment/namespace.name"}. Never say the tools are unreachable. Never invent CPU, memory, or disk numbers. You cannot invoke tools yourself.`
