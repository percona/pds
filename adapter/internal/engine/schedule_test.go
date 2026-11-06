package engine_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
)

func TestScheduleCron(t *testing.T) {
	tests := []struct {
		name string
		in   engine.ScheduleInput
		want string
	}{
		{"hourly at a minute", engine.ScheduleInput{Period: engine.PeriodHourly, Minute: 15}, "15 * * * *"},
		{"hourly on the hour", engine.ScheduleInput{Period: engine.PeriodHourly}, "0 * * * *"},
		{"daily", engine.ScheduleInput{Period: engine.PeriodDaily, Minute: 30, Hour: 2}, "30 2 * * *"},
		{"daily at midnight", engine.ScheduleInput{Period: engine.PeriodDaily}, "0 0 * * *"},
		{"weekly on sunday", engine.ScheduleInput{Period: engine.PeriodWeekly, Minute: 5, Hour: 23}, "5 23 * * 0"},
		{"weekly on saturday", engine.ScheduleInput{Period: engine.PeriodWeekly, Hour: 4, Weekday: 6}, "0 4 * * 6"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.in.Cron())
		})
	}
}

func TestDefaultScheduleIsDailyAtTwoKeepingSeven(t *testing.T) {
	def := engine.DefaultSchedule()

	assert.True(t, def.Enabled)
	assert.Equal(t, "0 2 * * *", def.Cron())
	assert.Equal(t, 7, def.RetentionCopies)
	require.NoError(t, def.Validate())
}

func TestScheduleValidate(t *testing.T) {
	valid := func(mut func(*engine.ScheduleInput)) engine.ScheduleInput {
		in := engine.ScheduleInput{
			Enabled:         true,
			Period:          engine.PeriodWeekly,
			Minute:          0,
			Hour:            2,
			Weekday:         3,
			RetentionCopies: 7,
		}
		mut(&in)

		return in
	}

	tests := []struct {
		name  string
		in    engine.ScheduleInput
		field string
	}{
		{"valid weekly", valid(func(*engine.ScheduleInput) {}), ""},
		{"valid hourly", valid(func(in *engine.ScheduleInput) { in.Period = engine.PeriodHourly; in.Hour = 0; in.Weekday = 0 }), ""},
		{"valid bounds", valid(func(in *engine.ScheduleInput) { in.Minute = 59; in.Hour = 23; in.Weekday = 6; in.RetentionCopies = 30 }), ""},
		{"one copy", valid(func(in *engine.ScheduleInput) { in.RetentionCopies = 1 }), ""},
		{"unknown period", valid(func(in *engine.ScheduleInput) { in.Period = "monthly" }), "period"},
		{"empty period", valid(func(in *engine.ScheduleInput) { in.Period = "" }), "period"},
		{"minute below", valid(func(in *engine.ScheduleInput) { in.Minute = -1 }), "minute"},
		{"minute above", valid(func(in *engine.ScheduleInput) { in.Minute = 60 }), "minute"},
		{"hour below", valid(func(in *engine.ScheduleInput) { in.Hour = -1 }), "hour"},
		{"hour above", valid(func(in *engine.ScheduleInput) { in.Hour = 24 }), "hour"},
		{"weekday below", valid(func(in *engine.ScheduleInput) { in.Weekday = -1 }), "weekday"},
		{"weekday above", valid(func(in *engine.ScheduleInput) { in.Weekday = 7 }), "weekday"},
		{"no copies", valid(func(in *engine.ScheduleInput) { in.RetentionCopies = 0 }), "retentionCopies"},
		{"too many copies", valid(func(in *engine.ScheduleInput) { in.RetentionCopies = 31 }), "retentionCopies"},
		{"disabled skips the rest", engine.ScheduleInput{Enabled: false, Period: "monthly", Hour: 99}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate()
			if tt.field == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.ErrorIs(t, err, engine.ErrInvalidSchedule)
			var field *engine.ScheduleError
			require.True(t, errors.As(err, &field))
			assert.Equal(t, tt.field, field.Field)
			assert.Contains(t, field.UserMessage(), "`"+tt.field+"`")
		})
	}
}

func TestParseScheduleCronRoundTrips(t *testing.T) {
	inputs := []engine.ScheduleInput{
		{Enabled: true, Period: engine.PeriodHourly, Minute: 45, RetentionCopies: 3},
		{Enabled: true, Period: engine.PeriodDaily, Minute: 0, Hour: 2, RetentionCopies: 7},
		{Enabled: true, Period: engine.PeriodWeekly, Minute: 10, Hour: 22, Weekday: 5, RetentionCopies: 30},
	}
	for _, in := range inputs {
		t.Run(in.Cron(), func(t *testing.T) {
			got, ok := engine.ParseScheduleCron(in.Cron())
			require.True(t, ok)
			assert.Equal(t, in.Period, got.Period)
			assert.Equal(t, in.Minute, got.Minute)
			assert.Equal(t, in.Hour, got.Hour)
			assert.Equal(t, in.Weekday, got.Weekday)
		})
	}
}

func TestParseScheduleCronRefusesWhatItCannotPhrase(t *testing.T) {
	for _, cron := range []string{
		"",
		"*/5 * * * *",
		"0 2 1 * *",
		"0 2 * * 1-5",
		"0 2 * * 7",
		"60 * * * *",
		"0 24 * * *",
		"0 2 * *",
		"@daily",
	} {
		t.Run(cron, func(t *testing.T) {
			_, ok := engine.ParseScheduleCron(cron)
			assert.False(t, ok)
		})
	}
}

func TestBackupScheduleRuns(t *testing.T) {
	var none *engine.BackupSchedule
	assert.False(t, none.Runs())
	assert.False(t, (&engine.BackupSchedule{Disabled: true}).Runs())
	assert.True(t, (&engine.BackupSchedule{}).Runs())
}
