package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

const starHubNow = "2026-09-08T12:00:00Z"

type memoryTokenTeams struct {
	mu sync.Mutex
	m  map[string]uint
}

func (m *memoryTokenTeams) remember(token string, team uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]uint{}
	}
	m.m[hub.HashToken(token)] = team
}

func (m *memoryTokenTeams) TokenTeam(_ context.Context, hash string) (uint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	team, ok := m.m[hash]
	if !ok {
		return 0, store.ErrTokenUnknown
	}

	return team, nil
}

func newTeamHub(t *testing.T) (*hub.Service, *hub.Inventory) {
	t.Helper()

	inv := hub.NewInventory(time.Minute)
	now := starHubClock()
	svc := hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
		Namespace:     testEverestNamespace,
		BackupStorage: testBackupStorage,
		Now:           func() time.Time { return now },
		Tokens:        hub.NewTokenStore(nil, 0),
	})

	return svc, inv
}

func starHubClock() time.Time {
	now, err := time.Parse(time.RFC3339, starHubNow)
	if err != nil {
		return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	}

	return now
}

func newStarHubServer(t *testing.T, lookup api.TokenTeamLookup, hubs map[uint]*hub.Service) *api.Server {
	t.Helper()

	databases := service.NewDatabase(engine.NewRegistry(), service.WithHubFor(func(ctx context.Context) (*hub.Service, error) {
		team, ok := auth.TeamFromContext(ctx)
		if !ok {
			return nil, errors.New("a team is required")
		}
		svc, ok := hubs[team]
		if !ok {
			return nil, errors.New("a team is required")
		}

		return svc, nil
	}))

	return api.NewServer(api.Deps{
		Config: config.Config{
			Addr:             ":8080",
			Env:              config.EnvDev,
			Organization:     "*",
			EverestNamespace: testEverestNamespace,
			HubGitDir:        "/tmp/hub-git",
			HubPublicURL:     "https://hub.example",
			HubGitURL:        "https://github.com/example/pds.git",
		},
		Logger:     log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:       auth.NewStub(),
		Databases:  databases,
		TokenTeams: lookup,
	})
}

func TestEnrollmentUsesTheTeamThatMintedTheToken(t *testing.T) {
	lookup := &memoryTokenTeams{}
	hubA, invA := newTeamHub(t)
	hubB, invB := newTeamHub(t)
	srv := newStarHubServer(t, lookup, map[uint]*hub.Service{
		1: hubA,
		2: hubB,
	})

	token, err := hubA.IssueJoin(t.Context(), "spoke-a")
	require.NoError(t, err)
	lookup.remember(token, 1)

	req := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body := decodeBody(t, rec)
	assert.Equal(t, "spoke-a", body["environment"])
	agent, ok := body["agentToken"].(string)
	require.True(t, ok)
	assert.True(t, agent != "" && agent != token)

	now := starHubClock()
	_, inA := invA.Get("spoke-a", now)
	_, inB := invB.Get("spoke-a", now)
	assert.True(t, inA, "the environment must land in the minting team inventory")
	assert.False(t, inB, "the environment must not land in another team inventory")
}

func TestHeartbeatWithTeamATokenDoesNotTouchTeamB(t *testing.T) {
	lookup := &memoryTokenTeams{}
	hubA, invA := newTeamHub(t)
	hubB, invB := newTeamHub(t)
	srv := newStarHubServer(t, lookup, map[uint]*hub.Service{
		1: hubA,
		2: hubB,
	})

	tokenA, err := hubA.IssueJoin(t.Context(), "prod")
	require.NoError(t, err)
	lookup.remember(tokenA, 1)
	_, err = hubB.IssueJoin(t.Context(), "prod")
	require.NoError(t, err)

	payload, err := json.Marshal(map[string]any{
		"environment": "prod",
		"namespace":   "pds",
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/prod/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+tokenA)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	now := starHubClock()
	a, ok := invA.Get("prod", now)
	require.True(t, ok)
	assert.Equal(t, hub.StatusConnected, a.Status)

	b, ok := invB.Get("prod", now)
	require.True(t, ok)
	assert.Equal(t, hub.StatusUnreachable, b.Status)
	assert.True(t, b.LastSeen.IsZero())
}

func TestEnrollmentUnknownTokenIsUnauthorizedWhenEveryOrganizationIsServed(t *testing.T) {
	lookup := &memoryTokenTeams{}
	hubA, _ := newTeamHub(t)
	srv := newStarHubServer(t, lookup, map[uint]*hub.Service{1: hubA})

	req := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	req.Header.Set("Authorization", "Bearer jnt_unknown")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestConnectBootstrapKeepsServingWithoutABearer(t *testing.T) {
	lookup := &memoryTokenTeams{}
	hubA, _ := newTeamHub(t)
	srv := newStarHubServer(t, lookup, map[uint]*hub.Service{1: hubA})

	rec := do(t, srv, http.MethodGet, "/api/internal/connect/bootstrap.sh", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "HUB_URL=")

	withToken := httptest.NewRequest(http.MethodGet, "/api/internal/connect/bootstrap.sh", nil)
	withToken.Header.Set("Authorization", "Bearer jnt_unknown")
	got := httptest.NewRecorder()
	srv.ServeHTTP(got, withToken)
	assert.Equal(t, http.StatusOK, got.Code)
	assert.Contains(t, got.Body.String(), "HUB_URL=")
}
