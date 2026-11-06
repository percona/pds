package api

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/client/servicenow"
	"github.com/percona/pds/adapter/internal/service"
)

// ticketNumberPattern is what a ServiceNow case number looks like, such as
// CS0061234. Anything else is a client that skipped the UI, and refusing it
// here keeps a stray path segment out of a request to Percona.
var ticketNumberPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)

// createTicketRequest is the body behind POST /api/support/tickets. Both fields
// are what the dialog shows, so both are editable and neither is trusted: the
// service sanitizes them before they leave the environment.
type createTicketRequest struct {
	Subject     string                `json:"subject" validate:"omitempty,max=400"`
	Description string                `json:"description" validate:"omitempty,max=40000"`
	Context     *ticketContextRequest `json:"context"`
}

// replyTicketRequest is the body behind POST /api/support/tickets/:number/replies.
type replyTicketRequest struct {
	Body string `json:"body" validate:"omitempty,max=40000"`
}

// ticketContextRequest is the database context the page already knew. Every
// field is optional: a ticket opened from the support page has none of it.
type ticketContextRequest struct {
	Database      string `json:"database" validate:"omitempty,max=253"`
	Profile       string `json:"profile" validate:"omitempty,max=32"`
	Engine        string `json:"engine" validate:"omitempty,max=32"`
	EngineVersion string `json:"engineVersion" validate:"omitempty,max=32"`
	ClusterType   string `json:"clusterType" validate:"omitempty,max=32"`
	Namespace     string `json:"namespace" validate:"omitempty,max=253"`
	Members       int    `json:"members" validate:"omitempty,min=0,max=99"`
	Status        string `json:"status" validate:"omitempty,max=32"`
	ErrorMessage  string `json:"errorMessage" validate:"omitempty,max=4000"`
}

// ticketResponse is one support case. PortalURL is filled in by the adapter so
// the UI does not have to know which ServiceNow instance this environment talks to.
type ticketResponse struct {
	Number      string `json:"number,omitempty"`
	ID          string `json:"id,omitempty"`
	State       string `json:"state,omitempty"`
	Priority    string `json:"priority,omitempty"`
	Account     string `json:"account,omitempty"`
	Contact     string `json:"contact,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Description string `json:"description,omitempty"`
	Technology  string `json:"technology,omitempty"`

	OpenedAt   *time.Time `json:"openedAt,omitempty"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	UpdatedAt  *time.Time `json:"updatedAt,omitempty"`

	Comments    []ticketCommentResponse `json:"comments,omitempty"`
	PortalURL   string                  `json:"portalUrl,omitempty"`
	ExternalURL string                  `json:"externalUrl,omitempty"`
}

// ticketCommentResponse is one public comment.
type ticketCommentResponse struct {
	At     *time.Time `json:"at,omitempty"`
	Author string     `json:"author,omitempty"`
	Label  string     `json:"label,omitempty"`
	Body   string     `json:"body"`
}

// ticketListResponse is the answer to GET /api/support/tickets. An account with
// no tickets is an empty list, not a 404.
type ticketListResponse struct {
	Tickets   []ticketResponse `json:"tickets"`
	PortalURL string           `json:"portalUrl,omitempty"`
}

// handleCreateTicket opens one case.
func (s *Server) handleCreateTicket(w http.ResponseWriter, r *http.Request) {
	var req createTicketRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	subject := strings.TrimSpace(req.Subject)
	if subject == "" {
		Respond(w, r, nil, Validation("A ticket needs a subject. Say what stopped working."))

		return
	}

	description := strings.TrimSpace(req.Description)
	if description == "" {
		Respond(w, r, nil, Validation("A ticket needs a description. Support reads it first."))

		return
	}

	ticket, err := s.support.Create(r.Context(), service.TicketDraft{
		Subject:     subject,
		Description: description,
		Context:     toTicketContext(req.Context),
	})
	if err != nil {
		Respond(w, r, nil, supportError(err, ""))

		return
	}

	RespondWithStatus(w, r, http.StatusCreated, toTicketResponse(*ticket), nil)
}

// handleListTickets returns the account's tickets for the support page.
func (s *Server) handleListTickets(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	limit, err := parsePageValue(query.Get("limit"), servicenow.DefaultPageSize, servicenow.MaxPageSize)
	if err != nil {
		Respond(w, r, nil, Validation("The limit has to be a whole number between 1 and "+
			strconv.Itoa(servicenow.MaxPageSize)+".").WithCause(err))

		return
	}

	offset, err := parsePageValue(query.Get("offset"), 0, 100000)
	if err != nil {
		Respond(w, r, nil, Validation("The offset has to be a whole number, zero or more.").WithCause(err))

		return
	}

	page, err := s.support.ListPage(r.Context(), limit, offset)
	if err != nil {
		Respond(w, r, nil, supportError(err, ""))

		return
	}

	out := ticketListResponse{
		Tickets: make([]ticketResponse, 0, len(page.Tickets)),
		// The read resolved the portal already, and each resolve is a Secret
		// read, so this does not ask for it a second time.
		PortalURL: page.PortalURL,
	}
	for _, ticket := range page.Tickets {
		out.Tickets = append(out.Tickets, toTicketResponse(ticket))
	}

	Respond(w, r, out, nil)
}

