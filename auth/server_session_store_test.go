package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeServerSessionStore is a tiny in-memory implementation of
// ServerSessionStore that records calls so we can assert Manager
// dispatched the right operation. It does NOT exercise the real memory
// driver; that is covered in auth/drivers/session.
type fakeServerSessionStore struct {
	mu    sync.Mutex
	byID  map[string]*StoredSession
	calls []string
}

func newFakeServerSessionStore() *fakeServerSessionStore {
	return &fakeServerSessionStore{byID: make(map[string]*StoredSession)}
}

func (f *fakeServerSessionStore) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeServerSessionStore) Get(_ context.Context, id string) (*StoredSession, error) {
	f.record("get:" + id)
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return s, nil
}

func (f *fakeServerSessionStore) Put(_ context.Context, s *StoredSession) error {
	f.record("put:" + s.ID)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[s.ID] = s
	return nil
}

func (f *fakeServerSessionStore) Delete(_ context.Context, id string) error {
	f.record("delete:" + id)
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.byID, id)
	return nil
}

func (f *fakeServerSessionStore) DeleteAllForUser(_ context.Context, userID string) error {
	f.record("deleteall:" + userID)
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, s := range f.byID {
		if s.UserID == userID {
			delete(f.byID, id)
		}
	}
	return nil
}

func (f *fakeServerSessionStore) ListForUser(_ context.Context, userID string) ([]*SessionMeta, error) {
	f.record("list:" + userID)
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*SessionMeta
	for _, s := range f.byID {
		if s.UserID == userID {
			out = append(out, s.ToMeta())
		}
	}
	return out, nil
}

func TestManager_SetServerSessionStore(t *testing.T) {
	m := NewManager()
	if m.ServerSessionStore() != nil {
		t.Fatal("default ServerSessionStore must be nil")
	}
	store := newFakeServerSessionStore()
	m.SetServerSessionStore(store)
	if got := m.ServerSessionStore(); got != store {
		t.Fatalf("expected installed store, got %v", got)
	}
	m.SetServerSessionStore(nil)
	if m.ServerSessionStore() != nil {
		t.Fatal("nil should remove store")
	}
}

func TestManager_RevokeSession(t *testing.T) {
	m := NewManager()
	store := newFakeServerSessionStore()
	m.SetServerSessionStore(store)
	now := time.Now()
	_ = store.Put(context.Background(), &StoredSession{
		ID: "s1", UserID: "u1", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	})

	if err := m.RevokeSession(context.Background(), "s1"); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, err := store.Get(context.Background(), "s1"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("session not deleted: %v", err)
	}
}

func TestManager_RevokeAllSessions(t *testing.T) {
	m := NewManager()
	store := newFakeServerSessionStore()
	m.SetServerSessionStore(store)
	now := time.Now()
	_ = store.Put(context.Background(), &StoredSession{ID: "a", UserID: "u1", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)})
	_ = store.Put(context.Background(), &StoredSession{ID: "b", UserID: "u1", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)})
	_ = store.Put(context.Background(), &StoredSession{ID: "c", UserID: "u2", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)})

	if err := m.RevokeAllSessions(context.Background(), "u1"); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}
	if list, _ := store.ListForUser(context.Background(), "u1"); len(list) != 0 {
		t.Fatalf("u1 sessions remain: %d", len(list))
	}
	if list, _ := store.ListForUser(context.Background(), "u2"); len(list) != 1 {
		t.Fatalf("u2 session removed by mistake")
	}
}

func TestManager_ListActiveSessions(t *testing.T) {
	m := NewManager()
	store := newFakeServerSessionStore()
	m.SetServerSessionStore(store)
	now := time.Now()
	_ = store.Put(context.Background(), &StoredSession{ID: "a", UserID: "u1", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour), IPAddress: "1.1.1.1", UserAgent: "ua"})

	list, err := m.ListActiveSessions(context.Background(), "u1")
	if err != nil {
		t.Fatalf("ListActiveSessions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	if list[0].ID != "a" || list[0].IPAddress != "1.1.1.1" {
		t.Fatalf("metadata not propagated: %+v", list[0])
	}
}

func TestManager_RevokeSession_NoStore(t *testing.T) {
	m := NewManager()
	err := m.RevokeSession(context.Background(), "anything")
	if !errors.Is(err, ErrNoServerSessionStore) {
		t.Fatalf("expected ErrNoServerSessionStore, got %v", err)
	}
}

func TestManager_RevokeAllSessions_NoStore(t *testing.T) {
	m := NewManager()
	err := m.RevokeAllSessions(context.Background(), "u1")
	if !errors.Is(err, ErrNoServerSessionStore) {
		t.Fatalf("expected ErrNoServerSessionStore, got %v", err)
	}
}

func TestManager_ListActiveSessions_NoStore(t *testing.T) {
	m := NewManager()
	_, err := m.ListActiveSessions(context.Background(), "u1")
	if !errors.Is(err, ErrNoServerSessionStore) {
		t.Fatalf("expected ErrNoServerSessionStore, got %v", err)
	}
}

func TestStoredSession_ToMeta(t *testing.T) {
	now := time.Now()
	in := &StoredSession{
		ID: "s", UserID: "u", Data: map[string]any{"k": "v"},
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
		IPAddress: "1.2.3.4", UserAgent: "ua",
	}
	m := in.ToMeta()
	if m.ID != "s" || m.UserID != "u" {
		t.Fatalf("identity fields missing: %+v", m)
	}
	if !m.CreatedAt.Equal(now) || !m.LastSeenAt.Equal(now) {
		t.Fatalf("timestamps not propagated: %+v", m)
	}
	if m.IPAddress != "1.2.3.4" || m.UserAgent != "ua" {
		t.Fatalf("metadata not propagated: %+v", m)
	}
	// Compile-time guarantee: SessionMeta has no Data field.
	_ = m
}

func TestManager_Concurrent_SetServerSessionStore(t *testing.T) {
	m := NewManager()
	store := newFakeServerSessionStore()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			m.SetServerSessionStore(store)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = m.ServerSessionStore()
		}
	}()
	wg.Wait()
}
