package servicenow_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/servicenow"
)

const (
	testAPIKey      = "sn-api-key-value"
	testClientToken = "sn-client-token-value"
)

// capture records what the fake ServiceNow received, so a test can assert the
// two auth carriers and the fixed field values without a live account.
type capture struct {
	Method      string
	Path        string
	Query       string
	APIKey      string
	ContentType string
	Body        map[string]any
	Raw         []byte
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("fixtures", name))
	require.NoError(t, err)

	return body
}

// newFakeServiceNow serves one canned response and records the request.
func newFakeServiceNow(t *testing.T, status int, body []byte) (*servicenow.Client, *capture) {
	t.Helper()

	got := &capture{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Method = r.Method
		got.Path = r.URL.Path
		got.Query = r.URL.RawQuery
		got.APIKey = r.Header.Get("x-sn-apikey")
		got.ContentType = r.Header.Get("Content-Type")

		raw, _ := io.ReadAll(r.Body)
		got.Raw = raw
		got.Body = nil
		_ = json.Unmarshal(raw, &got.Body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	return newClient(t, srv.URL), got
}

func newClient(t *testing.T, baseURL string) *servicenow.Client {
	t.Helper()

	client, err := servicenow.New(servicenow.Options{
		BaseURL:     baseURL,
		APIKey:      testAPIKey,
		ClientToken: testClientToken,
		// A test must not wait out a backoff.
		Sleep: func(time.Duration) {},
	})
	require.NoError(t, err)

	return client
}

func TestNewRejectsUnusableOptions(t *testing.T) {
	tests := []struct {
		name string
		opts servicenow.Options
		want string
	}{
		{
			name: "blank base URL",
			opts: servicenow.Options{APIKey: "k", ClientToken: "t"},
			want: "base URL",
		},
		{
			name: "base URL without scheme",
			opts: servicenow.Options{BaseURL: "example.service-now.com", APIKey: "k", ClientToken: "t"},
			want: "scheme",
		},
		{
			name: "missing API key",
			opts: servicenow.Options{BaseURL: "https://example.service-now.com", ClientToken: "t"},
			want: "API key",
		},
		{
			name: "missing client token",
			opts: servicenow.Options{BaseURL: "https://example.service-now.com", APIKey: "k"},
			want: "client token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := servicenow.New(tt.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestPortalURLIsEmptyWithoutAnInstance(t *testing.T) {
	var client *servicenow.Client

	assert.Empty(t, client.PortalURL())
}

func TestPortalURLFollowsTheConfiguredInstance(t *testing.T) {
	tests := map[string]string{
		"https://example.service-now.com":      "https://example.service-now.com/percona",
		"https://dev.example.service-now.com":  "https://dev.example.service-now.com/percona",
		"https://dev.example.service-now.com/": "https://dev.example.service-now.com/percona",
	}

	for base, want := range tests {
		t.Run(base, func(t *testing.T) {
			assert.Equal(t, want, newClient(t, base).PortalURL())
		})
	}
}

func TestTicketURLTargetsTheCaseRecord(t *testing.T) {
	assert.Equal(t,
		"https://example.service-now.com/sn_customerservice_case.do?sys_id=sys-1",
		servicenow.TicketURL("https://example.service-now.com", "sn_customerservice_case", "sys-1"))
	assert.Empty(t, servicenow.TicketURL("https://example.service-now.com", "", "sys-1"))
	assert.Empty(t, servicenow.TicketURL("https://example.service-now.com", "sn_customerservice_case", ""))
}

func TestCreateTicketPostsTheConnectorContract(t *testing.T) {
	client, got := newFakeServiceNow(t, http.StatusOK, fixture(t, "create.json"))

	ticket, err := client.CreateTicket(context.Background(), servicenow.CreateTicketInput{
		ShortDescription: "Cannot create database orders-prod",
		Description:      "Profile: prod",
	})
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/api/pellc/percona_connector/create", got.Path)
	assert.Equal(t, testAPIKey, got.APIKey, "the API key rides in the x-sn-apikey header")
	assert.Equal(t, "application/json", got.ContentType)

	assert.Equal(t, testClientToken, got.Body["client_token"], "the token rides in the body")
	assert.Equal(t, "Cannot create database orders-prod", got.Body["short_description"])
	assert.Equal(t, "Profile: prod", got.Body["description"])
	assert.Equal(t, "CASE", got.Body["ticket_type"], "ticket_type is always CASE")
	assert.Equal(t, "MySQL", got.Body["u_main_technology"], "MySQL is the default technology")

	assert.Equal(t, "3f1c9a2e6b7d4e8fa0c1d2e3f4a5b6c7", ticket.SysID)
	assert.Equal(t, "sn_customerservice_case", ticket.TableName)
	assert.Empty(t, ticket.Number, "this connector answer carries no number yet")
}

func TestCreateTicketPostsPostgreSQLAsTheTechnology(t *testing.T) {
	client, got := newFakeServiceNow(t, http.StatusOK, fixture(t, "create.json"))

	_, err := client.CreateTicket(context.Background(), servicenow.CreateTicketInput{
		ShortDescription: "Cannot create database reporting-dev",
		Description:      "Profile: dev",
		MainTechnology:   servicenow.TechnologyFor("postgresql"),
	})
	require.NoError(t, err)

	assert.Equal(t, "PostgreSQL", got.Body["u_main_technology"])
}

func TestCreateTicketSendsMongoDBAsTheMainTechnology(t *testing.T) {
	client, got := newFakeServiceNow(t, http.StatusOK, fixture(t, "create.json"))

	_, err := client.CreateTicket(context.Background(), servicenow.CreateTicketInput{
		ShortDescription: "Cannot create database orders",
		Description:      "Profile: dev",
		MainTechnology:   servicenow.TechnologyFor("mongodb"),
	})
	require.NoError(t, err)

	assert.Equal(t, "MongoDB", got.Body["u_main_technology"])
}

func TestTechnologyForMapsTheEngine(t *testing.T) {
	assert.Equal(t, "MySQL", servicenow.TechnologyFor(""))
	assert.Equal(t, "MySQL", servicenow.TechnologyFor("mysql"))
	assert.Equal(t, "PostgreSQL", servicenow.TechnologyFor("postgresql"))
	assert.Equal(t, "PostgreSQL", servicenow.TechnologyFor("Postgres"))
	assert.Equal(t, "MongoDB", servicenow.TechnologyFor("mongodb"))
	assert.Equal(t, "MongoDB", servicenow.TechnologyFor(" MongoDB "))
	assert.Equal(t, "MySQL", servicenow.TechnologyFor("oracle"), "an engine PDS does not know routes as MySQL")
}

func TestCreateTicketReadsTheNumberWhenTheAnswerCarriesOne(t *testing.T) {
	client, _ := newFakeServiceNow(t, http.StatusOK, fixture(t, "create-with-number.json"))

	ticket, err := client.CreateTicket(context.Background(), servicenow.CreateTicketInput{
		ShortDescription: "Cannot create database orders-prod",
		Description:      "Profile: prod",
	})
	require.NoError(t, err)

	assert.Equal(t, "CS0061234", ticket.Number)
}

func TestCreateTicketRejectsABlankSubject(t *testing.T) {
	client, got := newFakeServiceNow(t, http.StatusOK, fixture(t, "create.json"))

	_, err := client.CreateTicket(context.Background(), servicenow.CreateTicketInput{
		ShortDescription: "   ",
		Description:      "Profile: prod",
	})
	require.Error(t, err)
	assert.Empty(t, got.Method, "nothing is posted for a request the client refuses")
}

// A 200 with success false is how the connector reports a rejected request, so
// the status alone cannot decide whether a call worked.
func TestCreateTicketFailsOnARejectedResult(t *testing.T) {
	client, _ := newFakeServiceNow(t, http.StatusOK, fixture(t, "create-rejected.json"))

	_, err := client.CreateTicket(context.Background(), servicenow.CreateTicketInput{
		ShortDescription: "Cannot create database orders-prod",
		Description:      "Profile: prod",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client_token does not match an active account")
}

func TestListTicketsPostsPagingAsQueryParameters(t *testing.T) {
	client, got := newFakeServiceNow(t, http.StatusOK, fixture(t, "ticket-list.json"))

	tickets, err := client.ListTickets(context.Background(), 10, 20)
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/api/pellc/percona_connector/tickets", got.Path)
	assert.Equal(t, "sysparm_limit=10&sysparm_offset=20", got.Query)
	assert.Equal(t, testClientToken, got.Body["client_token"])

	require.Len(t, tickets, 2)
	assert.Equal(t, "CS0061234", tickets[0].Number)
	assert.Equal(t, "Awaiting Percona", tickets[0].State)
	assert.Equal(t, "MySQL", tickets[0].MainTechnology)
	assert.Equal(t, "CS0061198", tickets[1].Number)
	assert.Equal(t, "Resolved", tickets[1].State)

	opened := time.Date(2026, 8, 20, 9, 12, 33, 0, time.UTC)
	assert.Equal(t, opened, tickets[0].OpenedAt, "ServiceNow timestamps are UTC without a zone")
	assert.True(t, tickets[0].ResolvedAt.IsZero(), "an empty timestamp is not a zero date")
	assert.False(t, tickets[1].ResolvedAt.IsZero())
}

// The connector answers a list either as an array or wrapped in an object. Both
// are read, because a shape change on the Percona side should not blank the page.
func TestListTicketsReadsAWrappedResult(t *testing.T) {
	client, _ := newFakeServiceNow(t, http.StatusOK, fixture(t, "ticket-list-wrapped.json"))

	tickets, err := client.ListTickets(context.Background(), 10, 0)
	require.NoError(t, err)

	require.Len(t, tickets, 1)
	assert.Equal(t, "CS0061234", tickets[0].Number)
}

func TestListTicketsClampsPaging(t *testing.T) {
	client, got := newFakeServiceNow(t, http.StatusOK, fixture(t, "ticket-list.json"))

	_, err := client.ListTickets(context.Background(), 0, -5)
	require.NoError(t, err)

	assert.Equal(t, "sysparm_limit=25&sysparm_offset=0", got.Query,
		"a zero limit means the default page, and an offset cannot be negative")
}

func TestGetTicketPostsTheNumber(t *testing.T) {
	client, got := newFakeServiceNow(t, http.StatusOK, fixture(t, "ticket-details.json"))

	ticket, err := client.GetTicket(context.Background(), "CS0061234")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/api/pellc/percona_connector/ticket_details", got.Path)
	assert.Equal(t, testClientToken, got.Body["client_token"])
	assert.Equal(t, "CS0061234", got.Body["ticket_number"])
	assert.NotContains(t, got.Body, "ticket_id")

	assert.Equal(t, "CS0061234", ticket.Number)
	assert.Equal(t, "Awaiting Percona", ticket.State)
	assert.Equal(t, "3 - Moderate", ticket.Priority)
	assert.Equal(t, "Example Corp", ticket.Account)
	assert.Equal(t, "Cannot create database orders-prod", ticket.ShortDescription)
	assert.Equal(t, "MySQL", ticket.MainTechnology)

	require.Len(t, ticket.Comments, 2)
	assert.Equal(t, "percona.support", ticket.Comments[0].Author)
	assert.Equal(t, "Events attached.", ticket.Comments[1].Body)
	assert.Equal(t, time.Date(2026, 8, 21, 14, 2, 10, 0, time.UTC), ticket.Comments[1].At)
}

func TestGetTicketByIDPostsTheSysID(t *testing.T) {
	client, got := newFakeServiceNow(t, http.StatusOK, fixture(t, "ticket-details.json"))

	ticket, err := client.GetTicketByID(context.Background(), "3f1c9a2e6b7d4e8fa0c1d2e3f4a5b6c7")
	require.NoError(t, err)

	assert.Equal(t, "/api/pellc/percona_connector/ticket_details", got.Path)
	assert.Equal(t, "3f1c9a2e6b7d4e8fa0c1d2e3f4a5b6c7", got.Body["ticket_id"])
	assert.NotContains(t, got.Body, "ticket_number")
	assert.Equal(t, "CS0061234", ticket.Number)
}

// The connector nests the case under `ticket_details` and keeps only the id and
// the table at the top level. Reading the top level alone answers with a ticket
// that has an id and no number, which is a ticket nobody can quote.
func TestGetTicketReadsTheNestedRecord(t *testing.T) {
	client, _ := newFakeServiceNow(t, http.StatusOK, []byte(`{"result":{
		"success": true,
		"ticket_id": "b36ce9290fcb8310ad20a8d530d1b2a9",
		"table_name": "sn_customerservice_case",
		"error_message": "",
		"ticket_details": {
			"number": "CS0062779",
			"sys_id": "b36ce9290fcb8310ad20a8d530d1b2a9",
			"state": "New",
			"priority": "P4",
			"short_description": "Cannot create database orders-prod",
			"description": "Reported by the Percona Database Service control plane.",
			"opened_at": "2026-08-31 16:05:18",
			"comments": "2026-08-31 16:05:18 - Percona GAS User (Additional comments)\nevents attached\n"
		}
	}}`))

	ticket, err := client.GetTicket(context.Background(), "CS0062779")
	require.NoError(t, err)

	assert.Equal(t, "CS0062779", ticket.Number)
	assert.Equal(t, "b36ce9290fcb8310ad20a8d530d1b2a9", ticket.SysID)
	assert.Equal(t, "sn_customerservice_case", ticket.TableName, "the table is only on the outer answer")
	assert.Equal(t, "New", ticket.State)
	assert.Equal(t, "P4", ticket.Priority)
	assert.Equal(t, "Cannot create database orders-prod", ticket.ShortDescription)
	assert.Equal(t, time.Date(2026, 8, 31, 16, 5, 18, 0, time.UTC), ticket.OpenedAt)
	require.Len(t, ticket.Comments, 1)
}

func TestGetTicketKeepsBlankLineHeaderLikeTextInTheJournalBody(t *testing.T) {
	client, _ := newFakeServiceNow(t, http.StatusOK, []byte(`{"result":{
		"success": true,
		"ticket_details": {
			"number": "CS0062779",
			"comments": "2026-08-31 16:05:18 - Percona GAS User (Additional comments)\nEvents attached.\n\nSee the environment note.\n\n2026-08-31 17:42:03 - Dana Platform (Additional comments)\nPlease let me know if you need more detail.\n"
		}
	}}`))

	ticket, err := client.GetTicket(context.Background(), "CS0062779")
	require.NoError(t, err)

	require.Len(t, ticket.Comments, 1)
	assert.Equal(t, time.Date(2026, 8, 31, 16, 5, 18, 0, time.UTC), ticket.Comments[0].At)
	assert.Equal(t, "Percona GAS User", ticket.Comments[0].Author)
	assert.Equal(t, "Additional comments", ticket.Comments[0].Label)
	assert.Equal(t, "Events attached.\n\nSee the environment note.\n\n2026-08-31 17:42:03 - Dana Platform (Additional comments)\nPlease let me know if you need more detail.", ticket.Comments[0].Body)
}

func TestGetTicketKeepsAHeaderLikeBodyLineInTheMessage(t *testing.T) {
	client, _ := newFakeServiceNow(t, http.StatusOK, []byte(`{"result":{
		"success": true,
		"ticket_details": {
			"number": "CS0062779",
			"comments": "2026-08-31 16:05:18 - Percona GAS User (Additional comments)\nThe operator event contains this line:\n2026-08-31 17:42:03 - Dana Platform (Additional comments)\nIt is not a separate journal entry."
		}
	}}`))

	ticket, err := client.GetTicket(context.Background(), "CS0062779")
	require.NoError(t, err)

	require.Len(t, ticket.Comments, 1)
	assert.Equal(t, "Percona GAS User", ticket.Comments[0].Author)
	assert.Equal(t, "Additional comments", ticket.Comments[0].Label)
	assert.Equal(t, "The operator event contains this line:\n2026-08-31 17:42:03 - Dana Platform (Additional comments)\nIt is not a separate journal entry.", ticket.Comments[0].Body)
}

func TestGetTicketPreservesAnAmbiguousJournalCommentBlob(t *testing.T) {
	client, _ := newFakeServiceNow(t, http.StatusOK, []byte(`{"result":{
		"success": true,
		"ticket_details": {
			"number": "CS0062779",
			"comments": "Percona GAS User (Additional comments)\nEvents attached."
		}
	}}`))

	ticket, err := client.GetTicket(context.Background(), "CS0062779")
	require.NoError(t, err)

	require.Len(t, ticket.Comments, 1)
	assert.Empty(t, ticket.Comments[0].Author)
	assert.Empty(t, ticket.Comments[0].Label)
	assert.Equal(t, "Percona GAS User (Additional comments)\nEvents attached.", ticket.Comments[0].Body)
}

// A build that answers with the record at the top level has to keep working.
func TestGetTicketReadsAFlatRecord(t *testing.T) {
	client, _ := newFakeServiceNow(t, http.StatusOK, fixture(t, "ticket-details-flat.json"))

	ticket, err := client.GetTicket(context.Background(), "CS0061234")
	require.NoError(t, err)

	assert.Equal(t, "CS0061234", ticket.Number)
	assert.Equal(t, "Awaiting Percona", ticket.State)
	assert.Len(t, ticket.Comments, 2)
}

// An empty `ticket_details` is not a record, so it must not flatten over the
// outer answer and hand back a ticket with nothing but an echoed id.
func TestGetTicketMapsBlankDetailsToNotFound(t *testing.T) {
	client, _ := newFakeServiceNow(t, http.StatusOK,
		[]byte(`{"result":{"success":true,"ticket_details":{}}}`))

	_, err := client.GetTicket(context.Background(), "CS0000000")
	require.Error(t, err)
	assert.ErrorIs(t, err, servicenow.ErrNotFound)
}

func TestGetTicketRejectsABlankNumber(t *testing.T) {
	client, got := newFakeServiceNow(t, http.StatusOK, fixture(t, "ticket-details.json"))

	_, err := client.GetTicket(context.Background(), "  ")
	require.Error(t, err)
	assert.Empty(t, got.Method)
}

func TestGetTicketMapsAnEmptyResultToNotFound(t *testing.T) {
	client, _ := newFakeServiceNow(t, http.StatusOK, []byte(`{"result":{}}`))

	_, err := client.GetTicket(context.Background(), "CS0000000")
	require.Error(t, err)
	assert.ErrorIs(t, err, servicenow.ErrNotFound)
}

func TestReplyTicketTransportErrorNamesPatchMethod(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	baseURL := srv.URL
	srv.Close()

	client := newClient(t, baseURL)

	err := client.ReplyTicket(context.Background(), servicenow.ReplyTicketInput{
		TableName: "sn_customerservice_case",
		SysID:     "3f1c0b1a4f8b4a10",
		Body:      "The operator events are attached.",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), http.MethodPatch)
	assert.NotContains(t, err.Error(), "POST")
}

func TestReplyTicketPostsAPublicComment(t *testing.T) {
	client, got := newFakeServiceNow(t, http.StatusNoContent, nil)

	err := client.ReplyTicket(context.Background(), servicenow.ReplyTicketInput{
		TableName: "sn_customerservice_case",
		SysID:     "3f1c0b1a4f8b4a10",
		Body:      "The operator events are attached.",
	})
	require.NoError(t, err)

	assert.Equal(t, http.MethodPatch, got.Method)
	assert.Equal(t, "/api/now/table/sn_customerservice_case/3f1c0b1a4f8b4a10", got.Path)
	assert.Equal(t, testAPIKey, got.APIKey)
	assert.Equal(t, testClientToken, got.Body["client_token"])
	assert.Equal(t, "The operator events are attached.", got.Body["comments"])
	assert.NotContains(t, got.Body, "work_notes")
}

func TestUploadAttachmentPostsMultipart(t *testing.T) {
	client, got := newFakeServiceNow(t, http.StatusCreated, fixture(t, "attachment.json"))

	err := client.UploadAttachment(context.Background(),
		"sn_customerservice_case",
		"3f1c9a2e6b7d4e8fa0c1d2e3f4a5b6c7",
		strings.NewReader("operator event log\n"),
		"operator-events.txt",
	)
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/api/now/attachment/upload", got.Path)
	assert.Equal(t, testAPIKey, got.APIKey)

	mediaType, params, err := mime.ParseMediaType(got.ContentType)
	require.NoError(t, err)
	require.Equal(t, "multipart/form-data", mediaType)

	fields, files := readMultipart(t, got.Raw, params["boundary"])

	assert.Equal(t, "sn_customerservice_case", fields["table_name"])
	assert.Equal(t, "3f1c9a2e6b7d4e8fa0c1d2e3f4a5b6c7", fields["table_sys_id"])
	assert.Equal(t, testClientToken, fields["client_token"], "auth on every request, upload included")
	assert.Equal(t, "operator event log\n", files["uploadFile"])
}

func TestUploadAttachmentRejectsBlankTargets(t *testing.T) {
	client, got := newFakeServiceNow(t, http.StatusCreated, fixture(t, "attachment.json"))

	err := client.UploadAttachment(context.Background(), "", "sys", strings.NewReader("x"), "f.txt")
	require.Error(t, err)

	err = client.UploadAttachment(context.Background(), "table", "", strings.NewReader("x"), "f.txt")
	require.Error(t, err)

	err = client.UploadAttachment(context.Background(), "table", "sys", strings.NewReader("x"), " ")
	require.Error(t, err)

	assert.Empty(t, got.Method)
}

func TestUnauthorizedMapsToASentinel(t *testing.T) {
	client, _ := newFakeServiceNow(t, http.StatusUnauthorized, fixture(t, "error-unauthorized.json"))

	_, err := client.ListTickets(context.Background(), 10, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, servicenow.ErrUnauthorized)

	var apiErr *servicenow.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusUnauthorized, apiErr.Status)
	assert.Contains(t, apiErr.Message, "User Not Authenticated")
}

func TestErrorsNeverCarryTheCredentials(t *testing.T) {
	client, _ := newFakeServiceNow(t, http.StatusInternalServerError, []byte(`{"error":{"message":"boom"}}`))

	_, err := client.ListTickets(context.Background(), 10, 0)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testAPIKey)
	assert.NotContains(t, err.Error(), testClientToken)
}

// Retry covers the failure a support ticket must survive: a ServiceNow instance
// that answers 503 while it rolls, or a connection that drops mid flight.
func TestRetriesServerErrorsThenSucceeds(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, "create-with-number.json"))
	}))
	t.Cleanup(srv.Close)

	ticket, err := newClient(t, srv.URL).CreateTicket(context.Background(), servicenow.CreateTicketInput{
		ShortDescription: "Cannot create database orders-prod",
		Description:      "Profile: prod",
	})
	require.NoError(t, err)

	assert.Equal(t, "CS0061234", ticket.Number)
	assert.Equal(t, int32(3), attempts.Load())
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	_, err := newClient(t, srv.URL).ListTickets(context.Background(), 10, 0)
	require.Error(t, err)
	assert.Equal(t, int32(1), attempts.Load(), "a wrong key will be wrong on the next attempt too")
}

