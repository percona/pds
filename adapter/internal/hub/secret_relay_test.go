package hub_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/hub"
)

func newRelayHub(t *testing.T) *hub.Service {
	t.Helper()

	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	inv.Register("spoke-b", "pds")

	return hub.NewService(hub.NewMemoryStore(), inv, hub.Options{Namespace: "pds"})
}

func readInBackground(ctx context.Context, svc *hub.Service, environment string) <-chan struct {
	value string
	err   error
} {
	out := make(chan struct {
		value string
		err   error
	}, 1)
	go func() {
		v, err := svc.ReadSecret(ctx, environment, hub.SecretPMMAdminPassword)
		out <- struct {
			value string
			err   error
		}{v, err}
	}()

	return out
}

func takeRequest(t *testing.T, svc *hub.Service, environment string) hub.SecretRequest {
	t.Helper()

	var got []hub.SecretRequest
	require.Eventually(t, func() bool {
		got = svc.TakeSecretRequests(environment)

		return len(got) > 0
	}, time.Second, 5*time.Millisecond)

	return got[0]
}

func TestReadSecretReturnsWhatTheSpokeAnswers(t *testing.T) {
	svc := newRelayHub(t)
	answer := readInBackground(context.Background(), svc, "spoke-a")

	req := takeRequest(t, svc, "spoke-a")
	svc.DeliverSecretResults("spoke-a", []hub.SecretResult{{ID: req.ID, Value: "pw"}})

	got := <-answer
	require.NoError(t, got.err)
	assert.Equal(t, "pw", got.value)
}

func TestReadSecretLeavesNothingBehind(t *testing.T) {
	svc := newRelayHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := svc.ReadSecret(ctx, "spoke-a", hub.SecretPMMAdminPassword)

	require.ErrorIs(t, err, hub.ErrSecretTimeout)
	assert.Empty(t, svc.TakeSecretRequests("spoke-a"), "a request nobody waits on is not handed out")
}

func TestReadSecretIgnoresAnAnswerFromAnotherEnvironment(t *testing.T) {
	svc := newRelayHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	answer := readInBackground(ctx, svc, "spoke-a")

	req := takeRequest(t, svc, "spoke-a")
	svc.DeliverSecretResults("spoke-b", []hub.SecretResult{{ID: req.ID, Value: "from-b"}})

	got := <-answer
	require.ErrorIs(t, got.err, hub.ErrSecretTimeout)
	assert.Empty(t, got.value)
}

func TestReadSecretCarriesTheSpokesFailure(t *testing.T) {
	svc := newRelayHub(t)
	answer := readInBackground(context.Background(), svc, "spoke-a")

	req := takeRequest(t, svc, "spoke-a")
	svc.DeliverSecretResults("spoke-a", []hub.SecretResult{{ID: req.ID, Error: "forbidden"}})

	got := <-answer
	require.ErrorIs(t, got.err, hub.ErrSecretUnavailable)
	assert.Empty(t, got.value)
}

func TestReadSecretForAnUnknownEnvironment(t *testing.T) {
	svc := newRelayHub(t)

	_, err := svc.ReadSecret(context.Background(), "nope", hub.SecretPMMAdminPassword)

	require.Error(t, err)
	assert.NotErrorIs(t, err, hub.ErrSecretTimeout)
}

// A result carries a credential, so formatting one anywhere must not print it.
func TestSecretResultNeverPrintsItsValue(t *testing.T) {
	res := hub.SecretResult{ID: "abc", Value: "hunter2"}

	assert.NotContains(t, fmt.Sprintf("%v %+v %s", res, res, res), "hunter2")

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("beat", slog.Any("result", res))
	assert.NotContains(t, buf.String(), "hunter2")
}
