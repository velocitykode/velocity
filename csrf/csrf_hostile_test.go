package csrf

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// hostileLoggerStore is a token store taking a logger whose SetLogger runs
// code first.
type hostileLoggerStore struct {
	*nonAtomicStore
	code *hostile.Code

	mu     sync.Mutex
	logger contract.Logger
}

func (s *hostileLoggerStore) SetLogger(l contract.Logger) {
	s.code.Run()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = l
}

func (s *hostileLoggerStore) held() contract.Logger {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logger
}

// The instance hands its logger to the store with no lock held: a store
// whose SetLogger panics, blocks or calls back into the instance leaves
// the instance serving, and once it behaves a retry leaves the store
// writing through the instance's logger.
func TestCSRF_StoreSetLoggerIsUserCode(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			logs := hostile.NewLogger(nil)
			var c *CSRF
			code := hostile.New(t, mode, func() {
				c.SetLogger(logs)
				c.log(context.Background()).Warn("from inside the store's SetLogger")
			})
			store := &hostileLoggerStore{nonAtomicStore: newNonAtomicStore(), code: code}
			cfg := DefaultConfig()
			cfg.SessionIDResolver = testCookieResolver("session_id")
			cfg.Store = store
			c = New(cfg)

			call := func() { c.SetLogger(logs) }
			if mode == hostile.Block {
				go func() { //safe-goroutine: the test releases the block below
					c.SetLogger(logs)
				}()
				<-code.Entered()
				// A request reading the logger must not wait on the store.
				call = func() { c.log(context.Background()).Warn("while the store's SetLogger blocks") }
			}
			if p := hostile.Within(t, hostile.Deadline, call); p != nil && mode != hostile.Panic {
				t.Fatalf("panicked: %v", p)
			}
			code.Release()
			code.Disarm()

			hostile.Within(t, hostile.Deadline, func() { c.SetLogger(logs) })
			l := store.held()
			if l == nil {
				t.Fatal("the store holds no logger after the retry")
			}
			l.Warn("probe")
			if logs.Count(hostile.Warn, "probe") != 1 {
				t.Fatal("the store's logger does not reach the instance's")
			}
		})
	}
}

// deleteFailingStore is a token store whose Delete fails, so accepting a
// single-use token logs the failed delete.
type deleteFailingStore struct {
	*nonAtomicStore
}

func (deleteFailingStore) Delete(context.Context, string) error {
	return errors.New("token store down")
}

// The failed-delete line of a single-use token is written after the
// process-wide single-use lock is released: a logger that blocks on it
// holds up its own request only, never another session's validation.
func TestCSRF_SingleUseDeleteFailureLogDoesNotHoldTheLock(t *testing.T) {
	store := deleteFailingStore{newNonAtomicStore()}
	cfg := DefaultConfig()
	cfg.SessionIDResolver = testCookieResolver("session_id")
	cfg.SingleUse = true
	cfg.Store = store
	c := New(cfg)
	code := hostile.New(t, hostile.Block, nil)
	c.SetLogger(hostile.NewLogger(code, hostile.Error))

	validate := func(sessionID string) int {
		token, err := GenerateToken()
		if err != nil {
			t.Errorf("GenerateToken: %v", err)
			return 0
		}
		if err := store.Set(context.Background(), sessionID, token); err != nil {
			t.Errorf("seed: %v", err)
			return 0
		}
		req := httptest.NewRequest(http.MethodPost, "/submit", nil)
		req.Header.Set("X-CSRF-Token", token)
		req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})
		w := httptest.NewRecorder()
		c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(w, req)
		return w.Code
	}

	go func() { //safe-goroutine: the test releases the block below
		validate("first")
	}()
	<-code.Entered()
	var status int
	hostile.Within(t, hostile.Deadline, func() {
		// Disarmed, so this request's own line does not block: only the
		// first request's blocked line could hold it up.
		code.Disarm()
		status = validate("second")
	})
	code.Release()
	if status != http.StatusOK {
		t.Fatalf("second session's single-use validation answered %d, want 200", status)
	}
}
