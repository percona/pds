package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/client/servicenow"
)

// Support errors the HTTP layer maps onto the API error contract.
var (
	// ErrSupportUnavailable means the adapter has no ServiceNow Secret, so
	// there is nothing to open a ticket against. The support endpoints answer
	// 501 for it and the UI hides its actions.
	ErrSupportUnavailable = errors.New("support is not configured")
	// ErrInvalidTicket means the draft cannot be sent as it is.
	ErrInvalidTicket = errors.New("invalid ticket")
	// ErrSupportInstance means the stored ServiceNow instance cannot be used,
	// such as a Secret naming a plaintext URL. No token is sent to it.
	ErrSupportInstance = errors.New("servicenow instance is not usable")
	// ErrSupportSearchDenied means the instance refused the case table read
	// that the ticket list is built from. The same key can still open and read
	// cases through the connector, so this is not a rejected token.
	ErrSupportSearchDenied = errors.New("servicenow refused the case table read")
)

// Limits on what one ticket carries. ServiceNow truncates a long field
// silently, so the adapter does it visibly instead.
const (
	// MaxTicketSubject caps the short description.
	MaxTicketSubject = 160
	// MaxTicketDescription caps the body.
	MaxTicketDescription = 20000
)

// redacted is what replaces anything that authenticates against something.
const redacted = "[redacted]"

// pdsTicketMarker identifies descriptions created through this control
// plane. The connector list route cannot filter by it, so the service scopes
// the account page after reading it.
const pdsTicketMarker = "Reported by the Percona Database Service control plane."

// legacyTicketMarker is what the same line said before the product was renamed
// to PDS. Those cases are already stored in ServiceNow and nothing rewrites
// them, so the list matches this too and new descriptions never carry it.
const legacyTicketMarker = "Reported by the Percona Basecamp control plane." // allow-legacy-name

// ticketMarkers is every line that means the case was opened here.
func ticketMarkers() []string {
	return []string{pdsTicketMarker, legacyTicketMarker}
}

// TicketContext is the database context a ticket carries. Every field is
// optional: a ticket opened from the support page has none of it, and one
// opened from a failed create has all of it.
type TicketContext struct {
	Database      string
	Profile       string
	Engine        string
	EngineVersion string
	// ClusterType is accepted from the client. The facts block states members
	// instead, so the value is not written into the ticket.
	ClusterType string
	Namespace   string
	Members     int
	Status      string
	// ErrorMessage is what the operator or the adapter reported.
	ErrorMessage string
}

// TicketDraft is what the UI submits. Subject and Description are the editable
// fields in the dialog; Context is the facts the page already knew.
type TicketDraft struct {
	Subject     string
	Description string
	Context     TicketContext
}

// TicketComment is one public comment on a ticket.
type TicketComment struct {
	At     time.Time
	Author string
	Label  string
	Body   string
}

// Ticket is one support case as the adapter serves it.
type Ticket struct {
	// Number is what a developer quotes, such as CS0061234. It is empty only
	// when ServiceNow accepted the case and did not report the number yet.
	Number string
	// ID is the ServiceNow record id, which an attachment needs.
	ID        string
	TableName string

	State    string
	Priority string
	Account  string
	Contact  string

	Subject     string
	Description string
	Technology  string

	OpenedAt   time.Time
	ResolvedAt time.Time
	UpdatedAt  time.Time

	Comments []TicketComment

	// PortalURL is where the developer reads the ticket in the Percona Support
	// Portal. The adapter fills it in so the UI does not hardcode the instance.
	PortalURL string
	// ExternalURL opens this specific case in ServiceNow when its stable record
	// identity is available.
	ExternalURL string
}

