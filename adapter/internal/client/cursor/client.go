package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/log"
)

const (
	agentName      = "pds-chat"
	agentsPath     = "/v1/agents"
	pollInterval   = 500 * time.Millisecond
	fallbackReply  = `{"text":"The assistant could not produce a usable reply.","actions":[{"label":"Contact support","kind":"support"}]}`
	followUpSuffix = `These are adapter tool results. You cannot invoke tools yourself. Reply with more {"tool_calls":[...]} or the final {"text":"...","actions":[...]} JSON only.`
	finalSuffix    = `These are adapter tool results. No more tools are available in this turn. Reply with the final {"text":"...","actions":[...]} JSON only.`
	questionSuffix = `You cannot invoke tools yourself. Reply with {"tool_calls":[...]} to read more data, or the final {"text":"...","actions":[...]} JSON only.`
	toolCallHint   = `To call a tool, reply with {"tool_calls":[{"id":"1","name":"TOOL","input":{}}]} as the entire message. You cannot invoke tools yourself.`
)

// errAgentRefused marks a refusal that belongs to the agent rather than to the
// key or to Cursor: it is gone, archived, still busy or does not accept the
// run. A new agent can take over from it.
var errAgentRefused = errors.New("cursor: agent refused the run")

// Client drives one Chat POST through Cursor Cloud Agents. It runs on one
// agent: the one it was given, or the one its first call created.
type Client struct {
	baseURL         *url.URL
	apiKey          string
	model           string
	http            *http.Client
	sleep           func(time.Duration)
	completeTimeout time.Duration
	agentID         string
}

// New validates the options and returns a client.
func New(opts Options) (*Client, error) {
	apiKey := strings.TrimSpace(opts.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("cursor: API key must not be blank")
	}

	raw := strings.TrimSpace(opts.BaseURL)
	if raw == "" {
		raw = DefaultBaseURL
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("cursor: base URL %q is not a URL: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("cursor: base URL %q needs an http or https scheme", raw)
	}

	completeTimeout := opts.Timeout
	if completeTimeout <= 0 {
		completeTimeout = CompleteTimeout
	}

	httpClient := opts.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: completeTimeout}
	}

	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	parsed.Path = strings.TrimSuffix(parsed.Path, "/")

	return &Client{
		baseURL:         parsed,
		apiKey:          apiKey,
		model:           strings.TrimSpace(opts.Model),
		http:            httpClient,
		sleep:           sleep,
		completeTimeout: completeTimeout,
		agentID:         strings.TrimSpace(opts.AgentID),
	}, nil
}

// AgentID is the agent the client's runs go to: the one it was given, the one
// it created, or the one that replaced an agent that refused a run. Empty
// until a call created one.
func (c *Client) AgentID() string {
	return c.agentID
}

// Complete sends one agent create or follow-up run and returns the assistant turn.
func (c *Client) Complete(ctx context.Context, req claude.Request) (*claude.Response, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.completeTimeout)
		defer cancel()
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.model
	}
	if model == "" {
		return nil, fmt.Errorf("cursor: model must not be blank")
	}

	var runID string
	var err error
	if c.agentID == "" {
		c.agentID, runID, err = c.createAgent(ctx, req, model)
	} else {
		runID, err = c.createRun(ctx, req)
		if errors.Is(err, errAgentRefused) {
			runID, err = c.replaceAgent(ctx, req, model)
		}
	}
	if err != nil {
		return nil, err
	}

	runID, err = c.waitForRun(ctx, c.agentID, runID)
	if err != nil {
		return nil, err
	}

	result, err := c.fetchRunResult(ctx, c.agentID, runID)
	if err != nil {
		return nil, err
	}

	return parseAgentResult(result), nil
}

// replaceAgent starts a new agent from the whole request, because the one the
// client holds refused the run, and drops the old one so it does not keep
// holding the conversation. Deleting it is best effort: the answer matters more.
func (c *Client) replaceAgent(ctx context.Context, req claude.Request, model string) (string, error) {
	previous := c.agentID
	agentID, runID, err := c.createAgent(ctx, req, model)
	if err != nil {
		return "", err
	}
	c.agentID = agentID
	if err := c.DeleteAgent(ctx, previous); err != nil {
		log.FromContext(ctx).WarnContext(ctx, "a Cursor agent that refused a run was not deleted",
			slog.String("agent", previous),
			slog.String("cause", log.RedactText(err.Error())))
	}

	return runID, nil
}

// DeleteAgent deletes an agent and the conversation it holds. An agent that is
// already gone is not an error.
func (c *Client) DeleteAgent(ctx context.Context, agentID string) error {
	endpoint := c.endpoint(agentsPath + "/" + url.PathEscape(agentID))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("cursor: build delete agent request: %w", err)
	}
	c.setHeaders(httpReq)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("cursor: delete agent: %w", err)
	}
	defer resp.Body.Close()

	raw, err := readBody(resp)
	if err != nil {
		return err
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrUnauthorized, truncate(raw))
	case resp.StatusCode >= 400:
		return fmt.Errorf("%w: status %d: %s", ErrRejected, resp.StatusCode, truncate(raw))
	}

	return nil
}

