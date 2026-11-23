// Package mongoguard decides whether the data server may send one MongoDB command. For MySQL and
// PostgreSQL the database enforces a read only transaction, and that is the net under the SQL
// guard. MongoDB has no such transaction and the account the control plane hands out can write,
// so this guard is the only thing between a read tool and a write.
//
// It is deny by default. The command is parsed as strict JSON, never matched as text. Its first key
// is the command name, and the name, every top level field, every aggregation stage and every
// sub-pipeline have to be on a list. Server-side JavaScript is refused everywhere in the document.
// What is returned for the runner to send is the checked tree written out again as canonical JSON,
// so what runs is what was checked.
package mongoguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/percona/pds/apps/mcp/internal/messages"
)

// MaxLength is the longest command the data server sends, in bytes.
const MaxLength = 100_000

// maxDepth is how deeply documents may nest. BSON allows 100 levels.
const maxDepth = 100

// Mode is what the caller may do with the command.
type Mode int

// The modes: a read, or any command on the write list as well.
const (
	ReadOnly Mode = iota
	ReadWrite
)

// Kind says whether a command reads or changes data.
type Kind int

// The kinds.
const (
	Read Kind = iota
	Write
)

// Command is a command that passed. JSON is the checked document, to be sent as it is.
type Command struct {
	Name string
	Kind Kind
	JSON string
	// Database is where the command runs: the connection's database, except for listDatabases,
	// which the server answers only on admin.
	Database string
}

// Refusal is why a command was not sent. Its Reason is a sentence for an agent.
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return r.Reason }

func refuse(reason string) *Refusal { return &Refusal{Reason: reason} }

type member struct {
	key   string
	value any
}

// object is a JSON object that keeps its key order. Values are object, []any, string,
// json.Number, bool or nil.
type object []member

func (o object) get(key string) (any, bool) {
	for _, m := range o {
		if m.key == key {
			return m.value, true
		}
	}

	return nil, false
}

// Check parses one command and decides whether the mode may run it against database.
func Check(text, database string, mode Mode) (Command, error) {
	if len(text) > MaxLength {
		return Command{}, refuse(messages.MongoTooLong)
	}
	doc, err := parse(text)
	if err != nil {
		return Command{}, err
	}
	name := doc[0].key

	if jsCommands[name] || hasJavaScript(doc) {
		return Command{}, refuse(messages.MongoJavaScript)
	}
	if adminCommands[name] {
		return Command{}, refuse(messages.MongoAdmin)
	}
	sp, ok := commands[name]
	if !ok {
		return Command{}, refuse(messages.MongoUnknownCommand(name))
	}

	c := &checker{command: name, database: database}
	if r := c.fields(doc, sp); r != nil {
		return Command{}, r
	}
	kind := sp.kind
	if c.writes {
		kind = Write
	}
	if kind == Write && mode == ReadOnly {
		if c.writes {
			return Command{}, refuse(messages.MongoWriteStage)
		}

		return Command{}, refuse(messages.MongoNotRead)
	}

	run := database
	if sp.admin {
		run = "admin"
	}
	var out bytes.Buffer
	marshal(&out, doc)

	return Command{Name: name, Kind: kind, JSON: out.String(), Database: run}, nil
}

// parse reads strict JSON into an ordered tree. A repeated key is refused at any depth, because
// two readers can disagree about which one wins.
func parse(text string) (object, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()

	tok, err := dec.Token()
	switch {
	case errors.Is(err, io.EOF):
		return nil, refuse(messages.MongoEmpty)
	case err != nil:
		return nil, refuse(messages.MongoNotJSON)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, refuse(messages.MongoNotJSON)
	}
	doc, err := readObject(dec, 1)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, refuse(messages.MongoNotJSON)
	}
	if len(doc) == 0 {
		return nil, refuse(messages.MongoEmpty)
	}

	return doc, nil
}

