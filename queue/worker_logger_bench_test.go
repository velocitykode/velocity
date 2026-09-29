package queue

import (
	"context"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/log"
)

// loopDriver pops the same job on every call, so a benchmark can run the
// worker's per-job path back to back.
type loopDriver struct {
	noMarshalDriver
	job Job
}

func (d loopDriver) PopCtx(context.Context, string) (Job, error) { return d.job, nil }

type benchIDJob struct{}

func (benchIDJob) Handle() error { return nil }
func (benchIDJob) Failed(error)  {}
func (benchIDJob) JobID() string { return "bench-job" }

// fieldLogger is an installed logger whose With copies the bound pairs,
// as a typical structured logger does.
type fieldLogger struct{ fields []any }

func (fieldLogger) Debug(string, ...any) {}
func (fieldLogger) Info(string, ...any)  {}
func (fieldLogger) Warn(string, ...any)  {}
func (fieldLogger) Error(string, ...any) {}
func (fieldLogger) Fatal(string, ...any) {}
func (l fieldLogger) With(kvs ...any) contract.Logger {
	return fieldLogger{fields: append(append([]any(nil), l.fields...), kvs...)}
}

// BenchmarkWorker_ProcessJobLogger measures the worker's per-job path for
// a job that succeeds, which binds the job's fields to the worker's logger
// once (With) and writes no line, for each kind of logger.
func BenchmarkWorker_ProcessJobLogger(b *testing.B) {
	consoleLogger, err := log.NewLogger(log.LogConfig{Driver: "console"})
	if err != nil {
		b.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		logger contract.Logger
	}{
		{"none", nil},
		{"default", consoleLogger},
		{"installed", fieldLogger{}},
	} {
		b.Run(c.name, func(b *testing.B) {
			opts := []Option{WithBackoff(func(int) time.Duration { return 0 })}
			if c.logger != nil {
				opts = append(opts, WithWorkerLogger(c.logger))
			}
			w := NewWorker(loopDriver{job: benchIDJob{}}, "bench", func(Job) error { return nil }, opts...)
			w.ctx, w.cancel = context.WithCancel(context.Background())
			defer w.cancel()
			b.ReportAllocs()
			for b.Loop() {
				if err := w.processJob(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