func (c *Client) createAgent(ctx context.Context, req claude.Request, model string) (string, string, error) {
	payload := map[string]any{
		"model": map[string]string{"id": model},
		"name":  agentName,
		"prompt": map[string]string{
			"text": buildCreatePrompt(req),
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", fmt.Errorf("cursor: encode create request: %w", err)
	}

	endpoint := c.endpoint(agentsPath)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("cursor: build create request: %w", err)
	}
	c.setHeaders(httpReq)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", "", fmt.Errorf("cursor: create agent: %w", err)
	}
	defer resp.Body.Close()

	raw, err := readBody(resp)
	if err != nil {
		return "", "", err
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", "", fmt.Errorf("%w: %s", ErrUnauthorized, truncate(raw))
	}
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("%w: status %d: %s", ErrRejected, resp.StatusCode, truncate(raw))
	}

	var parsed struct {
		Agent struct {
			ID string `json:"id"`
		} `json:"agent"`
		Run struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"run"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", "", fmt.Errorf("cursor: decode create response: %w", err)
	}

	agentID := strings.TrimSpace(parsed.Agent.ID)
	if agentID == "" {
		return "", "", fmt.Errorf("cursor: create response missing agent id")
	}

	return agentID, strings.TrimSpace(parsed.Run.ID), nil
}

func (c *Client) createRun(ctx context.Context, req claude.Request) (string, error) {
	payload := map[string]any{
		"prompt": map[string]string{
			"text": buildFollowUpPrompt(req),
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("cursor: encode run request: %w", err)
	}

	path := agentsPath + "/" + url.PathEscape(c.agentID) + "/runs"
	endpoint := c.endpoint(path)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("cursor: build run request: %w", err)
	}
	c.setHeaders(httpReq)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("cursor: create run: %w", err)
	}
	defer resp.Body.Close()

	raw, err := readBody(resp)
	if err != nil {
		return "", err
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("%w: %s", ErrUnauthorized, truncate(raw))
	}
	if resp.StatusCode >= 400 {
		rejected := fmt.Errorf("%w: status %d: %s", ErrRejected, resp.StatusCode, truncate(raw))
		// A 4xx is the agent's answer. A 5xx, or a request limit, says nothing
		// about the agent, and another one would meet the same.
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return "", fmt.Errorf("%w: %w", errAgentRefused, rejected)
		}

		return "", rejected
	}

	var parsed struct {
		Run struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"run"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("cursor: decode run response: %w", err)
	}

	return strings.TrimSpace(parsed.Run.ID), nil
}

func (c *Client) waitForRun(ctx context.Context, agentID, hintRunID string) (string, error) {
	runID := hintRunID
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		if runID == "" {
			agent, err := c.getAgent(ctx, agentID)
			if err != nil {
				return "", err
			}
			runID = agent.LatestRunID
			if runID == "" {
				c.sleep(pollInterval)
				continue
			}
		}

		run, err := c.getRun(ctx, agentID, runID)
		if err != nil {
			return "", err
		}

		switch run.Status {
		case "FINISHED":
			return runID, nil
		case "ERROR", "CANCELLED", "EXPIRED":
			return "", fmt.Errorf("%w: run status %s", ErrRejected, run.Status)
		default:
			runID = ""
			c.sleep(pollInterval)
		}
	}
}

func (c *Client) getAgent(ctx context.Context, agentID string) (*agentStatus, error) {
	path := agentsPath + "/" + url.PathEscape(agentID)
	endpoint := c.endpoint(path)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("cursor: build get agent request: %w", err)
	}
	c.setHeaders(httpReq)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("cursor: get agent: %w", err)
	}
	defer resp.Body.Close()

	raw, err := readBody(resp)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, truncate(raw))
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w: status %d: %s", ErrRejected, resp.StatusCode, truncate(raw))
	}

	var parsed agentStatus
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("cursor: decode agent response: %w", err)
	}

	return &parsed, nil
}

func (c *Client) getRun(ctx context.Context, agentID, runID string) (*runStatus, error) {
	path := agentsPath + "/" + url.PathEscape(agentID) + "/runs/" + url.PathEscape(runID)
	endpoint := c.endpoint(path)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("cursor: build get run request: %w", err)
	}
	c.setHeaders(httpReq)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("cursor: get run: %w", err)
	}
	defer resp.Body.Close()

	raw, err := readBody(resp)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, truncate(raw))
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w: status %d: %s", ErrRejected, resp.StatusCode, truncate(raw))
	}

	var parsed runStatus
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("cursor: decode run response: %w", err)
	}

	return &parsed, nil
}

func (c *Client) fetchRunResult(ctx context.Context, agentID, runID string) (string, error) {
	run, err := c.getRun(ctx, agentID, runID)
	if err != nil {
		return "", err
	}

	return run.Result, nil
}

