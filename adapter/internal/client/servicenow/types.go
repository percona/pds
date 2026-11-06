package servicenow

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// TicketType is the only ticket type this adapter opens: every ticket the
// control plane creates is a customer service case.
const TicketType = "CASE"

// MainTechnology is the default `u_main_technology` value. MySQL stays the
// default so a ticket with no engine context still routes as it did.
const MainTechnology = "MySQL"

// MainTechnologyPostgreSQL is the `u_main_technology` value for PostgreSQL.
const MainTechnologyPostgreSQL = "PostgreSQL"

// TechnologyFor maps an engine id to the ServiceNow technology field.
func TechnologyFor(engine string) string {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "postgresql", "postgres":
		return MainTechnologyPostgreSQL
	default:
		return MainTechnology
	}
}

// CreateTicketInput is one new case. The description is assembled and sanitized
// by the service layer before it gets here.
type CreateTicketInput struct {
	// ShortDescription is the ticket subject.
	ShortDescription string
	// Description is the body support reads.
	Description string
	// MainTechnology is `u_main_technology`. Empty means MySQL.
	MainTechnology string
}

// ReplyTicketInput is one public customer comment on an existing case.
type ReplyTicketInput struct {
	TableName string
	SysID     string
	Body      string
}

// Comment is one public comment on a ticket.
type Comment struct {
	At     time.Time
	Author string
	Label  string
	Body   string
}

// Ticket is one ServiceNow case, in the shape the adapter serves it.
//
// A create answers with the sys_id and the table, and sometimes with the number
// as well. The number is what a developer quotes, so a create that did not
// carry one is resolved with a details read rather than reported without it.
type Ticket struct {
	// Number is the human ticket number, such as CS0061234.
	Number string
	// SysID is the ServiceNow record id, needed to attach a file.
	SysID string
	// TableName is the table the record lives in, needed for the same reason.
	TableName string

	State    string
	Priority string
	Account  string
	Contact  string

	ShortDescription string
	Description      string
	MainTechnology   string

	OpenedAt   time.Time
	ResolvedAt time.Time
	UpdatedAt  time.Time

	Comments []Comment
}

// envelope is the ServiceNow response wrapper. Every scripted REST answer this
// client reads puts its payload under `result`.
type envelope struct {
	Result json.RawMessage `json:"result"`
}

// ticketPayload is one record as the connector serializes it. Both spellings of
// the id and the number are read: the create route answers with `ticket_id` and
// the detail route with `sys_id`.
type ticketPayload struct {
	Success      *bool  `json:"success"`
	ErrorMessage string `json:"error_message"`

	// TicketDetails is where the detail route puts the case. That route answers
	// with the id and the table at the top level and the record one level down,
	// so a payload has to be flattened before it is read. See resolve.
	TicketDetails *ticketPayload `json:"ticket_details"`

	Number       string `json:"number"`
	TicketNumber string `json:"ticket_number"`
	SysID        string `json:"sys_id"`
	TicketID     string `json:"ticket_id"`
	TableName    string `json:"table_name"`

	State    flexString `json:"state"`
	Priority flexString `json:"priority"`
	Account  flexString `json:"account"`
	Contact  flexString `json:"contact"`

	ShortDescription string     `json:"short_description"`
	Description      string     `json:"description"`
	MainTechnology   flexString `json:"u_main_technology"`

	OpenedAt   snTime `json:"opened_at"`
	ResolvedAt snTime `json:"resolved_at"`
	UpdatedAt  snTime `json:"sys_updated_on"`

	Comments commentList `json:"comments"`
}

