// Package fallbacklog is the one logger a framework value writes through
// when nobody gave it a logger: a package used standalone, outside an app
// built by velocity.New, or a value whose logger was set back to nil. In an
// app, velocity.New hands the app logger to every framework value that
// takes one, so these lines go to the app's log instead.
//
// It depends on the standard library and the contract leaf only, so any
// framework package can use it without growing its import graph.
//
// Warn, Error and Fatal lines go to standard error, one line each, in one
// format: a UTC timestamp, the level, the message, then the key-value pairs
// as key=value. A value (or a message) that would break the line or be
// ambiguous is written as a quoted Go string:
//
//	2026-09-27T10:04:05.123Z WARN velocity/queue: redis driver connecting without TLS host=cache.internal
//	2026-09-27T10:04:05.124Z ERROR async: panic recovered panic=boom stack="goroutine 7 [running]:\n..."
//
// Debug and Info lines are dropped: nobody reads the routine output of a
// value used standalone, only its warnings and failures. Fatal writes an
// ERROR line and never exits the process.
package fallbacklog

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/velocitykode/velocity/contract"
)

// Logger is the fallback logger. The zero value is ready to use and every
// Logger writes to the same output.
type Logger struct{}

var _ contract.Logger = Logger{}

// Debug drops the line.
func (Logger) Debug(string, ...any) {}

// Info drops the line.
func (Logger) Info(string, ...any) {}

// Warn writes a WARN line.
func (Logger) Warn(msg string, kvs ...any) { write("WARN", msg, kvs) }

// Error writes an ERROR line.
func (Logger) Error(msg string, kvs ...any) { write("ERROR", msg, kvs) }

// Fatal writes an ERROR line; library code never exits the process.
func (Logger) Fatal(msg string, kvs ...any) { write("ERROR", msg, kvs) }

// With binds kvs before each line's own pairs.
func (l Logger) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

// Resolve returns l, or the fallback Logger when l is nil.
func Resolve(l contract.Logger) contract.Logger {
	if l == nil {
		return Logger{}
	}
	return l
}

var (
	mu  sync.Mutex
	out io.Writer = os.Stderr
)

// SetOutput redirects every fallback line to w and returns the writer it
// replaces; nil restores standard error. It exists for tests that assert
// what a value writes when it has no logger.
func SetOutput(w io.Writer) (previous io.Writer) {
	if w == nil {
		w = os.Stderr
	}
	mu.Lock()
	defer mu.Unlock()
	previous, out = out, w
	return previous
}

// timeLayout is RFC 3339 in UTC with millisecond precision.
const timeLayout = "2006-01-02T15:04:05.000Z07:00"

// write formats one line and writes it with a single Write under mu, so
// lines from concurrent goroutines never interleave.
func write(level, msg string, kvs []any) {
	b := make([]byte, 0, 128)
	b = time.Now().UTC().AppendFormat(b, timeLayout)
	b = append(b, ' ')
	b = append(b, level...)
	b = append(b, ' ')
	b = appendMessage(b, msg)
	for i := 0; i < len(kvs); i += 2 {
		b = append(b, ' ')
		if i+1 == len(kvs) {
			// A key without a value: keep the value, name the gap.
			b = append(b, "!BADKEY="...)
			b = appendValue(b, fmt.Sprint(kvs[i]))
			break
		}
		b = appendKey(b, fmt.Sprint(kvs[i]))
		b = append(b, '=')
		b = appendValue(b, fmt.Sprint(kvs[i+1]))
	}
	b = append(b, '\n')

	mu.Lock()
	defer mu.Unlock()
	_, _ = out.Write(b)
}

// appendMessage writes msg as is unless it would break the line.
func appendMessage(b []byte, msg string) []byte {
	if breaksLine(msg) {
		return strconv.AppendQuote(b, msg)
	}
	return append(b, msg...)
}

// appendKey writes a key, quoted when it holds a space, an equals sign, a
// quote or anything that would break the line.
func appendKey(b []byte, key string) []byte {
	if needsQuote(key) {
		return strconv.AppendQuote(b, key)
	}
	return append(b, key...)
}

// appendValue writes a value, quoted when it is empty or holds a space, an
// equals sign, a quote or anything that would break the line.
func appendValue(b []byte, v string) []byte {
	if needsQuote(v) {
		return strconv.AppendQuote(b, v)
	}
	return append(b, v...)
}

func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r == ' ' || r == '=' || r == '"' || r == utf8.RuneError || !unicode.IsPrint(r) {
			return true
		}
	}
	return false
}

// breaksLine reports whether s holds a character that would end or garble
// the line (a newline, a carriage return, any other control character, or
// invalid UTF-8).
func breaksLine(s string) bool {
	for _, r := range s {
		if r == utf8.RuneError || (r != ' ' && !unicode.IsPrint(r)) {
			return true
		}
	}
	return false
}