func TestGivesUpAfterTheAttemptBudget(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	_, err := newClient(t, srv.URL).ListTickets(context.Background(), 10, 0)
	require.Error(t, err)
	assert.Equal(t, servicenow.DefaultMaxAttempts, int(attempts.Load()))
}

func TestCanceledContextStopsRetrying(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newClient(t, srv.URL).ListTickets(ctx, 10, 0)
	require.Error(t, err)
	assert.Zero(t, attempts.Load())
}

// readMultipart splits a recorded multipart body into plain fields and files.
func readMultipart(t *testing.T, raw []byte, boundary string) (map[string]string, map[string]string) {
	t.Helper()
	require.NotEmpty(t, boundary)

	fields := map[string]string{}
	files := map[string]string{}

	reader := multipart.NewReader(strings.NewReader(string(raw)), boundary)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)

		body, err := io.ReadAll(part)
		require.NoError(t, err)

		if part.FileName() == "" {
			fields[part.FormName()] = string(body)
		} else {
			files[part.FormName()] = string(body)
		}
	}

	return fields, files
}

func TestListTicketsReportsAnOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A body past the cap should report ErrResponseTooLarge, not a JSON decode error.
		padding := strings.Repeat("x", 9<<20)
		_, _ = io.WriteString(w, `{"result":{"tickets":[{"number":"CS0061234","description":"`+padding+`"}]}}`)
	}))
	defer server.Close()

	client, err := servicenow.New(servicenow.Options{
		BaseURL:     server.URL,
		APIKey:      "api-key",
		ClientToken: "client-token",
		Sleep:       func(time.Duration) {},
	})
	require.NoError(t, err)

	_, err = client.ListTickets(context.Background(), servicenow.MaxPageSize, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, servicenow.ErrResponseTooLarge)
	assert.NotContains(t, err.Error(), "unexpected EOF",
		"a body past the cap reads as a size problem, not as malformed json")
}