// resolve flattens a record the connector nested under `ticket_details`.
//
// The detail route answers `{"result": {"success": true, "ticket_id": "...",
// "table_name": "...", "ticket_details": {...}}}`: the outer object carries the
// id, the table, and the outcome, and the inner one carries the number, the
// state, the description, and the comments. Reading only the outer object
// yields a ticket with an id and nothing else, which is a ticket with no number
// to quote.
//
// A payload with no `ticket_details` is returned as it is, so the create route
// and a connector build that answers flat both still decode.
func (p ticketPayload) resolve() ticketPayload {
	if p.TicketDetails == nil || p.TicketDetails.blank() {
		return p
	}

	inner := *p.TicketDetails
	inner.TicketDetails = nil

	// The outer object is the one that always names the id and the table, and
	// it is the only one that says whether the call was refused.
	inner.Success = p.Success
	inner.ErrorMessage = p.ErrorMessage

	for _, field := range []struct{ inner, outer *string }{
		{&inner.Number, &p.Number},
		{&inner.TicketNumber, &p.TicketNumber},
		{&inner.SysID, &p.SysID},
		{&inner.TicketID, &p.TicketID},
		{&inner.TableName, &p.TableName},
	} {
		if *field.inner == "" {
			*field.inner = *field.outer
		}
	}

	return inner
}

// rejected reports a 200 that says the request was refused. The connector
// answers `success: false` with a reason rather than a status code, so the code
// alone cannot decide whether a call worked.
func (p ticketPayload) rejected() bool {
	return p.Success != nil && !*p.Success
}

// empty reports a result with no record in it, which is how the detail route
// answers a number that does not exist.
func (p ticketPayload) empty() bool {
	return p.number() == "" && p.sysID() == ""
}

// blank reports a record carrying none of the fields a ticket is read from,
// which is how a `ticket_details` key holding an empty object decodes. Such a
// record is not worth flattening over the outer answer, which at least names
// the id that was asked for.
func (p ticketPayload) blank() bool {
	return p.empty() && p.ShortDescription == "" && p.Description == "" &&
		p.State == "" && len(p.Comments) == 0
}

func (p ticketPayload) number() string {
	if p.Number != "" {
		return p.Number
	}

	return p.TicketNumber
}

func (p ticketPayload) sysID() string {
	if p.SysID != "" {
		return p.SysID
	}

	return p.TicketID
}

func (p ticketPayload) toTicket() Ticket {
	return Ticket{
		Number:           p.number(),
		SysID:            p.sysID(),
		TableName:        p.TableName,
		State:            string(p.State),
		Priority:         string(p.Priority),
		Account:          string(p.Account),
		Contact:          string(p.Contact),
		ShortDescription: p.ShortDescription,
		Description:      p.Description,
		MainTechnology:   string(p.MainTechnology),
		OpenedAt:         time.Time(p.OpenedAt),
		ResolvedAt:       time.Time(p.ResolvedAt),
		UpdatedAt:        time.Time(p.UpdatedAt),
		Comments:         p.Comments.toComments(),
	}
}

// listPayload is the wrapped form of a list answer. The connector documents an
// array under `result`; a build that wraps it in an object is read too, because
// a shape change on the Percona side should leave the page working.
type listPayload struct {
	Tickets []ticketPayload `json:"tickets"`
	Records []ticketPayload `json:"records"`
	Result  []ticketPayload `json:"result"`
}

func (p listPayload) tickets() []ticketPayload {
	switch {
	case len(p.Tickets) > 0:
		return p.Tickets
	case len(p.Records) > 0:
		return p.Records
	default:
		return p.Result
	}
}

// snTimeLayout is how ServiceNow writes a timestamp: UTC, no zone, no T.
const snTimeLayout = "2006-01-02 15:04:05"

// snTime is a ServiceNow timestamp. A blank value is a zero time rather than a
// parse failure, because "not resolved yet" is written as an empty string.
type snTime time.Time

// UnmarshalJSON reads the ServiceNow layout and falls back to RFC 3339, so a
// connector build that switched to ISO timestamps still decodes.
func (t *snTime) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(raw, []byte("null")) {
		return nil
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	for _, layout := range []string{snTimeLayout, time.RFC3339, time.RFC3339Nano} {
		if parsed, err := time.Parse(layout, value); err == nil {
			*t = snTime(parsed.UTC())

			return nil
		}
	}

	// An unreadable timestamp is not worth failing a ticket read over: the
	// state and the description are what the developer came for.
	return nil
}

