package engine_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/percona/pds/adapter/internal/engine"
)

// A PostgreSQL copy's name is not built into a restore job, so the sentence
// must not blame one.
func TestRestoreNameTooLongSaysWhatTheNameIsBuiltInto(t *testing.T) {
	t.Parallel()

	job := &engine.RestoreNameTooLongError{Name: "x", Limit: 19}
	members := &engine.RestoreNameTooLongError{Name: "x", Limit: 37, Reason: engine.RestoreNameMembers}

	assert.Contains(t, job.UserMessage(), "restore job")
	assert.Contains(t, members.UserMessage(), "37 characters")
	assert.Contains(t, members.UserMessage(), "every member")
	assert.NotContains(t, members.UserMessage(), "restore job")
}
