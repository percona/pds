package ses_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/ses"
)

type captured struct {
	Path string
	Auth string
	Body map[string]any
}

func newFake(t *testing.T, status int, body string) (*ses.Client, *captured) {
	t.Helper()

	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Path = r.URL.Path
		got.Auth = r.Header.Get("Authorization")
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &got.Body))
		w.Header().Set("Content-Type", "application/json")
		if status >= 400 {
			w.Header().Set("X-Amzn-Errortype", "AccessDeniedException")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	client, err := ses.New(t.Context(), ses.Options{
		Region:       "us-west-1",
		BaseEndpoint: srv.URL,
		Credentials:  credentials.NewStaticCredentialsProvider("AKIATESTONLY", "test-secret", ""),
		MaxAttempts:  1,
	})
	require.NoError(t, err)

	return client, got
}

func TestSendPostsOnePlainTextEmailSignedForTheRegion(t *testing.T) {
	client, got := newFake(t, http.StatusOK, `{"MessageId":"abc"}`)

	err := client.Send(t.Context(), ses.Email{
		From:    "noreply@pds.percona.com",
		To:      []string{"ada@example.com"},
		Subject: "You are invited",
		Text:    "Open the link",
	})
	require.NoError(t, err)

	assert.Equal(t, "/v2/email/outbound-emails", got.Path)
	assert.Contains(t, got.Auth, "/us-west-1/ses/aws4_request")
	assert.Equal(t, "noreply@pds.percona.com", got.Body["FromEmailAddress"])
	assert.Equal(t, map[string]any{"ToAddresses": []any{"ada@example.com"}}, got.Body["Destination"])
	simple := got.Body["Content"].(map[string]any)["Simple"].(map[string]any)
	assert.Equal(t, "You are invited", simple["Subject"].(map[string]any)["Data"])
	assert.Equal(t, "Open the link", simple["Body"].(map[string]any)["Text"].(map[string]any)["Data"])
}

func TestSendReturnsTheRefusalWithoutTheMessage(t *testing.T) {
	client, _ := newFake(t, http.StatusForbidden,
		`{"message":"User is not authorized to perform ses:SendEmail"}`)

	err := client.Send(t.Context(), ses.Email{
		From: "other@pds.percona.com", To: []string{"ada@example.com"},
		Subject: "s", Text: "secret-token-in-body",
	})
	require.Error(t, err)

	assert.Contains(t, err.Error(), "ses: send email")
	assert.Contains(t, err.Error(), "AccessDeniedException")
	assert.NotContains(t, err.Error(), "secret-token-in-body")
}

func TestNewRefusesABlankRegion(t *testing.T) {
	_, err := ses.New(t.Context(), ses.Options{Region: " "})
	require.Error(t, err)
}