// flexString is a field ServiceNow serializes either as a plain string or as
// the `{display_value, value}` pair its reference fields use.
type flexString string

// UnmarshalJSON accepts a string, a reference object, or a number.
func (s *flexString) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(raw, []byte("null")) {
		return nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		*s = flexString(strings.TrimSpace(text))

		return nil
	}

	var reference struct {
		DisplayValue string `json:"display_value"`
		Value        string `json:"value"`
	}
	if err := json.Unmarshal(raw, &reference); err == nil {
		if reference.DisplayValue != "" {
			*s = flexString(strings.TrimSpace(reference.DisplayValue))
		} else {
			*s = flexString(strings.TrimSpace(reference.Value))
		}

		return nil
	}

	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		*s = flexString(number.String())
	}

	return nil
}

// commentPayload is one journal entry.
type commentPayload struct {
	CreatedOn snTime     `json:"created_on"`
	CreatedBy flexString `json:"created_by"`
	Label     string     `json:"label"`
	Value     string     `json:"value"`
	// Some builds name the body `comments` instead of `value`.
	Comments string `json:"comments"`
}

func (c commentPayload) body() string {
	if c.Value != "" {
		return c.Value
	}

	return c.Comments
}

// commentList is the comments field. ServiceNow journals are sometimes a single
// text blob rather than an array, so both are read and neither fails the ticket.
type commentList []commentPayload

// UnmarshalJSON accepts an array, a plain string, or null.
func (l *commentList) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(raw, []byte("null")) {
		return nil
	}

	var entries []commentPayload
	if err := json.Unmarshal(raw, &entries); err == nil {
		*l = entries

		return nil
	}

	var blob string
	if err := json.Unmarshal(raw, &blob); err == nil && strings.TrimSpace(blob) != "" {
		if entries, ok := parseJournalBlob(blob); ok {
			*l = entries
		} else {
			*l = commentList{{Value: blob}}
		}
	}

	return nil
}

func (l commentList) toComments() []Comment {
	if len(l) == 0 {
		return nil
	}

	out := make([]Comment, 0, len(l))
	for _, entry := range l {
		body := strings.TrimSpace(entry.body())
		if body == "" {
			continue
		}

		out = append(out, Comment{
			At:     time.Time(entry.CreatedOn),
			Author: string(entry.CreatedBy),
			Label:  strings.TrimSpace(entry.Label),
			Body:   body,
		})
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

var journalHeader = regexp.MustCompile(
	`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) - (.+?) \(([^()\r\n]+)\)\s*$`)

var journalLabels = map[string]struct{}{
	"additional comments": {},
	"work notes":          {},
}

// parseJournalBlob recognizes the connector's documented journal export. It
// extracts metadata only from its opening header. The connector supplies no
// unambiguous separator for later entries, so later header-like text remains
// in the body.
func parseJournalBlob(blob string) (commentList, bool) {
	lines := strings.Split(strings.ReplaceAll(blob, "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return nil, false
	}

	first := parseJournalHeader(lines[0])
	if first == nil {
		return nil, false
	}

	first.Value = strings.TrimSpace(strings.Join(lines[1:], "\n"))
	if first.Value == "" {
		return nil, false
	}

	return commentList{*first}, true
}

func parseJournalHeader(line string) *commentPayload {
	matches := journalHeader.FindStringSubmatch(line)
	if matches == nil {
		return nil
	}

	at, err := time.Parse(snTimeLayout, matches[1])
	if err != nil {
		return nil
	}

	author := strings.TrimSpace(matches[2])
	label := strings.TrimSpace(matches[3])
	if author == "" || label == "" {
		return nil
	}
	if _, ok := journalLabels[strings.ToLower(label)]; !ok {
		return nil
	}

	return &commentPayload{
		CreatedOn: snTime(at.UTC()),
		CreatedBy: flexString(author),
		Label:     label,
	}
}