// TicketClient is the slice of the ServiceNow client the support service needs.
type TicketClient interface {
	CreateTicket(ctx context.Context, in servicenow.CreateTicketInput) (*servicenow.Ticket, error)
	ReplyTicket(ctx context.Context, in servicenow.ReplyTicketInput) error
	SearchTickets(ctx context.Context, match []string, limit, offset int) ([]servicenow.Ticket, error)
	GetTicket(ctx context.Context, number string) (*servicenow.Ticket, error)
	GetTicketByID(ctx context.Context, sysID string) (*servicenow.Ticket, error)
}

// Support creates and reads support tickets.
type Support struct {
	client    TicketClient
	source    ServiceNowSource
	now       func() time.Time
	portalURL string
	guideURL  string
}

// ServiceNowSource supplies live ServiceNow credentials, typically from a
// Kubernetes Secret the UI wrote. The support service builds a client per
// request so a paste in the connections panel takes effect without a pod restart.
type ServiceNowSource interface {
	ServiceNow(ctx context.Context) (ServiceNowCredentials, error)
}

// SupportOption configures a Support service.
type SupportOption func(*Support)

// WithSupportClock replaces the clock, so a test asserts an exact timestamp.
func WithSupportClock(now func() time.Time) SupportOption {
	return func(s *Support) {
		if now != nil {
			s.now = now
		}
	}
}

// WithSupportPortalURL sets the portal link the UI shows. It comes from the
// client, so a non-production instance links to its own portal.
func WithSupportPortalURL(portalURL string) SupportOption {
	return func(s *Support) {
		if trimmed := strings.TrimSpace(portalURL); trimmed != "" {
			s.portalURL = trimmed
		}
	}
}

// WithSupportGuideURL sets the link the case note and the console's share text
// give an engineer for the connection steps. Without it they carry no link.
func WithSupportGuideURL(guideURL string) SupportOption {
	return func(s *Support) {
		s.guideURL = strings.TrimSpace(guideURL)
	}
}

// WithServiceNowSource builds a Connector client from live credentials on
// every call. Tests that inject a TicketClient do not set this.
func WithServiceNowSource(source ServiceNowSource) SupportOption {
	return func(s *Support) {
		s.source = source
	}
}

// NewSupport returns the support service. A nil client is a deployment with no
// ServiceNow Secret, and every call then answers ErrSupportUnavailable.
func NewSupport(client TicketClient, opts ...SupportOption) *Support {
	s := &Support{
		client: client,
		now:    time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}

	return s
}

// GuideURL is the link to the engineer's connection steps, or empty when none
// is configured.
func (s *Support) GuideURL() string {
	if s == nil {
		return ""
	}

	return s.guideURL
}

// Configured reports whether there is a ServiceNow account behind this service.
// It reads the live Secret, so pasting tokens in the connections panel opens the
// support routes without a pod restart.
func (s *Support) Configured(ctx context.Context) bool {
	if s == nil {
		return false
	}
	if s.client != nil {
		return true
	}
	if s.source == nil {
		return false
	}
	_, err := s.credentials(ctx)

	return err == nil
}

// credentials reads the live credentials and refuses an instance the tokens
// must not be sent to. A Secret is written by Helm, by kubectl, and by the
// connections panel, so the instance it names is checked here and not only where it
// was pasted.
func (s *Support) credentials(ctx context.Context) (ServiceNowCredentials, error) {
	creds, err := s.source.ServiceNow(ctx)
	if err != nil {
		return ServiceNowCredentials{}, err
	}

	if err := servicenow.ValidateInstanceURL(creds.URL); err != nil {
		return ServiceNowCredentials{}, fmt.Errorf("%w: %s", ErrSupportInstance, instanceReason(err))
	}

	return creds, nil
}

// clientFor returns the TicketClient this request should use. A fixed client
// wins, for tests; otherwise the source builds one from the live Secret.
func (s *Support) clientFor(ctx context.Context) (TicketClient, error) {
	if s == nil {
		return nil, ErrSupportUnavailable
	}
	if s.client != nil {
		return s.client, nil
	}
	if s.source == nil {
		return nil, ErrSupportUnavailable
	}

	creds, err := s.credentials(ctx)
	if err != nil {
		return nil, err
	}

	client, err := servicenow.New(servicenow.Options{
		BaseURL:     creds.URL,
		APIKey:      creds.APIKey,
		ClientToken: creds.ClientToken,
	})
	if err != nil {
		return nil, fmt.Errorf("support: servicenow client: %w", err)
	}

	return client, nil
}

