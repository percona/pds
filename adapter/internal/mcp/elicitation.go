package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// confirmKey names the one question a call puts to the person. A call asks at
// most once.
const confirmKey = "confirm"

// confirmationWindow is how long an answer stays good. The server keeps nothing
// between the question and the answer, so the limit travels in the request
// state the client echoes back.
const confirmationWindow = 5 * time.Minute

const (
	msgDeleteDeclined      = "The user did not confirm the delete, so nothing was changed. Do not try again unless the user asks."
	msgOpenDeclined        = "The user did not confirm opening the database to the internet, so nothing was created. Do not try again unless the user asks."
	msgTypedNameMismatch   = "The name that was typed does not match the name of this database, so nothing was changed."
	msgConfirmationExpired = "The confirmation expired before it was answered, so nothing was changed. Call the tool again to ask the user again."
	msgNeedsAnswer         = "The user has to answer a question before this call can continue."
)

// plainText is what a name, an environment or a range may be made of for the
// tool to show it to a person. An agent chose these values, and a question the
// person reads must not carry anything an agent could dress up as its own words.
var plainText = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,100}$`)

// question is returned by a tool that needs the person's answer before it can
// go on. define turns it into an input required result, and the client asks.
type question struct {
	params *sdk.ElicitParams
	state  string
}

func (q *question) Error() string { return msgNeedsAnswer }

// canAsk reports whether the client declared form mode elicitation. An empty
// declaration means form.
func canAsk(caps *sdk.ClientCapabilities) bool {
	return caps != nil && caps.Elicitation != nil && (caps.Elicitation.Form != nil || caps.Elicitation.URL == nil)
}

// ask puts a question to the person, or returns their answer to it. The answer
// counts only when the request state says it was asked for this very call: the
// state holds a digest of what the person was shown, so an answer to one
// question cannot go with other arguments, and the time it was good until.
func (c *call) ask(params *sdk.ElicitParams, shown ...string) (*sdk.ElicitResult, error) {
	sum := sha256.Sum256([]byte(strings.Join(shown, "\x00")))
	digest := hex.EncodeToString(sum[:])
	now := c.registry.clock.Now()

	if answer, answered := c.answers[confirmKey].(*sdk.ElicitResult); answered {
		if until, bound := stateOf(c.state, digest); bound {
			if now.Unix() > until {
				return nil, &toolError{Kind: kindConfirmationRequired, Message: msgConfirmationExpired}
			}

			return answer, nil
		}
	}

	return nil, &question{params: params, state: strconv.FormatInt(now.Add(confirmationWindow).Unix(), 10) + "." + digest}
}

// stateOf reads the request state of an answer that was asked for the digest,
// and says until when it is good.
func stateOf(state, digest string) (int64, bool) {
	stamp, found := strings.CutSuffix(state, "."+digest)
	if !found {
		return 0, false
	}
	until, err := strconv.ParseInt(stamp, 10, 64)

	return until, err == nil
}

func form(message, field string, schema map[string]any) *sdk.ElicitParams {
	return &sdk.ElicitParams{
		Mode: "form", Message: message,
		RequestedSchema: map[string]any{
			"type": "object", "properties": map[string]any{field: schema}, "required": []string{field},
		},
	}
}

// deleteQuestion asks the person to type the name of the database to delete.
func deleteQuestion(stored database) *sdk.ElicitParams {
	where := ""
	if stored.Environment != "" {
		where = " on " + stored.Environment
	}

	return form(
		fmt.Sprintf("Delete the database %s%s? This cannot be undone. Type the database name to confirm.", stored.Name, where),
		"name", map[string]any{"type": "string", "title": "Database name", "description": "Type " + stored.Name + " to delete it."},
	)
}

// openQuestion asks the person whether any address on the internet may connect.
func openQuestion(in createDatabaseInput) *sdk.ElicitParams {
	return form(
		fmt.Sprintf("Create the database %s on %s and let any address on the internet connect to it? Source ranges: %s.",
			in.Name, in.Environment, strings.Join(in.SourceRanges, ", ")),
		"confirm", map[string]any{"type": "boolean", "title": "Any address on the internet may connect", "default": false},
	)
}

// saidYes reports whether the person accepted the question.
func saidYes(answer *sdk.ElicitResult) bool {
	return answer.Action == "accept"
}
