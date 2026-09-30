package schemes

import (
	"net/http"
	"net/http/httptest"
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
// checks), counting the reads that were refused.
func BenchmarkSessionScheme_UserParallel(b *testing.B) {
	scheme, req := benchSignedInRequest(b, false)
	b.ReportAllocs()
	b.ResetTimer()
	var refused int64
	b.RunParallel(func(pb *testing.PB) {
		var n int64
		for pb.Next() {
			if scheme.User(req) == nil {
				n++
			}
		}
		if n > 0 {
			b.ReportMetric(float64(n), "refused")
		}
		_ = refused
	})
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
