package schemes

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/router"
)

// kvLog records every warning with its key/value pairs.
type kvLog struct {
	mu      sync.Mutex
	entries []kvEntry
}

type kvEntry struct {
	msg string
	kvs map[string]any
}

func (l *kvLog) Info(string, ...any)  {}
func (l *kvLog) Error(string, ...any) {}
func (l *kvLog) Warn(msg string, kvs ...any) {
	fields := map[string]any{}
	for i := 0; i+1 < len(kvs); i += 2 {
		if k, ok := kvs[i].(string); ok {
			fields[k] = kvs[i+1]
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, kvEntry{msg: msg, kvs: fields})
}

func (*kvLog) Debug(string, ...any) {}
func (*kvLog) Fatal(string, ...any) {}

// find returns the first warning whose message contains s.
func (l *kvLog) find(s string) (kvEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if strings.Contains(e.msg, s) {
			return e, true
		}
	}
	return kvEntry{}, false
}

// schemeLogger reads the logger the scheme holds.
func schemeLogger(g *SessionScheme) contract.Logger {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.logger
}

// The manager's logger reaches the session scheme whichever of SetLogger
// and RegisterScheme runs first, and SetLogger(nil) clears it.
func TestManagerLogger_ReachesTheSessionScheme(t *testing.T) {
	tests := []struct {
		name        string
		loggerFirst bool
		clearAtEnd  bool
		wantScheme  bool
	}{
		{name: "logger before register", loggerFirst: true, wantScheme: true},
		{name: "logger after register", wantScheme: true},
		{name: "logger cleared", loggerFirst: true, clearAtEnd: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme, _ := storeScheme(t, false)
			m := auth.NewManager()
			logs := &kvLog{}
			if tt.loggerFirst {
				m.SetLogger(logs)
			}
			m.RegisterScheme("web", scheme)
			if !tt.loggerFirst {
				m.SetLogger(logs)
			}
			if tt.clearAtEnd {
				m.SetLogger(nil)
			}
			got := schemeLogger(scheme)
			if tt.wantScheme && got != logs {
				t.Fatalf("scheme logger = %v, want the manager's", got)
			}
			if !tt.wantScheme && got != nil {
				t.Fatalf("scheme logger = %v, want nil after SetLogger(nil)", got)
			}
		})
	}
}

// A scheme registered on a manager with no logger keeps the logger it was
// given directly.
func TestManagerLogger_UnsetManagerLeavesASchemeLoggerAlone(t *testing.T) {
	scheme, _ := storeScheme(t, false)
	own := &kvLog{}
	scheme.SetLogger(own)
	auth.NewManager().RegisterScheme("web", scheme)
	if got := schemeLogger(scheme); got != own {
		t.Fatalf("scheme logger = %v, want the one set on the scheme", got)
	}
}

// Through the session seam, the warnings a session scheme raises reach the
// logger installed on the manager: the oversize cookie-mode save and a
// body write from a write queued behind the save.
func TestManagerLogger_SessionSeamWarningsReachTheManagerLogger(t *testing.T) {
	scheme, _ := storeScheme(t, false)
	m := auth.NewManager()
	logs := &kvLog{}
	m.SetLogger(logs)
	m.RegisterScheme("web", scheme)

	b := newStoreBrowser(t, scheme)
	r := b.handler.(*router.VelocityRouterV2)
	r.Get("/queued-body", func(c *router.Context) error {
		QueueAfterSessionSave(c.Request, func(w http.ResponseWriter) {
			_, _ = w.Write([]byte("late"))
		})
		scheme.Session(c.Request).Put("k", "v")
		return c.String(http.StatusOK, "ok")
	})
	r.Get("/queued-status", func(c *router.Context) error {
		QueueAfterSessionSave(c.Request, func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusTeapot)
		})
		scheme.Session(c.Request).Put("k", "v")
		return c.String(http.StatusOK, "ok")
	})

	b.do(http.MethodPost, "/login")
	b.do(http.MethodPost, "/draft?v="+strings.Repeat("x", 4000))
	e, ok := logs.find("the session cookie would exceed 4096 bytes")
	if !ok {
		t.Fatalf("oversize save not logged through the manager logger; got %v", logs.entries)
	}
	if e.msg != "velocity/auth: session not saved: the session cookie would exceed 4096 bytes, so none was sent; keep less in the session or set SESSION_STORE=server" {
		t.Errorf("oversize message = %q", e.msg)
	}
	if id, _ := e.kvs["session_id"].(string); id == "" {
		t.Errorf("oversize warning session_id = %v, want the session id", e.kvs["session_id"])
	}
	if e.kvs["error"] == nil {
		t.Error("oversize warning carries no error")
	}

	if w := b.do(http.MethodGet, "/queued-body"); w.Body.String() != "ok" {
		t.Fatalf("body = %q, want the handler's", w.Body.String())
	}
	e, ok = logs.find("wrote a response body; ignored")
	if !ok {
		t.Fatalf("queued body write not logged; got %v", logs.entries)
	}
	if e.kvs["bytes"] != 4 {
		t.Errorf("bytes = %v, want 4", e.kvs["bytes"])
	}

	if w := b.do(http.MethodGet, "/queued-status"); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want the handler's 200", w.Code)
	}
	e, ok = logs.find("set a response status; ignored")
	if !ok {
		t.Fatalf("queued status write not logged; got %v", logs.entries)
	}
	if e.kvs["status"] != http.StatusTeapot {
		t.Errorf("status = %v, want %d", e.kvs["status"], http.StatusTeapot)
	}
}
