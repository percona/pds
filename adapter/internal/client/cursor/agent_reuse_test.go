package cursor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/client/cursor"
)

// fakeCursor answers the Cloud Agents calls the client makes and keeps what
// it was asked, so a test can say which agent a prompt went to.
type fakeCursor struct {
	mu sync.Mutex
	// refuse is the status a run on that agent is answered with.
	refuse map[string]int
	// deleteStatus answers DELETE of an agent, 200 when zero.
	deleteStatus int
	created      []string
	runs         map[string][]string
	deleted      []string
	nextAgent    int
}

func newFakeCursor(t *testing.T) (*fakeCursor, string) {
	t.Helper()

	f := &fakeCursor{refuse: map[string]int{}, runs: map[string][]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	return f, srv.URL
}

func promptOf(r *http.Request) string {
	var body struct {
		Prompt struct {
			Text string `json:"text"`
		} `json:"prompt"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)

	return body.Prompt.Text
}

func (f *fakeCursor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/agents":
		f.nextAgent++
		f.created = append(f.created, promptOf(r))
		_, _ = fmt.Fprintf(w, `{"agent":{"id":"bc-new-%d"},"run":{"id":"run-1","status":"FINISHED"}}`, f.nextAgent)
	case r.Method == http.MethodPost && len(parts) == 4 && parts[3] == "runs":
		if status := f.refuse[parts[2]]; status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":"refused"}}`))

			return
		}
		f.runs[parts[2]] = append(f.runs[parts[2]], promptOf(r))
		_, _ = w.Write([]byte(`{"run":{"id":"run-2","status":"FINISHED"}}`))
	case r.Method == http.MethodGet && len(parts) == 5 && parts[3] == "runs":
		_, _ = w.Write([]byte(`{"id":"` + parts[4] + `","status":"FINISHED","result":"{\"text\":\"answer\",\"actions\":[]}"}`))
	case r.Method == http.MethodGet && len(parts) == 3:
		_, _ = w.Write([]byte(`{"id":"` + parts[2] + `","latestRunId":"run-2","status":"IDLE"}`))
	case r.Method == http.MethodDelete && len(parts) == 3:
		f.deleted = append(f.deleted, parts[2])
		if f.deleteStatus != 0 {
			w.WriteHeader(f.deleteStatus)
		}
		_, _ = w.Write([]byte(`{"id":"` + parts[2] + `"}`))
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
	}
}

func newKeptClient(t *testing.T, url, agentID string) *cursor.Client {
	t.Helper()

	client, err := cursor.New(cursor.Options{
		BaseURL: url, APIKey: "cursor-key-test", Model: "composer-2", AgentID: agentID,
		Sleep: func(time.Duration) {},
	})
	require.NoError(t, err)

	return client
}

func textMessage(role, text string) claude.Message {
	return claude.Message{Role: role, Content: []claude.ContentBlock{{Type: "text", Text: text}}}
}

func thread() []claude.Message {
	return []claude.Message{
		textMessage("user", "first question"),
		textMessage("assistant", "first answer"),
		textMessage("user", "second question"),
	}
}

func TestCompleteOnAKeptAgentSendsOnlyTheNewQuestion(t *testing.T) {
	f, url := newFakeCursor(t)
	client := newKeptClient(t, url, "bc-kept")

	got, err := client.Complete(context.Background(), claude.Request{System: "Be brief.", Messages: thread()})
	require.NoError(t, err)

	assert.Empty(t, f.created, "a kept agent is not created again")
	require.Len(t, f.runs["bc-kept"], 1)
	prompt := f.runs["bc-kept"][0]
	assert.Contains(t, prompt, "second question")
	assert.NotContains(t, prompt, "first question", "the agent already holds the earlier turns")
	assert.NotContains(t, prompt, "first answer")
	assert.Contains(t, prompt, "JSON", "the reply keeps its JSON form")
	assert.Equal(t, "bc-kept", client.AgentID())
	assert.Contains(t, got.Content[0].Text, "answer")
}

func TestAgentIDIsTheAgentTheCallStarted(t *testing.T) {
	f, url := newFakeCursor(t)
	client := newKeptClient(t, url, "")
	assert.Empty(t, client.AgentID())

	_, err := client.Complete(context.Background(), claude.Request{Messages: thread()[:1]})
	require.NoError(t, err)

	require.Len(t, f.created, 1)
	assert.Equal(t, "bc-new-1", client.AgentID())
}

