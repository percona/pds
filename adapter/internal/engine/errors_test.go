package engine_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
)

// TestBoundsErrorMatchesOutOfBounds proves the HTTP layer can branch on the
// sentinel while still reading the numbers off the concrete type.
func TestBoundsErrorMatchesOutOfBounds(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("create orders: %w", &engine.BoundsError{
		Field: "CPU", Unit: "cores", Value: 99, Min: 1, Max: 64,
	})

	require.ErrorIs(t, err, engine.ErrOutOfBounds)

	var bounds *engine.BoundsError
	require.True(t, errors.As(err, &bounds))
	assert.Equal(t, 99, bounds.Value)
}

// TestBoundsErrorUserMessage locks the sentence the developer reads: sentence
// case, the range stated, a full stop, and no dashes.
func TestBoundsErrorUserMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  engine.BoundsError
		want string
	}{
		{
			name: "cpu",
			err:  engine.BoundsError{Field: "CPU", Unit: "cores", Value: 99, Min: 1, Max: 64},
			want: "CPU must be between 1 and 64 cores.",
		},
		{
			name: "storage",
			err:  engine.BoundsError{Field: "Storage", Unit: "GB", Value: 1, Min: 10, Max: 16000},
			want: "Storage must be between 10 and 16000 GB.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.err.UserMessage())
		})
	}
}

// TestBoundsErrorLogTextCarriesTheValue keeps the offending value in the log
// line, which the user-facing sentence deliberately leaves out.
func TestBoundsErrorLogTextCarriesTheValue(t *testing.T) {
	t.Parallel()

	err := &engine.BoundsError{Field: "Memory", Unit: "GB", Value: 4096, Min: 1, Max: 512}

	assert.Contains(t, err.Error(), "Memory is 4096 GB")
	assert.Contains(t, err.Error(), "want 1 to 512 GB")
}

// TestChoiceErrorMatchesOutOfBounds covers a value that has a fixed set of
// answers rather than a range, which is what member count is: a Galera cluster
// needs an odd number of members for quorum, so 2 is not "between 1 and 3".
func TestChoiceErrorMatchesOutOfBounds(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("scale orders: %w", &engine.ChoiceError{
		Field: "Members", Value: 2, Allowed: []int{1, 3, 5},
	})

	require.ErrorIs(t, err, engine.ErrOutOfBounds)

	var choice *engine.ChoiceError
	require.True(t, errors.As(err, &choice))
	assert.Equal(t, 2, choice.Value)
	assert.Contains(t, choice.Error(), "Members is 2")
}

func TestChoiceErrorUserMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  engine.ChoiceError
		want string
	}{
		{
			name: "three choices",
			err:  engine.ChoiceError{Field: "Members", Value: 2, Allowed: []int{1, 3, 5}},
			want: "Members must be 1, 3, or 5.",
		},
		{
			name: "two choices",
			err:  engine.ChoiceError{Field: "Members", Value: 4, Allowed: []int{1, 3}},
			want: "Members must be 1 or 3.",
		},
		{
			name: "one choice",
			err:  engine.ChoiceError{Field: "Members", Value: 4, Allowed: []int{1}},
			want: "Members must be 1.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.err.UserMessage())
		})
	}
}

// TestBusyErrorMatchesConflict is the 409 path: a scale that clashes with an
// operation already running is a conflict, not a validation failure.
func TestBusyErrorMatchesConflict(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("scale orders: %w", &engine.BusyError{Operation: "backup"})

	require.ErrorIs(t, err, engine.ErrBusy)

	var busy *engine.BusyError
	require.True(t, errors.As(err, &busy))
	assert.Equal(t, "backup", busy.Operation)
}

func TestBusyErrorUserMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  engine.BusyError
		want string
	}{
		{
			name: "backup",
			err:  engine.BusyError{Operation: "backup"},
			want: "A backup is running. Scale once it finishes.",
		},
		{
			name: "unnamed operation",
			err:  engine.BusyError{},
			want: "Another change is running on this database. Scale once it finishes.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.err.UserMessage())
		})
	}
}

// TestBackupSourceErrorSplitsMissingFromUnfinished is what makes a restore from
// a backup of another database a 404 and a restore from one still running a
// 409, off one error type.
func TestBackupSourceErrorSplitsMissingFromUnfinished(t *testing.T) {
	t.Parallel()

	missing := fmt.Errorf("restore orders: %w", &engine.BackupSourceError{BackupID: "orders-1"})
	require.ErrorIs(t, missing, engine.ErrBackupNotFound)
	assert.NotErrorIs(t, missing, engine.ErrBackupUnusable)

	running := fmt.Errorf("restore orders: %w",
		&engine.BackupSourceError{BackupID: "orders-1", State: engine.BackupRunning})
	require.ErrorIs(t, running, engine.ErrBackupUnusable)
	assert.NotErrorIs(t, running, engine.ErrBackupNotFound)

	var source *engine.BackupSourceError
	require.True(t, errors.As(running, &source))
	assert.Equal(t, "orders-1", source.BackupID)
	assert.Contains(t, source.Error(), "orders-1 is running")
}

func TestPresetErrorMatchesKind(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("create orders: %w", &engine.PresetError{
		Profile: "dev",
		Name:    "pds-mysql-dev",
		Kind:    engine.ErrPresetMissing,
	})

	require.ErrorIs(t, err, engine.ErrPresetMissing)
	assert.NotErrorIs(t, err, engine.ErrPresetChanged)

	var preset *engine.PresetError
	require.True(t, errors.As(err, &preset))
	assert.Equal(t, "pds-mysql-dev", preset.Name)
}

func TestPresetErrorUserMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  engine.PresetError
		want string
	}{
		{
			name: "missing",
			err:  engine.PresetError{Profile: "dev", Name: "pds-mysql-dev", Kind: engine.ErrPresetMissing},
			want: "The Dev profile preset is not installed.",
		},
		{
			name: "incompatible",
			err: engine.PresetError{
				Profile: "staging",
				Name:    "pds-mysql-staging",
				Kind:    engine.ErrPresetIncompatible,
			},
			want: "The Staging profile preset is not compatible with this engine.",
		},
		{
			name: "changed",
			err:  engine.PresetError{Profile: "prod", Name: "pds-mysql-prod", Kind: engine.ErrPresetChanged},
			want: "The Prod profile preset has changed. Contact Percona support.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.err.UserMessage())
			assert.NotContains(t, tt.err.UserMessage(), "!")
			assert.NotContains(t, tt.err.UserMessage(), "\u2013")
			assert.NotContains(t, tt.err.UserMessage(), "\u2014")
		})
	}
}

func TestBackupSourceErrorUserMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  engine.BackupSourceError
		want string
	}{
		{
			name: "not on this database",
			err:  engine.BackupSourceError{BackupID: "payments-1"},
			want: "That backup is not on this database.",
		},
		{
			name: "still running",
			err:  engine.BackupSourceError{BackupID: "orders-1", State: engine.BackupRunning},
			want: "That backup has not finished. Restore once it succeeds.",
		},
		{
			name: "failed",
			err:  engine.BackupSourceError{BackupID: "orders-1", State: engine.BackupFailed},
			want: "That backup failed, so there is nothing to restore from.",
		},
		{
			name: "being deleted",
			err:  engine.BackupSourceError{BackupID: "orders-1", State: engine.BackupDeleting},
			want: "That backup is being deleted.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.err.UserMessage())
		})
	}
}
