package view

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// warnLog records warn-level messages.
type warnLog struct {
	mu    sync.Mutex
	warns []string
}

func (l *warnLog) Debug(string, ...any) {}
func (l *warnLog) Info(string, ...any)  {}
func (l *warnLog) Warn(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg)
}
func (l *warnLog) Error(string, ...any) {}
func (l *warnLog) Fatal(string, ...any) {}

func (l *warnLog) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

// The engine takes a logger (contract.LoggerAware) and hands it to the
// Bond it renders through, so the Bond's warnings reach it.
func TestEngineSetLogger_ReachesItsBond(t *testing.T) {
	e, err := NewEngine(Config{})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	logs := &warnLog{}
	var aware contract.LoggerAware = e
	aware.SetLogger(logs)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "same.example"
	e.Redirect(httptest.NewRecorder(), r, "/dashboard")

	logs.mu.Lock()
	defer logs.mu.Unlock()
	if len(logs.warns) != 1 || !strings.HasPrefix(logs.warns[0], "velocity/bond: no RedirectAllowlist configured") {
		t.Errorf("engine logger warns = %q, want the bond's redirect-allowlist warning", logs.warns)
	}
}
