package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// runFieldLine is one line a runFieldLogger recorded, bound pairs first.
type runFieldLine struct {
	msg string
	kvs []any
}

func (l runFieldLine) field(key string) any {
	for i := 0; i+1 < len(l.kvs); i += 2 {
		if k, ok := l.kvs[i].(string); ok && k == key {
			return l.kvs[i+1]
		}
	}
	return nil
}

type runFieldSink struct {
	mu    sync.Mutex
	lines []runFieldLine
}

// runFieldLogger records every line with its bound and own key-value
// pairs; With binds pairs written before each line's own.
type runFieldLogger struct {
	sink  *runFieldSink
	bound []any
}

func (l runFieldLogger) Debug(msg string, kvs ...any) { l.add(msg, kvs) }
func (l runFieldLogger) Info(msg string, kvs ...any)  { l.add(msg, kvs) }
func (l runFieldLogger) Warn(msg string, kvs ...any)  { l.add(msg, kvs) }
func (l runFieldLogger) Error(msg string, kvs ...any) { l.add(msg, kvs) }
func (l runFieldLogger) Fatal(msg string, kvs ...any) { l.add(msg, kvs) }

func (l runFieldLogger) With(kvs ...any) contract.Logger {
	return runFieldLogger{sink: l.sink, bound: append(append([]any(nil), l.bound...), kvs...)}
}

func (l runFieldLogger) add(msg string, kvs []any) {
	all := append(append([]any(nil), l.bound...), kvs...)
	l.sink.mu.Lock()
	defer l.sink.mu.Unlock()
	l.sink.lines = append(l.sink.lines, runFieldLine{msg: msg, kvs: all})
}

// The line the scheduler writes for a run names the task under task_name
// and carries the trace and span the run's events carry.
func TestRunLines_CarryTheTaskNameAndTheRunTrace(t *testing.T) {
	s := New()
	logger := runFieldLogger{sink: &runFieldSink{}}
	s.SetLogger(logger)
	var mu sync.Mutex
	var starting *ScheduledTaskStarting
	s.SetEventDispatcher(func(_ context.Context, ev interface{}) error {
		if e, ok := ev.(*ScheduledTaskStarting); ok {
			mu.Lock()
			starting = e
			mu.Unlock()
		}
		return nil
	})
	s.Call(func() {}).Name("nightly-report").Cron("* * * * *")

	s.runDueJobs()
	done := make(chan struct{})
	go func() {
		waitTicks(s)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waiting for the ticks timed out")
	}

	mu.Lock()
	defer mu.Unlock()
	if starting == nil {
		t.Fatal("no scheduled.starting event")
	}
	logger.sink.mu.Lock()
	defer logger.sink.mu.Unlock()
	var run *runFieldLine
	for _, l := range logger.sink.lines {
		if l.msg == "Running job" {
			l := l
			run = &l
		}
	}
	if run == nil {
		t.Fatalf("no run line in %+v", logger.sink.lines)
	}
	for key, want := range map[string]string{
		"task_name": "nightly-report",
		"trace_id":  starting.TraceID,
		"span_id":   starting.SpanID,
	} {
		if got := run.field(key); got != want {
			t.Errorf("%s = %v, want %q (%v)", key, got, want, run.kvs)
		}
	}
	if got := run.field("name"); got != nil {
		t.Errorf("run line still carries name = %v", got)
	}
}
