package goroutine

import (
	"sync"
	"testing"
)

func TestID_DiffersAcrossGoroutinesAndIsStable(t *testing.T) {
	here := ID()
	if here == 0 || here >= 1<<63 {
		t.Fatalf("ID() = %d, want a parsed goroutine id", here)
	}
	if again := ID(); again != here {
		t.Fatalf("ID() changed on one goroutine: %d then %d", here, again)
	}
	other := make(chan uint64)
	go func() { other <- ID() }()
	if id := <-other; id == here {
		t.Fatalf("two goroutines share id %d", id)
	}
}

func TestSet_ZeroValueIsEmpty(t *testing.T) {
	var s Set
	if s.Contains(ID()) {
		t.Fatal("zero Set contains the caller")
	}
	s.Leave(ID()) // an id never entered is ignored
	if s.Contains(ID()) {
		t.Fatal("Leave of an absent id added it")
	}
}

func TestSet_EnterLeaveNested(t *testing.T) {
	var s Set
	id := ID()
	if first := s.Enter(id); !first || !s.Contains(ID()) {
		t.Fatalf("first Enter: first=%v contains=%v, want true true", first, s.Contains(ID()))
	}
	if first := s.Enter(id); first {
		t.Fatal("nested Enter reported a first entry")
	}
	s.Leave(id)
	if !s.Contains(ID()) {
		t.Fatal("the caller left at the inner Leave; it must stay until the outer one")
	}
	s.Leave(id)
	if s.Contains(ID()) {
		t.Fatal("the caller is still in the set after its last Leave")
	}
}

func TestSet_MembershipIsPerGoroutine(t *testing.T) {
	var s Set
	id := ID()
	s.Enter(id)
	defer s.Leave(id)
	seen := make(chan bool)
	go func() { seen <- s.Contains(ID()) }()
	if <-seen {
		t.Fatal("another goroutine is reported as in the set")
	}
}

// Many goroutines entering, checking and leaving at once: run under -race.
func TestSet_Concurrent(t *testing.T) {
	var s Set
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			for range 200 {
				id := ID()
				if first := s.Enter(id); !first || !s.Contains(ID()) {
					t.Error("a goroutine did not see its own first Enter")
				}
				s.Leave(id)
				if s.Contains(ID()) {
					t.Error("a goroutine is still in the set after leaving")
				}
			}
		})
	}
	wg.Wait()
	if len(s.ids) != 0 {
		t.Fatalf("%d ids left in the set", len(s.ids))
	}
}

func BenchmarkSetEnterLeave(b *testing.B) {
	var s Set
	b.ReportAllocs()
	for b.Loop() {
		id := ID()
		s.Enter(id)
		s.Leave(id)
	}
}

func BenchmarkID(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = ID()
	}
}
