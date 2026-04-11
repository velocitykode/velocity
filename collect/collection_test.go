package collect

import (
	"reflect"
	"slices"
	"testing"
)

// --- From -------------------------------------------------------------------

func TestFrom(t *testing.T) {
	t.Run("copies input", func(t *testing.T) {
		original := []int{1, 2, 3}
		c := From(original)
		original[0] = 99
		if c.All()[0] != 1 {
			t.Error("From did not copy the input slice")
		}
	})

	t.Run("nil returns empty", func(t *testing.T) {
		c := From[int](nil)
		if c.Count() != 0 {
			t.Errorf("got %d; want 0", c.Count())
		}
		if c.All() == nil {
			t.Error("All() should return non-nil empty slice for nil input")
		}
	})

	t.Run("empty returns empty", func(t *testing.T) {
		c := From([]int{})
		if c.Count() != 0 {
			t.Errorf("got %d; want 0", c.Count())
		}
	})
}

// --- All --------------------------------------------------------------------

func TestAll(t *testing.T) {
	c := From([]int{1, 2, 3})
	result := c.All()
	assertSlice(t, result, []int{1, 2, 3})
}

// --- Count ------------------------------------------------------------------

func TestCollectionCount(t *testing.T) {
	tests := []struct {
		name     string
		items    []int
		expected int
	}{
		{"basic", []int{1, 2, 3}, 3},
		{"empty", []int{}, 0},
		{"single", []int{1}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := From(tt.items)
			if c.Count() != tt.expected {
				t.Errorf("got %d; want %d", c.Count(), tt.expected)
			}
		})
	}
}

// --- IsEmpty / IsNotEmpty ---------------------------------------------------

func TestIsEmpty(t *testing.T) {
	if !From[int](nil).IsEmpty() {
		t.Error("nil collection should be empty")
	}
	if !From([]int{}).IsEmpty() {
		t.Error("empty collection should be empty")
	}
	if From([]int{1}).IsEmpty() {
		t.Error("non-empty collection should not be empty")
	}
}

func TestIsNotEmpty(t *testing.T) {
	if From[int](nil).IsNotEmpty() {
		t.Error("nil collection should not be not-empty")
	}
	if From([]int{}).IsNotEmpty() {
		t.Error("empty collection should not be not-empty")
	}
	if !From([]int{1}).IsNotEmpty() {
		t.Error("non-empty collection should be not-empty")
	}
}

// --- Filter -----------------------------------------------------------------

func TestCollectionFilter(t *testing.T) {
	c := From([]int{1, 2, 3, 4, 5})
	result := c.Filter(func(n int) bool { return n > 3 })
	assertSlice(t, result.All(), []int{4, 5})
}

func TestCollectionFilter_Immutability(t *testing.T) {
	c := From([]int{1, 2, 3, 4, 5})
	_ = c.Filter(func(n int) bool { return n > 3 })
	assertSlice(t, c.All(), []int{1, 2, 3, 4, 5})
}

// --- Reject -----------------------------------------------------------------

func TestCollectionReject(t *testing.T) {
	c := From([]int{1, 2, 3, 4, 5})
	result := c.Reject(func(n int) bool { return n > 3 })
	assertSlice(t, result.All(), []int{1, 2, 3})
}

func TestCollectionReject_Immutability(t *testing.T) {
	c := From([]int{1, 2, 3, 4, 5})
	_ = c.Reject(func(n int) bool { return n > 3 })
	assertSlice(t, c.All(), []int{1, 2, 3, 4, 5})
}

// --- Each -------------------------------------------------------------------

func TestCollectionEach(t *testing.T) {
	var sum int
	c := From([]int{1, 2, 3})
	returned := c.Each(func(n int) { sum += n })
	if sum != 6 {
		t.Errorf("got sum %d; want 6", sum)
	}
	// Each returns same collection for chaining
	if returned != c {
		t.Error("Each should return the same collection for chaining")
	}
}

// --- Contains / Every / None ------------------------------------------------

func TestCollectionContains(t *testing.T) {
	c := From([]int{1, 2, 3})
	if !c.Contains(func(n int) bool { return n == 2 }) {
		t.Error("should contain 2")
	}
	if c.Contains(func(n int) bool { return n == 5 }) {
		t.Error("should not contain 5")
	}
}

func TestCollectionEvery(t *testing.T) {
	c := From([]int{2, 4, 6})
	if !c.Every(func(n int) bool { return n%2 == 0 }) {
		t.Error("all should be even")
	}
	c2 := From([]int{2, 3, 6})
	if c2.Every(func(n int) bool { return n%2 == 0 }) {
		t.Error("not all should be even")
	}
}

func TestCollectionNone(t *testing.T) {
	c := From([]int{1, 3, 5})
	if !c.None(func(n int) bool { return n%2 == 0 }) {
		t.Error("none should be even")
	}
	c2 := From([]int{1, 2, 5})
	if c2.None(func(n int) bool { return n%2 == 0 }) {
		t.Error("some are even")
	}
}

