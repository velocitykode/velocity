package velocity

import (
	"sync"

	"github.com/velocitykode/velocity/contract"
)

// fieldLine is one line a fieldLogger recorded: its level, message and
// every key-value pair it carried, the bound ones first.
type fieldLine struct {
	level string
	msg   string
	kvs   []any
}

// field returns the value of key on the line, or nil.
func (l fieldLine) field(key string) any {
	for i := 0; i+1 < len(l.kvs); i += 2 {
		if k, ok := l.kvs[i].(string); ok && k == key {
			return l.kvs[i+1]
		}
	}
	return nil
}

// fieldSink is the storage every logger bound from one fieldLogger shares.
type fieldSink struct {
	mu    sync.Mutex
	lines []fieldLine
}

// fieldLogger records every line with its bound and own key-value pairs.
// With binds pairs written before each line's own.
type fieldLogger struct {
	sink  *fieldSink
	bound []any
}

func newFieldLogger() fieldLogger { return fieldLogger{sink: &fieldSink{}} }

func (l fieldLogger) Debug(msg string, kvs ...any) { l.add("debug", msg, kvs) }
func (l fieldLogger) Info(msg string, kvs ...any)  { l.add("info", msg, kvs) }
func (l fieldLogger) Warn(msg string, kvs ...any)  { l.add("warn", msg, kvs) }
func (l fieldLogger) Error(msg string, kvs ...any) { l.add("error", msg, kvs) }
func (l fieldLogger) Fatal(msg string, kvs ...any) { l.add("fatal", msg, kvs) }

func (l fieldLogger) With(kvs ...any) contract.Logger {
	bound := append(append([]any(nil), l.bound...), kvs...)
	return fieldLogger{sink: l.sink, bound: bound}
}

func (l fieldLogger) add(level, msg string, kvs []any) {
	all := append(append([]any(nil), l.bound...), kvs...)
	l.sink.mu.Lock()
	defer l.sink.mu.Unlock()
	l.sink.lines = append(l.sink.lines, fieldLine{level: level, msg: msg, kvs: all})
}

func (l fieldLogger) snapshot() []fieldLine {
	l.sink.mu.Lock()
	defer l.sink.mu.Unlock()
	return append([]fieldLine(nil), l.sink.lines...)
}

func (l fieldLogger) reset() {
	l.sink.mu.Lock()
	defer l.sink.mu.Unlock()
	l.sink.lines = nil
}
