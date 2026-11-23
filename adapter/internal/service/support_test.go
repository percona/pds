package service_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/servicenow"
	"github.com/percona/pds/adapter/internal/service"
)

var supportNow = time.Date(2026, 8, 25, 18, 30, 0, 0, time.UTC)

// fakeTickets is the slice of the ServiceNow client the support service uses.
type fakeTickets struct {
	created   []servicenow.CreateTicketInput
	createOut *servicenow.Ticket
	createErr error

	replies  []servicenow.ReplyTicketInput
	replyErr error

	searchOut []servicenow.Ticket
	searchErr error
	searches  []ticketSearch

	listOut []servicenow.Ticket
	listErr error
	limit   int
	offset  int
	lists   []ticketListRequest
	pages   map[int][]servicenow.Ticket

	getOut  *servicenow.Ticket
	getErr  error
	asked   []string
	details map[string]*servicenow.Ticket

	byIDOut *servicenow.Ticket
	byIDErr error
	askedID []string
}

type ticketListRequest struct {
	limit  int
	offset int
}

type ticketSearch struct {
	match  []string
	limit  int
	offset int
}

func (f *fakeTickets) CreateTicket(
	_ context.Context,
	in servicenow.CreateTicketInput,
) (*servicenow.Ticket, error) {
	f.created = append(f.created, in)
	if f.createErr != nil {
		return nil, f.createErr
	}

	return f.createOut, nil
}

func (f *fakeTickets) ReplyTicket(_ context.Context, in servicenow.ReplyTicketInput) error {
	f.replies = append(f.replies, in)

	return f.replyErr
}

func (f *fakeTickets) SearchTickets(
	_ context.Context,
	match []string,
	limit, offset int,
) ([]servicenow.Ticket, error) {
	f.searches = append(f.searches, ticketSearch{match: match, limit: limit, offset: offset})
	if f.searchErr != nil {
		return nil, f.searchErr
	}

	return f.searchOut, nil
}

func (f *fakeTickets) ListTickets(_ context.Context, limit, offset int) ([]servicenow.Ticket, error) {
	f.limit, f.offset = limit, offset
	f.lists = append(f.lists, ticketListRequest{limit: limit, offset: offset})
	if f.pages != nil {
		return f.pages[offset], f.listErr
	}

	return f.listOut, f.listErr
}

func (f *fakeTickets) GetTicket(_ context.Context, number string) (*servicenow.Ticket, error) {
	f.asked = append(f.asked, number)
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.details != nil {
		return f.details[number], nil
	}

	return f.getOut, nil
}

func (f *fakeTickets) GetTicketByID(_ context.Context, sysID string) (*servicenow.Ticket, error) {
	f.askedID = append(f.askedID, sysID)
	if f.byIDErr != nil {
		return nil, f.byIDErr
	}

	return f.byIDOut, nil
}

// fakeSource stands in for the Secret a platform engineer pasted on the
// connections panel. It records what it was asked and with which context.
type fakeSource struct {
	creds   service.ServiceNowCredentials
	err     error
	reads   int
	lastCtx context.Context
}

func (f *fakeSource) ServiceNow(ctx context.Context) (service.ServiceNowCredentials, error) {
	f.reads++
	f.lastCtx = ctx
	if f.err != nil {
		return service.ServiceNowCredentials{}, f.err
	}

	return f.creds, nil
}

type sourceCtxKey struct{}

func newSupport(t *testing.T, client service.TicketClient) *service.Support {
	t.Helper()

	return service.NewSupport(client,
		service.WithSupportClock(func() time.Time { return supportNow }),
		service.WithSupportPortalURL("https://example.service-now.com/percona"),
	)
}

func TestSupportWithoutAClientIsUnavailable(t *testing.T) {
	support := service.NewSupport(nil)

	assert.False(t, support.Configured(context.Background()))

	_, err := support.Create(context.Background(), service.TicketDraft{Subject: "x", Description: "y"})
	assert.ErrorIs(t, err, service.ErrSupportUnavailable)

	_, err = support.List(context.Background(), 10, 0)
	assert.ErrorIs(t, err, service.ErrSupportUnavailable)

	_, err = support.Get(context.Background(), "CS0061234")
	assert.ErrorIs(t, err, service.ErrSupportUnavailable)

	err = support.Reply(context.Background(), "CS0061234", "I have more information.")
	assert.ErrorIs(t, err, service.ErrSupportUnavailable)
}

