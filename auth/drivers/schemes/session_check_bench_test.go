package schemes

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// benchSignedInRequest returns a scheme and a request whose session is
// loaded and signed in, with or without a server session store.
func benchSignedInRequest(b *testing.B, withServerStore bool) (*SessionScheme, *http.Request) {
	b.Helper()
	scheme := &SessionScheme{
		store:  &mockSessionStore{},
		config: auth.SessionConfig{Name: "test_session"},
		hasher: auth.NewBcryptHasher(10),
	}
	scheme.userStore.Store(&userStoreHolder{p: &holderRaceUserStore{}})
	scheme.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})
	if withServerStore {
		scheme.SetServerSessionStore(&holderRaceStore{user: "1"})
	}
	req := WithSessionContext(httptest.NewRequest(http.MethodGet, "/", nil))
	sess := newMockSession()
	sess.data["user_id"] = "1"
	req.Context().Value(sessionCtxKey{}).(*sessionHolder).setSession(sess)
	if !scheme.Check(req) {
		b.Fatal("premise: the request is not signed in")
	}
	return scheme, req
}

// BenchmarkSessionScheme_CheckCached measures Check on a request whose
// session is already loaded, the read every authorization gate repeats.
func BenchmarkSessionScheme_CheckCached(b *testing.B) {
	for _, withServerStore := range []bool{false, true} {
		name := "cookie"
		if withServerStore {
			name = "server store"
		}
		b.Run(name, func(b *testing.B) {
			scheme, req := benchSignedInRequest(b, withServerStore)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if !scheme.Check(req) {
					b.Fatal("signed-in read was not authenticated")
				}
			}
		})
	}
}

// BenchmarkSessionScheme_UserParallel measures reads of the signed-in user
// from many goroutines of one request (a handler fanning out authorization
// checks), and reports the reads that were refused.
func BenchmarkSessionScheme_UserParallel(b *testing.B) {
	scheme, req := benchSignedInRequest(b, false)
	var refused atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if scheme.User(req) == nil {
				refused.Add(1)
			}
		}
	})
	b.ReportMetric(float64(refused.Load()), "refused")
}

// BenchmarkSessionScheme_UserColdRequest measures the first read of the
// signed-in user on a request: each iteration is a new request whose
// session is loaded but whose user is not resolved yet.
func BenchmarkSessionScheme_UserColdRequest(b *testing.B) {
	scheme, _ := benchSignedInRequest(b, false)
	sess := newMockSession()
	sess.data["user_id"] = "1"
	base := httptest.NewRequest(http.MethodGet, "/", nil)
	b.ReportAllocs()
	for b.Loop() {
		req := WithSessionContext(base)
		req.Context().Value(sessionCtxKey{}).(*sessionHolder).setSession(sess)
		if scheme.User(req) == nil {
			b.Fatal("cold read returned no user")
		}
	}
}

// BenchmarkSessionScheme_UserColdFanOut measures four goroutines of a new
// request reading the signed-in user at once, before any of them resolved
// it, and reports the reads that were refused.
func BenchmarkSessionScheme_UserColdFanOut(b *testing.B) {
	scheme, _ := benchSignedInRequest(b, false)
	sess := newMockSession()
	sess.data["user_id"] = "1"
	base := httptest.NewRequest(http.MethodGet, "/", nil)
	const readers = 4
	var refused atomic.Int64
	b.ReportAllocs()
	for b.Loop() {
		req := WithSessionContext(base)
		req.Context().Value(sessionCtxKey{}).(*sessionHolder).setSession(sess)
		var start, done sync.WaitGroup
		start.Add(1)
		done.Add(readers)
		for range readers {
			go func() { //safe-goroutine: the iteration waits for every reader
				defer done.Done()
				start.Wait()
				if scheme.User(req) == nil {
					refused.Add(1)
				}
			}()
		}
		start.Done()
		done.Wait()
	}
	b.ReportMetric(float64(refused.Load()), "refused")
}

// BenchmarkSessionScheme_Busy measures the refused paths: each call meets
// a request whose authentication gate another operation holds.
func BenchmarkSessionScheme_Busy(b *testing.B) {
	scheme, req := benchSignedInRequest(b, false)
	scheme.SetAttemptFloor(-1)
	holder := req.Context().Value(sessionCtxKey{}).(*sessionHolder)
	holder.setResponseWriter(httptest.NewRecorder())
	holder.mu.Lock()
	holder.busy = true
	holder.mu.Unlock()
	w := httptest.NewRecorder()
	creds := map[string]interface{}{"email": "t@example.com", "password": "pw"}
	b.Run("User", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if scheme.User(req) != nil {
				b.Fatal("busy read returned a user")
			}
		}
	})
	b.Run("CheckWithError", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if ok, _ := scheme.CheckWithError(req); ok {
				b.Fatal("busy read was authenticated")
			}
		}
	})
	b.Run("LoginByID", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if scheme.LoginByID(w, req, "1") == nil {
				b.Fatal("busy LoginByID succeeded")
			}
		}
	})
	b.Run("Attempt", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if ok, _ := scheme.Attempt(w, req, creds); ok {
				b.Fatal("busy Attempt succeeded")
			}
		}
	})
}
