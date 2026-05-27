package drivers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/velocitykode/velocity/log/internal/sanitize"
)

// Default permission bits for the on-disk log file and its containing
// directory. Log files routinely capture request bodies, error stack
// traces, headers, and the occasional session ID or PII shape; on a
// multi-tenant host the previous 0o644 / 0o755 defaults let any local
// user grep through every record. 0o600 / 0o700 is the principle of
// least privilege for files holding sensitive material (CLAUDE.md
// rule 4). Operators who need group/world read can opt in with
// WithFileMode.
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
	days        int         // retention days; 0 means keep forever
	level       int         // minimum level: 0=debug, 1=info, 2=warn, 3=error, 4=fatal
	fileMode    os.FileMode // perms applied to new and pre-existing log files
	dirMode     os.FileMode // perms applied to the containing directory
	useFileLock bool        // opt-in cross-process advisory locking; see WithFileLock
	mu          sync.Mutex
	file        *os.File
	date        string
}

// NewFileLogger creates a file logger that writes to the specified directory.
// Log files are named velocity-YYYY-MM-DD.log and rotate daily.
// days sets retention (0 = keep forever). level sets minimum severity (0=debug .. 4=fatal).
// Files default to mode 0o600 inside a 0o700 directory; use WithFileMode to override.
func NewFileLogger(path string, days int, level int, opts ...FileLoggerOption) *FileLogger {
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
	// listable. Same defense as the M-40 maintenance follow-up.
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
		go f.cleanup()
	}

	return nil
}

// log writes a formatted message to the log file with proper locking
func (f *FileLogger) log(level, msg string, kvs ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.ensureFile(); err != nil {
		fmt.Printf("Failed to open log file: %v\n", err)
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")

	// Sanitise the user-controlled msg before emit. Without this, a
	// caller passing an HTTP URL path or any other request-derived
	// string can drop a literal CRLF into the log file and forge a
	// second record. See log/internal/sanitize and audit finding H-30.
	logLine := fmt.Sprintf("[%s] %s: %s", timestamp, level, sanitize.Value(msg))

	if len(kvs) > 0 {
		logLine += " |"
		for i := 0; i < len(kvs); i += 2 {
			if i+1 < len(kvs) {
				// Both halves of the kv pair are sanitised: nothing in
				// the framework prevents a user-tainted string from
				// being passed as a key, and a CRLF in the key forges
				// a log line just as effectively as one in the value.
				k := sanitize.Value(fmt.Sprintf("%v", kvs[i]))
				v := sanitize.Value(fmt.Sprintf("%v", kvs[i+1]))
				logLine += fmt.Sprintf(" %s=%s", k, v)
			}
		}
	}

	// Cross-process advisory lock around the single write so two
	// processes sharing this log file cannot interleave bytes
	// mid-record (POSIX append is atomic below PIPE_BUF on Linux but
	// behaviour is OS-dependent on Darwin and undefined for writes
	// above the limit). No-op when useFileLock is false and on
	// platforms without flock support.
	if f.useFileLock && f.file != nil {
		release, lockErr := lockFile(f.file)
		if lockErr == nil {
			defer release()
		}
		// On flock error we still proceed with the write rather than
		// drop the line; the worst case (mixed bytes) beats silent
		// data loss.
	}

	_, err := fmt.Fprintln(f.file, logLine)
	if err != nil {
		return
	}
}

// Debug logs a debug-level message to file
func (f *FileLogger) Debug(msg string, kvs ...any) {
	if f.level > 0 {
		return
	}
	f.log("DEBUG", msg, kvs...)
}

// Info logs an info-level message to file
func (f *FileLogger) Info(msg string, kvs ...any) {
	if f.level > 1 {
		return
	}
	f.log("INFO", msg, kvs...)
}

// Warn logs a warning-level message to file
func (f *FileLogger) Warn(msg string, kvs ...any) {
	if f.level > 2 {
		return
	}
	f.log("WARN", msg, kvs...)
}

// Error logs an error-level message to file
func (f *FileLogger) Error(msg string, kvs ...any) {
	if f.level > 3 {
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

// Shutdown closes the underlying file handle, honoring the context deadline.
func (f *FileLogger) Shutdown(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file != nil {
		err := f.file.Close()
		f.file = nil
		return err
	}
	return nil
}