// readObject reads the members of an object whose opening brace was consumed.
func readObject(dec *json.Decoder, depth int) (object, error) {
	if depth > maxDepth {
		return nil, refuse(messages.MongoTooDeep)
	}
	var out object
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok {
			return nil, refuse(messages.MongoNotJSON)
		}
		if seen[key] {
			return nil, refuse(messages.MongoDuplicateKey)
		}
		seen[key] = true
		value, err := readValue(dec, depth)
		if err != nil {
			return nil, err
		}
		out = append(out, member{key: key, value: value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, refuse(messages.MongoNotJSON)
	}

	return out, nil
}

func readValue(dec *json.Decoder, depth int) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, refuse(messages.MongoNotJSON)
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil
	}
	if delim == '{' {
		return readObject(dec, depth+1)
	}
	if depth+1 > maxDepth {
		return nil, refuse(messages.MongoTooDeep)
	}
	array := []any{}
	for dec.More() {
		value, err := readValue(dec, depth+1)
		if err != nil {
			return nil, err
		}
		array = append(array, value)
	}
	if _, err := dec.Token(); err != nil {
		return nil, refuse(messages.MongoNotJSON)
	}

	return array, nil
}

// marshal writes the tree as compact JSON. Numbers keep the text the agent wrote.
func marshal(b *bytes.Buffer, v any) {
	switch v := v.(type) {
	case object:
		b.WriteByte('{')
		for i, m := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, m.key)
			b.WriteByte(':')
			marshal(b, m.value)
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			marshal(b, e)
		}
		b.WriteByte(']')
	case string:
		writeString(b, v)
	case json.Number:
		b.WriteString(v.String())
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	default:
		b.WriteString("null")
	}
}

func writeString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b.Truncate(b.Len() - 1)
}

// javaScriptKeys run code on the database, or build a value that is code. They are compared in
// lower case, and after the JSON escapes in a key were decoded.
var javaScriptKeys = map[string]bool{"$where": true, "$function": true, "$accumulator": true, "$code": true, "$scope": true}

// isJavaScriptKey also catches the server's internal JavaScript operators, which an external
// client cannot use and which have no business in a command.
func isJavaScriptKey(key string) bool {
	key = strings.ToLower(key)

	return javaScriptKeys[key] || strings.HasPrefix(key, "$_internaljs")
}

// jsCommands are commands that run JavaScript.
var jsCommands = map[string]bool{"mapReduce": true, "eval": true, "$eval": true}

// hasJavaScript looks at every key at every depth, in objects inside arrays too.
func hasJavaScript(v any) bool {
	switch v := v.(type) {
	case object:
		for _, m := range v {
			if isJavaScriptKey(m.key) || hasJavaScript(m.value) {
				return true
			}
		}
	case []any:
		for _, e := range v {
			if hasJavaScript(e) {
				return true
			}
		}
	}

	return false
}

// adminCommands manage users, roles, databases, indexes or the server. The account the control
// plane hands out may run them, and an agent never should: they are named only to give a better
// sentence, because a command on no list is refused anyway.
var adminCommands = map[string]bool{
	"createUser": true, "updateUser": true, "dropUser": true, "dropAllUsersFromDatabase": true,
	"grantRolesToUser": true, "revokeRolesFromUser": true, "createRole": true, "updateRole": true, "dropRole": true,
	"dropAllRolesFromDatabase": true, "grantRolesToRole": true, "revokeRolesFromRole": true,
	"grantPrivilegesToRole": true, "revokePrivilegesFromRole": true, "usersInfo": true, "rolesInfo": true,
	"dropDatabase": true, "dropIndexes": true, "collMod": true, "renameCollection": true, "convertToCapped": true,
	"compact": true, "reIndex": true, "shutdown": true, "replSetReconfig": true, "replSetStepDown": true,
	"replSetInitiate": true, "killOp": true, "killAllSessions": true, "killSessions": true, "fsync": true,
	"fsyncUnlock": true, "setParameter": true, "getParameter": true, "setFeatureCompatibilityVersion": true,
	"profile": true, "currentOp": true, "serverStatus": true, "logRotate": true,
	"enableSharding": true, "shardCollection": true, "moveChunk": true, "applyOps": true,
}