// --- First / Last / FirstWhere ----------------------------------------------

func TestCollectionFirst(t *testing.T) {
	c := From([]int{1, 2, 3, 4})
	item, ok := c.First(func(n int) bool { return n > 2 })
	if !ok || item != 3 {
		t.Errorf("got (%d, %v); want (3, true)", item, ok)
	}

	_, ok = c.First(func(n int) bool { return n > 10 })
	if ok {
		t.Error("expected not found")
	}
}

func TestCollectionLast(t *testing.T) {
	c := From([]int{1, 2, 3, 4})
	item, ok := c.Last(func(n int) bool { return n%2 == 0 })
	if !ok || item != 4 {
		t.Errorf("got (%d, %v); want (4, true)", item, ok)
	}

	_, ok = c.Last(func(n int) bool { return n > 10 })
	if ok {
		t.Error("expected not found")
	}
}

func TestCollectionFirstWhere(t *testing.T) {
	c := From([]int{1, 2, 3})
	item, ok := c.FirstWhere(func(n int) bool { return n == 2 })
	if !ok || item != 2 {
		t.Errorf("got (%d, %v); want (2, true)", item, ok)
	}
}

// --- Reverse ----------------------------------------------------------------

func TestCollectionReverse(t *testing.T) {
	c := From([]int{1, 2, 3})
	result := c.Reverse()
	assertSlice(t, result.All(), []int{3, 2, 1})
}

func TestCollectionReverse_Immutability(t *testing.T) {
	c := From([]int{1, 2, 3})
	_ = c.Reverse()
	assertSlice(t, c.All(), []int{1, 2, 3})
}

// --- Sort -------------------------------------------------------------------

func TestCollectionSort(t *testing.T) {
	c := From([]int{3, 1, 2})
	result := c.Sort(func(a, b int) bool { return a < b })
	assertSlice(t, result.All(), []int{1, 2, 3})
}

func TestCollectionSort_Immutability(t *testing.T) {
	c := From([]int{3, 1, 2})
	_ = c.Sort(func(a, b int) bool { return a < b })
	assertSlice(t, c.All(), []int{3, 1, 2})
}

// --- Chunk ------------------------------------------------------------------

func TestCollectionChunk(t *testing.T) {
	c := From([]int{1, 2, 3, 4, 5})
	result := c.Chunk(2)
	expected := [][]int{{1, 2}, {3, 4}, {5}}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("got %v; want %v", result, expected)
	}
}

// --- Take -------------------------------------------------------------------

func TestCollectionTake(t *testing.T) {
	c := From([]int{1, 2, 3, 4, 5})
	result := c.Take(3)
	assertSlice(t, result.All(), []int{1, 2, 3})
}

func TestCollectionTake_Immutability(t *testing.T) {
	c := From([]int{1, 2, 3})
	_ = c.Take(1)
	assertSlice(t, c.All(), []int{1, 2, 3})
}

// --- Skip -------------------------------------------------------------------

func TestCollectionSkip(t *testing.T) {
	c := From([]int{1, 2, 3, 4, 5})
	result := c.Skip(2)
	assertSlice(t, result.All(), []int{3, 4, 5})
}

func TestCollectionSkip_Immutability(t *testing.T) {
	c := From([]int{1, 2, 3})
	_ = c.Skip(1)
	assertSlice(t, c.All(), []int{1, 2, 3})
}

// --- Shuffle ----------------------------------------------------------------

func TestCollectionShuffle(t *testing.T) {
	c := From([]int{1, 2, 3, 4, 5})
	result := c.Shuffle()
	if result.Count() != 5 {
		t.Fatalf("got %d items; want 5", result.Count())
	}
	sorted := make([]int, len(result.All()))
	copy(sorted, result.All())
	slices.Sort(sorted)
	assertSlice(t, sorted, []int{1, 2, 3, 4, 5})
}

func TestCollectionShuffle_Immutability(t *testing.T) {
	c := From([]int{1, 2, 3})
	_ = c.Shuffle()
	assertSlice(t, c.All(), []int{1, 2, 3})
}

// --- Pop --------------------------------------------------------------------

func TestCollectionPop(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		c := From([]int{1, 2, 3})
		remaining, item, ok := c.Pop()
		if !ok || item != 3 {
			t.Errorf("got (%d, %v); want (3, true)", item, ok)
		}
		assertSlice(t, remaining.All(), []int{1, 2})
	})

	t.Run("empty", func(t *testing.T) {
		c := From[int](nil)
		_, _, ok := c.Pop()
		if ok {
			t.Error("expected ok to be false for empty collection")
		}
	})
}

func TestCollectionPop_Immutability(t *testing.T) {
	c := From([]int{1, 2, 3})
	_, _, _ = c.Pop()
	assertSlice(t, c.All(), []int{1, 2, 3})
}