func TestListTicketsReadsAPageLargerThanAMegabyte(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		rows := make([]string, servicenow.MaxPageSize)
		// Roughly what the connector returns per case, which put a full scan
		// page over the old one-megabyte cap.
		body := strings.Repeat("y", 2048)
		for i := range rows {
			rows[i] = `{"number":"CS006` + strconv.Itoa(1000+i) + `","description":"` + body + `"}`
		}
		_, _ = io.WriteString(w, `{"result":{"tickets":[`+strings.Join(rows, ",")+`]}}`)
	}))
	defer server.Close()

	client, err := servicenow.New(servicenow.Options{
		BaseURL:     server.URL,
		APIKey:      "api-key",
		ClientToken: "client-token",
		Sleep:       func(time.Duration) {},
	})
	require.NoError(t, err)

	tickets, err := client.ListTickets(context.Background(), servicenow.MaxPageSize, 0)
	require.NoError(t, err)
	assert.Len(t, tickets, servicenow.MaxPageSize)
}

// countingBody reports how much of the response the client actually read.
type countingBody struct {
	read   int
	closed bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	b.read += len(p)

	return len(p), nil
}

func (b *countingBody) Close() error {
	b.closed = true

	return nil
}

type bodyTransport struct{ body *countingBody }

func (t *bodyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       t.body,
	}, nil
}