// clientAndPortal returns both things a read of the account needs from one
// credential read. Resolving them separately reads the Secret twice, and the
// support page makes one request per source page, so that doubles with the
// size of the account.
func (s *Support) clientAndPortal(ctx context.Context) (TicketClient, string, error) {
	if s == nil {
		return nil, "", ErrSupportUnavailable
	}
	if s.client != nil {
		return s.client, s.PortalURL(ctx), nil
	}
	if s.source == nil {
		return nil, "", ErrSupportUnavailable
	}

	creds, err := s.credentials(ctx)
	if err != nil {
		return nil, "", err
	}

	client, err := servicenow.New(servicenow.Options{
		BaseURL:     creds.URL,
		APIKey:      creds.APIKey,
		ClientToken: creds.ClientToken,
	})
	if err != nil {
		return nil, "", fmt.Errorf("support: servicenow client: %w", err)
	}

	return client, servicenowPortalURL(creds.URL), nil
}

// PortalURL is the Percona Support Portal link the UI shows next to a ticket.
// It follows the instance the credentials name, so an environment pointed at the
// development instance links to the development portal.
//
// The link is the same for every ticket in one answer, so a handler resolves it
// once and passes it down rather than resolving it per ticket: each resolve is
// a Secret read.
func (s *Support) PortalURL(ctx context.Context) string {
	if s != nil && s.source != nil {
		if creds, err := s.credentials(ctx); err == nil {
			return servicenowPortalURL(creds.URL)
		}
	}
	if s == nil {
		return ""
	}

	return s.portalURL
}

func servicenowPortalURL(base string) string {
	return strings.TrimSuffix(strings.TrimSpace(base), "/") + "/percona"
}

// Create opens one case. The description is assembled from the draft and the
// database context, and both fields are sanitized before they leave the
// environment: a ticket is read by people outside it.
//
// A create answers with the record id and not always with the number, and the
// number is what the UI shows, so it is resolved with one details read. That
// read failing does not fail the create: the case exists either way, and
// reporting an error for it would read as "nothing happened".
func (s *Support) Create(ctx context.Context, draft TicketDraft) (*Ticket, error) {
	client, err := s.clientFor(ctx)
	if err != nil {
		return nil, err
	}

	subject := Sanitize(strings.TrimSpace(draft.Subject))
	if subject == "" {
		return nil, fmt.Errorf("%w: a ticket needs a subject", ErrInvalidTicket)
	}
	if len(subject) > MaxTicketSubject {
		subject = strings.TrimSpace(subject[:MaxTicketSubject])
	}

	created, err := client.CreateTicket(ctx, servicenow.CreateTicketInput{
		ShortDescription: subject,
		Description:      s.AssembleDescription(draft),
		MainTechnology:   servicenow.TechnologyFor(draft.Context.Engine),
	})
	if err != nil {
		return nil, fmt.Errorf("support: create ticket: %w", err)
	}
	if created == nil {
		return nil, fmt.Errorf("support: create ticket: the connector returned no ticket")
	}

	portalURL := s.PortalURL(ctx)

	ticket := s.toTicket(portalURL, *created)
	if ticket.Number == "" && ticket.ID != "" {
		if resolved, err := client.GetTicketByID(ctx, ticket.ID); err == nil && resolved != nil {
			ticket = s.merge(ticket, s.toTicket(portalURL, *resolved))
		}
	}
	ticket.ExternalURL = servicenow.TicketURL(
		strings.TrimSuffix(portalURL, "/percona"),
		ticket.TableName,
		ticket.ID,
	)

	// The create route answers with the record and not with the fields it was
	// given, so the subject comes back from what was sent rather than blank.
	if ticket.Subject == "" {
		ticket.Subject = subject
	}

	return &ticket, nil
}

