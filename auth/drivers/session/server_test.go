package session

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/cache/drivers"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/internal/sessionclock"
)

// serverStoreFixture is a ServerStore over a cache-backed record store, the
// shape New builds for SESSION_STORE=server.
func serverStoreFixture(t *testing.T) (*ServerStore, *CacheStore) {
	t.Helper()
	backend := drivers.NewMemoryStore("sessions")
	t.Cleanup(func() { _ = backend.Shutdown(context.Background()) })
	records, err := NewCacheStore(backend)
	if err != nil {
		t.Fatalf("NewCacheStore: %v", err)
	}
	store, err := NewServerStore(testConfig(), records)
	if err != nil {
		t.Fatalf("NewServerStore: %v", err)
	}
	return store, records
}

// sessionCookieOf returns the Set-Cookie line for name in w, or "".
func sessionCookieOf(w *httptest.ResponseRecorder, name string) string {
	for _, h := range w.Result().Header.Values("Set-Cookie") {
		if strings.HasPrefix(h, name+"=") {
			return h
		}
	}
	return ""
}

// requestWith returns a request carrying the cookies w set.
func requestWith(w *httptest.ResponseRecorder) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}
	return r
}

func TestServerStore_LargeSessionRoundTripsWithShortCookie(t *testing.T) {
	store, records := serverStoreFixture(t)
	big := strings.Repeat("x", 10*1024)

	sess, _ := store.Create("")
	sess.Put("draft", big)
	sess.Flash("status", "saved")
	w := httptest.NewRecorder()
	if err := sess.Save(w); err != nil {
		t.Fatalf("Save: %v", err)
	}
	line := sessionCookieOf(w, testConfig().Name)
	if line == "" || len(line) >= 200 {
		t.Fatalf("Set-Cookie = %d bytes %q, want a cookie under 200 bytes", len(line), line)
	}

	got, err := store.Get(requestWith(w), sess.ID())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID() != sess.ID() || got.Get("draft") != big || got.GetFlash("status") != "saved" {
		t.Fatalf("session did not round-trip: id %q, draft %d bytes, flash %v", got.ID(), len(got.Get("draft").(string)), got.GetFlash("status"))
	}

	// One record per session: the payload lives in the record the
	// revocation index reads.
	rec, err := records.Get(context.Background(), sess.ID())
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if rec.UserID != "" || rec.Data == nil {
		t.Fatalf("record = %+v, want a signed-out record holding the payload", rec)
	}
}

func TestServerStore_SaveSlidesTheRecordPastTheCookie(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	defer sessionclock.Set(func() time.Time { return now })()
	store, records := serverStoreFixture(t)

	sess, _ := store.Create("")
	sess.Put("k", "v")
	w := httptest.NewRecorder()
	if err := sess.Save(w); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cookie := w.Result().Cookies()[0]
	rec, err := records.Get(context.Background(), sess.ID())
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	policyEnd := now.Add(120 * time.Minute)
	if cookie.MaxAge != 7200 || !cookie.Expires.Equal(policyEnd) {
		t.Fatalf("cookie MaxAge %d Expires %v, want 7200 and %v", cookie.MaxAge, cookie.Expires, policyEnd)
	}
	if !rec.ExpiresAt.Equal(policyEnd.Add(time.Minute)) {
		t.Fatalf("record ExpiresAt %v, want the policy end plus one minute %v", rec.ExpiresAt, policyEnd.Add(time.Minute))
	}

	// Idle past the policy: the record is the authority and ends it,
	// whatever cookie arrives.
	now = now.Add(121 * time.Minute)
	got, err := store.Get(requestWith(w), sess.ID())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID() == sess.ID() || got.Get("k") != nil {
		t.Fatal("an idle-expired record still loaded its session")
	}
	if _, err := records.Get(context.Background(), sess.ID()); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("expired record not removed: %v", err)
	}
}

