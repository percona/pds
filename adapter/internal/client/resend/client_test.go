package resend_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/resend"
)

const testKey = "re_test_key_value"

type captured struct {
	Path          string
	Authorization string
	ContentType   string
	Body          map[string]any
}

func newFake(t *testing.T, status int, body string) (*resend.Client, *captured) {
	t.Helper()

	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Path = r.URL.Path
		got.Authorization = r.Header.Get("Authorization")
		got.ContentType = r.Header.Get("Content-Type")
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &got.Body))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	client, err := resend.New(resend.Options{BaseURL: srv.URL, APIKey: testKey})
	require.NoError(t, err)

	return client, got
}

func TestSendPostsTheEmailWithTheKeyAsBearer(t *testing.T) {
	client, got := newFake(t, http.StatusOK, `{"id":"abc"}`)

	err := client.Send(context.Background(), resend.Email{
		From:    "PDS landing <onboarding@resend.dev>",
		To:      []string{"owner@example.com"},
		ReplyTo: "ada@example.com",
		Subject: "Hello",
		Text:    "Body",
	})
	require.NoError(t, err)

	assert.Equal(t, resend.EmailsPath, got.Path)
	assert.Equal(t, "Bearer "+testKey, got.Authorization)
	assert.Equal(t, "application/json", got.ContentType)
	assert.Equal(t, map[string]any{
		"from":     "PDS landing <onboarding@resend.dev>",
		"to":       []any{"owner@example.com"},
		"reply_to": "ada@example.com",
		"subject":  "Hello",
		"text":     "Body",
	}, got.Body)
}

func TestSendReportsARefusalWithACappedBody(t *testing.T) {
	client, _ := newFake(t, http.StatusUnprocessableEntity, strings.Repeat("x", 10000))

	err := client.Send(context.Background(), resend.Email{To: []string{"a@example.com"}})
	require.Error(t, err)

	var apiErr *resend.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Status)
	assert.Len(t, apiErr.Body, 2048)
	assert.NotContains(t, err.Error(), testKey)
}

func TestSendTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()

	client, err := resend.New(resend.Options{BaseURL: srv.URL, APIKey: testKey, Timeout: time.Second})
	require.NoError(t, err)

	err = client.Send(context.Background(), resend.Email{})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testKey)
}

func TestNewRefusesBadOptions(t *testing.T) {
	tests := []struct {
		name string
		opts resend.Options
		want string
	}{
		{name: "blank key", opts: resend.Options{}, want: "API key"},
		{name: "bad scheme", opts: resend.Options{BaseURL: "ftp://x", APIKey: "k"}, want: "scheme"},
		{name: "no host", opts: resend.Options{BaseURL: "https://", APIKey: "k"}, want: "host"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resend.New(tc.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestNewDefaultsToResend(t *testing.T) {
	client, err := resend.New(resend.Options{APIKey: "k"})
	require.NoError(t, err)
	assert.NotNil(t, client)
}