// List reads the cases this control plane opened, newest first.
//
// The instance does the filtering. The connector list route answers with a
// capped slice of the account that leaves recently opened cases out of it
// entirely, so walking that route could not find these tickets however long it
// ran, and the account is far larger than the handful of cases we opened.
func (s *Support) List(ctx context.Context, limit, offset int) ([]Ticket, error) {
	page, err := s.ListPage(ctx, limit, offset)

	return page.Tickets, err
}

// TicketPage is a page of tickets with the portal link that goes with it.
// Resolving that link is a Secret read, so the page carries the one the read
// already resolved rather than leaving the caller to ask again.
type TicketPage struct {
	Tickets   []Ticket
	PortalURL string
}

// ListPage is List with the portal link for the page.
func (s *Support) ListPage(ctx context.Context, limit, offset int) (TicketPage, error) {
	client, portalURL, err := s.clientAndPortal(ctx)
	if err != nil {
		return TicketPage{}, err
	}

	if limit <= 0 {
		limit = servicenow.DefaultPageSize
	}
	if limit > servicenow.MaxPageSize {
		limit = servicenow.MaxPageSize
	}
	if offset < 0 {
		offset = 0
	}

	found, err := client.SearchTickets(ctx, ticketMarkers(), limit, offset)
	if err != nil {
		// The same key opens and reads cases through the connector, so a
		// refusal here is about reading the table, not about the token. A
		// rejected key is a 401 and stays what it is.
		if servicenow.Forbidden(err) {
			return TicketPage{}, fmt.Errorf("support: list tickets: %w: %w", ErrSupportSearchDenied, err)
		}

		return TicketPage{}, fmt.Errorf("support: list tickets: %w", err)
	}

	tickets := make([]Ticket, 0, len(found))
	for _, one := range found {
		// The instance matched the marker, and this says so again on what came
		// back, so a loose match cannot put someone else's case on the page.
		// A ServiceNow LIKE is case insensitive, so a case carrying a
		// differently cased copy of the line does match there and is not ours.
		//
		// The window was applied by the instance, so dropping a row here can
		// leave a page short but cannot skip one: the next offset still lands
		// where this page ended. A short page is the better end of that trade
		// than someone else's case on a support page.
		if !isPDSTicket(one) {
			continue
		}

		tickets = append(tickets, s.toTicket(portalURL, one))
	}

	return TicketPage{Tickets: tickets, PortalURL: portalURL}, nil
}

func isPDSTicket(ticket servicenow.Ticket) bool {
	for _, marker := range ticketMarkers() {
		if strings.Contains(ticket.Description, marker) {
			return true
		}
	}

	return false
}

// Get reads one ticket with its public comments.
func (s *Support) Get(ctx context.Context, number string) (*Ticket, error) {
	client, err := s.clientFor(ctx)
	if err != nil {
		return nil, err
	}

	trimmed := strings.TrimSpace(number)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: a ticket number is required", ErrInvalidTicket)
	}

	found, err := client.GetTicket(ctx, trimmed)
	if err != nil {
		return nil, fmt.Errorf("support: read ticket %s: %w", trimmed, err)
	}
	if found == nil {
		return nil, fmt.Errorf("support: read ticket %s: %w", trimmed, servicenow.ErrNotFound)
	}

	ticket := s.toTicket(s.PortalURL(ctx), *found)

	return &ticket, nil
}

