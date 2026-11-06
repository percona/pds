// Package sqlguard reads one SQL statement closely enough to refuse the ones the data server must
// not send. It is a second net: the read only transaction in the database is what stops a write,
// and the privileges of the database user are what bound everything else. The guard exists
// because some things a read only transaction allows are still unwelcome here, such as writing a
// file on the database host, and because a refusal before the network costs nothing.
package sqlguard

import (
	"strings"

	"github.com/percona/pds/apps/mcp/internal/messages"
)

// MaxLength is the longest statement the data server sends, in bytes.
const MaxLength = 100_000

// Dialect is the SQL the statement is written in. Quoting differs, and a guard that reads a
// comment where the database reads a statement separator can be walked around.
type Dialect int

// The dialects of the engines the data server speaks to.
const (
	MySQL Dialect = iota
	Postgres
)

// Mode is what the caller may do with the statement.
type Mode int

// The modes: a query, or any one statement that is not transaction control.
const (
	ReadOnly Mode = iota
	ReadWrite
)

// Kind says how a statement's answer is read.
type Kind int

// The kinds: a statement that answers with rows, and one that answers with a count.
const (
	ReturnsRows Kind = iota
	ReturnsCount
)

// Refusal is why a statement was not sent. Its Reason is a sentence for an agent.
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return r.Reason }

func refuse(reason string) (Kind, error) { return ReturnsCount, &Refusal{Reason: reason} }

// readStarts are the words a query may begin with.
var readStarts = map[string]bool{
	"SELECT": true, "WITH": true, "SHOW": true, "EXPLAIN": true, "VALUES": true, "TABLE": true,
	"DESCRIBE": true, "DESC": true,
}

// controlStarts are the words that begin transaction control or change the session. The data
// server owns the transaction, so no statement may end it or change its mode.
var controlStarts = map[string]bool{
	"COMMIT": true, "ROLLBACK": true, "BEGIN": true, "START": true, "END": true, "ABORT": true,
	"SAVEPOINT": true, "RELEASE": true, "SET": true, "COPY": true, "LOAD": true, "XA": true,
}

// dataWords change data from inside what looks like a query: a data modifying CTE, a locking read
// or EXPLAIN ANALYZE of a write, a SELECT INTO.
var dataWords = map[string]bool{"INTO": true, "UPDATE": true, "DELETE": true, "INSERT": true, "MERGE": true}

// fileWords reach the file system or other servers of the database host, in any mode.
var fileWords = map[string]bool{
	"LOAD_FILE": true, "OUTFILE": true, "DUMPFILE": true, "LO_IMPORT": true, "LO_EXPORT": true,
}

// filePrefixes are functions that read or write files, run SQL held in a string, or control other
// sessions. A read only transaction allows them, and a quoted name or a string can hide them from a
// reader that looks only at bare words, which is why quoted identifiers are checked too.
var filePrefixes = []string{
	"PG_READ_", "PG_LS_", "PG_STAT_FILE", "PG_FILE_", "DBLINK", "PG_TERMINATE_", "PG_CANCEL_", "SET_CONFIG",
	"QUERY_TO_", "TABLE_TO_XML", "CURSOR_TO_XML", "SCHEMA_TO_XML", "DATABASE_TO_XML",
}

// Check reads sql and says how to run it, or why not. It returns a *Refusal for a statement that
// must not be sent.
func Check(sql string, d Dialect, mode Mode) (Kind, error) {
	if len(sql) > MaxLength {
		return refuse(messages.SQLTooLong)
	}
	lex, reason := scan(sql, d)
	if reason != "" {
		return refuse(reason)
	}
	if len(lex.words) == 0 {
		return refuse(messages.SQLEmpty)
	}

	first := lex.words[0]
	if controlStarts[first] {
		return refuse(messages.SQLNotRead)
	}
	if mode == ReadOnly && !readStarts[first] {
		return refuse(messages.SQLNotRead)
	}
	for _, word := range lex.words {
		switch {
		case mode == ReadOnly && dataWords[word], fileWords[word], hasPrefix(word):
			return refuse(messages.SQLForbidden)
		}
	}

	for _, name := range lex.quoted {
		if fileWords[name] || hasPrefix(name) {
			return refuse(messages.SQLForbidden)
		}
	}

	if readStarts[first] || lex.returning {
		return ReturnsRows, nil
	}

	return ReturnsCount, nil
}

func hasPrefix(word string) bool {
	for _, prefix := range filePrefixes {
		if strings.HasPrefix(word, prefix) {
			return true
		}
	}

	return false
}

type lexed struct {
	// words are the unquoted words of the statement, upper case, in order.
	words []string
	// quoted are the names written in quotes. They are checked only against what must never run, because
	// a column may be called update.
	quoted    []string
	returning bool
}