type agentStatus struct {
	ID          string `json:"id"`
	LatestRunID string `json:"latestRunId"`
	Status      string `json:"status"`
}

type runStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Result string `json:"result"`
}

func (c *Client) endpoint(path string) string {
	u := *c.baseURL
	u.Path = strings.TrimSuffix(c.baseURL.Path, "/") + path
	return u.String()
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.apiKey, "")
}

func readBody(resp *http.Response) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("cursor: read body: %w", err)
	}
	if len(raw) > maxResponseBody {
		return nil, fmt.Errorf("cursor: response too large")
	}

	return raw, nil
}

func buildCreatePrompt(req claude.Request) string {
	var b strings.Builder

	if system := strings.TrimSpace(req.System); system != "" {
		b.WriteString(system)
		b.WriteString("\n\n")
	}

	turn := 1
	for _, message := range req.Messages {
		text := messageText(message)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "%d. %s: %s\n", turn, titleCase(message.Role), text)
		turn++
	}

	if len(req.Tools) > 0 {
		b.WriteString("\nTools:\n")
		for _, tool := range req.Tools {
			schema := strings.TrimSpace(string(tool.InputSchema))
			if schema == "" {
				schema = "{}"
			}
			fmt.Fprintf(&b, "- %s: %s. Schema: %s\n", tool.Name, tool.Description, schema)
		}
		b.WriteString("\n")
		if req.NoToolCalls {
			b.WriteString(finalSuffix)
		} else {
			b.WriteString(toolCallHint)
		}
		b.WriteString("\n")
	}

	b.WriteString("\nRespond with JSON only.")
	return b.String()
}

// buildFollowUpPrompt is what a run on an agent that already holds the thread
// is sent: the results of the tools it asked for, or, when the turn has just
// begun, only the new question.
func buildFollowUpPrompt(req claude.Request) string {
	var lastUser *claude.Message
	for i := range req.Messages {
		if strings.EqualFold(strings.TrimSpace(req.Messages[i].Role), "user") {
			lastUser = &req.Messages[i]
		}
	}
	if lastUser == nil {
		return ""
	}

	results := make([]map[string]any, 0)
	for _, block := range lastUser.Content {
		if block.Type != "tool_result" {
			continue
		}
		results = append(results, map[string]any{
			"id":       block.ToolUseID,
			"content":  block.Content,
			"is_error": block.IsError,
		})
	}
	if len(results) == 0 {
		return messageText(*lastUser) + "\n" + questionSuffix
	}

	body, _ := json.Marshal(map[string]any{"tool_results": results})
	if req.NoToolCalls {
		return string(body) + "\n" + finalSuffix
	}

	return string(body) + "\n" + followUpSuffix
}

// messageText is a message as one line of prompt: its text, and the tools it
// called or the results they returned, so an agent started part way through a
// turn still knows what was read.
func messageText(message claude.Message) string {
	var parts []string
	for _, block := range message.Content {
		switch block.Type {
		case "text":
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
			}
		case "tool_use":
			parts = append(parts, fmt.Sprintf("tool_call %s %s %s", block.ID, block.Name, strings.TrimSpace(string(block.Input))))
		case "tool_result":
			parts = append(parts, fmt.Sprintf("tool_result %s: %s", block.ToolUseID, block.Content))
		}
	}

	return strings.Join(parts, " ")
}

func titleCase(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant":
		return "Assistant"
	default:
		return "User"
	}
}

func parseAgentResult(raw string) *claude.Response {
	cleaned := stripMarkdownFences(strings.TrimSpace(raw))
	if cleaned == "" {
		return fallbackResponse()
	}

	var toolCalls struct {
		ToolCalls []struct {
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(cleaned), &toolCalls); err == nil && len(toolCalls.ToolCalls) > 0 {
		blocks := make([]claude.ContentBlock, 0, len(toolCalls.ToolCalls))
		for _, call := range toolCalls.ToolCalls {
			blocks = append(blocks, claude.ContentBlock{
				Type:  "tool_use",
				ID:    call.ID,
				Name:  call.Name,
				Input: call.Input,
			})
		}

		return &claude.Response{StopReason: "tool_use", Content: blocks}
	}

	var finished struct {
		Text json.RawMessage `json:"text"`
	}
	if err := json.Unmarshal([]byte(cleaned), &finished); err == nil && finished.Text != nil {
		return &claude.Response{
			StopReason: "end_turn",
			Content:    []claude.ContentBlock{{Type: "text", Text: cleaned}},
		}
	}

	return fallbackResponse()
}

func fallbackResponse() *claude.Response {
	return &claude.Response{
		StopReason: "end_turn",
		Content:    []claude.ContentBlock{{Type: "text", Text: fallbackReply}},
	}
}

func stripMarkdownFences(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "```") {
		return raw
	}

	lines := strings.Split(raw, "\n")
	if len(lines) < 2 {
		return raw
	}

	start := 1
	end := len(lines)
	if strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		end = len(lines) - 1
	}

	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}
