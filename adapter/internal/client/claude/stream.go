package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// maxStreamLine bounds one server-sent event. A tool call's input arrives in
// pieces far smaller than this.
const maxStreamLine = 1 << 20

// streamBlock is a content block being assembled from its deltas.
type streamBlock struct {
	fields    map[string]any
	text      strings.Builder
	thinking  strings.Builder
	signature string
	input     strings.Builder
	citations []json.RawMessage
}

// finish returns the block as the API would have returned it whole.
func (b *streamBlock) finish() (json.RawMessage, error) {
	switch b.fields["type"] {
	case "text":
		b.fields["text"] = b.text.String()
		if len(b.citations) > 0 {
			b.fields["citations"] = b.citations
		}
	case "thinking":
		b.fields["thinking"] = b.thinking.String()
		if b.signature != "" {
			b.fields["signature"] = b.signature
		}
	case "tool_use", "server_tool_use":
		input := strings.TrimSpace(b.input.String())
		if input == "" || !json.Valid([]byte(input)) {
			// A call that has no arguments streams no input, and one cut off by
			// the token limit streams a fragment. Neither is worth sending back.
			input = "{}"
		}
		b.fields["input"] = json.RawMessage(input)
	}

	raw, err := json.Marshal(b.fields)
	if err != nil {
		return nil, fmt.Errorf("claude: encode content block: %w", err)
	}

	return raw, nil
}

type streamEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index"`
	Message      *message        `json:"message"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        struct {
		Type        string          `json:"type"`
		Text        string          `json:"text"`
		Thinking    string          `json:"thinking"`
		Signature   string          `json:"signature"`
		PartialJSON string          `json:"partial_json"`
		Citation    json.RawMessage `json:"citation"`
		StopReason  string          `json:"stop_reason"`
	} `json:"delta"`
	Usage Usage `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// readStream assembles a message from a server-sent event stream, calling
// onText with each piece of text as it arrives.
func readStream(body io.Reader, onText func(string)) (message, error) {
	limited := &io.LimitedReader{R: body, N: maxResponseBody + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 0, 64<<10), maxStreamLine)

	var (
		out      message
		blocks   = map[int]*streamBlock{}
		finished = map[int]json.RawMessage{}
		stopped  bool
	)

	for scanner.Scan() {
		data, ok := bytes.CutPrefix(scanner.Bytes(), []byte("data:"))
		if !ok {
			continue
		}
		var event streamEvent
		if err := json.Unmarshal(bytes.TrimSpace(data), &event); err != nil {
			return message{}, fmt.Errorf("claude: decode stream event: %w", err)
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				out.Usage = event.Message.Usage
			}
		case "content_block_start":
			block := &streamBlock{fields: map[string]any{}}
			if err := json.Unmarshal(event.ContentBlock, &block.fields); err != nil {
				return message{}, fmt.Errorf("claude: decode content block: %w", err)
			}
			blocks[event.Index] = block
		case "content_block_delta":
			block := blocks[event.Index]
			if block == nil {
				return message{}, fmt.Errorf("claude: stream sent a delta for block %d before it started", event.Index)
			}
			switch event.Delta.Type {
			case "text_delta":
				block.text.WriteString(event.Delta.Text)
				if event.Delta.Text != "" {
					onText(event.Delta.Text)
				}
			case "thinking_delta":
				block.thinking.WriteString(event.Delta.Thinking)
			case "signature_delta":
				block.signature = event.Delta.Signature
			case "input_json_delta":
				block.input.WriteString(event.Delta.PartialJSON)
			case "citations_delta":
				block.citations = append(block.citations, event.Delta.Citation)
			}
		case "content_block_stop":
			block := blocks[event.Index]
			if block == nil {
				continue
			}
			raw, err := block.finish()
			if err != nil {
				return message{}, err
			}
			finished[event.Index] = raw
			delete(blocks, event.Index)
		case "message_delta":
			out.StopReason = event.Delta.StopReason
			out.Usage.mergeFinal(event.Usage)
		case "message_stop":
			stopped = true
		case "error":
			return message{}, fmt.Errorf("%w: stream error %s: %s", ErrRejected, event.Error.Type, event.Error.Message)
		}
	}
	if err := scanner.Err(); err != nil {
		return message{}, fmt.Errorf("claude: read stream: %w", err)
	}
	if limited.N <= 0 {
		return message{}, fmt.Errorf("claude: response too large")
	}
	if !stopped {
		return message{}, fmt.Errorf("claude: stream ended before the message was complete")
	}

	out.Content = orderedBlocks(finished)

	return out, nil
}

// mergeFinal takes the counts message_delta carries, which are totals for the
// whole response, over the ones message_start opened with.
func (u *Usage) mergeFinal(final Usage) {
	if final.InputTokens > 0 {
		u.InputTokens = final.InputTokens
	}
	if final.OutputTokens > 0 {
		u.OutputTokens = final.OutputTokens
	}
	if final.CacheCreationInputTokens > 0 {
		u.CacheCreationInputTokens = final.CacheCreationInputTokens
	}
	if final.CacheReadInputTokens > 0 {
		u.CacheReadInputTokens = final.CacheReadInputTokens
	}
}

// orderedBlocks returns the finished blocks in index order. When the response
// fell back to another model part way through, the blocks written before the
// marker are dropped except for their text: the fallback model cannot read
// another model's thinking, and a call it never saw has no place in the turn.
func orderedBlocks(finished map[int]json.RawMessage) []json.RawMessage {
	indexes := slices.Sorted(maps.Keys(finished))
	marker := -1
	for _, i := range indexes {
		if blockType(finished[i]) == "fallback" {
			marker = i
		}
	}

	var out []json.RawMessage
	for _, i := range indexes {
		switch kind := blockType(finished[i]); {
		case kind == "fallback":
		case i < marker && kind != "text":
		default:
			out = append(out, finished[i])
		}
	}

	return out
}

func blockType(raw json.RawMessage) string {
	var head struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &head)

	return head.Type
}
