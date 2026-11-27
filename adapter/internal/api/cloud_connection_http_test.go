package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/testutil"
)

const cloudPrincipal = "arn:aws:iam::111122223333:role/pds-control-plane"

type answeringInspector struct{ result aws.Inspection }

func (a answeringInspector) Inspect(context.Context, aws.InspectRequest) (aws.Inspection, error) {
	return a.result, nil
}

type cloudHTTP struct {
	srv   *api.Server
	svc   *service.CloudConnections
	token string
}

func newCloudHTTP(t *testing.T) *cloudHTTP {
	t.Helper()

	svc := service.NewCloudConnections(testutil.NewCloudStore(), answeringInspector{aws.Inspection{
		AccountID: "123456789012", StackStatus: "CREATE_COMPLETE", TemplateVersion: service.CloudTemplateVersion,
	}}, service.CloudOptions{PrincipalARN: cloudPrincipal})
	issued, err := svc.IssueToken(t.Context(), auth.Scope{UserID: 1, Team: auth.Team{ID: 7}}, []string{"eu-west-1"})
	require.NoError(t, err)

	return &cloudHTTP{
		svc:   svc,
		token: issued.Token,
		srv: api.NewServer(api.Deps{
			Config: testConfig(), Logger: log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
			Auth: auth.NewStub(), CloudConnections: svc,
		}),
	}
}

func (c *cloudHTTP) post(t *testing.T, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/internal/cloud-connection/"+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	c.srv.ServeHTTP(rec, req)

	return rec
}

func TestTheTemplateAndTheScriptAreServedWithoutACredential(t *testing.T) {
	c := newCloudHTTP(t)

	tpl := do(t, c.srv, http.MethodGet, "/api/internal/cloud-connection/template.yaml", nil)
	assert.Equal(t, http.StatusOK, tpl.Code)
	assert.Contains(t, tpl.Header().Get("Content-Type"), "yaml")
	assert.Contains(t, tpl.Body.String(), "AWSTemplateFormatVersion")

	script := do(t, c.srv, http.MethodGet, "/api/internal/cloud-connection/connect.sh", nil)
	assert.Equal(t, http.StatusOK, script.Code)
	assert.Contains(t, script.Body.String(), `HUB_URL="https://hub.example"`)
	assert.NotContains(t, script.Body.String(), "__HUB_URL__")
}

func TestTheScriptEnrollsThenReportsThenVerifiesToReady(t *testing.T) {
	c := newCloudHTTP(t)

	enrolled := c.post(t, "enroll", c.token, `{"accountId":"123456789012","alias":"acme","region":"eu-west-1"}`)
	require.Equal(t, http.StatusOK, enrolled.Code, enrolled.Body.String())
	var got map[string]any
	require.NoError(t, json.Unmarshal(enrolled.Body.Bytes(), &got))
	assert.Len(t, got["connectionId"], 12)
	assert.Equal(t, "pds-"+got["connectionId"].(string), got["stackName"])
	assert.Equal(t, cloudPrincipal, got["principalArn"])
	assert.NotEmpty(t, got["externalId"])
	assert.Equal(t, []any{"eu-west-1"}, got["allowedRegions"])
	assert.Equal(t, "https://hub.example/api/internal/cloud-connection/template.yaml", got["templateUrl"])

	event := c.post(t, "events", c.token, `{"step":"stack","status":"started"}`)
	assert.Equal(t, http.StatusNoContent, event.Code, event.Body.String())

	verified := c.post(t, "verify", c.token, "")
	require.Equal(t, http.StatusOK, verified.Code, verified.Body.String())
	var state map[string]any
	require.NoError(t, json.Unmarshal(verified.Body.Bytes(), &state))
	assert.Equal(t, "ready", state["state"])

	again := c.post(t, "verify", c.token, "")
	assert.Equal(t, http.StatusUnauthorized, again.Code, "Ready ends the token")
}

func TestARefusedTokenSaysNothingAboutWhyAndNothingIsEnrolled(t *testing.T) {
	c := newCloudHTTP(t)

	for _, token := range []string{"", "ccn_wrong", "not-a-token"} {
		rec := c.post(t, "enroll", token, `{"accountId":"123456789012","region":"eu-west-1"}`)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, token)
		assert.NotContains(t, rec.Body.String(), "expired")
	}
}

func TestEnrollRefusesABadAccountAndABadBody(t *testing.T) {
	c := newCloudHTTP(t)

	for _, body := range []string{`{"accountId":"12","region":"eu-west-1"}`, `{"accountId":"123456789012","region":"mars"}`, `nope`, `{}`} {
		rec := c.post(t, "enroll", c.token, body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
}

func TestReportingBeforeEnrollingIsRefused(t *testing.T) {
	c := newCloudHTTP(t)

	rec := c.post(t, "events", c.token, `{"step":"stack","status":"started"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestWithoutTheServiceTheRoutesAreNotImplemented(t *testing.T) {
	srv := api.NewServer(api.Deps{Config: testConfig(), Logger: log.New(io.Discard, log.FormatJSON, slog.LevelDebug), Auth: auth.NewStub()})

	req := httptest.NewRequest(http.MethodPost, "/api/internal/cloud-connection/enroll", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer ccn_x")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotImplemented, rec.Code)
	assert.Equal(t, http.StatusOK, do(t, srv, http.MethodGet, "/api/internal/cloud-connection/template.yaml", nil).Code,
		"the generic template is always served")
}
