package schemes

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// With a one-minute idle lifetime, a client active every 25 seconds stays
// signed in: the cookie is re-issued, and the server record touched, once
// half the idle window has passed, so the window slides before the cookie
// it was issued with expires. The record and the cookie slide on the same
// requests.
func TestSessionLifetime_ShortIdleLifetimeSlides(t *testing.T) {
	for _, mode := range lifetimeModes {
		t.Run(mode.name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, mem := newLifetimeSchemeFor(t, 1, 480, mode)
			b := newLifetimeBrowser(t, scheme)
			if code := b.do(http.MethodPost, "/login").Code; code != http.StatusOK {
				t.Fatalf("login: status %d", code)
			}
			start := clock.Now()

			for elapsed := 25 * time.Second; elapsed <= 300*time.Second; elapsed += 25 * time.Second {
				clock.advance(25 * time.Second)
				w := b.do(http.MethodGet, "/check")
				if w.Code != http.StatusOK {
					t.Fatalf("signed out at +%v with a request every 25s (idle lifetime 1m): %v", elapsed, b.lastErr)
				}
				reissued := sessionCookieOf(w)
				// The interval is 30s: requests at +50s, +100s, ... re-issue.
				wantReissue := elapsed%(50*time.Second) == 0
				if wantReissue != (reissued != nil) {
					t.Fatalf("+%v: session cookie re-issued = %v, want %v", elapsed, reissued != nil, wantReissue)
				}
				if reissued != nil && reissued.MaxAge != 60 {
					t.Fatalf("+%v: re-issued cookie MaxAge = %d, want 60", elapsed, reissued.MaxAge)
				}
				if mem == nil {
					continue
				}
				list, _ := mem.ListForUser(context.Background(), "u1")
				if len(list) != 1 {
					t.Fatalf("server records: %v", list)
				}
				// The record was last touched on the latest re-issue.
				lastReissue := start.Add(elapsed - elapsed%(50*time.Second))
				if !list[0].LastSeenAt.Equal(lastReissue) {
					t.Fatalf("+%v: record LastSeenAt = +%v, want +%v (the last re-issue)", elapsed, list[0].LastSeenAt.Sub(start), lastReissue.Sub(start))
				}
				if list[0].ExpiresAt.Before(lastReissue.Add(time.Minute)) {
					t.Fatalf("+%v: record ExpiresAt +%v ends before the re-issued cookie (+%v)", elapsed, list[0].ExpiresAt.Sub(start), lastReissue.Add(time.Minute).Sub(start))
				}
			}
		})
	}
}

// A session idle for longer than the one-minute idle lifetime still ends.
func TestSessionLifetime_ShortIdleLifetimeStillEnds(t *testing.T) {
	for _, mode := range lifetimeModes {
		t.Run(mode.name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, _ := newLifetimeSchemeFor(t, 1, 480, mode)
			b := newLifetimeBrowser(t, scheme)
			b.do(http.MethodPost, "/login")
			clock.advance(61 * time.Second)
			if b.signedIn() {
				t.Fatal("session outlived a 61s idle gap with a 1m idle lifetime")
			}
		})
	}
}