func TestSupportWithAClientIsConfigured(t *testing.T) {
	support := newSupport(t, &fakeTickets{})

	assert.True(t, support.Configured(context.Background()))
	assert.Equal(t, "https://example.service-now.com/percona", support.PortalURL(context.Background()))
}

func TestCreateRejectsABlankSubject(t *testing.T) {
	client := &fakeTickets{}
	support := newSupport(t, client)

	_, err := support.Create(context.Background(), service.TicketDraft{
		Subject:     "   ",
		Description: "something broke",
	})
	require.ErrorIs(t, err, service.ErrInvalidTicket)
	assert.Empty(t, client.created, "nothing reaches ServiceNow for a ticket with no subject")
}

func TestCreateAssemblesTheDescriptionFromTheContext(t *testing.T) {
	client := &fakeTickets{
		createOut: &servicenow.Ticket{
			Number:    "CS0061234",
			SysID:     "sys-1",
			TableName: "sn_customerservice_case",
		},
	}
	support := newSupport(t, client)

	ticket, err := support.Create(context.Background(), service.TicketDraft{
		Subject:     "Cannot create database orders-prod",
		Description: "The create stopped at Starting database.",
		Context: service.TicketContext{
			Database:      "orders-prod",
			Profile:       "prod",
			Engine:        "mysql",
			EngineVersion: "8.0.36",
			ClusterType:   "environment",
			Namespace:     "pds",
			Members:       3,
			Status:        "failed",
			ErrorMessage:  "0/3 nodes are available: insufficient cpu",
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "CS0061234", ticket.Number)
	assert.Equal(t, "https://example.service-now.com/percona", ticket.PortalURL)
	assert.Equal(t,
		"https://example.service-now.com/sn_customerservice_case.do?sys_id=sys-1",
		ticket.ExternalURL)

	require.Len(t, client.created, 1)
	sent := client.created[0]

	assert.Equal(t, "Cannot create database orders-prod", sent.ShortDescription)

	body := sent.Description
	assert.Contains(t, body, "The create stopped at Starting database.")
	assert.Contains(t, body, "Database: orders-prod")
	assert.Contains(t, body, "Profile: prod")
	assert.Contains(t, body, "Engine: MySQL 8.0.36")
	assert.NotContains(t, body, "Environment type:")
	assert.Contains(t, body, "Namespace: pds")
	assert.Contains(t, body, "Members: 3")
	assert.Contains(t, body, "Status: failed")
	assert.Contains(t, body, "Error: 0/3 nodes are available: insufficient cpu")
	assert.Contains(t, body, "Reported at: 2026-08-25T18:30:00Z")
}

func TestCreateLeavesOutContextItDoesNotHave(t *testing.T) {
	client := &fakeTickets{createOut: &servicenow.Ticket{Number: "CS0061234", SysID: "sys-1"}}
	support := newSupport(t, client)

	_, err := support.Create(context.Background(), service.TicketDraft{
		Subject:     "Metrics are missing on orders-dev",
		Description: "No charts on the Metrics tab.",
		Context:     service.TicketContext{Database: "orders-dev"},
	})
	require.NoError(t, err)

	body := client.created[0].Description
	assert.Contains(t, body, "Database: orders-dev")
	assert.NotContains(t, body, "Profile:", "a blank field is left out rather than sent empty")
	assert.NotContains(t, body, "Members:")
	assert.NotContains(t, body, "Error:")
}

// A create answers with the sys_id and not always with the number, and the
// number is what the UI shows, so it is resolved with a details read.
func TestCreateResolvesTheNumberFromTheTicketID(t *testing.T) {
	client := &fakeTickets{
		createOut: &servicenow.Ticket{SysID: "sys-1", TableName: "sn_customerservice_case"},
		byIDOut:   &servicenow.Ticket{Number: "CS0061234", SysID: "sys-1", State: "New"},
	}
	support := newSupport(t, client)

	ticket, err := support.Create(context.Background(), service.TicketDraft{
		Subject:     "Cannot create database orders-prod",
		Description: "It failed.",
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"sys-1"}, client.askedID)
	assert.Equal(t, "CS0061234", ticket.Number)
	assert.Equal(t, "New", ticket.State)
	assert.Equal(t,
		"https://example.service-now.com/sn_customerservice_case.do?sys_id=sys-1",
		ticket.ExternalURL)
}

// The ticket exists either way, so a failed lookup must not turn a created
// ticket into an error the developer reads as "nothing happened".
func TestCreateStillSucceedsWhenTheNumberCannotBeResolved(t *testing.T) {
	client := &fakeTickets{
		createOut: &servicenow.Ticket{SysID: "sys-1", TableName: "sn_customerservice_case"},
		byIDErr:   errors.New("ticket_details timed out"),
	}
	support := newSupport(t, client)

	ticket, err := support.Create(context.Background(), service.TicketDraft{
		Subject:     "Cannot create database orders-prod",
		Description: "It failed.",
	})
	require.NoError(t, err)

	assert.Empty(t, ticket.Number)
	assert.Equal(t, "sys-1", ticket.ID)
	assert.NotEmpty(t, ticket.PortalURL)
	assert.Equal(t,
		"https://example.service-now.com/sn_customerservice_case.do?sys_id=sys-1",
		ticket.ExternalURL)
}

func TestCreateReportsAFailedCreate(t *testing.T) {
	client := &fakeTickets{createErr: servicenow.ErrUnauthorized}
	support := newSupport(t, client)

	_, err := support.Create(context.Background(), service.TicketDraft{
		Subject:     "Cannot create database orders-prod",
		Description: "It failed.",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, servicenow.ErrUnauthorized)
}

func TestGetReadsOneTicketWithItsComments(t *testing.T) {
	client := &fakeTickets{
		getOut: &servicenow.Ticket{
			Number:           "CS0061234",
			State:            "Awaiting Percona",
			ShortDescription: "Cannot create database orders-prod",
			Comments: []servicenow.Comment{
				{
					At:     supportNow,
					Author: "percona.support",
					Label:  "Additional comments",
					Body:   "Looking at it.",
				},
			},
		},
	}
	support := newSupport(t, client)

	ticket, err := support.Get(context.Background(), "  CS0061234 ")
	require.NoError(t, err)

	assert.Equal(t, []string{"CS0061234"}, client.asked, "the number is trimmed before the read")
	require.Len(t, ticket.Comments, 1)
	assert.Equal(t, "Looking at it.", ticket.Comments[0].Body)
	assert.Equal(t, "Additional comments", ticket.Comments[0].Label)
}

func TestGetRejectsABlankNumber(t *testing.T) {
	client := &fakeTickets{}
	support := newSupport(t, client)

	_, err := support.Get(context.Background(), "   ")
	require.ErrorIs(t, err, service.ErrInvalidTicket)
	assert.Empty(t, client.asked)
}

func TestReplyPostsASanitizedPublicCommentToTheTicket(t *testing.T) {
	client := &fakeTickets{
		getOut: &servicenow.Ticket{
			Number:    "CS0061234",
			SysID:     "sys-1",
			TableName: "sn_customerservice_case",
		},
	}
	support := newSupport(t, client)

	err := support.Reply(
		context.Background(),
		" CS0061234 ",
		"Please use mysql://root:letmein@orders-prod:3306/appdb\napi_key=abc123456",
	)
	require.NoError(t, err)

	assert.Equal(t, []string{"CS0061234"}, client.asked)
	require.Len(t, client.replies, 1)
	assert.Equal(t, "sn_customerservice_case", client.replies[0].TableName)
	assert.Equal(t, "sys-1", client.replies[0].SysID)
	assert.NotContains(t, client.replies[0].Body, "letmein")
	assert.NotContains(t, client.replies[0].Body, "abc123456")
	assert.Contains(t, client.replies[0].Body, "[redacted]")
}

func TestReplyRejectsAnEmptyMessage(t *testing.T) {
	client := &fakeTickets{}
	support := newSupport(t, client)

	err := support.Reply(context.Background(), "CS0061234", "  ")

	require.ErrorIs(t, err, service.ErrInvalidTicket)
	assert.Empty(t, client.asked)
	assert.Empty(t, client.replies)
}

// Sanitization is the part of this feature that has to be right. A ticket is
// read by people outside the customer's environment, so nothing that authenticates
// against anything can be in it.
func TestSanitizeStripsSecrets(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{
			name:    "mysql connection string with a password",
			in:      "Connection: mysql://root:s3cr3t-pass@orders-prod-haproxy:3306/appdb",
			absent:  []string{"s3cr3t-pass", "orders-prod-haproxy"},
			present: []string{"Connection:", "[redacted]"},
		},
		{
			name:   "jdbc connection string",
			in:     "jdbc:mysql://admin:hunter2@10.4.5.6:3306/appdb?useSSL=true",
			absent: []string{"hunter2", "10.4.5.6"},
		},
		{
			name:   "postgres and mongodb connection strings",
			in:     "postgres://u:p@db:5432/x and mongodb+srv://u:p@environment/x",
			absent: []string{"://u:p@", "environment/x"},
		},
		{
			name:    "password field",
			in:      "root password: N0tSafe!\nother: keep me",
			absent:  []string{"N0tSafe"},
			present: []string{"keep me", "[redacted]"},
		},
		{
			name:   "assorted credential keys",
			in:     "MYSQL_PWD=abc123\napi_key=k-1234567890\nclient_token=tok-99\nsecret = shhh",
			absent: []string{"abc123", "k-1234567890", "tok-99", "shhh"},
		},
		{
			name:   "bearer token",
			in:     "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.abc",
			absent: []string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
		},
		{
			name:   "mysql client password flag",
			in:     "mysql -h orders -u root -pTopSecret1 -e 'select 1'",
			absent: []string{"TopSecret1"},
		},
		{
			name:   "private key block",
			in:     "key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAK\n-----END RSA PRIVATE KEY-----\ndone",
			absent: []string{"MIIEowIBAAK", "BEGIN RSA PRIVATE KEY"},
			present: []string{
				"done",
			},
		},
		{
			name:   "x-sn-apikey header",
			in:     "x-sn-apikey: 9f8e7d6c5b4a",
			absent: []string{"9f8e7d6c5b4a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := service.Sanitize(tt.in)

			for _, absent := range tt.absent {
				assert.NotContains(t, got, absent, "sanitized text: %s", got)
			}
			for _, present := range tt.present {
				assert.Contains(t, got, present)
			}
		})
	}
}

func TestCreateNamesTheEngineAndFilesTheTicketUnderItsTechnology(t *testing.T) {
	for _, tt := range []struct{ engine, line, technology string }{
		{"mysql", "Engine: MySQL 8.4", servicenow.MainTechnology},
		{"postgresql", "Engine: PostgreSQL 8.4", servicenow.MainTechnologyPostgreSQL},
		{"mongodb", "Engine: MongoDB 8.4", servicenow.MainTechnologyMongoDB},
		{"oracle", "Engine: oracle 8.4", servicenow.MainTechnology},
	} {
		t.Run(tt.engine, func(t *testing.T) {
			client := &fakeTickets{createOut: &servicenow.Ticket{Number: "CS0061234", SysID: "sys-1", TableName: "sn_customerservice_case"}}

			_, err := newSupport(t, client).Create(context.Background(), service.TicketDraft{
				Subject: "Cannot create database orders",
				Context: service.TicketContext{
					Database: "orders", Profile: "dev", Engine: tt.engine, EngineVersion: "8.4", Namespace: "pds", Members: 3, Status: "failed",
				},
			})
			require.NoError(t, err)

			require.Len(t, client.created, 1)
			assert.Contains(t, client.created[0].Description, tt.line)
			assert.Equal(t, tt.technology, client.created[0].MainTechnology)
		})
	}
}

func TestCreateKeepsAMongoDBConnectionStringOutOfTheTicket(t *testing.T) {
	client := &fakeTickets{createOut: &servicenow.Ticket{Number: "CS0061234", SysID: "sys-1", TableName: "sn_customerservice_case"}}

	_, err := newSupport(t, client).Create(context.Background(), service.TicketDraft{
		Subject:     "Cannot connect to orders",
		Description: "I used mongodb://databaseAdmin:hunter2@orders-rs0-0:27017,orders-rs0-1:27017/admin?replicaSet=rs0&ssl=false and it timed out.",
		Context:     service.TicketContext{Database: "orders", Engine: "mongodb"},
	})
	require.NoError(t, err)

	require.Len(t, client.created, 1)
	assert.NotContains(t, client.created[0].Description, "hunter2")
	assert.Contains(t, client.created[0].Description, "it timed out")
}

func TestSanitizeKeepsOrdinaryContext(t *testing.T) {
	in := strings.Join([]string{
		"Database: orders-prod",
		"Profile: prod",
		"Engine: mysql 8.0.36",
		"Status: failed",
		"Error: 0/3 nodes are available: insufficient cpu, insufficient memory",
		"Namespace: pds",
		"The operator restarted the pod twice.",
	}, "\n")

	assert.Equal(t, in, service.Sanitize(in), "clean context must survive untouched")
}

func TestSanitizeCapsTheLength(t *testing.T) {
	got := service.Sanitize(strings.Repeat("a", service.MaxTicketDescription+500))

	assert.LessOrEqual(t, len(got), service.MaxTicketDescription+80)
	assert.Contains(t, got, "truncated")
}

// The subject rides on the same sanitizer, because a failed connect is exactly
// the error a developer would paste into it.
func TestCreateSanitizesBothFields(t *testing.T) {
	client := &fakeTickets{createOut: &servicenow.Ticket{Number: "CS0061234", SysID: "sys-1"}}
	support := newSupport(t, client)

	_, err := support.Create(context.Background(), service.TicketDraft{
		Subject:     "Cannot connect with mysql://root:letmein@orders-prod:3306",
		Description: "Tried mysql -u root -pletmein and it refused.",
		Context: service.TicketContext{
			Database:     "orders-prod",
			ErrorMessage: "Access denied for user 'root'@'10.1.2.3' (using password: YES) token=abc123",
		},
	})
	require.NoError(t, err)

	sent := client.created[0]
	assert.NotContains(t, sent.ShortDescription, "letmein")
	assert.NotContains(t, sent.Description, "letmein")
	assert.NotContains(t, sent.Description, "abc123")
	assert.Contains(t, sent.Description, "Database: orders-prod")
}

func TestFactsStateMembersWithoutAClusterType(t *testing.T) {
	client := &fakeTickets{createOut: &servicenow.Ticket{Number: "CS0061234", SysID: "sys-1"}}
	support := newSupport(t, client)

	_, err := support.Create(context.Background(), service.TicketDraft{
		Subject: "Cannot create database orders-dev",
		Context: service.TicketContext{Database: "orders-dev", Members: 1, ClusterType: "standalone"},
	})
	require.NoError(t, err)
	assert.Contains(t, client.created[0].Description, "Members: 1")
	assert.NotContains(t, client.created[0].Description, "Environment type:")
	assert.NotContains(t, client.created[0].Description, "standalone")

	client.created = nil
	_, err = support.Create(context.Background(), service.TicketDraft{
		Subject: "Cannot create database orders-prod",
		Context: service.TicketContext{Database: "orders-prod", Members: 3, ClusterType: "environment"},
	})
	require.NoError(t, err)
	assert.Contains(t, client.created[0].Description, "Members: 3")
	assert.NotContains(t, client.created[0].Description, "Environment type:")
}

func TestPortalURLFollowsTheStoredInstance(t *testing.T) {
	source := &fakeSource{creds: service.ServiceNowCredentials{
		URL:         "https://example.service-now.com",
		APIKey:      "api-key-value",
		ClientToken: "client-token-value",
	}}
	support := service.NewSupport(&fakeTickets{}, service.WithServiceNowSource(source))

	assert.Equal(t, "https://example.service-now.com/percona", support.PortalURL(context.Background()))
}

func TestPortalURLIsEmptyWithoutCredentials(t *testing.T) {
	support := service.NewSupport(nil, service.WithServiceNowSource(
		&fakeSource{err: service.ErrSupportUnavailable}))

	assert.Empty(t, support.PortalURL(context.Background()))
}

func TestPortalURLIsTheConfiguredInstanceWhenTheSecretIsMissing(t *testing.T) {
	support := service.NewSupport(nil,
		service.WithServiceNowSource(&fakeSource{err: service.ErrSupportUnavailable}),
		service.WithSupportPortalURL("https://example.service-now.com/percona"))

	assert.Equal(t, "https://example.service-now.com/percona", support.PortalURL(context.Background()))
}

// The portal link is the same for every ticket on the page, so it is resolved
// once for the page. Resolving it per ticket made a page of 25 tickets 25
// Secret reads.
func TestListResolvesThePortalOnceForThePage(t *testing.T) {
	source := &fakeSource{creds: service.ServiceNowCredentials{
		URL:         "https://example.service-now.com",
		APIKey:      "api-key-value",
		ClientToken: "client-token-value",
	}}
	client := &fakeTickets{searchOut: []servicenow.Ticket{
		{Number: "CS0061234", Description: "Reported by the Percona Database Service control plane."},
		{Number: "CS0061235", Description: "Reported by the Percona Database Service control plane."},
		{Number: "CS0061236", Description: "Reported by the Percona Database Service control plane."},
	}}
	support := service.NewSupport(client, service.WithServiceNowSource(source))

	ctx := context.WithValue(context.Background(), sourceCtxKey{}, "request-1")
	tickets, err := support.List(ctx, 25, 0)
	require.NoError(t, err)

	require.Len(t, tickets, 3)
	for _, ticket := range tickets {
		assert.Equal(t, "https://example.service-now.com/percona", ticket.PortalURL)
	}
	assert.Equal(t, 1, source.reads, "one credential read for the page, not one per ticket")
	assert.Equal(t, "request-1", source.lastCtx.Value(sourceCtxKey{}),
		"the request context reaches the credential read")
}

func TestConfiguredCarriesTheRequestContext(t *testing.T) {
	source := &fakeSource{creds: service.ServiceNowCredentials{
		URL:         "https://example.service-now.com",
		APIKey:      "api-key-value",
		ClientToken: "client-token-value",
	}}
	support := service.NewSupport(nil, service.WithServiceNowSource(source))

	ctx := context.WithValue(context.Background(), sourceCtxKey{}, "request-2")
	assert.True(t, support.Configured(ctx))
	assert.Equal(t, "request-2", source.lastCtx.Value(sourceCtxKey{}))
}

// A Secret is written by Helm, by kubectl, and by the connections panel, so the
// instance it names is checked again here rather than only where it is pasted.
func TestAPlaintextStoredInstanceSendsNothing(t *testing.T) {
	source := &fakeSource{creds: service.ServiceNowCredentials{
		URL:         "http://pds-servicenow:8080",
		APIKey:      "api-key-value",
		ClientToken: "client-token-value",
	}}
	support := service.NewSupport(nil, service.WithServiceNowSource(source))

	assert.False(t, support.Configured(context.Background()))

	_, err := support.List(context.Background(), 25, 0)
	require.ErrorIs(t, err, service.ErrSupportInstance)

	_, err = support.Create(context.Background(), service.TicketDraft{
		Subject:     "Cannot create database orders-prod",
		Description: "It failed.",
	})
	require.ErrorIs(t, err, service.ErrSupportInstance)

	_, err = support.Get(context.Background(), "CS0061234")
	require.ErrorIs(t, err, service.ErrSupportInstance)

	assert.Empty(t, support.PortalURL(context.Background()),
		"an unusable instance with no configured URL leaves no portal link")
}

func TestListAsksTheInstanceForOurTicketsOnly(t *testing.T) {
	client := &fakeTickets{
		searchOut: []servicenow.Ticket{
			{
				Number:      "CS0062783",
				State:       "New",
				OpenedAt:    supportNow,
				Description: "Reported by the Percona Database Service control plane.",
			},
			{
				Number:      "CS0062782",
				State:       "New",
				Description: "Reported by the Percona Database Service control plane.",
			},
		},
	}
	support := newSupport(t, client)

	tickets, err := support.List(context.Background(), 10, 0)
	require.NoError(t, err)

	require.Len(t, tickets, 2)
	assert.Equal(t, "CS0062783", tickets[0].Number)
	assert.Equal(t, "https://example.service-now.com/percona", tickets[0].PortalURL)

	require.Len(t, client.searches, 1, "one request, whatever the size of the account")
	assert.Equal(t, []string{
		"Reported by the Percona Database Service control plane.",
		"Reported by the Percona Basecamp control plane.", // allow-legacy-name
	}, client.searches[0].match,
		"one request covers both markers, so a case opened before the rename is still found")
	assert.Equal(t, 10, client.searches[0].limit)
	assert.Equal(t, 0, client.searches[0].offset)
	assert.Empty(t, client.lists, "the account is not walked any more")
	assert.Empty(t, client.asked, "the search already carries what the list shows")
}

func TestListPagesThroughTheInstance(t *testing.T) {
	client := &fakeTickets{}
	support := newSupport(t, client)

	_, err := support.List(context.Background(), 5, 10)
	require.NoError(t, err)

	require.Len(t, client.searches, 1)
	assert.Equal(t, 5, client.searches[0].limit)
	assert.Equal(t, 10, client.searches[0].offset)
}

// The instance filters, but a case that does not carry the marker must not slip
// through on a loose match.
func TestListStillChecksTheMarkerOnWhatCameBack(t *testing.T) {
	client := &fakeTickets{
		searchOut: []servicenow.Ticket{
			{Number: "CS0062783", Description: "Reported by the Percona Database Service control plane."},
			{Number: "CS0060001", Description: "A case opened in the Percona support portal."},
		},
	}
	support := newSupport(t, client)

	tickets, err := support.List(context.Background(), 10, 0)
	require.NoError(t, err)

	require.Len(t, tickets, 1)
	assert.Equal(t, "CS0062783", tickets[0].Number)
}

// Resolving the portal link is a Secret read, so the page carries the one the
// list already resolved rather than making the handler ask again.
func TestListPageCarriesThePortalItAlreadyResolved(t *testing.T) {
	source := &fakeSource{creds: service.ServiceNowCredentials{
		URL:         "https://example.service-now.com",
		APIKey:      "api-key-value",
		ClientToken: "client-token-value",
	}}
	client := &fakeTickets{
		searchOut: []servicenow.Ticket{
			{Number: "CS0062783", Description: "Reported by the Percona Database Service control plane."},
		},
	}
	support := service.NewSupport(client, service.WithServiceNowSource(source))

	page, err := support.ListPage(context.Background(), 25, 0)
	require.NoError(t, err)

	assert.Equal(t, "https://example.service-now.com/percona", page.PortalURL)
	require.Len(t, page.Tickets, 1)
	assert.Equal(t, 1, source.reads, "one credential read for the page")
}

// A refused table read and a rejected key are different faults with different
// fixes, and the instance says which by status.
func TestListSeparatesARefusedReadFromARejectedKey(t *testing.T) {
	denied := &servicenow.APIError{
		Method: "GET",
		Path:   servicenow.SearchPath,
		Status: http.StatusForbidden,
	}
	client := &fakeTickets{searchErr: denied}
	_, err := newSupport(t, client).List(context.Background(), 25, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, service.ErrSupportSearchDenied)

	rejected := &servicenow.APIError{
		Method: "GET",
		Path:   servicenow.SearchPath,
		Status: http.StatusUnauthorized,
	}
	client = &fakeTickets{searchErr: rejected}
	_, err = newSupport(t, client).List(context.Background(), 25, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, servicenow.ErrUnauthorized)
	assert.NotErrorIs(t, err, service.ErrSupportSearchDenied,
		"a rejected key is not a missing permission")
}
