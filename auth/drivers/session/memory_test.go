package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
)

func newTestStore(t *testing.T, opts ...MemoryOption) *MemoryStore {
	t.Helper()
	// Long sweep interval by default so background goroutine does not
	// race the test clock unless the test opts in.
	opts = append([]MemoryOption{WithSweepInterval(time.Hour)}, opts...)
	s := NewMemoryStore(opts...)
	t.Cleanup(func() {
		_ = s.Close(context.Background())
	})
	return s
}

func makeSession(id, userID string, ttl time.Duration) *auth.StoredSession {
	now := time.Now()
	return &auth.StoredSession{
		ID:         id,
		UserID:     userID,
		Data:       map[string]any{"k": "v"},
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(ttl),
		IPAddress:  "127.0.0.1",
		UserAgent:  "test/1.0",
	}
}

func TestSessionStore_PutGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	in := makeSession("s1", "u1", time.Hour)

	if err := s.Put(ctx, in); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(ctx, "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "s1" || got.UserID != "u1" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got.Data["k"] != "v" {
		t.Fatalf("data not preserved: %+v", got.Data)
	}
	if got.IPAddress != "127.0.0.1" || got.UserAgent != "test/1.0" {
		t.Fatalf("metadata not preserved: %+v", got)
	}
	if got.LastSeenAt.IsZero() {
		t.Fatal("LastSeenAt should be set by Put")
	}

	// Mutating the returned copy must not bleed back into storage.
	got.Data["k"] = "tampered"
	again, _ := s.Get(ctx, "s1")
	if again.Data["k"] != "v" {
		t.Fatalf("storage mutated through returned pointer: %+v", again.Data)
	}
}

func TestSessionStore_Put_Validation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, nil); err == nil {
		t.Fatal("nil session must error")
	}
	if err := s.Put(ctx, &auth.StoredSession{UserID: "u1"}); err == nil {
		t.Fatal("empty id must error")
	}
	if err := s.Put(ctx, &auth.StoredSession{ID: "s1"}); err == nil {
		t.Fatal("empty user id must error")
	}
}

func TestSessionStore_Get_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get(context.Background(), "missing")
	if !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestSessionStore_Delete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.Put(ctx, makeSession("s1", "u1", time.Hour))

	if err := s.Delete(ctx, "s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "s1"); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound after delete, got %v", err)
	}
	// idempotent
	if err := s.Delete(ctx, "s1"); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
	if err := s.Delete(ctx, "never-existed"); err != nil {
		t.Fatalf("delete non-existent: %v", err)
	}
}

func TestSessionStore_DeleteAllForUser(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.Put(ctx, makeSession("a", "u1", time.Hour))
	_ = s.Put(ctx, makeSession("b", "u1", time.Hour))
	_ = s.Put(ctx, makeSession("c", "u2", time.Hour))

	if err := s.DeleteAllForUser(ctx, "u1"); err != nil {
		t.Fatalf("DeleteAllForUser: %v", err)
	}
	if _, err := s.Get(ctx, "a"); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatal("u1 session a not deleted")
	}
	if _, err := s.Get(ctx, "b"); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatal("u1 session b not deleted")
	}
	if _, err := s.Get(ctx, "c"); err != nil {
		t.Fatalf("u2 session must survive: %v", err)
	}
	// no-op for unknown user
	if err := s.DeleteAllForUser(ctx, "nobody"); err != nil {
		t.Fatalf("delete unknown user: %v", err)
	}
}

func TestSessionStore_ListForUser(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.Put(ctx, makeSession("a", "u1", time.Hour))
	_ = s.Put(ctx, makeSession("b", "u1", time.Hour))
	_ = s.Put(ctx, makeSession("c", "u2", time.Hour))

	list, err := s.ListForUser(ctx, "u1")
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}
	for _, m := range list {
		if m.UserID != "u1" {
			t.Fatalf("wrong user: %+v", m)
		}
		if m.IPAddress == "" || m.UserAgent == "" {
			t.Fatalf("metadata missing: %+v", m)
		}
		// SessionMeta has no Data field; this is the structural guarantee.
		// (Compile-time check would suffice but the assertion documents it.)
		_ = m
	}

	empty, err := s.ListForUser(ctx, "nobody")
	if err != nil {
		t.Fatalf("ListForUser(missing): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty list, got %d", len(empty))
	}
}

func TestSessionStore_ListForUser_OmitsData(t *testing.T) {
	// Compile-time guarantee: SessionMeta has no Data field. This test
	// just affirms the listing path returns SessionMeta (not StoredSession).
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.Put(ctx, makeSession("a", "u1", time.Hour))
	list, _ := s.ListForUser(ctx, "u1")
	var _ []*auth.SessionMeta = list
}

