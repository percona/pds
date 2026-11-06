package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/claude"
)

// approvalWait is how long a person has to answer before the call is dropped.
const approvalWait = 15 * time.Minute

// maxApprovalValue bounds one argument as the card shows it. The call still
// carries the whole argument.
const maxApprovalValue = 4000

const approvalValueCut = " (shortened here, sent in full)"

// How an approval ended.
const (
	OutcomeApproved  = "approved"
	OutcomeRejected  = "rejected"
	OutcomeExpired   = "expired"
	OutcomeCancelled = "cancelled"
)

var (
	// ErrApprovalNotFound means there is no open approval of that id for this
	// person: it was answered, it expired, the stream closed, or it is someone
	// else's.
	ErrApprovalNotFound = errors.New("no such approval")
	// ErrApprovalName means a delete was approved without the database's name.
	ErrApprovalName = errors.New("the database name does not match")
)

const (
	chatToolRejected = "The person rejected this change. Nothing was changed. Say so, and do not ask for it again unless they ask."
	chatToolNoAsk    = "This change needs the person's approval, and there is no way to ask them in this request. Nothing was changed."
	chatToolBadInput = "The arguments are not valid: each name may appear once, spelled exactly. Nothing was changed."

	chatApprovalExpired   = "The approval expired before you answered, so nothing changed. Ask again if you still want it."
	chatApprovalCancelled = "The approval was cancelled because you left the chat, so nothing changed. Ask again if you still want it."
)

// ApprovalLine is one argument of a call as the card shows it.
type ApprovalLine struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ChatApproval is a change the model asked for, put to the person before it is
// made.
type ChatApproval struct {
	ID    string `json:"id"`
	Tool  string `json:"tool"`
	Title string `json:"title"`
	// Database is the database's name, when the call is about one.
	Database string `json:"database,omitempty"`
	// Destructive is a call that cannot be undone. Approving it takes the
	// database's name.
	Destructive bool           `json:"destructive"`
	Lines       []ApprovalLine `json:"lines"`
	ExpiresAt   time.Time      `json:"expiresAt"`
}

// ChatApprovalEnd is how an approval ended, for the stream to tell the card.
type ChatApprovalEnd struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"`
}

// ChatDecision is the person's answer. Name is what they typed, and only a
// destructive call looks at it.
type ChatDecision struct {
	Approve bool
	Name    string
}

// chatApprovals holds the approvals waiting for an answer. They live in this
// process, in the stream that asked, and nowhere else: nothing survives a
// restart, which is the same as the person never approving.
type chatApprovals struct {
	mu   sync.Mutex
	open map[string]*pendingApproval
}

type pendingApproval struct {
	organization uint
	user         uint
	subject      string
	// name is what a destructive call needs typed, or empty.
	name    string
	decided chan ChatDecision
}

func newChatApprovals() *chatApprovals {
	return &chatApprovals{open: map[string]*pendingApproval{}}
}

func (a *chatApprovals) add(id string, p *pendingApproval) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.open[id] = p
}

// take removes the approval and says whether it was still open. Whoever takes
// it owns its outcome, so an answer and an expiry that arrive together cannot
// both count.
func (a *chatApprovals) take(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.open[id]
	delete(a.open, id)

	return ok
}

// Decide answers an open approval. It only reaches the person the model was
// asking: anyone else, and any id that is not open, is ErrApprovalNotFound. A
// wrong name leaves a destructive approval open.
func (c *Chat) Decide(ctx context.Context, id string, decision ChatDecision) error {
	if c == nil {
		return ErrApprovalNotFound
	}
	scope, hasScope := auth.ScopeFromContext(ctx)
	subject, hasSubject := auth.SubjectFromContext(ctx)

	c.approvals.mu.Lock()
	defer c.approvals.mu.Unlock()
	pending, ok := c.approvals.open[id]
	if !ok || !hasScope || !hasSubject ||
		pending.organization != scope.OrganizationID || pending.user != scope.UserID || pending.subject != subject {
		return ErrApprovalNotFound
	}
	if decision.Approve && pending.name != "" && decision.Name != pending.name {
		return ErrApprovalName
	}
	delete(c.approvals.open, id)
	pending.decided <- decision

	return nil
}

