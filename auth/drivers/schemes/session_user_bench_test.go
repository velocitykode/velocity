package schemes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// BenchmarkSessionScheme_UserCached measures reading the signed-in user of
// a request whose session is already loaded: the read every authorization
// check repeats. With a server session store the record lookup is cached on
// the request after the first read.
func BenchmarkSessionScheme_UserCached(b *testing.B) {
	for _, withServerStore := range []bool{false, true} {
		name := "cookie"
		if withServerStore {
			name = "server store"
		}
		b.Run(name, func(b *testing.B) {
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
			if scheme.User(req) == nil {
				b.Fatal("premise: the request is not signed in")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if scheme.User(req) == nil {
					b.Fatal("signed-in read returned no user")
				}
			}
		})
	}
}