// handleGetTicket returns one ticket with its state and public comments.
func (s *Server) handleGetTicket(w http.ResponseWriter, r *http.Request) {
	number := strings.TrimSpace(chi.URLParam(r, "number"))
	if !ticketNumberPattern.MatchString(number) {
		Respond(w, r, nil, Validation("That is not a ticket number. They look like CS0061234."))

		return
	}

	ticket, err := s.support.Get(r.Context(), number)
	if err != nil {
		Respond(w, r, nil, supportError(err, number))

		return
	}

	Respond(w, r, toTicketResponse(*ticket), nil)
}

// handleReplyTicket posts one public customer comment to a support case.
func (s *Server) handleReplyTicket(w http.ResponseWriter, r *http.Request) {
	number := strings.TrimSpace(chi.URLParam(r, "number"))
	if !ticketNumberPattern.MatchString(number) {
		Respond(w, r, nil, Validation("That is not a ticket number. They look like CS0061234."))

		return
	}

	var req replyTicketRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}

	if strings.TrimSpace(req.Body) == "" {
		Respond(w, r, nil, Validation("A reply needs a message."))

		return
	}

	if err := s.support.Reply(r.Context(), number, req.Body); err != nil {
		Respond(w, r, nil, supportError(err, number))

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// parsePageValue reads one paging parameter. Blank means the default, and a
// value outside the range is refused rather than clamped: a client asking for
// five hundred tickets has a bug worth hearing about.
func parsePageValue(raw string, fallback, max int) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback, nil
	}

	value, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, err
	}
	if value < 0 || value > max {
		return 0, errors.New("out of range: " + trimmed)
	}

	return value, nil
}

// supportError maps a support failure onto the API error contract. The reason
// ServiceNow gave is logged with the request ID and never served: it names
// Percona internals, and it is the one place a token could surface.
func supportError(err error, number string) *AppError {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, service.ErrSupportUnavailable):
		return NotImplemented("Support tickets need a ServiceNow connection, which is not " +
			"configured. Use the Percona support portal instead.").WithCause(err)

	case errors.Is(err, service.ErrSupportInstance):
		return NotImplemented("Support tickets need an https ServiceNow instance. The one this " +
			"control plane is configured with cannot be used.").WithCause(err)

	case errors.Is(err, service.ErrInvalidTicket):
		return Validation("That ticket cannot be sent as it is.").WithCause(err)

	case errors.Is(err, servicenow.ErrNotFound):
		message := "That ticket does not exist on this account."
		if number != "" {
			message = "No ticket named " + number + " on this account."
		}

		return NotFound(message).WithCause(err)

	case errors.Is(err, service.ErrSupportSearchDenied):
		return Downstream("ServiceNow refused to let the control plane read the "+
			servicenow.CaseTable+" table, which is how the ticket list is found. "+
			"Ask your Percona contact to allow this key to read it.", err)

	case errors.Is(err, servicenow.ErrUnauthorized):
		return Downstream("Percona support did not accept the control plane's credentials. "+
			"Ask your Percona contact to check the ServiceNow tokens.", err)

	case errors.Is(err, servicenow.ErrRejected):
		return Downstream("Percona support refused the ticket. Try again in a moment.", err)

	case errors.Is(err, servicenow.ErrResponseTooLarge):
		return Downstream("Percona support sent an answer too large to read. "+
			"Ask your Percona contact to look at the account.", err)

	default:
		return Downstream("Percona support did not answer. Try again in a moment.", err)
	}
}

func toTicketContext(in *ticketContextRequest) service.TicketContext {
	if in == nil {
		return service.TicketContext{}
	}

	return service.TicketContext{
		Database:      strings.TrimSpace(in.Database),
		Profile:       strings.TrimSpace(in.Profile),
		Engine:        strings.TrimSpace(in.Engine),
		EngineVersion: strings.TrimSpace(in.EngineVersion),
		ClusterType:   strings.TrimSpace(in.ClusterType),
		Namespace:     strings.TrimSpace(in.Namespace),
		Members:       in.Members,
		Status:        strings.TrimSpace(in.Status),
		ErrorMessage:  strings.TrimSpace(in.ErrorMessage),
	}
}

func toTicketResponse(in service.Ticket) ticketResponse {
	out := ticketResponse{
		Number:      in.Number,
		ID:          in.ID,
		State:       in.State,
		Priority:    in.Priority,
		Account:     in.Account,
		Contact:     in.Contact,
		Subject:     in.Subject,
		Description: in.Description,
		Technology:  in.Technology,
		OpenedAt:    timeOrNil(in.OpenedAt),
		ResolvedAt:  timeOrNil(in.ResolvedAt),
		UpdatedAt:   timeOrNil(in.UpdatedAt),
		PortalURL:   in.PortalURL,
		ExternalURL: in.ExternalURL,
	}

	for _, comment := range in.Comments {
		out.Comments = append(out.Comments, ticketCommentResponse{
			At:     timeOrNil(comment.At),
			Author: comment.Author,
			Label:  comment.Label,
			Body:   comment.Body,
		})
	}

	return out
}

// timeOrNil leaves a zero timestamp out of the body. A ticket that is not
// resolved has no resolved date, and rendering the zero year would be worse
// than the field being absent.
func timeOrNil(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}

	utc := at.UTC()

	return &utc
}
