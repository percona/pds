package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/mcp"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

const approvalsDatabase = "spoke-a/pds.orders-dev"

// signedInAs is a session of one person, so a turn runs as them. A request
// that sends X-Person is that person's instead.
type signedInAs struct{ subject string }

func (s signedInAs) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := s.subject
		if header := r.Header.Get("X-Person"); header != "" {
			who = header
		}
		ctx := auth.WithIdentity(r.Context(), auth.Identity{Subject: who, Scope: auth.Scope{
			UserID: 2, UserName: who, OrganizationID: 1, OrganizationName: "acme", Role: auth.RoleUser,
			Team: auth.Team{ID: 1, Name: "default"},
		}})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type fixedLimits struct{ access string }

func (f fixedLimits) AI(context.Context, uint) (store.AISettings, error) {
	return store.AISettings{Access: f.access}, nil
}

// changeLog is the API the registry's tools call, recording what it was asked.
type changeLog struct {
	mu   sync.Mutex
	seen []string
}

func (c *changeLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.seen = append(c.seen, r.Method+" "+r.URL.Path)
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": approvalsDatabase, "name": "orders-dev", "status": "ready"})
}

func (c *changeLog) changes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, seen := range c.seen {
		if !strings.HasPrefix(seen, "GET ") {
			out = append(out, seen)
		}
	}

	return out
}

type approvalLab struct {
	ts       *httptest.Server
	stream   *sseReader
	changes  *changeLog
	response *http.Response
}

func openApprovalStream(t *testing.T, subject, limit string, replies ...claude.Response) *approvalLab {
	t.Helper()

	lab := &approvalLab{changes: &changeLog{}}
	chat := service.NewChat(nil, nil,
		service.WithChatCompleter(&queuedCompleter{replies: replies}),
		service.WithChatStore(service.NewMemoryChats(fixedTeam(1)).Stores()),
		service.WithTools(mcp.New(lab.changes, slog.New(slog.DiscardHandler), "test")),
		service.WithAccessLimits(fixedLimits{access: limit}),
	)
	srv := api.NewServer(api.Deps{
		Config:        config.Config{Addr: ":8080", Env: config.EnvDev, EverestNamespace: "pds"},
		Logger:        log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:          signedInAs{subject: subject},
		Chat:          chat,
		ChatHeartbeat: 20 * time.Millisecond,
	})
	lab.ts = httptest.NewServer(srv)
	t.Cleanup(lab.ts.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, lab.ts.URL+chatPath, strings.NewReader(chatBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	lab.response, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lab.response.Body.Close() })
	lab.stream = &sseReader{scanner: bufio.NewScanner(lab.response.Body)}

	return lab
}

// queuedCompleter answers with its replies in order.
type queuedCompleter struct {
	mu      sync.Mutex
	replies []claude.Response
}

func (q *queuedCompleter) Complete(context.Context, claude.Request) (*claude.Response, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	reply := q.replies[0]
	q.replies = q.replies[1:]

	return &reply, nil
}

// nextOf reads events until one is called name, skipping keep-alives and the
// events that are not the one a test is about.
func (l *approvalLab) nextOf(t *testing.T, name string) sseEvent {
	t.Helper()

	for range 200 {
		event := l.stream.next(t, 5*time.Second)
		if event.name == name {
			return event
		}
		require.NotEqual(t, "eof", event.name, "the stream ended before %s", name)
	}
	t.Fatalf("no %s event", name)

	return sseEvent{}
}

func (l *approvalLab) decide(t *testing.T, subject, id, body string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, l.ts.URL+chatPath+"/approvals/"+id, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Person", subject)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	return resp
}

func scaleReplies(finish string) []claude.Response {
	return []claude.Response{
		{StopReason: "tool_use", Content: []claude.ContentBlock{{
			Type: "tool_use", ID: "1", Name: "scale_database", Input: json.RawMessage(`{"id":"` + approvalsDatabase + `","cpu":2}`),
		}}},
		{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: finish}}},
	}
}