// scan walks the statement the way the database does, so a quote, a comment or a dollar quote
// hides exactly what the database would not read as SQL. It returns a reason when the statement
// has more than one part or hides one in a comment the database would run.
func scan(sql string, d Dialect) (lexed, string) {
	var out lexed
	var terminated bool // a semicolon has ended the statement
	i, n := 0, len(sql)

	// content is called for every lexeme that is not white space or a comment.
	content := func() string {
		if terminated {
			return messages.SQLMultiple
		}

		return ""
	}

	for i < n {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < n && sql[i+1] == '-' && (d == Postgres || endsComment(sql, i+2)):
			i = skipLine(sql, i)
		case c == '#' && d == MySQL:
			i = skipLine(sql, i)
		case c == '/' && i+1 < n && sql[i+1] == '*':
			if d == MySQL && (strings.HasPrefix(sql[i:], "/*!") || strings.HasPrefix(sql[i:], "/*M!")) {
				// The database runs what is inside this comment.
				return out, messages.SQLForbidden
			}
			end, ok := skipBlock(sql, i, d == Postgres)
			if !ok {
				return out, messages.SQLForbidden
			}
			i = end
		case c == ';':
			if len(out.words) == 0 && !terminated {
				return out, messages.SQLEmpty
			}
			if terminated {
				return out, messages.SQLMultiple
			}
			terminated = true
			i++
		case c == '\'':
			if reason := content(); reason != "" {
				return out, reason
			}
			if d == Postgres && unicodeEscape(sql, i) {
				return out, messages.SQLForbidden
			}
			escapes := d == MySQL || (i > 0 && (sql[i-1] == 'e' || sql[i-1] == 'E') && (i < 2 || !isWord(sql[i-2])))
			end, ok := skipQuoted(sql, i, '\'', escapes)
			if !ok {
				return out, messages.SQLForbidden
			}
			i = end
		case c == '"' || c == '`':
			if reason := content(); reason != "" {
				return out, reason
			}
			if d == Postgres && unicodeEscape(sql, i) {
				// U&"..." spells a name with escapes, which no word check can read.
				return out, messages.SQLForbidden
			}
			if c == '`' && d == Postgres {
				i++

				continue
			}
			end, ok := skipQuoted(sql, i, c, d == MySQL && c == '"')
			if !ok {
				return out, messages.SQLForbidden
			}
			if c == '`' || d == Postgres {
				// An identifier in quotes names a function as well as a bare word does.
				out.quoted = append(out.quoted, strings.ToUpper(strings.ReplaceAll(sql[i+1:end-1], string(c)+string(c), string(c))))
			}
			i = end
		case c == '$' && d == Postgres && dollarTag(sql, i) != "":
			if reason := content(); reason != "" {
				return out, reason
			}
			tag := dollarTag(sql, i)
			rest := strings.Index(sql[i+len(tag):], tag)
			if rest < 0 {
				return out, messages.SQLForbidden
			}
			i += len(tag) + rest + len(tag)
		case isWord(c) || c >= 0x80:
			if reason := content(); reason != "" {
				return out, reason
			}
			start := i
			for i < n && (isWord(sql[i]) || sql[i] >= 0x80) {
				i++
			}
			word := strings.ToUpper(sql[start:i])
			out.words = append(out.words, word)
			if word == "RETURNING" {
				out.returning = true
			}
		default:
			if reason := content(); reason != "" {
				return out, reason
			}
			i++
		}
	}

	return out, ""
}

// unicodeEscape reports whether the quote at i opens a U& string or identifier.
func unicodeEscape(sql string, i int) bool {
	return i >= 2 && sql[i-1] == '&' && (sql[i-2] == 'U' || sql[i-2] == 'u') && (i < 3 || !isWord(sql[i-3]))
}

func isWord(c byte) bool {
	return c == '_' || c == '$' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// endsComment reports whether MySQL reads a double dash at i-2 as a comment: it has to be followed
// by white space or the end.
func endsComment(sql string, at int) bool {
	return at >= len(sql) || sql[at] == ' ' || sql[at] == '\t' || sql[at] == '\n' || sql[at] == '\r' || sql[at] < 0x20
}

func skipLine(sql string, i int) int {
	for i < len(sql) && sql[i] != '\n' {
		i++
	}

	return i
}

// skipBlock returns the index after the comment that starts at i. Postgres comments nest.
func skipBlock(sql string, i int, nests bool) (int, bool) {
	depth := 0
	for i < len(sql) {
		switch {
		case strings.HasPrefix(sql[i:], "/*") && (depth == 0 || nests):
			depth++
			i += 2
		case strings.HasPrefix(sql[i:], "*/"):
			depth--
			i += 2
			if depth == 0 {
				return i, true
			}
		default:
			i++
		}
	}

	return i, false
}

// skipQuoted returns the index after the quoted text that starts at i, where the quote character
// is doubled to put one in the text, and a backslash escapes the next character when escapes is set.
func skipQuoted(sql string, i int, quote byte, escapes bool) (int, bool) {
	i++
	for i < len(sql) {
		switch {
		case escapes && sql[i] == '\\':
			i += 2
		case sql[i] == quote && i+1 < len(sql) && sql[i+1] == quote:
			i += 2
		case sql[i] == quote:
			return i + 1, true
		default:
			i++
		}
	}

	return i, false
}

// dollarTag returns the opening tag of a Postgres dollar quote at i, such as $$ or $body$, or "".
func dollarTag(sql string, i int) string {
	for j := i + 1; j < len(sql); j++ {
		switch c := sql[j]; {
		case c == '$':
			return sql[i : j+1]
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (j > i+1 && c >= '0' && c <= '9') || c >= 0x80:
		default:
			return ""
		}
	}

	return ""
}
