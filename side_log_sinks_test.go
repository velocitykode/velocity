package velocity

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	_ "github.com/velocitykode/velocity/cache/redis"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/log"
	_ "github.com/velocitykode/velocity/log/file"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
)

// sideSinkLines are the framework lines the tests below drive once each,
// as the file driver writes them ("LEVEL: message").
var sideSinkLines = map[string]string{
	"CORS wildcard with credentials": "WARN: velocity/router: CORS allows every origin with credentials",
	"crypto decrypt failure":         "DEBUG: velocity/crypto: decrypt failed",
	"late route registration":        "WARN: velocity/router: route registered after server start",
	"unique rule DB failure":         "ERROR: velocity/validation: unique rule query failed",
	"redis cleartext":                "WARN: velocity/cache: redis store connecting to non-loopback host without TLS",
}

// driveSideSinkLines builds a bootstrapped app logging through logCfg and
// makes it write each of sideSinkLines once: a CORS middleware allowing
// every origin with credentials serves a request, the encryptor fails to
// authenticate a payload (CRYPTO_DEBUG=true), a Unique rule queries a missing table, a
// route is registered after the router started serving, and the Redis
// cache store connects without TLS to a host it cannot tell is loopback.
func driveSideSinkLines(t *testing.T, logCfg log.LogConfig) {
	t.Helper()
	t.Setenv("CRYPTO_DEBUG", "true")
	mr := miniredis.RunT(t)

	a, err := New(WithConfig(Config{
		Env:  "testing",
		Port: "0",
		Log:  logCfg,
		DB:   DBConfig{Connection: "sqlite", Database: ":memory:"},
		// "localhost." reaches the loopback miniredis, but the store does
		// not resolve names, so it warns as for a remote host.
		Cache:  CacheConfig{Driver: "redis", Prefix: "sinks", RedisHost: "localhost.", RedisPort: mr.Server().Addr().Port},
		Queue:  QueueConfig{Driver: "memory"},
		Mail:   mail.MailConfig{Driver: "log"},
		Crypto: crypto.Config{Key: "0123456789abcdef0123456789abcdef", Cipher: "AES-256-GCM"},
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })

	// The default cache store is built on first use.
	_, _ = a.Cache.Get("probe")

	// A payload sealed under another key fails authentication.
	other, err := crypto.NewEncryptor(crypto.Config{Key: "fedcba9876543210fedcba9876543210", Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	foreign, err := other.Encrypt("secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := a.Crypto.Decrypt(foreign); err == nil {
		t.Fatal("Decrypt under the wrong key succeeded")
	}

	cors := router.InsecureAllowAllCORS()
	cors.AllowCredentials = true
	a.Router.Use(router.CORS(cors))
	a.Router.Get("/check", func(c *router.Context) error {
		return c.Validate(validation.Rules{"email": {validation.Unique("no_such_table", "email")}})
	})
	req := httptest.NewRequest(http.MethodGet, "/check?email=someone@example.com", nil)
	req.Header.Set("Origin", "https://elsewhere.example")
	a.Router.ServeHTTP(httptest.NewRecorder(), req)

	a.Router.Get("/late", func(c *router.Context) error { return nil })
}

// With LOG_DRIVER=file, each framework line lands in the log file in the
// file driver's format.
func TestLogFileDriver_FrameworkLinesLandInTheLogFile(t *testing.T) {
	dir := t.TempDir()
	driveSideSinkLines(t, log.LogConfig{Driver: "file", Config: map[string]any{"path": dir, "level": "debug"}})

	files, err := filepath.Glob(filepath.Join(dir, "velocity-*.log"))
	if err != nil || len(files) != 1 {
		t.Fatalf("log files = %v (%v), want one", files, err)
	}
	content, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	for name, line := range sideSinkLines {
		if !strings.Contains(string(content), "] "+line) {
			t.Errorf("%s: log file has no %q line:\n%s", name, line, content)
		}
	}
}

// captureStdStreams points os.Stdout and os.Stderr at pipes until the
// returned func is called, which restores them and returns what was
// written.
func captureStdStreams(t *testing.T) func() string {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	restored := false
	restore := func() string {
		if restored {
			return ""
		}
		restored = true
		os.Stdout, os.Stderr = origOut, origErr
		_ = w.Close()
		return <-done
	}
	t.Cleanup(func() { restore() })
	return restore
}

// With LOG_DRIVER=null, none of the lines reaches standard output or
// standard error, nor the stdlib log, slog.Default or the fallback logger.
func TestLogNullDriver_FrameworkLinesReachNoStandardStream(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)
	streams := captureStdStreams(t)

	driveSideSinkLines(t, log.LogConfig{Driver: "null", Config: map[string]any{}})

	written := streams() + stdlib.String() + fallback.String()
	// Fragments each line has carried in every wording it has had.
	for name, fragment := range map[string]string{
		"CORS wildcard with credentials": "credential",
		"crypto decrypt failure":         "decrypt failed",
		"late route registration":        "after server start",
		"unique rule DB failure":         "unique rule query failed",
		"redis cleartext":                "non-loopback host without TLS",
	} {
		if strings.Contains(written, fragment) {
			t.Errorf("%s: %q reached a standard stream:\n%s", name, fragment, written)
		}
	}
}