func TestCompleteStartsANewAgentFromTheWholeThreadWhenTheKeptOneRefusesARun(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusGone} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f, url := newFakeCursor(t)
			f.refuse["bc-kept"] = status
			client := newKeptClient(t, url, "bc-kept")

			_, err := client.Complete(context.Background(), claude.Request{System: "Be brief.", Messages: thread()})
			require.NoError(t, err)

			require.Len(t, f.created, 1)
			for _, want := range []string{"Be brief.", "first question", "first answer", "second question"} {
				assert.Contains(t, f.created[0], want, "the new agent starts from the stored thread")
			}
			assert.Equal(t, "bc-new-1", client.AgentID())
			assert.Equal(t, []string{"bc-kept"}, f.deleted, "the agent that refused is not left holding the conversation")
		})
	}
}

func TestCompleteKeepsTheAnswerWhenTheReplacedAgentCannotBeDeleted(t *testing.T) {
	f, url := newFakeCursor(t)
	f.refuse["bc-kept"] = http.StatusNotFound
	f.deleteStatus = http.StatusInternalServerError
	client := newKeptClient(t, url, "bc-kept")

	got, err := client.Complete(context.Background(), claude.Request{Messages: thread()})

	require.NoError(t, err)
	assert.Contains(t, got.Content[0].Text, "answer")
	assert.Equal(t, "bc-new-1", client.AgentID())
}

func TestCompleteDoesNotReplaceAnAgentOnAFailureThatIsNotTheAgents(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusInternalServerError, cursor.ErrRejected},
		{http.StatusBadGateway, cursor.ErrRejected},
		{http.StatusTooManyRequests, cursor.ErrRejected},
		{http.StatusUnauthorized, cursor.ErrUnauthorized},
		{http.StatusForbidden, cursor.ErrUnauthorized},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			f, url := newFakeCursor(t)
			f.refuse["bc-kept"] = tt.status
			client := newKeptClient(t, url, "bc-kept")

			_, err := client.Complete(context.Background(), claude.Request{Messages: thread()})

			require.ErrorIs(t, err, tt.want)
			assert.Empty(t, f.created, "an outage or a refused key is not a reason to start another agent")
			assert.Empty(t, f.deleted)
			assert.Equal(t, "bc-kept", client.AgentID())
		})
	}
}

func TestCompleteCarriesToolResultsToTheAgentThatReplacesOneMidTurn(t *testing.T) {
	f, url := newFakeCursor(t)
	f.refuse["bc-kept"] = http.StatusNotFound
	client := newKeptClient(t, url, "bc-kept")
	messages := append(thread(),
		claude.Message{Role: "assistant", Content: []claude.ContentBlock{{
			Type: "tool_use", ID: "1", Name: "get_status", Input: json.RawMessage(`{"id":"pds.orders-dev"}`),
		}}},
		claude.Message{Role: "user", Content: []claude.ContentBlock{{
			Type: "tool_result", ToolUseID: "1", Content: "status=ready members=3",
		}}},
	)

	_, err := client.Complete(context.Background(), claude.Request{Messages: messages})
	require.NoError(t, err)

	require.Len(t, f.created, 1)
	assert.Contains(t, f.created[0], "get_status", "the tools asked for are part of the thread")
	assert.Contains(t, f.created[0], "status=ready members=3", "a replacement mid turn must not lose what the tools returned")
}

func TestCompleteTellsTheAgentThatReplacesOneOnTheFinalRoundNotToCallTools(t *testing.T) {
	f, url := newFakeCursor(t)
	f.refuse["bc-kept"] = http.StatusNotFound
	client := newKeptClient(t, url, "bc-kept")
	tools := []claude.Tool{{Name: "get_status", Description: "Read status"}}

	_, err := client.Complete(context.Background(), claude.Request{Messages: thread(), Tools: tools, NoToolCalls: true})
	require.NoError(t, err)

	require.Len(t, f.created, 1)
	assert.Contains(t, f.created[0], "No more tools are available")
	assert.NotContains(t, f.created[0], "To call a tool")
}

func TestDeleteAgent(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{"deleted", http.StatusOK, nil},
		{"already gone", http.StatusNotFound, nil},
		{"refused key", http.StatusUnauthorized, cursor.ErrUnauthorized},
		{"failure", http.StatusInternalServerError, cursor.ErrRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, url := newFakeCursor(t)
			f.deleteStatus = tt.status
			client := newKeptClient(t, url, "")

			err := client.DeleteAgent(context.Background(), "bc-kept")

			if tt.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.want)
			}
			assert.Equal(t, []string{"bc-kept"}, f.deleted)
		})
	}
}