// Reply posts one public comment to a case. The ticket is resolved by number
// first because the browser never receives the record id or table name needed
// by the ServiceNow Table API.
func (s *Support) Reply(ctx context.Context, number, body string) error {
	client, err := s.clientFor(ctx)
	if err != nil {
		return err
	}

	trimmedNumber := strings.TrimSpace(number)
	if trimmedNumber == "" {
		return fmt.Errorf("%w: a ticket number is required", ErrInvalidTicket)
	}

	message := Sanitize(strings.TrimSpace(body))
	if message == "" {
		return fmt.Errorf("%w: a ticket reply is required", ErrInvalidTicket)
	}

	ticket, err := client.GetTicket(ctx, trimmedNumber)
	if err != nil {
		return fmt.Errorf("support: read ticket %s for reply: %w", trimmedNumber, err)
	}
	if ticket == nil || strings.TrimSpace(ticket.SysID) == "" || strings.TrimSpace(ticket.TableName) == "" {
		return fmt.Errorf("%w: the ticket has no ServiceNow record", ErrInvalidTicket)
	}

	if err := client.ReplyTicket(ctx, servicenow.ReplyTicketInput{
		TableName: ticket.TableName,
		SysID:     ticket.SysID,
		Body:      message,
	}); err != nil {
		return fmt.Errorf("support: reply to ticket %s: %w", trimmedNumber, err)
	}

	return nil
}

// AssembleDescription builds the ticket body: what the developer wrote, then
// the facts support asks for every time. Everything is sanitized, including
// the operator message, which is where a connection string would
// realistically arrive.
func (s *Support) AssembleDescription(draft TicketDraft) string {
	var b strings.Builder

	if body := strings.TrimSpace(draft.Description); body != "" {
		b.WriteString(body)
		b.WriteString("\n\n")
	}

	b.WriteString(pdsTicketMarker)
	b.WriteString("\n")

	facts := draft.Context
	for _, line := range []struct{ label, value string }{
		{"Database", facts.Database},
		{"Profile", facts.Profile},
		{"Engine", engineLine(facts)},
		{"Namespace", facts.Namespace},
		{"Members", memberCount(facts.Members)},
		{"Status", facts.Status},
		{"Error", facts.ErrorMessage},
	} {
		if strings.TrimSpace(line.value) == "" {
			continue
		}

		b.WriteString(line.label)
		b.WriteString(": ")
		b.WriteString(line.value)
		b.WriteString("\n")
	}

	b.WriteString("Reported at: ")
	b.WriteString(s.clock()().UTC().Format(time.RFC3339))

	return Sanitize(b.String())
}

func (s *Support) clock() func() time.Time {
	if s == nil || s.now == nil {
		return time.Now
	}

	return s.now
}

func (s *Support) toTicket(portalURL string, in servicenow.Ticket) Ticket {
	comments := make([]TicketComment, 0, len(in.Comments))
	for _, comment := range in.Comments {
		comments = append(comments, TicketComment{
			At:     comment.At.UTC(),
			Author: comment.Author,
			Label:  comment.Label,
			Body:   comment.Body,
		})
	}

	if len(comments) == 0 {
		comments = nil
	}

	return Ticket{
		Number:      in.Number,
		ID:          in.SysID,
		TableName:   in.TableName,
		State:       in.State,
		Priority:    in.Priority,
		Account:     in.Account,
		Contact:     in.Contact,
		Subject:     in.ShortDescription,
		Description: in.Description,
		Technology:  in.MainTechnology,
		OpenedAt:    in.OpenedAt.UTC(),
		ResolvedAt:  in.ResolvedAt.UTC(),
		UpdatedAt:   in.UpdatedAt.UTC(),
		Comments:    comments,
		PortalURL:   portalURL,
		ExternalURL: servicenow.TicketURL(
			strings.TrimSuffix(portalURL, "/percona"),
			in.TableName,
			in.SysID,
		),
	}
}

// merge fills the created ticket from the details read. The create answer is
// the one that knows the table, and the details answer knows everything else.
func (s *Support) merge(created, resolved Ticket) Ticket {
	if resolved.ID == "" {
		resolved.ID = created.ID
	}
	if resolved.TableName == "" {
		resolved.TableName = created.TableName
	}
	if resolved.Number == "" {
		resolved.Number = created.Number
	}

	return resolved
}

