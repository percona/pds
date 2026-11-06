// Package claude is a thin client for the Anthropic Messages API. The API key
// is the customer's; it never leaves the adapter in a log or a response.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is Anthropic's public API.
	DefaultBaseURL = "https://api.anthropic.com"
	// DefaultModel is the model a call uses when the request does not name one.
	DefaultModel = "claude-sonnet-5-5"
	// DefaultEffort is how hard a model thinks before it answers. Low is the
	// level Anthropic suggests for chat.
	DefaultEffort = "low"
	// DefaultMaxTokens bounds one response. Thinking counts against it, so it
	// is far above the length of a reply.
	DefaultMaxTokens = 4096
	// FallbackBeta opts a request in to Anthropic's server-side refusal
	// fallback, which re-runs a declined request on a model Anthropic picks.
	FallbackBeta = "server-side-fallback-2026-07-01"
	// StopRefusal is the stop reason of a request the model declined.
	StopRefusal = "refusal"
	// RefusalSentence is what a refused turn answers with.
	RefusalSentence = "The assistant can't answer that question. Rephrase it, or ask about your databases and environments."
	// DefaultTimeout bounds one Complete call. The tool loop may call it a
	// few times, so this is longer than a ServiceNow ticket create.
	DefaultTimeout = 45 * time.Second
	// MessagesPath is the Messages API route.
	MessagesPath = "/v1/messages"
	// APIVersion is the anthropic-version header.
	APIVersion = "2023-06-01"
)

var (
	// ErrUnauthorized means the key was refused.
	ErrUnauthorized = errors.New("claude: unauthorized")
	// ErrRejected means Anthropic refused the request for another reason.
	ErrRejected = errors.New("claude: rejected")
	// ErrInvalidRequest means the request is one current models reject, so it
	// was not sent.
	ErrInvalidRequest = errors.New("claude: invalid request")
)

// cacheBreakpoint ends the prefix of tools and system prompt that a later
// request reads from the cache.
var cacheBreakpoint = map[string]string{"type": "ephemeral"}

// deprecatedModels are the models Anthropic has deprecated or retired.
var deprecatedModels = map[string]bool{
	"claude-sonnet-4-0":          true,
	"claude-sonnet-4-20250514":   true,
	"claude-opus-4-0":            true,
	"claude-opus-4-20250514":     true,
	"claude-opus-4-1":            true,
	"claude-opus-4-1-20250805":   true,
	"claude-3-haiku-20240307":    true,
	"claude-3-5-haiku-20241022":  true,
	"claude-3-7-sonnet-20250219": true,
	"claude-3-opus-20240229":     true,
}

// Deprecated reports whether Anthropic has deprecated or retired model.
func Deprecated(model string) bool {
	return deprecatedModels[strings.TrimSpace(model)]
}

// effortModelPrefixes name the model families that accept the effort setting.
// Older models answer it with an error.
var effortModelPrefixes = []string{
	"claude-sonnet-5", "claude-sonnet-4-6",
	"claude-opus-5", "claude-opus-4-5", "claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8",
	"claude-haiku-5", "claude-fable-", "claude-mythos-",
}

func acceptsEffort(model string) bool {
	for _, prefix := range effortModelPrefixes {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}

	return false
}

// fallbackModels are the models the server-side refusal fallback covers.
var fallbackModels = map[string]bool{
	"claude-sonnet-5-5": true,
	"claude-opus-5-5":   true,
	"claude-opus-5":     true,
	"claude-fable-5-1":  true,
	"claude-fable-5":    true,
}

const maxErrorBody = 8 << 10
const maxResponseBody = 2 << 20

// Options configures a Client.
type Options struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
	Timeout time.Duration
}

// Client talks to the Messages API.
type Client struct {
	baseURL *url.URL
	apiKey  string
	model   string
	http    *http.Client
}

// Completer is the seam tests use to inject a fake.
type Completer interface {
	Complete(ctx context.Context, req Request) (*Response, error)
}

// Request is one Messages call.
type Request struct {
	System    string
	Model     string
	Messages  []Message
	Tools     []Tool
	MaxTokens int
	// NoToolCalls keeps Tools defined but rules out calling one, so the
	// model has to answer. The definitions stay because earlier tool_use and
	// tool_result blocks in Messages are only valid alongside them.
	NoToolCalls bool
	// OnText, when set, makes the call a streaming one and hears each piece of
	// the answer's text as the model writes it, on the calling goroutine. The
	// Response is the same either way. A response that ends in a refusal has
	// been heard in part by then, so the caller decides what a person sees of it.
	OnText func(delta string) `json:"-"`
}

// Message is one turn.
type Message struct {
	Role    string
	Content []ContentBlock
}

// ContentBlock is text, a tool call, a tool result, or a block the model
// returned that only Anthropic reads, such as thinking.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	// Raw is the block as the API returned it. A block that has it goes back
	// to the API as is: a thinking block is only valid with its signature, and
	// a copy that dropped a field would be refused.
	Raw json.RawMessage `json:"-"`
}

// Tool is one function the model may call.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Response is the assistant turn.
type Response struct {
	StopReason string
	Content    []ContentBlock
	Usage      Usage
}

