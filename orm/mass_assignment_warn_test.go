package orm

import (
	"sync"
	"testing"
)

func TestMassAssignmentWarner_FiresOncePerType(t *testing.T) {
	var mu sync.Mutex
	got := map[string]int{}
	SetMassAssignmentWarner(func(modelType string) {
		mu.Lock()
		got[modelType]++
		mu.Unlock()
	})
	defer SetMassAssignmentWarner(nil)

	type warnModelOpenA struct{ Name string }
	type warnModelOpenB struct{ Name string }

	warnOpenMassAssignment(warnModelOpenA{})
	warnOpenMassAssignment(warnModelOpenA{}) // same type: must not warn twice
	warnOpenMassAssignment(warnModelOpenB{})

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("want 2 distinct types warned, got %d (%v)", len(got), got)
	}
	for typ, n := range got {
		if n != 1 {
			t.Errorf("type %s warned %d times, want exactly 1", typ, n)
		}
	}
}

func TestMassAssignmentWarner_NilIsSafe(t *testing.T) {
	SetMassAssignmentWarner(nil)
	type warnModelNilSafe struct{ Name string }
	// Must not panic when no warner is configured.
	warnOpenMassAssignment(warnModelNilSafe{})
}
