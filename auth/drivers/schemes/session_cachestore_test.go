package schemes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/cache/drivers"
)

// TestSessionScheme_CacheStore_RevokeAllFromOtherInstance runs the cookie
// flow against the cache-backed store: two schemes (two app replicas) share
// one backend, cookies issued on both are valid on both, and a revoke-all
// issued on one rejects every pre-revocation cookie on both while a login
// issued afterwards is accepted.
func TestSessionScheme_CacheStore_RevokeAllFromOtherInstance(t *testing.T) {
	backend := drivers.NewMemoryStore("sessions")
	t.Cleanup(func() { _ = backend.Shutdown(context.Background()) })
	storeA, err := session.NewCacheStore(backend)
	if err != nil {
		t.Fatalf("NewCacheStore: %v", err)
	}
	storeB, err := session.NewCacheStore(backend)
	if err != nil {
		t.Fatalf("NewCacheStore: %v", err)
	}

	schemeA, _ := newRevokeScheme(t, storeA)
	schemeB, _ := newRevokeScheme(t, storeB)

	cookieA := loginAndCookie(t, schemeA, "u1")
	cookieB := loginAndCookie(t, schemeB, "u1")

	for _, tc := range []struct {
		name   string
		scheme *SessionScheme
	}{{"A", schemeA}, {"B", schemeB}} {
		if !tc.scheme.Check(requestWith(cookieA)) || !tc.scheme.Check(requestWith(cookieB)) {
			t.Fatalf("instance %s rejected a live cookie before revocation", tc.name)
		}
	}
	list, err := storeA.ListForUser(context.Background(), "u1")
	if err != nil || len(list) != 2 {
		t.Fatalf("expected 2 listed sessions, got %d (%v)", len(list), err)
	}

	if err := storeB.DeleteAllForUser(context.Background(), "u1"); err != nil {
		t.Fatalf("DeleteAllForUser: %v", err)
	}

	for _, tc := range []struct {
		name   string
		scheme *SessionScheme
	}{{"A", schemeA}, {"B", schemeB}} {
		if tc.scheme.Check(requestWith(cookieA)) || tc.scheme.Check(requestWith(cookieB)) {
			t.Fatalf("instance %s accepted a pre-revocation cookie", tc.name)
		}
	}

	cookieAfter := loginAndCookie(t, schemeA, "u1")
	if !schemeB.Check(requestWith(cookieAfter)) {
		t.Fatal("post-revocation login rejected on the other instance")
	}
	if !schemeA.Check(requestWith(cookieAfter)) {
		t.Fatal("post-revocation login rejected on the issuing instance")
	}
	list, _ = storeB.ListForUser(context.Background(), "u1")
	if len(list) != 1 {
		t.Fatalf("expected exactly the post-revocation session listed, got %d", len(list))
	}
}

// tryLogin is the goroutine-safe variant of loginAndCookie.
func tryLogin(scheme *SessionScheme, userID string) (*http.Cookie, error) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r = WithSessionContext(r)
	if err := scheme.Login(w, r, &revokeTestUser{id: userID}); err != nil {
		return nil, err
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "vel_session" {
			return c, nil
		}
	}
	return nil, http.ErrNoCookie
}

// TestSessionScheme_CacheStore_LoginOverlapsRevokeAll issues logins on
// replica B in three waves around a revoke-all on replica A: before, during,
// after. Cookies from wave 1 are rejected on both replicas, wave 3 cookies
// are accepted on both, and for every cookie the two replicas and the
// listing agree.
func TestSessionScheme_CacheStore_LoginOverlapsRevokeAll(t *testing.T) {
	backend := drivers.NewMemoryStore("sessions")
	t.Cleanup(func() { _ = backend.Shutdown(context.Background()) })
	storeA, err := session.NewCacheStore(backend)
	if err != nil {
		t.Fatalf("NewCacheStore: %v", err)
	}
	storeB, err := session.NewCacheStore(backend)
	if err != nil {
		t.Fatalf("NewCacheStore: %v", err)
	}
	schemeA, _ := newRevokeScheme(t, storeA)
	schemeB, _ := newRevokeScheme(t, storeB)

	const perWave = 15
	login := func() []*http.Cookie {
		cookies := make([]*http.Cookie, perWave)
		var wg sync.WaitGroup
		for i := range cookies {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				c, err := tryLogin(schemeB, "u1")
				if err != nil {
					t.Errorf("Login: %v", err)
					return
				}
				cookies[i] = c
			}(i)
		}
		wg.Wait()
		return cookies
	}

	wave1 := login()
	var wave2 []*http.Cookie
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		wave2 = login()
	}()
	go func() {
		defer wg.Done()
		if err := storeA.DeleteAllForUser(context.Background(), "u1"); err != nil {
			t.Errorf("DeleteAllForUser: %v", err)
		}
	}()
	wg.Wait()
	wave3 := login()

	list, err := storeA.ListForUser(context.Background(), "u1")
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	var liveCount int
	check := func(cookies []*http.Cookie, want *bool) {
		for _, c := range cookies {
			if c == nil {
				continue
			}
			liveA := schemeA.Check(requestWith(c))
			liveB := schemeB.Check(requestWith(c))
			if liveA != liveB {
				t.Errorf("replicas disagree on a cookie: A=%v B=%v", liveA, liveB)
			}
			if liveA {
				liveCount++
			}
			if want != nil && liveA != *want {
				t.Errorf("cookie live=%v, want %v", liveA, *want)
			}
		}
	}
	no, yes := false, true
	check(wave1, &no)
	check(wave2, nil)
	check(wave3, &yes)
	if len(list) != liveCount {
		t.Errorf("listing has %d sessions, Check accepts %d cookies", len(list), liveCount)
	}
}