// Usage is the tokens a response billed. The cache fields show whether the
// prefix was written to the cache or read from it.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// New validates the options and returns a client.
func New(opts Options) (*Client, error) {
	apiKey := strings.TrimSpace(opts.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("claude: API key must not be blank")
	}

	raw := strings.TrimSpace(opts.BaseURL)
	if raw == "" {
		raw = DefaultBaseURL
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("claude: base URL %q is not a URL: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("claude: base URL %q needs an http or https scheme", raw)
	}

	httpClient := opts.HTTP
	if httpClient == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		httpClient = &http.Client{Timeout: timeout}
	}

	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = DefaultModel
	}

	parsed.Path = strings.TrimSuffix(parsed.Path, "/")

	return &Client{
		baseURL: parsed,
		apiKey:  apiKey,
		model:   model,
		http:    httpClient,
	}, nil
}

// Complete sends one Messages request. It must end on a user message, because
// current models reject a prefilled assistant turn.
func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	httpReq, err := c.newRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("claude: do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, statusError(resp)
	}

	var parsed message
	if req.OnText != nil {
		parsed, err = readStream(resp.Body, req.OnText)
	} else {
		parsed, err = readMessage(resp.Body)
	}
	if err != nil {
		return nil, err
	}

	return parsed.response()
}

// newRequest builds the HTTP request for req: the whole body is the same for a
// streaming call and a whole one, except for the stream flag.
func (c *Client) newRequest(ctx context.Context, req Request) (*http.Request, error) {
	if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == "assistant" {
		return nil, fmt.Errorf("%w: the last message is an assistant turn", ErrInvalidRequest)
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.model
	}

	payload := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages":   encodeMessages(req.Messages),
	}
	if req.OnText != nil {
		payload["stream"] = true
	}
	hasSystem := strings.TrimSpace(req.System) != ""
	if hasSystem {
		// The system prompt is a block so it can carry the breakpoint that
		// ends the prefix. Tools render before it, so one breakpoint covers
		// both.
		payload["system"] = []map[string]any{{
			"type":          "text",
			"text":          req.System,
			"cache_control": cacheBreakpoint,
		}}
	}
	if acceptsEffort(model) {
		payload["output_config"] = map[string]any{"effort": DefaultEffort}
	}
	if fallbackModels[model] {
		payload["fallbacks"] = "default"
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			item := map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
			}
			if len(tool.InputSchema) > 0 {
				var schema any
				if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
					return nil, fmt.Errorf("claude: tool %s schema: %w", tool.Name, err)
				}
				item["input_schema"] = schema
			}
			tools = append(tools, item)
		}
		if !hasSystem {
			tools[len(tools)-1]["cache_control"] = cacheBreakpoint
		}
		payload["tools"] = tools
		if req.NoToolCalls {
			payload["tool_choice"] = map[string]any{"type": "none"}
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("claude: encode request: %w", err)
	}

	endpoint := *c.baseURL
	endpoint.Path = strings.TrimSuffix(c.baseURL.Path, "/") + MessagesPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("claude: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", APIVersion)
	if fallbackModels[model] {
		httpReq.Header.Set("anthropic-beta", FallbackBeta)
	}

	return httpReq, nil
}

// statusError maps a refused request to the package's errors.
func statusError(resp *http.Response) error {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return fmt.Errorf("claude: read body: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w: %s", ErrUnauthorized, truncate(raw))
	}

	return fmt.Errorf("%w: status %d: %s", ErrRejected, resp.StatusCode, truncate(raw))
}

// message is a Messages response as it arrives, before refusals are handled.
type message struct {
	StopReason string            `json:"stop_reason"`
	Content    []json.RawMessage `json:"content"`
	Usage      Usage             `json:"usage"`
}

func readMessage(body io.Reader) (message, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxResponseBody+1))
	if err != nil {
		return message{}, fmt.Errorf("claude: read body: %w", err)
	}
	if len(raw) > maxResponseBody {
		return message{}, fmt.Errorf("claude: response too large")
	}

	var parsed message
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return message{}, fmt.Errorf("claude: decode response: %w", err)
	}

	return parsed, nil
}

func (m message) response() (*Response, error) {
	if m.StopReason == StopRefusal {
		// A refusal answers with a sentence for the customer. Whatever the model
		// wrote before it declined, and why it declined, stays out of the reply.
		return &Response{
			StopReason: StopRefusal,
			Content:    []ContentBlock{{Type: "text", Text: RefusalSentence}},
			Usage:      m.Usage,
		}, nil
	}

	content := make([]ContentBlock, 0, len(m.Content))
	for _, item := range m.Content {
		var block ContentBlock
		if err := json.Unmarshal(item, &block); err != nil {
			return nil, fmt.Errorf("claude: decode content block: %w", err)
		}
		block.Raw = item
		content = append(content, block)
	}

	return &Response{StopReason: m.StopReason, Content: content, Usage: m.Usage}, nil
}

func encodeMessages(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		out = append(out, map[string]any{
			"role":    message.Role,
			"content": encodeContent(message.Content),
		})
	}

	return out
}

func encodeContent(blocks []ContentBlock) []any {
	out := make([]any, 0, len(blocks))
	for _, block := range blocks {
		if len(block.Raw) > 0 {
			out = append(out, block.Raw)

			continue
		}
		item := map[string]any{"type": block.Type}
		switch block.Type {
		case "text":
			item["text"] = block.Text
		case "tool_use":
			item["id"] = block.ID
			item["name"] = block.Name
			if len(block.Input) > 0 {
				var input any
				_ = json.Unmarshal(block.Input, &input)
				item["input"] = input
			}
		case "tool_result":
			item["tool_use_id"] = block.ToolUseID
			item["content"] = block.Content
			if block.IsError {
				item["is_error"] = true
			}
		}
		out = append(out, item)
	}

	return out
}

func truncate(raw []byte) string {
	if len(raw) > maxErrorBody {
		raw = raw[:maxErrorBody]
	}

	return strings.TrimSpace(string(raw))
}
