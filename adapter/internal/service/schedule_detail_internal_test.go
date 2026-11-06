package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/percona/pds/adapter/internal/engine"
)

func TestResultDetailNamesATimeRetentionWindow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		window string
		want   string
	}{
		{"4w", "Daily at 02:00 UTC, keeps copies for 4 weeks"},
		{"1d", "Daily at 02:00 UTC, keeps copies for 1 day"},
		{"2m", "Daily at 02:00 UTC, keeps copies for 2 months"},
	}
	for _, tt := range tests {
		t.Run(tt.window, func(t *testing.T) {
			t.Parallel()

			schedule := &engine.BackupSchedule{Cron: "0 2 * * *", RetentionWindow: tt.window}

			assert.Equal(t, tt.want, resultDetail(schedule, nil, nil))
		})
	}
}
