package velocity

import (
	"errors"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
)

// recordingLogger keeps every Warn message for assertions.
type recordingLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *recordingLogger) Debug(string, ...any) {}
func (l *recordingLogger) Info(string, ...any)  {}
func (l *recordingLogger) Warn(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg)
}
func (l *recordingLogger) Error(string, ...any) {}
func (l *recordingLogger) Fatal(string, ...any) {}

// The environment gate covers the cookie security attributes only: a
// development profile warns about an insecure cookie and proceeds, and
// production refuses it.
func TestCheckSessionCookieSecurity(t *testing.T) {
	insecure := newTestSessionStoreConfig()
	insecure.HttpOnly = false
	for _, tt := range []struct {
		env      string
		wantErr  bool
		wantWarn bool
	}{
		{"testing", false, false},
		{"local", false, true},
		{"development", false, true},
		{"production", true, false},
	} {
		t.Run(tt.env, func(t *testing.T) {
			rec := &recordingLogger{}
			cfg := Config{Env: tt.env, Session: insecure}
			a := &App{Services: &app.Services{Log: rec}, config: &cfg}
			err := a.checkSessionCookieSecurity()
			if tt.wantErr != (err != nil) {
				t.Fatalf("checkSessionCookieSecurity = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, auth.ErrInsecureSessionConfig) {
				t.Fatalf("checkSessionCookieSecurity = %v, want ErrInsecureSessionConfig", err)
			}
			if got := len(rec.warns) > 0; got != tt.wantWarn {
				t.Fatalf("warned = %v (%q), want %v", got, rec.warns, tt.wantWarn)
			}
		})
	}
}