func TestServerStore_IdleSignedInRecordReportsExpiry(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	defer sessionclock.Set(func() time.Time { return now })()
	store, records := serverStoreFixture(t)
	ctx := context.Background()

	id := auth.NewSession("").ID()
	if err := records.Put(ctx, &auth.StoredSession{ID: id, UserID: "u1", CreatedAt: now, ExpiresAt: now.Add(3 * time.Hour)}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	now = now.Add(121 * time.Minute)
	got, _ := store.Get(httptest.NewRequest(http.MethodGet, "/", nil), id)
	if !got.(*ServerSession).AuthenticationExpired() {
		t.Fatal("a signed-in record past its idle window did not report AuthenticationExpired")
	}
}

func TestServerStore_MissingRecordReportsRecordDeleted(t *testing.T) {
	store, _ := serverStoreFixture(t)
	id := auth.NewSession("").ID()
	got, err := store.Get(httptest.NewRequest(http.MethodGet, "/", nil), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	ss := got.(*ServerSession)
	if !ss.RecordDeleted() || ss.ID() == id {
		t.Fatalf("RecordDeleted %v, id reused %v; want a fresh session reporting the deleted record", ss.RecordDeleted(), ss.ID() == id)
	}
}

func TestServerStore_ForgedIDIsNeverLookedUp(t *testing.T) {
	store, _ := serverStoreFixture(t)
	for _, id := range []string{"", "short", "session:user:u1", strings.Repeat("a", 44), strings.Repeat("A", 4096)} {
		got, err := store.Get(httptest.NewRequest(http.MethodGet, "/", nil), id)
		if err != nil {
			t.Fatalf("%q: %v", id, err)
		}
		if ss := got.(*ServerSession); ss.RecordDeleted() || ss.ID() == id {
			t.Fatalf("%q reached the record store", id)
		}
	}
}

func TestServerStore_RotatedIDRemovesTheOldRecord(t *testing.T) {
	store, records := serverStoreFixture(t)
	ctx := context.Background()

	sess, _ := store.Create("")
	sess.Put("cart", "three items")
	w := httptest.NewRecorder()
	if err := sess.Save(w); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, _ := store.Get(requestWith(w), sess.ID())
	oldID := loaded.ID()
	if err := loaded.Regenerate(); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	w2 := httptest.NewRecorder()
	if err := loaded.Save(w2); err != nil {
		t.Fatalf("Save after Regenerate: %v", err)
	}
	if _, err := records.Get(ctx, oldID); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("record of the rotated-away id survived: %v", err)
	}
	again, _ := store.Get(requestWith(w2), loaded.ID())
	if again.Get("cart") != "three items" {
		t.Fatal("data did not move to the new id")
	}
}

func TestServerStore_SaveNeverRecreatesARevokedRecord(t *testing.T) {
	store, records := serverStoreFixture(t)
	ctx := context.Background()

	sess, _ := store.Create("")
	sess.Put("k", "v")
	w := httptest.NewRecorder()
	if err := sess.Save(w); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, _ := store.Get(requestWith(w), sess.ID())
	if err := records.Delete(ctx, loaded.ID()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	loaded.Put("k", "changed")
	w2 := httptest.NewRecorder()
	if err := loaded.Save(w2); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("Save of a revoked session = %v, want ErrSessionNotFound", err)
	}
	if line := sessionCookieOf(w2, testConfig().Name); line != "" {
		t.Fatalf("cookie written for a revoked session: %q", line)
	}
	if _, err := records.Get(ctx, loaded.ID()); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("Save recreated the revoked record: %v", err)
	}
}

func TestServerStore_SignedInSessionWithoutRecordIsNotCreated(t *testing.T) {
	store, records := serverStoreFixture(t)
	sess, _ := store.Create("")
	sess.Put(auth.UserIDSessionKey, "u1")
	w := httptest.NewRecorder()
	if err := sess.Save(w); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("Save = %v, want ErrSessionNotFound: only a sign-in writes a signed-in record", err)
	}
	if _, err := records.Get(context.Background(), sess.ID()); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("Save created a signed-in record: %v", err)
	}
}

func TestServerStore_DestroyedSessionRemovesRecordAndCookie(t *testing.T) {
	store, records := serverStoreFixture(t)
	sess, _ := store.Create("")
	sess.Put("k", "v")
	w := httptest.NewRecorder()
	if err := sess.Save(w); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, _ := store.Get(requestWith(w), sess.ID())
	id := loaded.ID()
	if err := loaded.Invalidate(); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	w2 := httptest.NewRecorder()
	if err := loaded.Save(w2); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if c := w2.Result().Cookies(); len(c) != 1 || c[0].MaxAge >= 0 {
		t.Fatalf("cookies %v, want one delete cookie", c)
	}
	if _, err := records.Get(context.Background(), id); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("destroyed session's record survived: %v", err)
	}
}

func TestNewServerStore_RequiresRecords(t *testing.T) {
	if _, err := NewServerStore(testConfig(), nil); !errors.Is(err, auth.ErrNoServerSessionStore) {
		t.Fatalf("NewServerStore(nil) = %v, want ErrNoServerSessionStore", err)
	}
}

func TestCookieStore_OversizeSessionIsRefused(t *testing.T) {
	enc, err := crypto.NewEncryptor(crypto.Config{Key: "test-key-32-bytes-long-for-test!", Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	store, err := NewCookieStore(testConfig(), enc)
	if err != nil {
		t.Fatalf("NewCookieStore: %v", err)
	}
	tests := []struct {
		name    string
		payload int
		wantErr bool
	}{
		{"small session is written", 1000, false},
		{"session past 4096 bytes on the wire is refused", 4000, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess, _ := store.Create("")
			sess.Put("draft", strings.Repeat("x", tt.payload))
			w := httptest.NewRecorder()
			err := sess.Save(w)
			line := sessionCookieOf(w, testConfig().Name)
			if tt.wantErr {
				if !errors.Is(err, ErrCookieTooLarge) {
					t.Fatalf("Save = %v, want ErrCookieTooLarge", err)
				}
				if line != "" {
					t.Fatalf("an oversize cookie was sent (%d bytes)", len(line))
				}
				return
			}
			if err != nil || line == "" || len(line) > maxCookieBytes {
				t.Fatalf("Save = %v, cookie %d bytes; want a written cookie within 4096 bytes", err, len(line))
			}
		})
	}
}
