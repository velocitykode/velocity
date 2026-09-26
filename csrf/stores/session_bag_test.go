package stores

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testBag is a session bag with an id.
type testBag struct {
	id   string
	mu   sync.Mutex
	data map[string]any
}

func newTestBag(id string) *testBag { return &testBag{id: id, data: map[string]any{}} }

func (b *testBag) ID() string { return b.id }
func (b *testBag) Get(key string) any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data[key]
}
func (b *testBag) Put(key string, value any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data[key] = value
}
func (b *testBag) Remove(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.data, key)
}

type bagKey struct{}

func withBag(b SessionBag) context.Context {
	return context.WithValue(context.Background(), bagKey{}, b)
}

func bagFromContext(ctx context.Context) SessionBag {
	b, _ := ctx.Value(bagKey{}).(SessionBag)
	return b
}

func TestSessionBagStore_KeepsTheTokenInTheSession(t *testing.T) {
	s := NewSessionBagStore(bagFromContext, 0)
	bag := newTestBag("sess-1")
	ctx := withBag(bag)

	if _, err := s.Get(ctx, "sess-1"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("Get on an empty session = %v, want ErrTokenNotFound", err)
	}
	if err := s.Set(ctx, "sess-1", "tok"); err != nil {
		t.Fatal(err)
	}
	if got := bag.Get(TokenSessionKey); got != "tok" {
		t.Fatalf("session holds %v under %q, want the token", got, TokenSessionKey)
	}
	if got, err := s.Get(ctx, "sess-1"); err != nil || got != "tok" {
		t.Fatalf("Get = (%q, %v)", got, err)
	}
	if !s.Exists(ctx, "sess-1") {
		t.Fatal("Exists = false after Set")
	}
	if err := s.Delete(ctx, "sess-1"); err != nil {
		t.Fatal(err)
	}
	if bag.Get(TokenSessionKey) != nil {
		t.Fatal("Delete left the token in the session")
	}
}

func TestSessionBagStore_OnlyTheRequestsSession(t *testing.T) {
	s := NewSessionBagStore(bagFromContext, 0)
	bag := newTestBag("sess-1")
	bag.Put(TokenSessionKey, "tok")

	tests := []struct {
		name string
		ctx  context.Context
		id   string
	}{
		{"no session held", context.Background(), "sess-1"},
		{"another session's id", withBag(bag), "sess-2"},
		{"empty id", withBag(bag), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.Get(tt.ctx, tt.id); !errors.Is(err, ErrNoSessionBag) {
				t.Fatalf("Get = %v, want ErrNoSessionBag", err)
			}
			if err := s.Set(tt.ctx, tt.id, "other"); !errors.Is(err, ErrNoSessionBag) {
				t.Fatalf("Set = %v, want ErrNoSessionBag", err)
			}
			if err := s.Delete(tt.ctx, tt.id); err != nil {
				t.Fatalf("Delete = %v, want nil", err)
			}
			if ok, err := s.ConsumeIfMatch(tt.ctx, tt.id, "tok"); ok || err != nil {
				t.Fatalf("ConsumeIfMatch = (%v, %v), want (false, nil)", ok, err)
			}
			if bag.Get(TokenSessionKey) != "tok" {
				t.Fatal("a call for another session changed this session's token")
			}
		})
	}
}

// A consumed single-use token is refused on this instance even when a
// captured copy of the session (a replayed cookie) still carries it.
func TestSessionBagStore_ConsumedTokenIsNotAcceptedAgain(t *testing.T) {
	s := NewSessionBagStore(bagFromContext, time.Hour)
	bag := newTestBag("sess-1")
	bag.Put(TokenSessionKey, "tok")
	captured := newTestBag("sess-1")
	captured.Put(TokenSessionKey, "tok")

	if ok, _ := s.ConsumeIfMatch(withBag(bag), "sess-1", "wrong"); ok {
		t.Fatal("a wrong token was consumed")
	}
	if ok, _ := s.ConsumeIfMatch(withBag(bag), "sess-1", "tok"); !ok {
		t.Fatal("the first submit was refused")
	}
	if bag.Get(TokenSessionKey) != nil {
		t.Fatal("consuming left the token in the session")
	}
	if ok, _ := s.ConsumeIfMatch(withBag(captured), "sess-1", "tok"); ok {
		t.Fatal("a replay from a captured session was accepted")
	}
}

// Of concurrent requests carrying the same token in their own copies of
// the session, exactly one is accepted.
func TestSessionBagStore_ConcurrentDoubleSubmitAcceptsOne(t *testing.T) {
	s := NewSessionBagStore(bagFromContext, time.Hour)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		bag := newTestBag("sess-1")
		bag.Put(TokenSessionKey, "tok")
		go func() {
			defer wg.Done()
			if ok, _ := s.ConsumeIfMatch(withBag(bag), "sess-1", "tok"); ok {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := accepted.Load(); got != 1 {
		t.Fatalf("%d concurrent submits accepted, want 1", got)
	}
}

// The consumed record ends after its lifetime and is pruned.
func TestSessionBagStore_ConsumedRecordExpires(t *testing.T) {
	s := NewSessionBagStore(bagFromContext, 20*time.Millisecond)
	bag := newTestBag("sess-1")
	bag.Put(TokenSessionKey, "tok")
	if ok, _ := s.ConsumeIfMatch(withBag(bag), "sess-1", "tok"); !ok {
		t.Fatal("first submit refused")
	}
	time.Sleep(40 * time.Millisecond)
	s.mu.Lock()
	s.nextPrune = time.Time{}
	s.mu.Unlock()
	other := newTestBag("sess-2")
	other.Put(TokenSessionKey, "tok-2")
	if ok, _ := s.ConsumeIfMatch(withBag(other), "sess-2", "tok-2"); !ok {
		t.Fatal("submit refused")
	}
	s.mu.Lock()
	n := len(s.consumed)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d consumed records held, want 1 (the expired one pruned)", n)
	}
}