type approvalEvent struct {
	ID          string `json:"id"`
	Tool        string `json:"tool"`
	Title       string `json:"title"`
	Database    string `json:"database"`
	Destructive bool   `json:"destructive"`
	ExpiresAt   string `json:"expiresAt"`
	Lines       []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"lines"`
}

func TestChatStreamAsksForAnApprovalAndRunsTheChangeAfterApprove(t *testing.T) {
	lab := openApprovalStream(t, "ann", "change", scaleReplies("Scaled to 2 CPUs.")...)

	event := lab.nextOf(t, "approval")
	var approval approvalEvent
	require.NoError(t, json.Unmarshal([]byte(event.data), &approval))
	assert.Equal(t, "scale_database", approval.Tool)
	assert.Equal(t, "orders-dev", approval.Database)
	assert.NotEmpty(t, approval.ExpiresAt)
	assert.Empty(t, lab.changes.changes(), "the card is on screen and nothing was sent")

	resp := lab.decide(t, "ann", approval.ID, `{"decision":"approve"}`)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	ended := lab.nextOf(t, "approval_end")
	assert.JSONEq(t, `{"id":"`+approval.ID+`","outcome":"approved"}`, ended.data)
	reply := lab.nextOf(t, "reply")
	assert.Contains(t, reply.data, "Scaled to 2 CPUs.")
	assert.Equal(t, []string{"POST /api/databases/" + approvalsDatabase + "/scale"}, lab.changes.changes())
}

func TestChatStreamKeepsTheConnectionAliveWhileAnApprovalIsOpen(t *testing.T) {
	lab := openApprovalStream(t, "ann", "change", scaleReplies("ok")...)
	lab.nextOf(t, "approval")

	for range 3 {
		event := lab.stream.next(t, time.Second)
		require.Equal(t, "comment", event.name, "a quiet stream is dropped by a proxy")
	}
}

func TestChatApprovalRouteRejectsAndTheModelIsTold(t *testing.T) {
	lab := openApprovalStream(t, "ann", "change", scaleReplies("Left it as it was.")...)
	var approval approvalEvent
	require.NoError(t, json.Unmarshal([]byte(lab.nextOf(t, "approval").data), &approval))

	resp := lab.decide(t, "ann", approval.ID, `{"decision":"reject"}`)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	assert.Contains(t, lab.nextOf(t, "approval_end").data, `"outcome":"rejected"`)
	assert.Contains(t, lab.nextOf(t, "reply").data, "Left it as it was.")
	assert.Empty(t, lab.changes.changes())
}

func TestChatApprovalRouteAnswersAReplayedOrUnknownIDAsNotFound(t *testing.T) {
	lab := openApprovalStream(t, "ann", "change", scaleReplies("ok")...)
	var approval approvalEvent
	require.NoError(t, json.Unmarshal([]byte(lab.nextOf(t, "approval").data), &approval))
	require.Equal(t, http.StatusNoContent, lab.decide(t, "ann", approval.ID, `{"decision":"approve"}`).StatusCode)
	lab.nextOf(t, "reply")

	replay := lab.decide(t, "ann", approval.ID, `{"decision":"approve"}`)
	assert.Equal(t, http.StatusNotFound, replay.StatusCode)
	body, _ := io.ReadAll(replay.Body)
	assert.Contains(t, string(body), "expired or was already answered")
	assert.Equal(t, http.StatusNotFound, lab.decide(t, "ann", "nope", `{"decision":"approve"}`).StatusCode)
	assert.Len(t, lab.changes.changes(), 1)
}

func TestChatApprovalRouteRefusesAnotherPersonsApprovalAndAnUnknownDecision(t *testing.T) {
	lab := openApprovalStream(t, "ann", "change", scaleReplies("ok")...)
	var approval approvalEvent
	require.NoError(t, json.Unmarshal([]byte(lab.nextOf(t, "approval").data), &approval))

	assert.Equal(t, http.StatusNotFound, lab.decide(t, "bob", approval.ID, `{"decision":"approve"}`).StatusCode,
		"another person cannot answer what Ann was asked")
	assert.Equal(t, http.StatusBadRequest, lab.decide(t, "ann", approval.ID, `{"decision":"nonsense"}`).StatusCode,
		"only approve and reject are decisions")
	assert.Empty(t, lab.changes.changes())

	require.Equal(t, http.StatusNoContent, lab.decide(t, "ann", approval.ID, `{"decision":"reject"}`).StatusCode)
	lab.nextOf(t, "reply")
}

func TestChatApprovalRouteAnswersAWrongNameAsValidationAndKeepsTheApprovalOpen(t *testing.T) {
	lab := openApprovalStream(t, "ann", "change_delete", claude.Response{StopReason: "tool_use", Content: []claude.ContentBlock{{
		Type: "tool_use", ID: "1", Name: "delete_database", Input: json.RawMessage(`{"id":"` + approvalsDatabase + `"}`),
	}}}, claude.Response{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: "Deleted."}}})
	var approval approvalEvent
	require.NoError(t, json.Unmarshal([]byte(lab.nextOf(t, "approval").data), &approval))
	assert.True(t, approval.Destructive)

	wrong := lab.decide(t, "ann", approval.ID, `{"decision":"approve","name":"orders"}`)
	require.Equal(t, http.StatusBadRequest, wrong.StatusCode)
	body, _ := io.ReadAll(wrong.Body)
	assert.Contains(t, string(body), "database name exactly")
	assert.Empty(t, lab.changes.changes())

	require.Equal(t, http.StatusNoContent, lab.decide(t, "ann", approval.ID, `{"decision":"approve","name":"orders-dev"}`).StatusCode)
	assert.Contains(t, lab.nextOf(t, "reply").data, "Deleted.")
	assert.Equal(t, []string{"DELETE /api/databases/" + approvalsDatabase}, lab.changes.changes())
}