// --- Push -------------------------------------------------------------------

func TestCollectionPush(t *testing.T) {
	c := From([]int{1, 2})
	result := c.Push(3, 4)
	assertSlice(t, result.All(), []int{1, 2, 3, 4})
}

func TestCollectionPush_Immutability(t *testing.T) {
	c := From([]int{1, 2})
	_ = c.Push(3)
	assertSlice(t, c.All(), []int{1, 2})
}

// --- Tap --------------------------------------------------------------------

func TestCollectionTap(t *testing.T) {
	var inspected []int
	c := From([]int{1, 2, 3})
	returned := c.Tap(func(items []int) {
		inspected = make([]int, len(items))
		copy(inspected, items)
	})
	assertSlice(t, inspected, []int{1, 2, 3})
	if returned != c {
		t.Error("Tap should return the same collection")
	}
}

// --- When -------------------------------------------------------------------

func TestCollectionWhen(t *testing.T) {
	t.Run("true applies transformation", func(t *testing.T) {
		c := From([]int{1, 2, 3, 4, 5})
		result := c.When(true, func(items []int) []int {
			return Filter(items, func(n int) bool { return n > 2 })
		})
		assertSlice(t, result.All(), []int{3, 4, 5})
	})

	t.Run("false returns unchanged", func(t *testing.T) {
		c := From([]int{1, 2, 3})
		result := c.When(false, func(items []int) []int {
			return []int{99}
		})
		assertSlice(t, result.All(), []int{1, 2, 3})
	})
}

func TestCollectionWhen_Immutability(t *testing.T) {
	c := From([]int{1, 2, 3})
	_ = c.When(true, func(items []int) []int {
		return Filter(items, func(n int) bool { return n > 1 })
	})
	assertSlice(t, c.All(), []int{1, 2, 3})
}

// --- Chaining ---------------------------------------------------------------

func TestChaining(t *testing.T) {
	t.Run("filter then reverse then take", func(t *testing.T) {
		result := From([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}).
			Filter(func(n int) bool { return n%2 == 0 }).
			Reverse().
			Take(3).
			All()
		assertSlice(t, result, []int{10, 8, 6})
	})

	t.Run("skip then sort then push", func(t *testing.T) {
		result := From([]int{5, 3, 1, 4, 2}).
			Skip(1).
			Sort(func(a, b int) bool { return a < b }).
			Push(99).
			All()
		assertSlice(t, result, []int{1, 2, 3, 4, 99})
	})

	t.Run("filter with each side effect", func(t *testing.T) {
		var sum int
		result := From([]int{1, 2, 3, 4, 5}).
			Filter(func(n int) bool { return n > 2 }).
			Each(func(n int) { sum += n }).
			Reverse().
			All()
		assertSlice(t, result, []int{5, 4, 3})
		if sum != 12 {
			t.Errorf("sum = %d; want 12", sum)
		}
	})

	t.Run("reject then take", func(t *testing.T) {
		result := From([]int{1, 2, 3, 4, 5}).
			Reject(func(n int) bool { return n%2 == 0 }).
			Take(2).
			All()
		assertSlice(t, result, []int{1, 3})
	})

	t.Run("original unchanged after chain", func(t *testing.T) {
		c := From([]int{5, 3, 1, 4, 2})
		_ = c.Filter(func(n int) bool { return n > 2 }).
			Sort(func(a, b int) bool { return a < b }).
			Take(2).
			All()
		assertSlice(t, c.All(), []int{5, 3, 1, 4, 2})
	})
}

// --- Struct usage -----------------------------------------------------------

func TestCollectionWithStructs(t *testing.T) {
	users := []user{
		{Name: "Alice", Age: 30, Active: true},
		{Name: "Bob", Age: 25, Active: false},
		{Name: "Charlie", Age: 35, Active: true},
		{Name: "Diana", Age: 28, Active: true},
	}

	t.Run("filter active and sort by age", func(t *testing.T) {
		result := From(users).
			Filter(func(u user) bool { return u.Active }).
			Sort(func(a, b user) bool { return a.Age < b.Age }).
			All()
		if len(result) != 3 {
			t.Fatalf("got %d; want 3", len(result))
		}
		if result[0].Name != "Diana" || result[1].Name != "Alice" || result[2].Name != "Charlie" {
			t.Errorf("got %v", result)
		}
	})

	t.Run("first active", func(t *testing.T) {
		item, ok := From(users).First(func(u user) bool { return u.Active })
		if !ok || item.Name != "Alice" {
			t.Errorf("got (%v, %v); want Alice", item, ok)
		}
	})

	t.Run("contains inactive", func(t *testing.T) {
		if !From(users).Contains(func(u user) bool { return !u.Active }) {
			t.Error("should contain inactive user")
		}
	})

	t.Run("every active is false", func(t *testing.T) {
		if From(users).Every(func(u user) bool { return u.Active }) {
			t.Error("not all users are active")
		}
	})
}
