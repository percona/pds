package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/servicenow"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/testutil"
)

// Without the Secret there is no ServiceNow account to open a case against, so
// every support route answers 501 and the UI hides its actions.
func TestSupportAnswers501WithoutTheSecret(t *testing.T) {
	srv := newTestServerWithoutEverest(t)

	tests := []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "create", method: http.MethodPost, target: "/api/support/tickets", body: `{"subject":"x","description":"y"}`},
		{name: "list", method: http.MethodGet, target: "/api/support/tickets"},
		{name: "details", method: http.MethodGet, target: "/api/support/tickets/CS0061234"},
		{name: "reply", method: http.MethodPost, target: "/api/support/tickets/CS0061234/replies", body: `{"body":"I have more information."}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}

			rec := do(t, srv, tt.method, tt.target, body)

			require.Equal(t, http.StatusNotImplemented, rec.Code, "body: %s", rec.Body.String())

			decoded := decodeBody(t, rec)
			assert.Equal(t, "ErrNotImplemented", decoded["code"])
			assert.Contains(t, decoded["message"], "Support")
		})
	}
}

// Tokens alone do not open support: without an instance URL there is nowhere
// to send a case, and no portal to link to.
func TestSupportAnswers501WithoutAnInstanceURL(t *testing.T) {
	store := &memStore{secrets: map[string]map[string]string{
		"pds/pds-servicenow": {"SN_CLIENT_TOKEN": "client-token"},
	}}
	conns := service.NewConnections(store, service.ConnectionsOptions{
		Namespace:            "pds",
		ServiceNowSecretName: "pds-servicenow",
		EnvServiceNowAPIKey:  "env-api-key",
	}, service.WithConnectionsTransport(testutil.AcceptingProvider()))
	srv := api.NewServer(api.Deps{
		Config:      config.Config{Addr: ":8080", Env: config.EnvDev, EverestNamespace: "pds", HubOperatorToken: testHubOperatorToken},
		Logger:      log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:        auth.NewStub(),
		Connections: conns,
		Support:     service.NewSupport(nil, service.WithServiceNowSource(conns)),
	})

	rec := do(t, srv, http.MethodGet, "/api/support/tickets", nil)

	require.Equal(t, http.StatusNotImplemented, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "ErrNotImplemented", decodeBody(t, rec)["code"])
	assert.NotContains(t, rec.Body.String(), "portalUrl")
}

func TestCreateTicketReturnsTheNumberAndThePortalLink(t *testing.T) {
	fake := newFakeServiceNow(t)
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodPost, "/api/support/tickets", strings.NewReader(`{
		"subject": "Cannot create database orders-prod",
		"description": "The create stopped at Starting database.",
		"context": {
			"database": "orders-prod",
			"profile": "prod",
			"engine": "mysql",
			"engineVersion": "8.0.36",
			"namespace": "pds",
			"members": 3,
			"status": "failed",
			"errorMessage": "0/3 nodes are available: insufficient cpu"
		}
	}`))

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	decoded := decodeBody(t, rec)
	assert.Equal(t, "CS0061234", decoded["number"])
	assert.Equal(t, "Cannot create database orders-prod", decoded["subject"])
	assert.Contains(t, decoded["portalUrl"], "/percona")
	assert.Contains(t,
		decoded["externalUrl"],
		"/sn_customerservice_case.do?sys_id=sys-1")

	seen := fake.seen()
	require.Len(t, seen, 1)
	assert.Equal(t, servicenow.CreatePath, seen[0].Path)
	assert.Equal(t, "fake-sn-api-key", seen[0].APIKey)
	assert.Equal(t, "fake-sn-client-token", seen[0].Body["client_token"])
	assert.Equal(t, "CASE", seen[0].Body["ticket_type"])
	assert.Equal(t, "MySQL", seen[0].Body["u_main_technology"])

	description, ok := seen[0].Body["description"].(string)
	require.True(t, ok)
	assert.Contains(t, description, "The create stopped at Starting database.")
	assert.Contains(t, description, "Database: orders-prod")
	assert.Contains(t, description, "Engine: MySQL 8.0.36")
	assert.NotContains(t, description, "Environment type:")
	assert.Contains(t, description, "Members: 3")
	assert.Contains(t, description, "Error: 0/3 nodes are available: insufficient cpu")
	assert.Contains(t, description, "Reported at: 2026-08-25T12:00:00Z")
}

func TestCreateTicketSetsPostgreSQLAsTheTechnology(t *testing.T) {
	fake := newFakeServiceNow(t)
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodPost, "/api/support/tickets", strings.NewReader(`{
		"subject": "Cannot create database reporting-dev",
		"description": "The create stopped at Starting database.",
		"context": {
			"database": "reporting-dev",
			"profile": "dev",
			"engine": "postgresql"
		}
	}`))

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	seen := fake.seen()
	require.Len(t, seen, 1)
	assert.Equal(t, "PostgreSQL", seen[0].Body["u_main_technology"])
}

func TestCreateTicketAboutAMongoDBDatabaseSaysMongoDBAndCarriesItsFacts(t *testing.T) {
	fake := newFakeServiceNow(t)
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodPost, "/api/support/tickets", strings.NewReader(`{
		"subject": "Cannot connect to orders",
		"description": "It times out.",
		"context": {
			"database": "orders",
			"profile": "dev",
			"engine": "mongodb",
			"engineVersion": "8.0.4",
			"namespace": "pds",
			"members": 3,
			"status": "ready"
		}
	}`))

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	seen := fake.seen()
	require.Len(t, seen, 1)
	assert.Equal(t, "MongoDB", seen[0].Body["u_main_technology"])
	description, ok := seen[0].Body["description"].(string)
	require.True(t, ok)
	for _, fact := range []string{"Database: orders", "Profile: dev", "Engine: MongoDB 8.0.4", "Namespace: pds", "Members: 3", "Status: ready"} {
		assert.Contains(t, description, fact)
	}
}

// The documented create response carries the record id and no number, so the
// adapter resolves it rather than answering without the one field the UI shows.
func TestCreateTicketResolvesTheNumberWhenTheCreateOmitsIt(t *testing.T) {
	fake := newFakeServiceNow(t)
	fake.createWithoutNumber = true
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodPost, "/api/support/tickets",
		strings.NewReader(`{"subject":"Cannot create database orders-prod","description":"It failed."}`))

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "CS0061234", decodeBody(t, rec)["number"])

	seen := fake.seen()
	require.Len(t, seen, 2, "one create and one details read")
	assert.Equal(t, servicenow.DetailsPath, seen[1].Path)
	assert.Equal(t, "sys-1", seen[1].Body["ticket_id"])
}

// Nothing that authenticates against anything reaches ServiceNow, whichever
// field the developer pasted it into.
func TestCreateTicketSanitizesThePayload(t *testing.T) {
	fake := newFakeServiceNow(t)
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodPost, "/api/support/tickets", strings.NewReader(`{
		"subject": "Cannot connect with mysql://root:letmein@orders-prod:3306/appdb",
		"description": "Tried mysql -u root -pletmein.\nMYSQL_PWD=hunter2\nAuthorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload",
		"context": {
			"database": "orders-prod",
			"errorMessage": "Access denied for user 'root' token=abc1234567"
		}
	}`))

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	body := fake.lastBody(t)
	sent := body["short_description"].(string) + "\n" + body["description"].(string)

	for _, secret := range []string{
		"letmein",
		"hunter2",
		"eyJhbGciOiJIUzI1NiJ9.payload",
		"abc1234567",
	} {
		assert.NotContains(t, sent, secret, "payload: %s", sent)
	}

	assert.Contains(t, sent, "Database: orders-prod", "the context that is safe still goes")
}

func TestCreateTicketRejectsAnEmptyRequest(t *testing.T) {
	fake := newFakeServiceNow(t)
	srv := newSupportServer(t, fake)

	tests := []struct {
		name string
		body string
	}{
		{name: "no subject", body: `{"description":"y"}`},
		{name: "blank subject", body: `{"subject":"   ","description":"y"}`},
		{name: "no description", body: `{"subject":"x"}`},
		{name: "not json", body: `{`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, srv, http.MethodPost, "/api/support/tickets", strings.NewReader(tt.body))

			require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
			assert.Equal(t, "ErrValidation", decodeBody(t, rec)["code"])
		})
	}

	assert.Empty(t, fake.seen(), "a refused request never reaches ServiceNow")
}

func TestCreateTicketMapsARejectedCreateToADownstreamFailure(t *testing.T) {
	fake := newFakeServiceNow(t)
	fake.rejectMessage = "client_token does not match an active account"
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodPost, "/api/support/tickets",
		strings.NewReader(`{"subject":"Cannot create database orders-prod","description":"It failed."}`))

	require.Equal(t, http.StatusBadGateway, rec.Code, "body: %s", rec.Body.String())

	decoded := decodeBody(t, rec)
	assert.Equal(t, "ErrDownstream", decoded["code"])
	assert.NotContains(t, decoded["message"], "client_token",
		"the connector reason is logged, not served")
}

func TestCreateTicketMapsAnUnreachableServiceNowTo502(t *testing.T) {
	fake := newFakeServiceNow(t)
	fake.down()
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodPost, "/api/support/tickets",
		strings.NewReader(`{"subject":"Cannot create database orders-prod","description":"It failed."}`))

	require.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Equal(t, "ErrDownstream", decodeBody(t, rec)["code"])
}

func TestListTicketsReturnsThePage(t *testing.T) {
	fake := newFakeServiceNow(t)
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodGet, "/api/support/tickets?limit=10&offset=0", nil)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	decoded := decodeBody(t, rec)
	assert.Contains(t, decoded["portalUrl"], "/percona")

	tickets, ok := decoded["tickets"].([]any)
	require.True(t, ok, "body: %s", rec.Body.String())
	require.Len(t, tickets, 2)

	first, ok := tickets[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "CS0061234", first["number"])
	assert.Equal(t, "Awaiting Percona", first["state"])
	assert.Equal(t, "Cannot create database orders-prod", first["subject"])

	seen := fake.seen()
	require.Len(t, seen, 1, "the instance filters, so one request answers the page")
	assert.Equal(t, servicenow.SearchPath, seen[0].Path)
	assert.Contains(t, seen[0].Query, "descriptionLIKE")
	assert.Contains(t, seen[0].Query, "sysparm_limit=10")
}

func TestListTicketsExcludesTicketsNotCreatedByPDS(t *testing.T) {
	fake := newFakeServiceNow(t)
	fake.tickets = []map[string]any{
		{
			"number":            "CS0061234",
			"short_description": "PDS ticket",
			"description":       "Reported by the Percona Database Service control plane.\nDatabase: orders-prod",
		},
		{
			"number":            "CS0061235",
			"short_description": "Portal ticket",
			"description":       "A ticket created in the Percona support portal.",
		},
	}
	fake.details = map[string]map[string]any{
		"CS0061234": {
			"number":            "CS0061234",
			"short_description": "PDS ticket",
			"description":       "Reported by the Percona Database Service control plane.\nDatabase: orders-prod",
		},
		"CS0061235": {
			"number":            "CS0061235",
			"short_description": "Portal ticket",
			"description":       "A ticket created in the Percona support portal.",
		},
	}
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodGet, "/api/support/tickets?limit=10&offset=0", nil)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	tickets, ok := decodeBody(t, rec)["tickets"].([]any)
	require.True(t, ok)
	require.Len(t, tickets, 1)
	assert.Equal(t, "CS0061234", tickets[0].(map[string]any)["number"])

	seen := fake.seen()
	require.Len(t, seen, 1, "the portal case never leaves the instance")
	assert.Equal(t, servicenow.SearchPath, seen[0].Path)
}

func TestListTicketsKeepsTicketsOpenedBeforeTheRename(t *testing.T) {
	legacy := "Reported by the Percona Basecamp control plane.\nDatabase: orders-prod" // allow-legacy-name
	fake := newFakeServiceNow(t)
	fake.tickets = []map[string]any{
		{
			"number":            "CS0061234",
			"short_description": "Ticket opened before the rename",
			"description":       legacy,
		},
	}
	fake.details = map[string]map[string]any{
		"CS0061234": {
			"number":            "CS0061234",
			"short_description": "Ticket opened before the rename",
			"description":       legacy,
		},
	}
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodGet, "/api/support/tickets?limit=10&offset=0", nil)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	tickets, ok := decodeBody(t, rec)["tickets"].([]any)
	require.True(t, ok)
	require.Len(t, tickets, 1,
		"a case already stored in ServiceNow carries the old marker and still belongs on the page")
	assert.Equal(t, "CS0061234", tickets[0].(map[string]any)["number"])
}

func TestListTicketsRejectsUnreadablePaging(t *testing.T) {
	fake := newFakeServiceNow(t)
	srv := newSupportServer(t, fake)

	for _, target := range []string{
		"/api/support/tickets?limit=abc",
		"/api/support/tickets?offset=-3",
		"/api/support/tickets?limit=500",
	} {
		t.Run(target, func(t *testing.T) {
			rec := do(t, srv, http.MethodGet, target, nil)

			require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
			assert.Equal(t, "ErrValidation", decodeBody(t, rec)["code"])
		})
	}
}

func TestListTicketsAnswersAnEmptyAccountWithAnEmptyList(t *testing.T) {
	fake := newFakeServiceNow(t)
	fake.tickets = []map[string]any{}
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodGet, "/api/support/tickets", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []any{}, decodeBody(t, rec)["tickets"],
		"an account with no tickets is an empty list, not a 404")
}

func TestGetTicketReturnsTheCommentsAndTheState(t *testing.T) {
	fake := newFakeServiceNow(t)
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodGet, "/api/support/tickets/CS0061234", nil)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	decoded := decodeBody(t, rec)
	assert.Equal(t, "CS0061234", decoded["number"])
	assert.Equal(t, "Awaiting Percona", decoded["state"])
	assert.Equal(t, "3 - Moderate", decoded["priority"])
	assert.Contains(t,
		decoded["externalUrl"],
		"/sn_customerservice_case.do?sys_id=sys-1")
	assert.Equal(t, "Example Corp", decoded["account"])
	assert.Equal(t, "MySQL", decoded["technology"])

	comments, ok := decoded["comments"].([]any)
	require.True(t, ok)
	require.Len(t, comments, 1)

	first, ok := comments[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "percona.support", first["author"])
	assert.Equal(t, "Additional comments", first["label"])
	assert.Equal(t, "Could you attach the operator events?", first["body"])

	seen := fake.seen()
	require.Len(t, seen, 1)
	assert.Equal(t, servicenow.DetailsPath, seen[0].Path)
	assert.Equal(t, "CS0061234", seen[0].Body["ticket_number"])
}

func TestGetTicketMapsAMissingTicketTo404(t *testing.T) {
	fake := newFakeServiceNow(t)
	fake.missing = true
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodGet, "/api/support/tickets/CS0000000", nil)

	require.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())

	decoded := decodeBody(t, rec)
	assert.Equal(t, "ErrNotFound", decoded["code"])
	assert.Contains(t, decoded["message"], "CS0000000")
}

func TestGetTicketRejectsANumberThatIsNotOne(t *testing.T) {
	fake := newFakeServiceNow(t)
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodGet, "/api/support/tickets/not%20a%20number", nil)

	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "ErrValidation", decodeBody(t, rec)["code"])
	assert.Empty(t, fake.seen())
}

func TestReplyTicketPostsASanitizedPublicComment(t *testing.T) {
	fake := newFakeServiceNow(t)
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodPost, "/api/support/tickets/CS0061234/replies", strings.NewReader(`{
		"body": "Tried mysql://root:letmein@orders-prod:3306/appdb\napi_key=abc123456"
	}`))

	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())

	seen := fake.seen()
	require.Len(t, seen, 2, "the ticket is resolved before its comment is posted")
	assert.Equal(t, servicenow.DetailsPath, seen[0].Path)
	assert.Equal(t, "CS0061234", seen[0].Body["ticket_number"])
	assert.Equal(t, "/api/now/table/sn_customerservice_case/sys-1", seen[1].Path)
	assert.NotContains(t, seen[1].Body["comments"], "letmein")
	assert.NotContains(t, seen[1].Body["comments"], "abc123456")
	assert.Contains(t, seen[1].Body["comments"], "[redacted]")
}

func TestReplyTicketRejectsAnEmptyBody(t *testing.T) {
	fake := newFakeServiceNow(t)
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodPost, "/api/support/tickets/CS0061234/replies",
		strings.NewReader(`{"body":"  "}`))

	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "ErrValidation", decodeBody(t, rec)["code"])
	assert.Empty(t, fake.seen())
}

func TestReplyTicketMapsServiceNowErrorsToASafeDownstreamFailure(t *testing.T) {
	fake := newFakeServiceNow(t)
	fake.replyStatus = http.StatusUnauthorized
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodPost, "/api/support/tickets/CS0061234/replies",
		strings.NewReader(`{"body":"The operator events are attached."}`))

	require.Equal(t, http.StatusBadGateway, rec.Code, "body: %s", rec.Body.String())
	decoded := decodeBody(t, rec)
	assert.Equal(t, "ErrDownstream", decoded["code"])
	assert.NotContains(t, decoded["message"], "client_token")
}

func TestSupportResponsesNeverCarryTheCredentials(t *testing.T) {
	fake := newFakeServiceNow(t)
	fake.failStatus = http.StatusUnauthorized
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodGet, "/api/support/tickets", nil)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	assert.NotContains(t, rec.Body.String(), "fake-sn-api-key")
	assert.NotContains(t, rec.Body.String(), "fake-sn-client-token")
}

// A body past the cap is a size problem, not a blip, so telling the caller to
// try again in a moment would be wrong.
func TestListTicketsReportsAnOversizedAnswerAsItsOwnFault(t *testing.T) {
	fake := newFakeServiceNow(t)
	fake.oversized = true
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodGet, "/api/support/tickets", nil)

	require.Equal(t, http.StatusBadGateway, rec.Code, "body: %s", rec.Body.String())
	decoded := decodeBody(t, rec)
	assert.Equal(t, "ErrDownstream", decoded["code"])
	message, _ := decoded["message"].(string)
	assert.Contains(t, message, "too large")
	assert.NotContains(t, message, "Try again in a moment",
		"a size cap breach does not pass on its own")
}

func TestListTicketsNamesTheMissingTableRead(t *testing.T) {
	fake := newFakeServiceNow(t)
	fake.searchStatus = http.StatusForbidden
	srv := newSupportServer(t, fake)

	rec := do(t, srv, http.MethodGet, "/api/support/tickets", nil)

	require.Equal(t, http.StatusBadGateway, rec.Code, "body: %s", rec.Body.String())
	message, _ := decodeBody(t, rec)["message"].(string)
	assert.Contains(t, message, "sn_customerservice_case")
	assert.NotContains(t, message, "check the ServiceNow tokens",
		"the tokens can be right while the key cannot read the table")
}
