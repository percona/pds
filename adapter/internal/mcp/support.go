package mcp

import (
	"context"
	"net/url"
	"strconv"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/percona/pds/adapter/internal/auth"
)

const (
	maxTickets = 100

	// ticketNumberPattern is what a ticket number looks like, such as CS0061234.
	ticketNumberPattern = `^[A-Za-z0-9_-]{3,32}$`
)

type ticketSummary struct {
	Number     string `json:"number" jsonschema:"Pass this to get_support_ticket."`
	State      string `json:"state,omitempty"`
	Priority   string `json:"priority,omitempty"`
	Subject    string `json:"subject,omitempty"`
	OpenedAt   string `json:"openedAt,omitempty"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
	ResolvedAt string `json:"resolvedAt,omitempty"`
}

type ticketComment struct {
	At     string `json:"at,omitempty"`
	Author string `json:"author,omitempty"`
	Label  string `json:"label,omitempty"`
	Body   string `json:"body"`
}

type ticket struct {
	ticketSummary
	Description string          `json:"description,omitempty"`
	Comments    []ticketComment `json:"comments,omitempty" jsonschema:"The public comments, oldest first."`
	PortalURL   string          `json:"portalUrl,omitempty" jsonschema:"Where the user can open the ticket."`
}

type tickets struct {
	Tickets   []ticketSummary `json:"tickets"`
	PortalURL string          `json:"portalUrl,omitempty" jsonschema:"Where the user can see every ticket."`
}

type ticketsInput struct {
	Limit  *int `json:"limit,omitempty" jsonschema:"How many tickets, 1 to 100."`
	Offset *int `json:"offset,omitempty" jsonschema:"How many tickets to skip."`
}

type ticketInput struct {
	Number string `json:"number" jsonschema:"The ticket number, such as CS0061234."`
}

func supportReads() []tool {
	return []tool{
		define(spec{
			name:  "list_support_tickets",
			title: "List support tickets",
			description: "List the support tickets of this account, newest first, and the link to the support portal. " +
				"limit is 1 to 100 (default 25) and offset skips that many tickets. " +
				"Fails with not available when support is not set up.",
			annotations: readOnly(),
			tune: func(input, _ *jsonschema.Schema) {
				bounds(input.Properties["limit"], 1, maxTickets)
				zero := 0.0
				input.Properties["offset"].Minimum = &zero
			},
		}, listSupportTickets),

		define(spec{
			name:  "get_support_ticket",
			title: "Get a support ticket",
			description: "Get one support ticket with its state and public comments. " +
				"number is the ticket number from list_support_tickets or create_support_ticket, such as CS0061234. " +
				"Fails with not available when support is not set up.",
			annotations: readOnly(),
			tune: func(input, _ *jsonschema.Schema) {
				input.Properties["number"].Pattern = ticketNumberPattern
			},
		}, getSupportTicket),
	}
}

func listSupportTickets(ctx context.Context, c *call, in ticketsInput) (tickets, error) {
	query := url.Values{}
	if in.Limit != nil {
		query.Set("limit", strconv.Itoa(*in.Limit))
	}
	if in.Offset != nil {
		query.Set("offset", strconv.Itoa(*in.Offset))
	}

	var out tickets
	if err := c.get(ctx, "/api/support/tickets", query, &out); err != nil {
		return tickets{}, err
	}
	out.Tickets = orEmpty(out.Tickets)

	return out, nil
}

func getSupportTicket(ctx context.Context, c *call, in ticketInput) (ticket, error) {
	var out ticket
	if err := c.get(ctx, "/api/support/tickets/"+url.PathEscape(in.Number), nil, &out); err != nil {
		return ticket{}, err
	}

	return out, nil
}

const (
	maxTicketSubject = 400
	maxTicketText    = 40000

	msgTicketCreateCheck = "Call list_support_tickets to see whether the ticket was created."
	msgTicketReplyCheck  = "Call get_support_ticket to see whether the reply was added."
)

// ticketChanged is the envelope of a ticket change, with the way to the ticket.
type ticketChanged struct {
	changed
	PortalURL string `json:"portalUrl,omitempty" jsonschema:"Where the user can open the ticket."`
}

type createTicketInput struct {
	Subject     string `json:"subject" jsonschema:"What stopped working, in one line."`
	Description string `json:"description" jsonschema:"What happened, what you saw and what you tried."`
	DatabaseID  string `json:"databaseId,omitempty" jsonschema:"A database id, copied exactly from list_databases, when the ticket is about one."`
}

type replyTicketInput struct {
	Number string `json:"number" jsonschema:"The ticket number, such as CS0061234."`
	Body   string `json:"body" jsonschema:"The message to add to the ticket."`
}

// ticketDatabase is the database as a ticket is opened about it. The version
// is read here and left out of get_database, the way the console reads it.
type ticketDatabase struct {
	database
	Version string `json:"version"`
}

// ticketContext is the same facts the console attaches when a ticket is
// opened from a database.
type ticketContext struct {
	Database      string `json:"database"`
	Profile       string `json:"profile"`
	Engine        string `json:"engine"`
	EngineVersion string `json:"engineVersion,omitempty"`
	ClusterType   string `json:"clusterType"`
	Namespace     string `json:"namespace"`
	Members       int    `json:"members"`
	Status        string `json:"status"`
	ErrorMessage  string `json:"errorMessage,omitempty"`
}

type createTicketBody struct {
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	Context     *ticketContext `json:"context,omitempty"`
}

type replyTicketBody struct {
	Body string `json:"body"`
}

func supportChanges() []tool {
	return []tool{
		define(spec{
			name:  "create_support_ticket",
			title: "Open a support ticket",
			description: "Open a support ticket with Percona. Give a subject that says what stopped working and a description with what you saw and tried. " +
				"Pass databaseId to attach the database's profile, engine, version, members, namespace, status and error message to the ticket. " +
				"Returns the ticket number, its state and the support portal link. " +
				"Fails with not available when support is not set up.",
			annotations: changing(false, false),
			access:      auth.AccessChange,
			tune: func(input, _ *jsonschema.Schema) {
				subject, text := maxTicketSubject, maxTicketText
				input.Properties["subject"].MaxLength = &subject
				input.Properties["description"].MaxLength = &text
			},
		}, createSupportTicket),

		define(spec{
			name:  "reply_to_support_ticket",
			title: "Reply to a support ticket",
			description: "Add a reply to a support ticket. It is posted as a comment from the user's account, and Percona support reads it. " +
				"number is the ticket number from list_support_tickets or create_support_ticket, such as CS0061234. " +
				"body is the message, up to 40000 characters. Ask the user before sending anything they have not seen. " +
				"Fails with not available when support is not set up.",
			annotations: changing(false, false),
			access:      auth.AccessChange,
			tune: func(input, _ *jsonschema.Schema) {
				input.Properties["number"].Pattern = ticketNumberPattern
				shortest, longest := 1, maxTicketText
				input.Properties["body"].MinLength = &shortest
				input.Properties["body"].MaxLength = &longest
			},
		}, replyToSupportTicket),
	}
}

func createSupportTicket(ctx context.Context, c *call, in createTicketInput) (ticketChanged, error) {
	body := createTicketBody{Subject: in.Subject, Description: in.Description}
	if in.DatabaseID != "" {
		path, err := databasePath(in.DatabaseID)
		if err != nil {
			return ticketChanged{}, err
		}
		var about ticketDatabase
		if err := c.get(ctx, "/api/databases/"+path, nil, &about); err != nil {
			return ticketChanged{}, err
		}
		body.Context = &ticketContext{
			Database: about.Name, Profile: about.Profile, Engine: about.Engine, EngineVersion: about.Version,
			ClusterType: "standalone", Namespace: about.Namespace, Members: about.Members, Status: about.Status,
			ErrorMessage: about.Message,
		}
		if about.Members > 1 {
			body.Context.ClusterType = "cluster"
		}
	}

	var opened ticket
	if err := c.post(ctx, "/api/support/tickets", body, msgTicketCreateCheck, &opened); err != nil {
		return ticketChanged{}, err
	}

	out := ticketChanged{changed: accepted("create_support_ticket", opened.Number), PortalURL: opened.PortalURL}
	out.Status = opened.State

	return out, nil
}

func replyToSupportTicket(ctx context.Context, c *call, in replyTicketInput) (changed, error) {
	path := "/api/support/tickets/" + url.PathEscape(in.Number) + "/replies"
	if err := c.post(ctx, path, replyTicketBody{Body: in.Body}, msgTicketReplyCheck, nil); err != nil {
		return changed{}, hintedAs(err, msgTicketNotFoundHint)
	}

	return accepted("reply_to_support_ticket", in.Number), nil
}
