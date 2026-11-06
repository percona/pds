package api_test

import (
	"context"

	"github.com/percona/pds/adapter/internal/client/claude"
)

// scriptedCompleter is the fake Anthropic client the chat tests share.
// An empty script still finishes a turn, so a test that does not care about
// the text gets one.
type scriptedCompleter struct {
	replies []claude.Response
	err     error
	calls   []claude.Request
}

func (s *scriptedCompleter) Complete(_ context.Context, req claude.Request) (*claude.Response, error) {
	s.calls = append(s.calls, req)
	if s.err != nil {
		return nil, s.err
	}
	if len(s.replies) == 0 {
		return &claude.Response{StopReason: "end_turn", Content: []claude.ContentBlock{{
			Type: "text",
			Text: `{"sentence":"empty","findings":[],"actions":[]}`,
		}}}, nil
	}
	reply := s.replies[0]
	s.replies = s.replies[1:]

	return &reply, nil
}