func TestSessionStore_ExpiredSessionRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	expired := makeSession("expired", "u1", time.Hour)
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	_ = s.Put(ctx, expired)

	_, err := s.Get(ctx, "expired")
	if !errors.Is(err, auth.ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired, got %v", err)
	}
	// The expired record should have been removed by Get.
	_, err = s.Get(ctx, "expired")
	if !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound after expiry sweep, got %v", err)
	}
}

func TestSessionStore_ListForUser_ReapsExpired(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	live := makeSession("live", "u1", time.Hour)
	dead := makeSession("dead", "u1", time.Hour)
	dead.ExpiresAt = time.Now().Add(-time.Minute)
	_ = s.Put(ctx, live)
	_ = s.Put(ctx, dead)

	list, err := s.ListForUser(ctx, "u1")
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(list) != 1 || list[0].ID != "live" {
		t.Fatalf("expected only live session, got %+v", list)
	}
	if _, err := s.Get(ctx, "dead"); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("expected dead session reaped, got %v", err)
	}
}

func TestSessionStore_ExpirySweep(t *testing.T) {
	// Use a tight sweep cadence so the goroutine kicks in during the
	// test window. The test still asserts the sweep removed the entry.
	s := NewMemoryStore(WithSweepInterval(10 * time.Millisecond))
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	ctx := context.Background()
	expired := makeSession("e", "u1", time.Hour)
	expired.ExpiresAt = time.Now().Add(-time.Second)
	_ = s.Put(ctx, expired)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.RLock()
		_, present := s.byID["e"]
		s.mu.RUnlock()
		if !present {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background sweep did not remove expired session within deadline")
}

func TestSessionStore_Concurrent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const goroutines = 32
	const ops = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	var errs atomic.Int64
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			user := fmt.Sprintf("u%d", g%4)
			for i := 0; i < ops; i++ {
				id := fmt.Sprintf("g%d-i%d", g, i)
				if err := s.Put(ctx, makeSession(id, user, time.Hour)); err != nil {
					errs.Add(1)
				}
				if _, err := s.Get(ctx, id); err != nil && !errors.Is(err, auth.ErrSessionNotFound) {
					errs.Add(1)
				}
				if i%5 == 0 {
					_, _ = s.ListForUser(ctx, user)
				}
				if i%7 == 0 {
					_ = s.Delete(ctx, id)
				}
			}
		}(g)
	}
	wg.Wait()
	if errs.Load() != 0 {
		t.Fatalf("%d concurrent ops failed", errs.Load())
	}
}

func TestSessionStore_Close_Idempotent(t *testing.T) {
	s := NewMemoryStore(WithSweepInterval(time.Hour))
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("second Close must be idempotent: %v", err)
	}
}

func TestSessionStore_Close_RespectsContext(t *testing.T) {
	s := NewMemoryStore(WithSweepInterval(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Close(ctx); err == nil {
		t.Fatal("expected cancelled context error")
	}
}

func TestSessionStore_Get_RespectsContext(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Get(ctx, "anything"); err == nil {
		t.Fatal("expected cancelled context error")
	}
	if err := s.Put(ctx, makeSession("x", "u1", time.Hour)); err == nil {
		t.Fatal("expected cancelled context error on Put")
	}
	if err := s.Delete(ctx, "x"); err == nil {
		t.Fatal("expected cancelled context error on Delete")
	}
	if err := s.DeleteAllForUser(ctx, "u1"); err == nil {
		t.Fatal("expected cancelled context error on DeleteAllForUser")
	}
	if _, err := s.ListForUser(ctx, "u1"); err == nil {
		t.Fatal("expected cancelled context error on ListForUser")
	}
}

func TestSessionStore_Put_ReassignUser(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.Put(ctx, makeSession("s", "u1", time.Hour))
	_ = s.Put(ctx, makeSession("s", "u2", time.Hour))

	list1, _ := s.ListForUser(ctx, "u1")
	if len(list1) != 0 {
		t.Fatalf("u1 should no longer index s, got %d", len(list1))
	}
	list2, _ := s.ListForUser(ctx, "u2")
	if len(list2) != 1 {
		t.Fatalf("u2 should index s, got %d", len(list2))
	}
}

func TestSessionStore_Put_PreservesCreatedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	original := time.Now().Add(-2 * time.Hour)
	in := makeSession("s", "u1", time.Hour)
	in.CreatedAt = original
	_ = s.Put(ctx, in)
	got, _ := s.Get(ctx, "s")
	if !got.CreatedAt.Equal(original) {
		t.Fatalf("CreatedAt clobbered: want %v got %v", original, got.CreatedAt)
	}
}

func TestSessionStore_NoExpiresAt_NeverExpires(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	in := makeSession("forever", "u1", time.Hour)
	in.ExpiresAt = time.Time{}
	_ = s.Put(ctx, in)
	got, err := s.Get(ctx, "forever")
	if err != nil {
		t.Fatalf("zero ExpiresAt should mean no expiry, got %v", err)
	}
	if got.ID != "forever" {
		t.Fatalf("wrong session: %+v", got)
	}
}
