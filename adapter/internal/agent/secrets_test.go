package agent_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/agent"
)

type fakeSecrets struct {
	value string
	err   error
	asked []string
}

func (f *fakeSecrets) Read(_ context.Context, kind string) (string, error) {
	f.asked = append(f.asked, kind)

	return f.value, f.err
}

// secretHub replies to the first beat with one secret request and records
// every beat body after it.
func secretHub(t *testing.T) (*httptest.Server, chan string) {
	t.Helper()

	bodies := make(chan string, 16)
	var beats atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies <- string(raw)
		reply := `{"work":[]}`
		if beats.Add(1) == 1 {
			reply = `{"work":[],"secrets":[{"id":"sec_1","kind":"pmm-admin-password"}]}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)

	return srv, bodies
}

func runAgent(t *testing.T, srv *httptest.Server, secrets agent.SecretReader, interval time.Duration) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = agent.Run(ctx, agent.Options{
			HubURL:      srv.URL,
			Environment: "spoke-a",
			Namespace:   "pds",
			Interval:    interval,
			Databases:   &stubDatabases{},
			Secrets:     secrets,
		})
	}()
}

// The hub's request waits on the answer, so the agent beats again at once
// instead of leaving a browser waiting a whole interval.
func TestAgentAnswersASecretRequestOnAnImmediateBeat(t *testing.T) {
	srv, bodies := secretHub(t)
	secrets := &fakeSecrets{value: "pw-from-environment"}
	runAgent(t, srv, secrets, time.Hour)

	first := waitForBody(t, bodies)
	assert.NotContains(t, first, "secretResults")
	second := waitForBody(t, bodies)

	assert.Contains(t, second, `"secretResults":[{"id":"sec_1","value":"pw-from-environment"}]`)
	assert.Equal(t, []string{"pmm-admin-password"}, secrets.asked)
}

// A secret is sent once. Retrying would keep a credential in memory for a
// request the hub has already given up on.
func TestAgentSendsASecretAnswerOnlyOnce(t *testing.T) {
	srv, bodies := secretHub(t)
	runAgent(t, srv, &fakeSecrets{value: "pw"}, 20*time.Millisecond)

	waitForBody(t, bodies)
	carried := 0
	for range 6 {
		if strings.Contains(waitForBody(t, bodies), "secretResults") {
			carried++
		}
	}

	assert.Equal(t, 1, carried)
}

func TestAgentReportsAFailureToReadTheSecret(t *testing.T) {
	srv, bodies := secretHub(t)
	runAgent(t, srv, &fakeSecrets{err: errors.New("secret not found")}, time.Hour)

	waitForBody(t, bodies)
	second := waitForBody(t, bodies)

	assert.Contains(t, second, `"secretResults":[{"id":"sec_1","error":"secret not found"}]`)
	assert.NotContains(t, second, `"value"`)
}

func TestAgentWithoutASecretReaderSaysSo(t *testing.T) {
	srv, bodies := secretHub(t)
	runAgent(t, srv, nil, time.Hour)

	waitForBody(t, bodies)
	second := waitForBody(t, bodies)

	assert.Contains(t, second, `"id":"sec_1"`)
	assert.Contains(t, second, `"error"`)
	require.NotContains(t, second, `"value"`)
}
