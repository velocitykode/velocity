package velocity

import (
	"context"
	"log/slog"
	"runtime"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// slogLogger writes contract.Logger lines to a *slog.Logger's handler, each
// level at slog's level of the same name. Fatal writes at error level and
// never exits: library code never exits the process. A record names the
// caller of the contract.Logger method as its source, the frame the
// *slog.Logger method called directly would name, so a handler that reports
// the source (AddSource, or the default handler under log.Lshortfile) prints
// the same line.
type slogLogger struct{ l *slog.Logger }

var _ contract.Logger = slogLogger{}

func (s slogLogger) Debug(msg string, kvs ...any) { s.write(slog.LevelDebug, msg, kvs) }
func (s slogLogger) Info(msg string, kvs ...any)  { s.write(slog.LevelInfo, msg, kvs) }
func (s slogLogger) Warn(msg string, kvs ...any)  { s.write(slog.LevelWarn, msg, kvs) }
func (s slogLogger) Error(msg string, kvs ...any) { s.write(slog.LevelError, msg, kvs) }
func (s slogLogger) Fatal(msg string, kvs ...any) { s.write(slog.LevelError, msg, kvs) }

// write is the output path of *slog.Logger's level methods, with the source
// frame taken past slogLogger's own method.
func (s slogLogger) write(level slog.Level, msg string, kvs []any) {
	ctx := context.Background()
	if !s.l.Enabled(ctx, level) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(3, pcs[:]) // skip runtime.Callers, write and the level method
	r := slog.NewRecord(time.Now(), level, msg, pcs[0])
	r.Add(kvs...)
	_ = s.l.Handler().Handle(ctx, r)
}
