package service

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

const (
	// actionsOpen starts the structured part of an answer. Everything after it
	// is the buttons, a JSON array, and none of it is shown as text.
	actionsOpen  = "<actions>"
	actionsClose = "</actions>"
	pemBegin     = "-----BEGIN"
)

// riskyWords are the words that can start a credential rule in
// redactCredentials. A line that has one is held until it is whole, because a
// rule such as a password field reaches to the end of its line.
var riskyWords = []string{
	"://", "-----begin", "bearer", "password", "passwd", "pwd", "secret", "token",
	"apikey", "api_key", "api-key", "credential", "authorization",
}

// flagStart is the start of the mysql client's password flag, which the
// redaction reads on any line that also names mysql, wherever on the line.
var flagStart = regexp.MustCompile(`(^|[ \t])-p`)

// replyStream turns the text of an answer, as the model writes it, into the
// text a person is shown: the structured part is cut off, and a credential is
// redacted before any of it is let go. The text it released is the answer.
//
// It holds text back until it is safe to release: a marker that may be starting,
// the rest of a line that could carry a credential, and the last, unfinished
// word. Text is released at word and line boundaries, so what reaches the
// person lags the model by a word, or by a line when the line is risky.
type replyStream struct {
	emit    func(string)
	visible strings.Builder
	// mysqlLine is whether the line being written has named the mysql client,
	// which makes a password flag later on that line a credential.
	mysqlLine bool

	// Per model call.
	held     string
	trailer  strings.Builder
	inActs   bool
	received bool
	started  bool
}

func newReplyStream(emit func(string)) *replyStream {
	return &replyStream{emit: emit}
}

// beginRound starts a model call. Each call's structured part stands alone.
func (r *replyStream) beginRound() {
	r.held = ""
	r.trailer.Reset()
	r.inActs = false
	r.received = false
	r.started = false
}

// write takes the next piece of the model's text and releases what is safe.
func (r *replyStream) write(delta string) {
	r.take(delta)
	if !r.inActs {
		r.release(r.held[:len(r.held)-markerPrefixLen(r.held)])
	}
}

// take adds a piece of text to what is held, or to the structured part once
// the marker has come.
func (r *replyStream) take(delta string) {
	if delta == "" {
		return
	}
	r.received = true
	if r.inActs {
		r.trailer.WriteString(delta)

		return
	}

	r.held += delta
	if i := strings.Index(r.held, actionsOpen); i >= 0 {
		r.trailer.WriteString(r.held[i+len(actionsOpen):])
		r.held = r.held[:i]
		r.inActs = true
		r.flush()
	}
}

// endRound finishes a model call. A client that did not stream hands the whole
// text here, which goes out in one piece.
func (r *replyStream) endRound(whole string) {
	if !r.received {
		r.take(whole)
	}
	r.flush()
}

// text is what has been released so far.
func (r *replyStream) text() string {
	return r.visible.String()
}

// actions is the structured part of the last call, or none.
func (r *replyStream) actions() string {
	raw := r.trailer.String()
	if i := strings.Index(raw, actionsClose); i >= 0 {
		raw = raw[:i]
	}

	return strings.TrimSpace(raw)
}

// markerPrefixLen is how many bytes at the end of text could be the start of
// the marker, and so cannot be released yet.
func markerPrefixLen(text string) int {
	for n := min(len(actionsOpen)-1, len(text)); n > 0; n-- {
		if strings.HasSuffix(text, actionsOpen[:n]) {
			return n
		}
	}

	return 0
}

// release lets go of as much of candidate, the front of what is held, as is
// safe.
func (r *replyStream) release(candidate string) {
	cut := safePrefix(candidate)
	chunk := strings.TrimRight(candidate[:cut], " \t\r\n")
	if chunk == "" {
		return
	}
	r.held = r.held[len(chunk):]
	r.put(r.redact(chunk))
}

