package factory

import (
	"sync"
	"testing"
)

func TestNewFaker_Deterministic(t *testing.T) {
	for _, seed := range []int64{0, 1, 42, -7} {
		a := NewFaker(seed)
		b := NewFaker(seed)
		for i := 0; i < 20; i++ {
			if got, want := a.Name(), b.Name(); got != want {
				t.Fatalf("seed %d diverged at call %d: %q != %q", seed, i, got, want)
			}
		}
	}
}

func TestNewFaker_DifferentSeedsDiverge(t *testing.T) {
	a := NewFaker(1)
	b := NewFaker(2)
	same := true
	for i := 0; i < 10; i++ {
		if a.Name() != b.Name() {
			same = false
			break
		}
	}
	if same {
		t.Error("different seeds produced identical streams")
	}
}

func TestSetSeed_GlobalDeterministic(t *testing.T) {
	SetSeed(99)
	first := make([]string, 10)
	for i := range first {
		first[i] = Faker().Email()
	}

	SetSeed(99)
	for i := range first {
		if got := F().Email(); got != first[i] {
			t.Fatalf("global stream diverged at call %d: %q != %q", i, got, first[i])
		}
	}
}

func TestFaker_Concurrent(t *testing.T) {
	SetSeed(5)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if i := j % 3; i == 0 {
					_ = Faker().Name()
				} else if i == 1 {
					_ = F().Email()
				} else {
					SetSeed(int64(j))
				}
			}
		}()
	}
	wg.Wait()
}
