package schemes

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
)

// slowStep runs once when armed: the slow call of user code during which a
// remember credential's lifetime runs out.
type slowStep struct {
	armed bool
	run   func()
}

func (s *slowStep) take() {
	if s != nil && s.armed {
		s.armed = false
		s.run()
	}
}

type slowLookupUsers struct {
	*revokeTestStore
	slow *slowStep
}

func (u *slowLookupUsers) FindByIDCtx(ctx context.Context, id interface{}) (contract.Authenticatable, error) {
	u.slow.take()
	return u.revokeTestStore.FindByIDCtx(ctx, id)
}

type slowRotator struct {
	fakeCSRFRotator
	slow *slowStep
}

func (r *slowRotator) RotateToken(ctx context.Context, oldID, newID string) error {
	r.slow.take()
	return r.fakeCSRFRotator.RotateToken(ctx, oldID, newID)
}

type slowPutRecords struct {
	auth.ServerSessionStore
	slow *slowStep
}

func (s *slowPutRecords) Put(ctx context.Context, rec *auth.StoredSession) error {
	s.slow.take()
	return s.ServerSessionStore.Put(ctx, rec)
}

// A remember cookie that is inside its lifetime when the recall starts and
// past it by the time a slow call of the recall returns signs nobody in:
// the lifetime is decided again after the user lookup, again before the
// session is given the user, and again where the replacement credential is
// minted. The stored token is left as it is and no credential is issued.
func TestRememberRecall_CredentialThatExpiresDuringTheRecallSignsNobodyIn(t *testing.T) {
	for _, step := range []string{"user lookup", "csrf rotation", "server record write"} {
		t.Run("expires during the "+step, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, mem := newLifetimeScheme(t, 120, 0, true)
			slow := &slowStep{run: func() { clock.advance(2 * time.Minute) }}
			lookup, rotate, put := &slowStep{run: slow.run}, &slowStep{run: slow.run}, &slowStep{run: slow.run}
			users := userStoreOf(t, scheme)
			scheme.SetUserStore(&slowLookupUsers{revokeTestStore: users, slow: lookup})
			scheme.SetCSRFTokenRotator(&slowRotator{slow: rotate})
			scheme.SetServerSessionStore(&slowPutRecords{ServerSessionStore: mem, slow: put})

			b := newRememberBrowser(t, scheme)
			if code := b.do(http.MethodPost, "/login").Code; code != http.StatusOK {
				t.Fatalf("login: status %d", code)
			}
			stored := users.token("u1")
			if stored == "" || b.cookies[rememberCookieName] == nil {
				t.Fatal("premise: the sign-in issued no remember credential")
			}

			// One minute of the credential's lifetime is left; the session
			// itself ended long ago.
			clock.advance(scheme.config.RememberTimeout() - time.Minute)
			if b.cookies[sessionCookieName] != nil && b.expires[sessionCookieName].After(clock.Now()) {
				t.Fatal("premise: the session cookie is still live")
			}
			switch step {
			case "user lookup":
				lookup.armed = true
			case "csrf rotation":
				rotate.armed = true
			case "server record write":
				put.armed = true
			}

			if b.signedIn() {
				t.Fatal("the remember cookie signed the visitor in although its lifetime ended during the recall")
			}
			if lookup.armed || rotate.armed || put.armed {
				t.Fatal("premise: the recall did not reach the slow step")
			}
			if c := b.responseCookie(rememberCookieName); c != nil && c.MaxAge > 0 {
				t.Fatalf("the response issues a new remember credential: %v", c)
			}
			if got := users.token("u1"); got != stored {
				t.Fatalf("the stored remember token changed from %q to %q", stored, got)
			}
			if b.signedIn() {
				t.Fatal("the next request is signed in")
			}
		})
	}
}
