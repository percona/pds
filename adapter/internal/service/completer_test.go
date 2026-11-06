package service_test

import (
	"context"
	"time"

	"github.com/percona/pds/adapter/internal/client/claude"
	"github.com/percona/pds/adapter/internal/engine"
)

// scriptedCompleter is the fake Anthropic client the chat tests share.
// An empty script still finishes a turn, so a test that does not care about
// the text gets one.
type scriptedCompleter struct {
	replies []claude.Response
	calls   []claude.Request
}

func (s *scriptedCompleter) Complete(_ context.Context, req claude.Request) (*claude.Response, error) {
	s.calls = append(s.calls, req)
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

func testInstance() *engine.Instance {
	now := time.Now()

	return &engine.Instance{
		ID:             "pds.orders-dev",
		Name:           "orders-dev",
		Engine:         "mysql",
		Profile:        "dev",
		Status:         engine.Status{Phase: engine.PhaseReady, Message: "Ready"},
		Sizing:         engine.Sizing{Members: 1},
		CreatedAt:      now,
		LastBackupAt:   &now,
		BackupSchedule: &engine.BackupSchedule{Name: "daily", Cron: "0 2 * * *"},
		Connection: &engine.Connection{
			URI: "mysql://app:s3cret@orders-dev:3306/orders",
		},
	}
}
