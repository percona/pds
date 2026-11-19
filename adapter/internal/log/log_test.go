package log_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/log"
)

// newBufferLogger returns a JSON logger writing into buf, wrapped in the
// redaction handler under test.
func newBufferLogger(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	logger := log.New(&buf, log.FormatJSON, slog.LevelDebug)

	return logger, &buf
}

// decode reads the single JSON record written to buf.
func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()

	line := strings.TrimSpace(buf.String())
	require.NotEmpty(t, line, "expected one log record")

	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &rec))

	return rec
}

func TestRedactsCredentialAttributes(t *testing.T) {
	tests := []struct {
		name string
		key  string
		val  string
	}{
		{name: "password", key: "password", val: "hunter2"},
		{name: "mixed case password", key: "Password", val: "hunter2"},
		{name: "user password", key: "user_password", val: "hunter2"},
		{name: "pwd", key: "pwd", val: "hunter2"},
		{name: "api key", key: "api_key", val: "sn-api-key"},
		{name: "apikey", key: "apikey", val: "sn-api-key"},
		{name: "token", key: "client_token", val: "sn-client-token"},
		{name: "secret", key: "secret", val: "top-secret"},
		{name: "authorization", key: "authorization", val: "Bearer abc.def"},
		{name: "credentials", key: "credentials", val: "root/root"},
		{name: "dsn", key: "dsn", val: "root:root@tcp(127.0.0.1:3306)/app"},
		{name: "connection string", key: "connection_string", val: "mysql://app:pw@host:3306/db"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buf := newBufferLogger(t)
			logger.Info("downstream call", slog.String(tt.key, tt.val))

			rec := decode(t, buf)
			assert.Equal(t, log.Redacted, rec[tt.key])
			assert.NotContains(t, buf.String(), tt.val)
		})
	}
}

func TestRedactsCredentialsInsideValues(t *testing.T) {
	logger, buf := newBufferLogger(t)
	logger.Info("connect", slog.String("target", "mysql://app:hunter2@orders.svc:3306/app"))

	rec := decode(t, buf)
	assert.Equal(t, log.Redacted, rec["target"])
	assert.NotContains(t, buf.String(), "hunter2")
}

func TestKeepsSafeAttributes(t *testing.T) {
	logger, buf := newBufferLogger(t)
	logger.Info("request done",
		slog.String("method", "GET"),
		slog.String("path", "/healthz"),
		slog.Int("status", 200),
		slog.String("request_id", "req-123"),
	)

	rec := decode(t, buf)
	assert.Equal(t, "GET", rec["method"])
	assert.Equal(t, "/healthz", rec["path"])
	assert.Equal(t, float64(200), rec["status"])
	assert.Equal(t, "req-123", rec["request_id"])
	assert.Equal(t, "request done", rec["msg"])
}

func TestRedactsNestedAndPreAttachedAttributes(t *testing.T) {
	logger, buf := newBufferLogger(t)
	logger.With(slog.String("api_key", "sn-api-key")).
		WithGroup("servicenow").
		Info("ticket created",
			slog.Group("payload",
				slog.String("number", "CS0001"),
				slog.String("password", "hunter2"),
			),
		)

	body := buf.String()
	assert.NotContains(t, body, "sn-api-key")
	assert.NotContains(t, body, "hunter2")
	assert.Contains(t, body, "CS0001")
	assert.Contains(t, body, log.Redacted)
}

func TestTextFormatForDev(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, log.FormatText, slog.LevelInfo)
	logger.Info("starting", slog.String("addr", ":8080"), slog.String("token", "abc"))

	body := buf.String()
	assert.Contains(t, body, "msg=starting")
	assert.Contains(t, body, "addr=:8080")
	assert.NotContains(t, body, "abc")
}

func TestLevelIsHonoured(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, log.FormatJSON, slog.LevelInfo)
	logger.Debug("noisy")

	assert.Empty(t, buf.String())
}

func TestLoggerRoundTripsThroughContext(t *testing.T) {
	logger, buf := newBufferLogger(t)

	ctx := log.NewContext(context.Background(), logger.With(slog.String("request_id", "req-9")))
	log.FromContext(ctx).Info("handled")

	rec := decode(t, buf)
	assert.Equal(t, "req-9", rec["request_id"])
}

func TestFromContextFallsBackToDefault(t *testing.T) {
	assert.NotNil(t, log.FromContext(context.Background()))
}

// A password holding an unescaped slash, question mark or hash used to defeat
// the URL redactor: its password class excluded those bytes, so the pattern
// never reached the at sign and the whole credential was logged intact. The
// masking guard on the API side had the identical blind spot, so the two
// defences failed on the same input rather than covering for each other.
func TestRedactTextCatchesAwkwardPasswords(t *testing.T) {
	t.Parallel()

	for _, password := range []string{"s3c/r3t", "s3c?r3t", "s3c#r3t", "s3c/r3t?x", "p@ss/word", "p@ss"} {
		line := "connect failed for mysql://root:" + password + "@host:3306/db"

		if got := log.RedactText(line); strings.Contains(got, password) {
			t.Errorf("log.RedactText(%q) = %q, which still carries the password", line, got)
		}
	}
}