// The cap exists so an oversized answer cannot cost unbounded time and memory.
// Draining the rest of it on the way out would spend exactly what the cap saves.
func TestListTicketsDoesNotDrainAnOversizedResponse(t *testing.T) {
	body := &countingBody{}
	client, err := servicenow.New(servicenow.Options{
		BaseURL:     "https://example.service-now.com",
		APIKey:      "api-key",
		ClientToken: "client-token",
		HTTP:        &http.Client{Transport: &bodyTransport{body: body}},
		Sleep:       func(time.Duration) {},
	})
	require.NoError(t, err)

	_, err = client.ListTickets(context.Background(), servicenow.MaxPageSize, 0)
	require.ErrorIs(t, err, servicenow.ErrResponseTooLarge)

	assert.True(t, body.closed, "the body is still closed")
	assert.Less(t, body.read, 16<<20,
		"reading stops near the cap rather than draining an endless body")
}

func TestSearchTicketsFiltersOnTheInstance(t *testing.T) {
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":[
			{"number":"CS0062783","sys_id":"sys-a","state":"New","short_description":"Support request",
			 "description":"Reported by the Percona Database Service control plane.","opened_at":"2026-09-03 23:10:25",
			 "sys_updated_on":"2026-09-03 23:10:25","u_main_technology":"MySQL"},
			{"number":"CS0062782","sys_id":"sys-b","state":"New","short_description":"Support request",
			 "description":"Reported by the Percona Database Service control plane.","opened_at":"2026-09-03 20:52:12",
			 "sys_updated_on":"2026-09-03 20:52:12","u_main_technology":"MySQL"}]}`)
	}))
	defer server.Close()

	client, err := servicenow.New(servicenow.Options{
		BaseURL:     server.URL,
		APIKey:      "api-key",
		ClientToken: "client-token",
		Sleep:       func(time.Duration) {},
	})
	require.NoError(t, err)

	tickets, err := client.SearchTickets(context.Background(),
		[]string{"Reported by the Percona Database Service control plane."}, 25, 0)
	require.NoError(t, err)

	require.Len(t, tickets, 2)
	assert.Equal(t, "CS0062783", tickets[0].Number)
	assert.Equal(t, "Support request", tickets[0].ShortDescription)
	assert.Equal(t, servicenow.CaseTable, tickets[0].TableName,
		"a row from the case table is a case, which is what a reply needs to know")

	require.NotNil(t, seen)
	assert.Equal(t, http.MethodGet, seen.Method)
	assert.Equal(t, "/api/now/table/"+servicenow.CaseTable, seen.URL.Path)
	assert.Equal(t, "api-key", seen.Header.Get("x-sn-apikey"))

	query := seen.URL.Query()
	assert.Contains(t, query.Get("sysparm_query"),
		"descriptionLIKEReported by the Percona Database Service control plane.",
		"the instance does the filtering, not the adapter")
	assert.Contains(t, query.Get("sysparm_query"), "ORDERBYDESCsys_created_on")
	assert.Equal(t, "25", query.Get("sysparm_limit"))
	assert.Equal(t, "0", query.Get("sysparm_offset"))
	assert.Equal(t, "true", query.Get("sysparm_display_value"),
		"reference fields have to arrive readable, not as record ids")

	// The table has far more columns than a ticket row shows, and every one of
	// them would be paid for on the wire and against the response cap.
	fields := strings.Split(query.Get("sysparm_fields"), ",")
	assert.Subset(t, fields, []string{
		"number", "sys_id", "state", "short_description", "description",
		"opened_at", "sys_updated_on",
	})
	assert.NotContains(t, fields, "", "the field list is not empty")
}

func TestSearchTicketsNeedsSomethingToMatch(t *testing.T) {
	client, err := servicenow.New(servicenow.Options{
		BaseURL:     "https://example.service-now.com",
		APIKey:      "api-key",
		ClientToken: "client-token",
	})
	require.NoError(t, err)

	_, err = client.SearchTickets(context.Background(), []string{"  ", ""}, 25, 0)
	require.Error(t, err)
}

func TestSearchTicketsMatchesEveryMarker(t *testing.T) {
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":[]}`)
	}))
	defer server.Close()

	client, err := servicenow.New(servicenow.Options{
		BaseURL:     server.URL,
		APIKey:      "api-key",
		ClientToken: "client-token",
		Sleep:       func(time.Duration) {},
	})
	require.NoError(t, err)

	_, err = client.SearchTickets(context.Background(), []string{
		"Reported by the Percona Database Service control plane.",
		"Reported by the Percona Basecamp control plane.", // allow-legacy-name
	}, 25, 0)
	require.NoError(t, err)

	require.NotNil(t, seen)
	assert.Equal(t,
		"descriptionLIKEReported by the Percona Database Service control plane."+
			"^ORdescriptionLIKEReported by the Percona Basecamp control plane."+ // allow-legacy-name
			"^ORDERBYDESCsys_created_on",
		seen.URL.Query().Get("sysparm_query"),
		"one request covers both markers, so a case opened before the rename is still found")
}