// approver puts one change call to the person and says what to run.
type approver func(ctx context.Context, block claude.ContentBlock, tool changeTool) approval

// approval is what asking came to: the arguments to run, or why nothing runs.
type approval struct {
	input   json.RawMessage
	refusal string
	// ended is set when the turn should stop rather than go on: the approval
	// expired, or the person left.
	ended string
}

// changeTool is what the Chat knows of a tool above the read tier.
type changeTool struct {
	title       string
	destructive bool
}

// approverFor makes the approver of one turn. The approval waits on the
// person, the expiry, the stream the turn was asked on, and the turn itself,
// and the turn's time budget stands still while it does.
func (c *Chat) approverFor(ctx context.Context, req ChatRequest, budget *turnBudget, gone <-chan struct{}) approver {
	scope, _ := auth.ScopeFromContext(ctx)
	subject, _ := auth.SubjectFromContext(ctx)
	gate := make(chan struct{}, 1)
	var endedBy atomic.Value
	ended := func() string {
		outcome, _ := endedBy.Load().(string)

		return outcome
	}

	return func(ctx context.Context, block claude.ContentBlock, tool changeTool) approval {
		if req.Approval == nil {
			return approval{refusal: chatToolNoAsk}
		}
		var arguments map[string]any
		if len(block.Input) > 0 && string(block.Input) != "null" {
			if err := json.Unmarshal(block.Input, &arguments); err != nil {
				return approval{refusal: chatToolBadInput}
			}
		}
		if foldsKeys(arguments) {
			// The registry reads keys without regard to case, so a second spelling
			// of an argument could run a value the card does not show.
			return approval{refusal: chatToolBadInput}
		}
		card := ChatApproval{
			Tool: block.Name, Title: tool.title, Destructive: tool.destructive, Lines: approvalLines(arguments),
		}
		if card.Title == "" {
			card.Title = block.Name
		}
		typed := ""
		if id, ok := arguments["id"].(string); ok {
			card.Database = databaseNameOf(id)
		}
		if tool.destructive {
			if card.Database == "" {
				return approval{refusal: chatToolFailed}
			}
			typed = card.Database
		}

		// One card at a time, so the person decides in the order the model asked.
		select {
		case gate <- struct{}{}:
		case <-ctx.Done():
			return approval{refusal: chatToolStopped}
		case <-gone:
			endedBy.Store(OutcomeCancelled)

			return approval{refusal: chatApprovalCancelled, ended: OutcomeCancelled}
		}
		defer func() { <-gate }()
		if outcome := ended(); outcome != "" {
			refusal := chatApprovalCancelled
			if outcome == OutcomeExpired {
				refusal = chatApprovalExpired
			}

			return approval{refusal: refusal, ended: outcome}
		}

		id, err := newApprovalID()
		if err != nil {
			return approval{refusal: chatToolFailed}
		}
		card.ID = id
		card.ExpiresAt = c.now().Add(approvalWait)
		pending := &pendingApproval{
			organization: scope.OrganizationID, user: scope.UserID, subject: subject,
			name: typed, decided: make(chan ChatDecision, 1),
		}
		c.approvals.add(id, pending)

		expired := make(chan struct{})
		stopExpiry := c.timer(approvalWait, func() { close(expired) })
		defer stopExpiry()
		resume := budget.pause()
		defer resume()
		req.Approval(card)

		settle := func(outcome string) {
			if req.Settled != nil {
				req.Settled(ChatApprovalEnd{ID: id, Outcome: outcome})
			}
		}
		var decision ChatDecision
		var outcome string
		select {
		case decision = <-pending.decided:
		case <-expired:
			outcome = OutcomeExpired
		case <-gone:
			outcome = OutcomeCancelled
		case <-ctx.Done():
			outcome = OutcomeCancelled
		}
		if outcome != "" && !c.approvals.take(id) {
			// An answer took the approval first, and it is the person's word.
			decision, outcome = <-pending.decided, ""
		}

		switch {
		case outcome == OutcomeExpired:
			endedBy.Store(outcome)
			settle(outcome)

			return approval{refusal: chatApprovalExpired, ended: outcome}
		case outcome != "":
			endedBy.Store(outcome)
			settle(outcome)

			return approval{refusal: chatApprovalCancelled, ended: outcome}
		case !decision.Approve:
			settle(OutcomeRejected)

			return approval{refusal: chatToolRejected}
		}
		settle(OutcomeApproved)

		if !tool.destructive {
			return approval{input: block.Input}
		}
		// The name the person typed is the confirmation. Whatever the model
		// put in confirm is not.
		arguments["confirm"] = decision.Name
		input, err := json.Marshal(arguments)
		if err != nil {
			return approval{refusal: chatToolFailed}
		}

		return approval{input: input}
	}
}

