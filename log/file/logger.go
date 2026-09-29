package file

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/log/internal/sanitize"
)

// Default permission bits for the on-disk log file and its containing
// directory. Log files routinely capture request bodies, error stack
// traces, headers, and the occasional session ID or PII shape; on a
// multi-tenant host the previous 0o644 / 0o755 defaults let any local
// user grep through every record. 0o600 / 0o700 applies least
// privilege for files holding sensitive material. Operators who need
// group/world read can opt in with WithFileMode.
const (
	defaultFileMode os.FileMode = 0o600
	defaultDirMode  os.FileMode = 0o700
)

// FileLoggerOption mutates a FileLogger at construction time.
type FileLoggerOption func(*FileLogger)

// WithFileMode overrides the default 0o600 / 0o700 perms used for the
// log file and its parent directory. The directory mode is derived
// from the file mode by mirroring the user-execute bit into the
// requested mode so a 0o644 file mode produces a 0o755 directory, and
// 0o660 produces 0o770. Operators who genuinely want a world-readable
// log file (rare, and usually a bad idea) opt in here. The default
// stays tight so a stock deployment never leaks logs across local
// user accounts.
func WithFileMode(mode os.FileMode) FileLoggerOption {
	return func(f *FileLogger) {
		f.fileMode = mode
		f.dirMode = dirModeFromFileMode(mode)
	}
}

// WithFileLock enables advisory whole-file locking around every write
// so two Velocity processes writing to the same log file do not
// interleave bytes mid-record. Required when running multiple
// instances behind a load balancer that share a host log directory,
// or when systemd Type=forking spawns siblings sharing the same
// stdout/file.
//
// The lock is acquired via flock(LOCK_EX) on supported platforms
// (Linux, *BSD, Darwin). On Windows the option is a no-op (the
// stdlib does not expose a portable equivalent and Windows file
// semantics differ enough that callers should serialize at the
// application layer). Default off: matches Monolog's useLocking
// default and avoids the ~5-10 percent per-write cost on the common
// single-writer deployment.
//
// Even with WithFileLock enabled the in-process mutex still
// serialises writes within one process so concurrent goroutines do
// not contend with each other through the kernel. The flock layer
// only kicks in for cross-process coordination.
func WithFileLock() FileLoggerOption {
	return func(f *FileLogger) {
		f.useFileLock = true
	}
}

// dirModeFromFileMode derives a sensible directory mode from a file
// mode: any read bit becomes both read+execute on the directory (so
// the entry is listable as well as openable). Default file mode 0o600
// yields directory mode 0o700; 0o644 yields 0o755; 0o660 yields 0o770.
func dirModeFromFileMode(fileMode os.FileMode) os.FileMode {
	perm := fileMode & 0o777
	dir := perm
	// Mirror the read bit into the execute bit at each scope so a
	// readable file lives in a traversable directory.
	if perm&0o400 != 0 {
		dir |= 0o100
	}
	if perm&0o040 != 0 {
		dir |= 0o010
	}
	if perm&0o004 != 0 {
		dir |= 0o001
	}
	return dir
}

// FileLogger writes log messages to daily rotating files.
// Thread-safe with automatic date-based file rotation and optional retention cleanup.
type FileLogger struct {
	path        string
	days        int               // retention days; 0 means keep forever
	level       contract.LogLevel // lowest level written; Unset writes every level
	fileMode    os.FileMode       // perms applied to new and pre-existing log files
	dirMode     os.FileMode       // perms applied to the containing directory
	useFileLock bool              // opt-in cross-process advisory locking; see WithFileLock
	mu          sync.Mutex
	file        *os.File
	date        string
	// closed is set by Shutdown, under mu, and never cleared: a write
	// after it is dropped instead of reopening the file.
	closed bool
	// fields are the key-value pairs With bound, written before each
	// line's own pairs.
	fields []any
	// base is the logger that owns the file a logger With returned writes
	// to, under base's lock; nil on the owner itself.
	base *FileLogger
}