// engineLine names the engine as support reads it, so a ticket about a MongoDB
// database says MongoDB. An engine id PDS does not know is written as given.
func engineLine(facts TicketContext) string {
	engine := strings.TrimSpace(facts.Engine)
	if technology, known := servicenow.KnownTechnology(engine); known {
		engine = technology
	}
	version := strings.TrimSpace(facts.EngineVersion)

	switch {
	case engine == "" && version == "":
		return ""
	case engine == "":
		return version
	case version == "":
		return engine
	default:
		return engine + " " + version
	}
}

func memberCount(members int) string {
	if members <= 0 {
		return ""
	}

	return strconv.Itoa(members)
}

// Sanitization patterns, in the order they run. A block spanning lines has to
// go before the line oriented rules, and a bearer token before the key rule,
// whose value pattern would otherwise stop at the word "Bearer".
var (
	// pemBlock is a key or certificate pasted whole.
	pemBlock = regexp.MustCompile(`(?s)-----BEGIN[^\n]*-----.*?-----END[^\n]*-----`)

	// databaseURI is a connection string. The host and database are redacted
	// with the credential: a URI is the one place they arrive together, and the
	// context block already names the database.
	databaseURI = regexp.MustCompile(
		`(?i)\b(?:jdbc:)?(?:mysql|mariadb|postgres|postgresql|mongodb\+srv|mongodb|rediss|redis)://\S*`)

	// userInfoURI is any other URI carrying credentials. The scheme and user
	// stay, because knowing which service refused the login is the point.
	userInfoURI = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^\s:/@]+):([^\s/@]+)@`)

	// bearerToken is an Authorization header value.
	bearerToken = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._\-+/=]{8,}`)

	// credentialField is any key whose name says its value authenticates
	// something. The name is matched loosely so MYSQL_PWD and client_token are
	// caught as well as a bare "password", and the value is taken to the end of
	// the line: a token with a space in it is still a token.
	credentialField = regexp.MustCompile(
		`(?i)([A-Za-z0-9_.\-]*(?:password|passwd|pwd|secret|token|apikey|api[_-]key|credential|authorization)[A-Za-z0-9_.\-]*)[ \t]*[:=][ \t]*[^\n]+`)

	// mysqlPassword is the -p flag of the mysql client. It is applied only to
	// lines that mention mysql, because -p means something else everywhere else.
	mysqlLine       = regexp.MustCompile(`(?i)\bmysql(?:dump|admin|sh)?\b`)
	mysqlPassword   = regexp.MustCompile(`(^|[ \t])-p(?:assword)?=?[^\s]+`)
	truncatedNotice = "\n[truncated: the ticket body was longer than the limit]"
)

// Sanitize removes anything from ticket text that authenticates against
// something: connection strings, passwords, API keys, bearer tokens, and pasted
// private keys.
//
// It errs toward redacting. A ticket that lost a line of context costs one
// round trip with support; a ticket that carried a production password costs a
// credential rotation.
func Sanitize(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}

	out := redactCredentials(text)
	if len(out) > MaxTicketDescription {
		out = out[:MaxTicketDescription] + truncatedNotice
	}

	return out
}

// redactCredentials is Sanitize without the length limit and the blank check,
// so a caller can apply it to a piece of text that is not the whole.
func redactCredentials(text string) string {
	out := pemBlock.ReplaceAllString(text, redacted)
	out = databaseURI.ReplaceAllString(out, redacted)
	out = userInfoURI.ReplaceAllString(out, "${1}${2}:"+redacted+"@")
	out = bearerToken.ReplaceAllString(out, "Bearer "+redacted)
	out = credentialField.ReplaceAllString(out, "${1}: "+redacted)

	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if mysqlLine.MatchString(line) {
			lines[i] = mysqlPassword.ReplaceAllString(line, "${1}-p"+redacted)
		}
	}

	return strings.Join(lines, "\n")
}