// foldsKeys reports whether two keys differ only in case.
func foldsKeys(arguments map[string]any) bool {
	seen := make(map[string]bool, len(arguments))
	for key := range arguments {
		folded := strings.ToLower(key)
		if seen[folded] {
			return true
		}
		seen[folded] = true
	}

	return false
}

func newApprovalID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("make an approval id: %w", err)
	}

	return hex.EncodeToString(raw), nil
}

// databaseNameOf is the name in a database id, which is
// environment/namespace.name. A name has no dot.
func databaseNameOf(id string) string {
	_, name, found := strings.Cut(id[strings.LastIndex(id, "/")+1:], ".")
	if !found {
		return ""
	}

	return name
}

// argumentLabels says what each argument of the change tools is called to the
// person. The model's names are camelCase and not for them.
var argumentLabels = map[string]string{
	"id":                        "Database",
	"databaseId":                "Database",
	"backupId":                  "Backup",
	"number":                    "Ticket",
	"body":                      "Message",
	"cpu":                       "CPU cores",
	"memoryGb":                  "Memory (GB)",
	"storageGb":                 "Storage (GB)",
	"members":                   "Members",
	"enabled":                   "Backup schedule on",
	"period":                    "Runs",
	"minute":                    "Minute (UTC)",
	"hour":                      "Hour (UTC)",
	"weekday":                   "Weekday (0 is Sunday)",
	"retentionCopies":           "Backups to keep",
	"sourceRanges":              "Addresses that may connect",
	"acknowledgeOpenToInternet": "Open to the whole internet",
}

// argumentOrder lists the arguments that lead a card, in order. The rest
// follow by name.
var argumentOrder = []string{"id", "databaseId", "number", "backupId", "name", "environment", "engine", "profile"}

// approvalLines is a call's arguments in words, for the card. A credential in
// a value is replaced as it is everywhere else the Chat shows text. The model's
// own confirm is left out: a delete is confirmed by the name the person types.
func approvalLines(arguments map[string]any) []ApprovalLine {
	keys := make([]string, 0, len(arguments))
	for key := range arguments {
		if key != "confirm" {
			keys = append(keys, key)
		}
	}
	rank := func(key string) int {
		if i := slices.Index(argumentOrder, key); i >= 0 {
			return i
		}

		return len(argumentOrder)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if byRank := rank(a) - rank(b); byRank != 0 {
			return byRank
		}

		return strings.Compare(a, b)
	})

	lines := make([]ApprovalLine, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, ApprovalLine{Label: argumentLabel(key), Value: approvalValue(arguments[key])})
	}

	return lines
}

// argumentLabel is the label of an argument, or its name split into words.
func argumentLabel(key string) string {
	if label, ok := argumentLabels[key]; ok {
		return label
	}
	var words strings.Builder
	for i, r := range key {
		switch {
		case i == 0:
			words.WriteRune(unicode.ToUpper(r))
		case unicode.IsUpper(r):
			words.WriteByte(' ')
			words.WriteRune(unicode.ToLower(r))
		default:
			words.WriteRune(r)
		}
	}

	return words.String()
}

func approvalValue(value any) string {
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case bool:
		text = map[bool]string{true: "Yes", false: "No"}[v]
	case float64:
		text = strconv.FormatFloat(v, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, approvalValue(item))
		}
		text = strings.Join(parts, ", ")
	default:
		body, _ := json.Marshal(v)
		text = string(body)
	}
	text = redactCredentials(text)
	if utf8.RuneCountInString(text) > maxApprovalValue {
		text = string([]rune(text)[:maxApprovalValue]) + approvalValueCut
	}

	return text
}