// NewFileLogger creates a file logger that writes to the specified directory.
// Log files are named velocity-YYYY-MM-DD.log and rotate daily.
// days sets retention (0 = keep forever). level sets the lowest severity written.
// Files default to mode 0o600 inside a 0o700 directory; use WithFileMode to override.
func NewFileLogger(path string, days int, level contract.LogLevel, opts ...FileLoggerOption) *FileLogger {
	f := &FileLogger{
		path:     path,
		days:     days,
		level:    level,
		fileMode: defaultFileMode,
		dirMode:  defaultDirMode,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// ensureFile ensures the log file exists and handles daily rotation.
// Creates new file if date has changed or file doesn't exist
func (f *FileLogger) ensureFile() error {
	currentDate := time.Now().Format("2006-01-02")

	if f.date == currentDate && f.file != nil {
		return nil
	}

	if f.file != nil {
		err := f.file.Close()
		if err != nil {
			return err
		}
	}

	if err := os.MkdirAll(f.path, f.dirMode); err != nil {
		return err
	}
	// MkdirAll preserves the perms of a pre-existing directory. Force
	// the configured dirMode here so a stale 0o755 .vel/logs from an
	// older binary tightens on next boot instead of staying world-
	// listable.
	if err := os.Chmod(f.path, f.dirMode); err != nil {
		return err
	}

	filename := filepath.Join(f.path, fmt.Sprintf("velocity-%s.log", currentDate))
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, f.fileMode)
	if err != nil {
		return err
	}
	// os.OpenFile does NOT chmod a pre-existing file. A log file laid
	// down by an older 0o644 binary, or chmodded loose by an operator,
	// keeps its world-read bits across re-runs and leaks the entire
	// log history. Force the configured fileMode on every open so the
	// tight perm becomes an invariant the next reader can rely on, not
	// just an initial condition.
	if err := os.Chmod(filename, f.fileMode); err != nil {
		_ = file.Close()
		return err
	}

	f.file = file
	oldDate := f.date
	f.date = currentDate

	// Clean up old log files on rotation (date changed)
	if f.days > 0 && oldDate != currentDate {
		async.Go(f.cleanup)
	}

	return nil
}

// owner returns the logger that owns the file f writes to: f itself, or
// the logger With bound f from.
func (f *FileLogger) owner() *FileLogger {
	if f.base != nil {
		return f.base
	}
	return f
}

// With returns a FileLogger that writes to f's file, under f's lock and at
// f's level, with kvs written before each line's own pairs, after any
// pairs f already binds. A trailing key without a value is left out. The
// returned logger does not own the file: its Shutdown closes nothing.
func (f *FileLogger) With(kvs ...any) contract.Logger {
	if len(kvs)%2 == 1 {
		kvs = kvs[:len(kvs)-1]
	}
	fields := make([]any, 0, len(f.fields)+len(kvs))
	fields = append(fields, f.fields...)
	return &FileLogger{level: f.level, fields: append(fields, kvs...), base: f.owner()}
}

// log writes a formatted message to the log file with proper locking
func (f *FileLogger) log(level, msg string, kvs ...any) {
	o := f.owner()
	o.mu.Lock()
	defer o.mu.Unlock()

	// Shutdown is terminal: a late writer (a goroutine still holding this
	// logger) must not reopen the file and leave a descriptor open.
	if o.closed {
		return
	}
	if err := o.ensureFile(); err != nil {
		// The file driver cannot write through itself: the failure goes to
		// the framework's standalone fallback logger (standard error).
		fallbacklog.Logger{}.Error("velocity/log: open log file failed; the line is dropped", "error", err)
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")

	// Sanitise the user-controlled msg before emit. Without this, a
	// caller passing an HTTP URL path or any other request-derived
	// string can drop a literal CRLF into the log file and forge a
	// second record. See log/internal/sanitize.
	logLine := fmt.Sprintf("[%s] %s: %s", timestamp, level, sanitize.Value(msg))

	if len(f.fields) > 0 || len(kvs) > 0 {
		logLine += " |"
		logLine = appendPairs(logLine, f.fields)
		logLine = appendPairs(logLine, kvs)
	}

	// Cross-process advisory lock around the single write so two
	// processes sharing this log file cannot interleave bytes
	// mid-record (POSIX append is atomic below PIPE_BUF on Linux but
	// behaviour is OS-dependent on Darwin and undefined for writes
	// above the limit). No-op when useFileLock is false and on
	// platforms without flock support.
	if o.useFileLock && o.file != nil {
		release, lockErr := lockFile(o.file)
		if lockErr == nil {
			defer release()
		}
		// On flock error we still proceed with the write rather than
		// drop the line; the worst case (mixed bytes) beats silent
		// data loss.
	}

	_, err := fmt.Fprintln(o.file, logLine)
	if err != nil {
		return
	}
}

// appendPairs appends each complete key-value pair of kvs to line as
// " key=value"; a trailing key without a value is left out.
func appendPairs(line string, kvs []any) string {
	for i := 0; i+1 < len(kvs); i += 2 {
		// Both halves of the kv pair are sanitised: nothing in
		// the framework prevents a user-tainted string from
		// being passed as a key, and a CRLF in the key forges
		// a log line just as effectively as one in the value.
		k := sanitize.Value(fmt.Sprintf("%v", kvs[i]))
		v := sanitize.Value(fmt.Sprintf("%v", kvs[i+1]))
		line += fmt.Sprintf(" %s=%s", k, v)
	}
	return line
}

// Level returns the configured minimum severity. A redacting wrapper reads
// this to skip redaction work for records this logger would discard by
// level.
func (f *FileLogger) Level() contract.LogLevel { return f.level }

// Debug logs a debug-level message to file
func (f *FileLogger) Debug(msg string, kvs ...any) {
	if f.level > contract.LogLevelDebug {
		return
	}
	f.log("DEBUG", msg, kvs...)
}

// Info logs an info-level message to file
func (f *FileLogger) Info(msg string, kvs ...any) {
	if f.level > contract.LogLevelInfo {
		return
	}
	f.log("INFO", msg, kvs...)
}

// Warn logs a warning-level message to file
func (f *FileLogger) Warn(msg string, kvs ...any) {
	if f.level > contract.LogLevelWarn {
		return
	}
	f.log("WARN", msg, kvs...)
}

// Error logs an error-level message to file
func (f *FileLogger) Error(msg string, kvs ...any) {
	if f.level > contract.LogLevelError {
		return
	}
	f.log("ERROR", msg, kvs...)
}

// Fatal logs a fatal-level message to file
func (f *FileLogger) Fatal(msg string, kvs ...any) {
	f.log("FATAL", msg, kvs...)
}

// cleanup removes log files older than the configured retention period.
func (f *FileLogger) cleanup() {
	if f.days <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -f.days)
	entries, err := os.ReadDir(f.path)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if len(name) < 23 || name[:9] != "velocity-" || name[len(name)-4:] != ".log" {
			continue
		}
		dateStr := name[9 : len(name)-4] // extract YYYY-MM-DD
		fileDate, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		if fileDate.Before(cutoff) {
			os.Remove(filepath.Join(f.path, name))
		}
	}
}

// Shutdown closes the underlying file handle. It is terminal: every later
// write, through f or a logger With returned from it, is dropped rather
// than reopening the file, and a second Shutdown returns nil. A logger With
// returned owns no file and closes nothing.
func (f *FileLogger) Shutdown(ctx context.Context) error {
	if f.base != nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	if f.file != nil {
		err := f.file.Close()
		f.file = nil
		return err
	}
	return nil
}
