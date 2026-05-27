//go:build unix

package drivers

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWithFileLock_SetsField pins the option-wiring: WithFileLock
// flips useFileLock to true. Default stays false so single-writer
// deployments do not pay the syscall cost.
func TestWithFileLock_SetsField(t *testing.T) {
	defaultLogger := NewFileLogger(t.TempDir(), 0, 0)
	if defaultLogger.useFileLock {
		t.Error("default NewFileLogger has useFileLock=true; want false")
	}

	withLock := NewFileLogger(t.TempDir(), 0, 0, WithFileLock())
	if !withLock.useFileLock {
		t.Error("NewFileLogger(..., WithFileLock()) has useFileLock=false; want true")
	}
}

// TestLockFile_AcquiresAndReleases pins the lockFile primitive
// behaviour: a successful Lock returns a release closure that is safe
// to call once; after release a second Lock attempt succeeds. This is
// the load-bearing primitive WithFileLock layers on; if it regresses
// the multi-process invariant disappears.
func TestLockFile_AcquiresAndReleases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock.test")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	release1, err := lockFile(f)
	if err != nil {
		t.Fatalf("lockFile first acquire: %v", err)
	}
	release1()

	// Re-lock after release must succeed.
	release2, err := lockFile(f)
	if err != nil {
		t.Fatalf("lockFile re-acquire after release: %v", err)
	}
	release2()
}

// TestFileLogger_WithFileLock_InProcessParallelWritesAtomic pins that
// concurrent writes through one FileLogger instance with WithFileLock
// enabled produce whole-record output: every emitted line is one of
// the input strings, never an interleaving fragment. The in-process
// mutex is the primary serialiser; this test exercises the combined
// mu+flock path under load.
func TestFileLogger_WithFileLock_InProcessParallelWritesAtomic(t *testing.T) {
	dir := t.TempDir()
	logger := NewFileLogger(dir, 0, 0, WithFileLock())
	defer func() {
		if logger.file != nil {
			logger.file.Close()
		}
	}()

	const writers = 16
	const perWriter = 64
	payload := strings.Repeat("X", 4*1024) // 4 KiB body so any tearing > PIPE_BUF surfaces

	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				logger.Info(payload)
			}
		}()
	}
	wg.Wait()

	// Flush to disk
	if logger.file != nil {
		_ = logger.file.Sync()
	}

	logFile := filepath.Join(dir, "velocity-"+time.Now().Format("2006-01-02")+".log")
	fh, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}
	defer fh.Close()

	scanner := bufio.NewScanner(fh)
	scanner.Buffer(make([]byte, 64*1024), 128*1024)
	lines := 0
	for scanner.Scan() {
		line := scanner.Text()
		// Every line must end with INFO: <payload> shape. Anything
		// else indicates a torn write.
		if !strings.Contains(line, "INFO: ") || !strings.HasSuffix(line, payload) {
			t.Fatalf("torn line at #%d: %.120s... (len=%d)", lines, line, len(line))
		}
		lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if lines != writers*perWriter {
		t.Errorf("got %d lines, want %d", lines, writers*perWriter)
	}
}

// TestFileLogger_WithFileLock_CrossProcessNoTear spawns this binary
// (re-execs itself) with TEST_FILELOCK_WRITER set to a payload
// directive so two PIDs share the same log file with WithFileLock
// enabled. After both finish, the log file must contain only whole
// lines (each one ending with the expected payload). This is the
// canonical regression for the cross-process coordination claim:
// without WithFileLock, writes above PIPE_BUF (4 KiB on Linux) can
// interleave; with it, they cannot.
//
// Skipped when the test binary is run via `go test -short` or when
// the helper directive is set (we are the helper).
func TestFileLogger_WithFileLock_CrossProcessNoTear(t *testing.T) {
	if os.Getenv("TEST_FILELOCK_WRITER") != "" {
		// We are a helper subprocess; the main test invokes us below.
		runFileLockWriterHelper()
		return
	}
	if testing.Short() {
		t.Skip("cross-process spawn skipped under -short")
	}

	dir := t.TempDir()
	// Pre-create today's log file at the canonical name so both
	// processes point at the same target. The helper logger writes
	// here when LOG_DIR is set.
	binPath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	// Test self-exec needs to be able to re-enter THIS test. The
	// stdlib test binary supports the -test.run flag for re-entry.
	// Payload sized at 8 KiB so writes exceed POSIX PIPE_BUF (4 KiB on
	// Linux, 512 on some BSDs). Below PIPE_BUF append() is atomic by
	// kernel guarantee even without flock; above it, O_APPEND alone
	// does not serialise so two processes can interleave bytes. This
	// is exactly the regime WithFileLock is meant to cover.
	const writes = 32
	payload := strings.Repeat("p", 8*1024) + "END"

	spawn := func() *exec.Cmd {
		cmd := exec.Command(binPath, "-test.run=TestFileLogger_WithFileLock_CrossProcessNoTear", "-test.v")
		cmd.Env = append(os.Environ(),
			"TEST_FILELOCK_WRITER=1",
			"LOG_DIR="+dir,
			"LOG_WRITES=32",
			"LOG_PAYLOAD="+payload,
		)
		return cmd
	}

	cmdA := spawn()
	cmdB := spawn()

	errA := make(chan error, 1)
	errB := make(chan error, 1)
	go func() { errA <- cmdA.Run() }()
	go func() { errB <- cmdB.Run() }()

	if err := <-errA; err != nil {
		t.Fatalf("helper A: %v", err)
	}
	if err := <-errB; err != nil {
		t.Fatalf("helper B: %v", err)
	}

	logFile := filepath.Join(dir, "velocity-"+time.Now().Format("2006-01-02")+".log")
	fh, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}
	defer fh.Close()

	scanner := bufio.NewScanner(fh)
	scanner.Buffer(make([]byte, 64*1024), 128*1024)
	lines := 0
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasSuffix(line, payload) {
			t.Fatalf("torn cross-process line at #%d: %.120s... (len=%d)", lines, line, len(line))
		}
		lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	want := writes * 2
	if lines != want {
		t.Errorf("got %d lines from two helpers, want %d", lines, want)
	}
}

// runFileLockWriterHelper is the re-entry point used by the
// cross-process test. Reads LOG_DIR / LOG_WRITES / LOG_PAYLOAD from
// the env, writes that many lines through a WithFileLock-enabled
// logger, and returns. Lives in test code so the main package does
// not grow a magic entry point.
func runFileLockWriterHelper() {
	dir := os.Getenv("LOG_DIR")
	if dir == "" {
		return
	}
	payload := os.Getenv("LOG_PAYLOAD")
	writes := 32

	logger := NewFileLogger(dir, 0, 0, WithFileLock())
	defer func() { _ = logger.Shutdown(context.Background()) }()
	for i := 0; i < writes; i++ {
		logger.Info(payload)
	}
}