// redact redacts a piece of text that may continue a line already released.
func (r *replyStream) redact(chunk string) string {
	var out string
	if r.mysqlLine {
		// The line named mysql in text that has already gone, so the chunk is
		// read as if it followed that word.
		out = strings.TrimPrefix(redactCredentials("mysql "+chunk), "mysql ")
	} else {
		out = redactCredentials(chunk)
	}
	last := chunk
	if i := strings.LastIndex(chunk, "\n"); i >= 0 {
		r.mysqlLine = false
		last = chunk[i+1:]
	}
	if mysqlLine.MatchString(last) {
		r.mysqlLine = true
	}

	return out
}

// flush lets go of everything held. The call is over, so the last word is
// whole and a marker that never completed is text.
func (r *replyStream) flush() {
	chunk := strings.TrimRight(r.held, " \t\r\n")
	r.held = ""
	if i := openKey(chunk); i >= 0 {
		// A key that never ends cannot be redacted as a block, so none of it goes.
		chunk = chunk[:i] + redacted
	}
	if chunk != "" {
		r.put(r.redact(chunk))
	}
}

// note adds a sentence of ours to the answer, as a paragraph of its own.
func (r *replyStream) note(sentence string) {
	out := sentence
	if r.visible.Len() > 0 {
		out = "\n\n" + sentence
	}
	r.started = true
	r.visible.WriteString(out)
	if r.emit != nil {
		r.emit(out)
	}
}

// openKey is where the first private key that has not ended begins in text, or
// -1. A key counts as ended only when its whole closing line is there, because
// that is what redaction reads.
func openKey(text string) int {
	closed := pemBlock.FindAllStringIndex(text, -1)
	for from := 0; ; {
		i := strings.Index(text[from:], pemBegin)
		if i < 0 {
			return -1
		}
		i += from
		inside := false
		for _, block := range closed {
			if i >= block[0] && i < block[1] {
				inside = true
				from = block[1]

				break
			}
		}
		if !inside {
			return i
		}
	}
}

// put shows text. The first text of a call starts a new paragraph when an
// earlier call already wrote some.
func (r *replyStream) put(out string) {
	if !r.started {
		out = strings.TrimLeft(out, " \t\r\n")
		if out == "" {
			return
		}
		if r.visible.Len() > 0 {
			out = "\n\n" + out
		}
		r.started = true
	}
	r.visible.WriteString(out)
	if r.emit != nil {
		r.emit(out)
	}
}

// safePrefix is how much of text can be released: every finished line, and the
// finished words of the last one unless it could carry a credential. A key
// that has begun holds everything from its line until it ends, and a key that
// has ended goes out whole, never cut part way through.
func safePrefix(text string) int {
	if i := openKey(text); i >= 0 {
		return strings.LastIndex(text[:i], "\n") + 1
	}

	cut := 0
	lines := strings.LastIndex(text, "\n") + 1
	tail := text[lines:]
	switch i := strings.LastIndexAny(tail, " \t"); {
	case riskyLine(tail):
		cut = lines
	case i >= 0:
		cut = lines + i
	default:
		cut = lines
	}
	for _, block := range pemBlock.FindAllStringIndex(text, -1) {
		if block[0] < cut && cut < block[1] {
			return block[1]
		}
	}

	return cut
}

func riskyLine(line string) bool {
	lower := strings.ToLower(line)
	for _, word := range riskyWords {
		if strings.Contains(lower, word) {
			return true
		}
	}

	return flagStart.MatchString(line)
}

// parseActions reads the structured part of an answer. One that does not parse
// is no buttons, and never an error the person has to read.
func parseActions(raw string) []ChatAction {
	var actions []ChatAction
	if err := json.Unmarshal([]byte(raw), &actions); err != nil {
		return nil
	}

	return actions
}

// reply is the reply an answer streamed: its text as shown, with the
// buttons that passed the same checks as always.
func (r *replyStream) reply(ctx context.Context, c *Chat) *ChatReply {
	return finishReply(ctx, c, r.text(), parseActions(r.actions()))
}
