package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/percona/pds/adapter/internal/log"
)

// slowQuery is how long a statement runs before it is worth a log line.
const slowQuery = 500 * time.Millisecond

// gormLogger sends GORM's output through the adapter's slog handler, which is
// the one that strips credentials. GORM's own logger writes straight to stdout
// and would print a failed connection string verbatim.
type gormLogger struct {
	logger *slog.Logger
	level  gormlogger.LogLevel
}

func newGormLogger(logger *slog.Logger) gormlogger.Interface {
	return &gormLogger{logger: logger, level: gormlogger.Warn}
}

func (l *gormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	return &gormLogger{logger: l.logger, level: level}
}

func (l *gormLogger) Info(ctx context.Context, msg string, args ...any) {
	l.log(ctx, gormlogger.Info, slog.LevelInfo, msg, args...)
}

func (l *gormLogger) Warn(ctx context.Context, msg string, args ...any) {
	l.log(ctx, gormlogger.Warn, slog.LevelWarn, msg, args...)
}

func (l *gormLogger) Error(ctx context.Context, msg string, args ...any) {
	l.log(ctx, gormlogger.Error, slog.LevelError, msg, args...)
}

func (l *gormLogger) log(ctx context.Context, want gormlogger.LogLevel, level slog.Level, msg string, args ...any) {
	if l.level < want {
		return
	}

	l.logger.Log(ctx, level, log.RedactText(msgWithArgs(msg, args...)))
}

// ParamsFilter drops the bind values before gorm renders the statement, so the
// log keeps the placeholders instead.
//
// gorm interpolates the values into the SQL on its way to the logger, and it
// only skips that for a logger implementing this method. Without it a model
// holding a token or a password writes it out on any failed or slow statement,
// and redaction cannot help: the value arrives as a bare SQL literal with no
// key beside it to match on.
func (l *gormLogger) ParamsFilter(_ context.Context, sql string, _ ...any) (string, []any) {
	return sql, nil
}

// Trace reports the statement that failed or ran long. The SQL is logged but
// never the bound values: a parameter can be a password.
func (l *gormLogger) Trace(
	ctx context.Context,
	begin time.Time,
	fc func() (string, int64),
	err error,
) {
	if l.level <= gormlogger.Silent {
		return
	}

	elapsed := time.Since(begin)

	switch {
	case err != nil && l.level >= gormlogger.Error && !errors.Is(err, gorm.ErrRecordNotFound):
		sql, rows := fc()
		l.logger.LogAttrs(ctx, slog.LevelError, "database statement failed",
			slog.String("sql", log.RedactText(sql)),
			slog.Int64("rows", rows),
			slog.Duration("elapsed", elapsed),
			slog.String("cause", log.RedactText(err.Error())))
	case elapsed > slowQuery && l.level >= gormlogger.Warn:
		sql, rows := fc()
		l.logger.LogAttrs(ctx, slog.LevelWarn, "slow database statement",
			slog.String("sql", log.RedactText(sql)),
			slog.Int64("rows", rows),
			slog.Duration("elapsed", elapsed))
	}
}

// msgWithArgs renders GORM's printf style call. slog takes a plain message, so
// a format string is expanded here rather than passed through unexpanded.
//
// GORM also calls the logger with args and a message carrying no verbs, which
// Sprintf would render with a trailing %!(EXTRA ...). Those are appended
// instead.
func msgWithArgs(msg string, args ...any) string {
	if len(args) == 0 {
		return msg
	}

	if strings.ContainsRune(msg, '%') {
		return fmt.Sprintf(msg, args...)
	}

	return strings.TrimSpace(msg + " " + fmt.Sprint(args...))
}
