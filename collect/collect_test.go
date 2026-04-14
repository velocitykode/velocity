package collect

import (
	"reflect"
	"slices"
	"testing"
)

// --- helpers ----------------------------------------------------------------

type user struct {
	Name   string
	Age    int
	Active bool
}

func assertSlice[T any](t *testing.T, got, want []T) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
}

// --- Filter -----------------------------------------------------------------

func TestFilter(t *testing.T) {
	tests := []struct {
		name     string
		items    []int
		fn       func(int) bool
		expected []int
	}{
		{"keep evens", []int{1, 2, 3, 4, 5}, func(n int) bool { return n%2 == 0 }, []int{2, 4}},
		{"empty slice", []int{}, func(n int) bool { return true }, []int{}},
		{"nil slice", nil, func(n int) bool { return true }, []int{}},
		{"no matches", []int{1, 3, 5}, func(n int) bool { return n%2 == 0 }, []int{}},
		{"all match", []int{2, 4, 6}, func(n int) bool { return n%2 == 0 }, []int{2, 4, 6}},
		{"single element match", []int{2}, func(n int) bool { return n%2 == 0 }, []int{2}},
		{"single element no match", []int{1}, func(n int) bool { return n%2 == 0 }, []int{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Filter(tt.items, tt.fn)
			assertSlice(t, result, tt.expected)
		})
	}
}

func TestFilter_DoesNotMutateOriginal(t *testing.T) {
	original := []int{1, 2, 3, 4, 5}
	backup := make([]int, len(original))
	copy(backup, original)

	Filter(original, func(n int) bool { return n > 3 })

	assertSlice(t, original, backup)
}

// --- Reject -----------------------------------------------------------------

func TestReject(t *testing.T) {
	tests := []struct {
		name     string
		items    []int
		fn       func(int) bool
		expected []int
	}{
		{"reject evens", []int{1, 2, 3, 4, 5}, func(n int) bool { return n%2 == 0 }, []int{1, 3, 5}},
		{"empty slice", []int{}, func(n int) bool { return true }, []int{}},
		{"nil slice", nil, func(n int) bool { return true }, []int{}},
		{"reject none", []int{1, 3, 5}, func(n int) bool { return n%2 == 0 }, []int{1, 3, 5}},
		{"reject all", []int{2, 4, 6}, func(n int) bool { return n%2 == 0 }, []int{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Reject(tt.items, tt.fn)
			assertSlice(t, result, tt.expected)
		})
	}
}

// --- Map --------------------------------------------------------------------

func TestMap(t *testing.T) {
	t.Run("int to string", func(t *testing.T) {
		result := Map([]int{1, 2, 3}, func(n int) string {
			return string(rune('a' + n - 1))
		})
		assertSlice(t, result, []string{"a", "b", "c"})
	})

	t.Run("double", func(t *testing.T) {
		result := Map([]int{1, 2, 3}, func(n int) int { return n * 2 })
		assertSlice(t, result, []int{2, 4, 6})
	})

	t.Run("empty slice", func(t *testing.T) {
		result := Map([]int{}, func(n int) int { return n })
		assertSlice(t, result, []int{})
	})

	t.Run("nil slice", func(t *testing.T) {
		result := Map(nil, func(n int) int { return n })
		assertSlice(t, result, []int{})
	})

	t.Run("struct field extraction", func(t *testing.T) {
		users := []user{{Name: "Alice"}, {Name: "Bob"}}
		result := Map(users, func(u user) string { return u.Name })
		assertSlice(t, result, []string{"Alice", "Bob"})
	})
}

func TestMap_DoesNotMutateOriginal(t *testing.T) {
	original := []int{1, 2, 3}
	backup := make([]int, len(original))
	copy(backup, original)

	Map(original, func(n int) int { return n * 2 })

	assertSlice(t, original, backup)
}

// --- Each -------------------------------------------------------------------

func TestEach(t *testing.T) {
	t.Run("collects side effects", func(t *testing.T) {
		var sum int
		Each([]int{1, 2, 3}, func(n int) { sum += n })
		if sum != 6 {
			t.Errorf("got %d; want 6", sum)
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		called := false
		Each([]int{}, func(n int) { called = true })
		if called {
			t.Error("fn should not be called for empty slice")
		}
	})

	t.Run("nil slice", func(t *testing.T) {
		called := false
		Each(nil, func(n int) { called = true })
		if called {
			t.Error("fn should not be called for nil slice")
		}
	})
}

// --- Reduce -----------------------------------------------------------------

func TestReduce(t *testing.T) {
	t.Run("sum", func(t *testing.T) {
		result := Reduce([]int{1, 2, 3, 4}, 0, func(acc, n int) int { return acc + n })
		if result != 10 {
			t.Errorf("got %d; want 10", result)
		}
	})

	t.Run("string concat", func(t *testing.T) {
		result := Reduce([]string{"a", "b", "c"}, "", func(acc, s string) string { return acc + s })
		if result != "abc" {
			t.Errorf("got %q; want %q", result, "abc")
		}
	})

	t.Run("empty returns initial", func(t *testing.T) {
		result := Reduce([]int{}, 42, func(acc, n int) int { return acc + n })
		if result != 42 {
			t.Errorf("got %d; want 42", result)
		}
	})

	t.Run("nil returns initial", func(t *testing.T) {
		result := Reduce(nil, 99, func(acc, n int) int { return acc + n })
		if result != 99 {
			t.Errorf("got %d; want 99", result)
		}
	})

	t.Run("type change", func(t *testing.T) {
		users := []user{{Name: "Alice", Age: 30}, {Name: "Bob", Age: 25}}
		result := Reduce(users, 0, func(acc int, u user) int { return acc + u.Age })
		if result != 55 {
			t.Errorf("got %d; want 55", result)
		}
	})
}

// --- Contains ---------------------------------------------------------------

func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		items    []int
		fn       func(int) bool
		expected bool
	}{
		{"found", []int{1, 2, 3}, func(n int) bool { return n == 2 }, true},
		{"not found", []int{1, 2, 3}, func(n int) bool { return n == 5 }, false},
		{"empty", []int{}, func(n int) bool { return true }, false},
		{"nil", nil, func(n int) bool { return true }, false},
		{"single match", []int{1}, func(n int) bool { return n == 1 }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Contains(tt.items, tt.fn)
			if result != tt.expected {
				t.Errorf("got %v; want %v", result, tt.expected)
			}
		})
	}
}

// --- Every ------------------------------------------------------------------

func TestEvery(t *testing.T) {
	tests := []struct {
		name     string
		items    []int
		fn       func(int) bool
		expected bool
	}{
		{"all match", []int{2, 4, 6}, func(n int) bool { return n%2 == 0 }, true},
		{"one fails", []int{2, 3, 6}, func(n int) bool { return n%2 == 0 }, false},
		{"empty", []int{}, func(n int) bool { return false }, true},
		{"nil", nil, func(n int) bool { return false }, true},
		{"single match", []int{2}, func(n int) bool { return n%2 == 0 }, true},
		{"single fail", []int{1}, func(n int) bool { return n%2 == 0 }, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Every(tt.items, tt.fn)
			if result != tt.expected {
				t.Errorf("got %v; want %v", result, tt.expected)
			}
		})
	}
}

// --- None -------------------------------------------------------------------

func TestNone(t *testing.T) {
	tests := []struct {
		name     string
		items    []int
		fn       func(int) bool
		expected bool
	}{
		{"none match", []int{1, 3, 5}, func(n int) bool { return n%2 == 0 }, true},
		{"one matches", []int{1, 2, 5}, func(n int) bool { return n%2 == 0 }, false},
		{"empty", []int{}, func(n int) bool { return true }, true},
		{"nil", nil, func(n int) bool { return true }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := None(tt.items, tt.fn)
			if result != tt.expected {
				t.Errorf("got %v; want %v", result, tt.expected)
			}
		})
	}
}

// --- First ------------------------------------------------------------------

func TestFirst(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		item, ok := First([]int{1, 2, 3, 4}, func(n int) bool { return n > 2 })
		if !ok || item != 3 {
			t.Errorf("got (%d, %v); want (3, true)", item, ok)
		}
	})

	t.Run("not found", func(t *testing.T) {
		item, ok := First([]int{1, 2, 3}, func(n int) bool { return n > 5 })
		if ok || item != 0 {
			t.Errorf("got (%d, %v); want (0, false)", item, ok)
		}
	})

	t.Run("empty", func(t *testing.T) {
		_, ok := First([]int{}, func(n int) bool { return true })
		if ok {
			t.Error("expected ok to be false for empty slice")
		}
	})

	t.Run("nil", func(t *testing.T) {
		_, ok := First(nil, func(n int) bool { return true })
		if ok {
			t.Error("expected ok to be false for nil slice")
		}
	})

	t.Run("returns first match", func(t *testing.T) {
		item, ok := First([]int{1, 2, 3, 4}, func(n int) bool { return n%2 == 0 })
		if !ok || item != 2 {
			t.Errorf("got (%d, %v); want (2, true)", item, ok)
		}
	})
}

// --- Last -------------------------------------------------------------------

func TestLast(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		item, ok := Last([]int{1, 2, 3, 4}, func(n int) bool { return n%2 == 0 })
		if !ok || item != 4 {
			t.Errorf("got (%d, %v); want (4, true)", item, ok)
		}
	})

	t.Run("not found", func(t *testing.T) {
		item, ok := Last([]int{1, 3, 5}, func(n int) bool { return n%2 == 0 })
		if ok || item != 0 {
			t.Errorf("got (%d, %v); want (0, false)", item, ok)
		}
	})

	t.Run("empty", func(t *testing.T) {
		_, ok := Last([]int{}, func(n int) bool { return true })
		if ok {
			t.Error("expected ok to be false for empty slice")
		}
	})

	t.Run("nil", func(t *testing.T) {
		_, ok := Last(nil, func(n int) bool { return true })
		if ok {
			t.Error("expected ok to be false for nil slice")
		}
	})

	t.Run("single element", func(t *testing.T) {
		item, ok := Last([]int{7}, func(n int) bool { return true })
		if !ok || item != 7 {
			t.Errorf("got (%d, %v); want (7, true)", item, ok)
		}
	})
}

// --- FirstWhere -------------------------------------------------------------

func TestFirstWhere(t *testing.T) {
	item, ok := FirstWhere([]int{1, 2, 3}, func(n int) bool { return n == 2 })
	if !ok || item != 2 {
		t.Errorf("got (%d, %v); want (2, true)", item, ok)
	}
}

// --- CountBy ----------------------------------------------------------------

func TestCountBy(t *testing.T) {
	tests := []struct {
		name     string
		items    []int
		fn       func(int) bool
		expected int
	}{
		{"some match", []int{1, 2, 3, 4, 5}, func(n int) bool { return n%2 == 0 }, 2},
		{"none match", []int{1, 3, 5}, func(n int) bool { return n%2 == 0 }, 0},
		{"all match", []int{2, 4, 6}, func(n int) bool { return n%2 == 0 }, 3},
		{"empty", []int{}, func(n int) bool { return true }, 0},
		{"nil", nil, func(n int) bool { return true }, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CountBy(tt.items, tt.fn)
			if result != tt.expected {
				t.Errorf("got %d; want %d", result, tt.expected)
			}
		})
	}
}

// --- Reverse ----------------------------------------------------------------

func TestReverse(t *testing.T) {
	tests := []struct {
		name     string
		items    []int
		expected []int
	}{
		{"basic", []int{1, 2, 3}, []int{3, 2, 1}},
		{"single", []int{1}, []int{1}},
		{"two elements", []int{1, 2}, []int{2, 1}},
		{"empty", []int{}, []int{}},
		{"nil", nil, []int{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Reverse(tt.items)
			assertSlice(t, result, tt.expected)
		})
	}
}

func TestReverse_DoesNotMutateOriginal(t *testing.T) {
	original := []int{1, 2, 3}
	backup := make([]int, len(original))
	copy(backup, original)

	Reverse(original)

	assertSlice(t, original, backup)
}

// --- Unique -----------------------------------------------------------------

func TestUnique(t *testing.T) {
	tests := []struct {
		name     string
		items    []int
		expected []int
	}{
		{"with duplicates", []int{1, 2, 2, 3, 3, 3}, []int{1, 2, 3}},
		{"no duplicates", []int{1, 2, 3}, []int{1, 2, 3}},
		{"all same", []int{5, 5, 5}, []int{5}},
		{"single", []int{1}, []int{1}},
		{"empty", []int{}, []int{}},
		{"nil", nil, []int{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Unique(tt.items)
			assertSlice(t, result, tt.expected)
		})
	}
}

func TestUnique_PreservesOrder(t *testing.T) {
	result := Unique([]int{3, 1, 2, 1, 3})
	assertSlice(t, result, []int{3, 1, 2})
}

// --- UniqueBy ---------------------------------------------------------------

func TestUniqueBy(t *testing.T) {
	t.Run("by struct field", func(t *testing.T) {
		users := []user{
			{Name: "Alice", Age: 30},
			{Name: "Bob", Age: 30},
			{Name: "Charlie", Age: 25},
		}
		result := UniqueBy(users, func(u user) int { return u.Age })
		if len(result) != 2 {
			t.Fatalf("got %d items; want 2", len(result))
		}
		if result[0].Name != "Alice" || result[1].Name != "Charlie" {
			t.Errorf("got %v; want Alice and Charlie", result)
		}
	})

	t.Run("empty", func(t *testing.T) {
		result := UniqueBy([]user{}, func(u user) int { return u.Age })
		assertSlice(t, result, []user{})
	})

	t.Run("nil", func(t *testing.T) {
		result := UniqueBy(nil, func(u user) int { return u.Age })
		assertSlice(t, result, []user{})
	})
}

// --- SortBy -----------------------------------------------------------------

func TestSortBy(t *testing.T) {
	t.Run("ascending by age", func(t *testing.T) {
		users := []user{
			{Name: "Charlie", Age: 35},
			{Name: "Alice", Age: 25},
			{Name: "Bob", Age: 30},
		}
		result := SortBy(users, func(u user) int { return u.Age })
		expected := []user{
			{Name: "Alice", Age: 25},
			{Name: "Bob", Age: 30},
			{Name: "Charlie", Age: 35},
		}
		assertSlice(t, result, expected)
	})

	t.Run("empty", func(t *testing.T) {
		result := SortBy([]user{}, func(u user) int { return u.Age })
		assertSlice(t, result, []user{})
	})

	t.Run("single", func(t *testing.T) {
		users := []user{{Name: "Alice", Age: 25}}
		result := SortBy(users, func(u user) int { return u.Age })
		assertSlice(t, result, users)
	})

	t.Run("already sorted", func(t *testing.T) {
		items := []int{1, 2, 3}
		result := SortBy(items, func(n int) int { return n })
		assertSlice(t, result, []int{1, 2, 3})
	})
}

func TestSortBy_DoesNotMutateOriginal(t *testing.T) {
	original := []int{3, 1, 2}
	backup := []int{3, 1, 2}

	SortBy(original, func(n int) int { return n })

	assertSlice(t, original, backup)
}

func TestSortBy_Stable(t *testing.T) {
	items := []user{
		{Name: "Alice", Age: 30},
		{Name: "Bob", Age: 30},
		{Name: "Charlie", Age: 30},
	}
	result := SortBy(items, func(u user) int { return u.Age })
	if result[0].Name != "Alice" || result[1].Name != "Bob" || result[2].Name != "Charlie" {
		t.Errorf("sort is not stable: %v", result)
	}
}

// --- SortByDesc -------------------------------------------------------------

func TestSortByDesc(t *testing.T) {
	t.Run("descending by age", func(t *testing.T) {
		users := []user{
			{Name: "Alice", Age: 25},
			{Name: "Bob", Age: 30},
			{Name: "Charlie", Age: 35},
		}
		result := SortByDesc(users, func(u user) int { return u.Age })
		expected := []user{
			{Name: "Charlie", Age: 35},
			{Name: "Bob", Age: 30},
			{Name: "Alice", Age: 25},
		}
		assertSlice(t, result, expected)
	})

	t.Run("with equal keys", func(t *testing.T) {
		users := []user{
			{Name: "Alice", Age: 30},
			{Name: "Bob", Age: 25},
			{Name: "Charlie", Age: 30},
		}
		result := SortByDesc(users, func(u user) int { return u.Age })
		if result[0].Age != 30 || result[1].Age != 30 || result[2].Age != 25 {
			t.Errorf("got %v", result)
		}
	})

	t.Run("empty", func(t *testing.T) {
		result := SortByDesc([]int{}, func(n int) int { return n })
		assertSlice(t, result, []int{})
	})
}

// --- Sort -------------------------------------------------------------------

func TestSort(t *testing.T) {
	t.Run("ascending", func(t *testing.T) {
		result := Sort([]int{3, 1, 4, 1, 5}, func(a, b int) bool { return a < b })
		assertSlice(t, result, []int{1, 1, 3, 4, 5})
	})

	t.Run("descending", func(t *testing.T) {
		result := Sort([]int{3, 1, 4, 1, 5}, func(a, b int) bool { return a > b })
		assertSlice(t, result, []int{5, 4, 3, 1, 1})
	})

	t.Run("empty", func(t *testing.T) {
		result := Sort([]int{}, func(a, b int) bool { return a < b })
		assertSlice(t, result, []int{})
	})

	t.Run("nil", func(t *testing.T) {
		result := Sort(nil, func(a, b int) bool { return a < b })
		assertSlice(t, result, []int{})
	})

	t.Run("single", func(t *testing.T) {
		result := Sort([]int{1}, func(a, b int) bool { return a < b })
		assertSlice(t, result, []int{1})
	})
}

func TestSort_DoesNotMutateOriginal(t *testing.T) {
	original := []int{3, 1, 2}
	backup := []int{3, 1, 2}

	Sort(original, func(a, b int) bool { return a < b })

	assertSlice(t, original, backup)
}

// --- GroupBy ----------------------------------------------------------------

func TestGroupBy(t *testing.T) {
	t.Run("basic grouping", func(t *testing.T) {
		items := []user{
			{Name: "Alice", Active: true},
			{Name: "Bob", Active: false},
			{Name: "Charlie", Active: true},
		}
		result := GroupBy(items, func(u user) bool { return u.Active })
		if len(result[true]) != 2 {
			t.Errorf("active group: got %d; want 2", len(result[true]))
		}
		if len(result[false]) != 1 {
			t.Errorf("inactive group: got %d; want 1", len(result[false]))
		}
	})

	t.Run("empty", func(t *testing.T) {
		result := GroupBy([]int{}, func(n int) int { return n })
		if len(result) != 0 {
			t.Errorf("got %d groups; want 0", len(result))
		}
	})

	t.Run("nil", func(t *testing.T) {
		result := GroupBy(nil, func(n int) int { return n })
		if len(result) != 0 {
			t.Errorf("got %d groups; want 0", len(result))
		}
	})

	t.Run("all same key", func(t *testing.T) {
		result := GroupBy([]int{1, 2, 3}, func(n int) string { return "all" })
		if len(result["all"]) != 3 {
			t.Errorf("got %d; want 3", len(result["all"]))
		}
	})
}

// --- KeyBy ------------------------------------------------------------------

func TestKeyBy(t *testing.T) {
	t.Run("by name", func(t *testing.T) {
		users := []user{
			{Name: "Alice", Age: 30},
			{Name: "Bob", Age: 25},
		}
		result := KeyBy(users, func(u user) string { return u.Name })
		if result["Alice"].Age != 30 {
			t.Errorf("Alice age: got %d; want 30", result["Alice"].Age)
		}
		if result["Bob"].Age != 25 {
			t.Errorf("Bob age: got %d; want 25", result["Bob"].Age)
		}
	})

	t.Run("duplicate keys keep last", func(t *testing.T) {
		items := []user{
			{Name: "Alice", Age: 25},
			{Name: "Alice", Age: 30},
		}
		result := KeyBy(items, func(u user) string { return u.Name })
		if result["Alice"].Age != 30 {
			t.Errorf("got %d; want 30 (last wins)", result["Alice"].Age)
		}
	})

	t.Run("empty", func(t *testing.T) {
		result := KeyBy([]user{}, func(u user) string { return u.Name })
		if len(result) != 0 {
			t.Errorf("got %d; want 0", len(result))
		}
	})

	t.Run("nil", func(t *testing.T) {
		result := KeyBy(nil, func(u user) string { return u.Name })
		if len(result) != 0 {
			t.Errorf("got %d; want 0", len(result))
		}
	})
}

// --- Chunk ------------------------------------------------------------------

func TestChunk(t *testing.T) {
	tests := []struct {
		name     string
		items    []int
		size     int
		expected [][]int
	}{
		{"even split", []int{1, 2, 3, 4}, 2, [][]int{{1, 2}, {3, 4}}},
		{"uneven split", []int{1, 2, 3, 4, 5}, 2, [][]int{{1, 2}, {3, 4}, {5}}},
		{"size larger than length", []int{1, 2}, 5, [][]int{{1, 2}}},
		{"size equals length", []int{1, 2, 3}, 3, [][]int{{1, 2, 3}}},
		{"size one", []int{1, 2, 3}, 1, [][]int{{1}, {2}, {3}}},
		{"single element", []int{1}, 2, [][]int{{1}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Chunk(tt.items, tt.size)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("got %v; want %v", result, tt.expected)
			}
		})
	}

	t.Run("zero size", func(t *testing.T) {
		result := Chunk([]int{1, 2, 3}, 0)
		if result != nil {
			t.Errorf("got %v; want nil", result)
		}
	})

	t.Run("negative size", func(t *testing.T) {
		result := Chunk([]int{1, 2, 3}, -1)
		if result != nil {
			t.Errorf("got %v; want nil", result)
		}
	})

	t.Run("empty", func(t *testing.T) {
		result := Chunk([]int{}, 2)
		if len(result) != 0 {
			t.Errorf("got %v; want empty", result)
		}
	})

	t.Run("nil", func(t *testing.T) {
		result := Chunk([]int(nil), 2)
		if len(result) != 0 {
			t.Errorf("got %v; want empty", result)
		}
	})
}

func TestChunk_DoesNotMutateOriginal(t *testing.T) {
	original := []int{1, 2, 3, 4}
	backup := []int{1, 2, 3, 4}

	chunks := Chunk(original, 2)
	chunks[0][0] = 99

	assertSlice(t, original, backup)
}

// --- Flatten ----------------------------------------------------------------

func TestFlatten(t *testing.T) {
	tests := []struct {
		name     string
		items    [][]int
		expected []int
	}{
		{"basic", [][]int{{1, 2}, {3, 4}}, []int{1, 2, 3, 4}},
		{"empty inner", [][]int{{}, {1}, {}}, []int{1}},
		{"single group", [][]int{{1, 2, 3}}, []int{1, 2, 3}},
		{"all empty", [][]int{{}, {}}, []int{}},
		{"empty outer", [][]int{}, []int{}},
		{"nil outer", nil, []int{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Flatten(tt.items)
			assertSlice(t, result, tt.expected)
		})
	}
}

// --- FlatMap ----------------------------------------------------------------

func TestFlatMap(t *testing.T) {
	t.Run("expand", func(t *testing.T) {
		result := FlatMap([]int{1, 2, 3}, func(n int) []int { return []int{n, n * 10} })
		assertSlice(t, result, []int{1, 10, 2, 20, 3, 30})
	})

	t.Run("empty result", func(t *testing.T) {
		result := FlatMap([]int{1, 2}, func(n int) []string { return nil })
		assertSlice(t, result, []string{})
	})

	t.Run("empty input", func(t *testing.T) {
		result := FlatMap([]int{}, func(n int) []int { return []int{n} })
		assertSlice(t, result, []int{})
	})

	t.Run("nil input", func(t *testing.T) {
		result := FlatMap(nil, func(n int) []int { return []int{n} })
		assertSlice(t, result, []int{})
	})
}

// --- Take -------------------------------------------------------------------

func TestTake(t *testing.T) {
	tests := []struct {
		name     string
		items    []int
		n        int
		expected []int
	}{
		{"basic", []int{1, 2, 3, 4, 5}, 3, []int{1, 2, 3}},
		{"more than length", []int{1, 2}, 5, []int{1, 2}},
		{"exact length", []int{1, 2, 3}, 3, []int{1, 2, 3}},
		{"zero", []int{1, 2, 3}, 0, []int{}},
		{"negative", []int{1, 2, 3}, -1, []int{}},
		{"single", []int{1}, 1, []int{1}},
		{"empty", []int{}, 3, []int{}},
		{"nil", nil, 3, []int{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Take(tt.items, tt.n)
			assertSlice(t, result, tt.expected)
		})
	}
}

func TestTake_DoesNotMutateOriginal(t *testing.T) {
	original := []int{1, 2, 3}
	backup := []int{1, 2, 3}

	result := Take(original, 2)
	result[0] = 99

	assertSlice(t, original, backup)
}

// --- Skip -------------------------------------------------------------------

func TestSkip(t *testing.T) {
	tests := []struct {
		name     string
		items    []int
		n        int
		expected []int
	}{
		{"basic", []int{1, 2, 3, 4, 5}, 2, []int{3, 4, 5}},
		{"more than length", []int{1, 2}, 5, []int{}},
		{"exact length", []int{1, 2, 3}, 3, []int{}},
		{"zero", []int{1, 2, 3}, 0, []int{1, 2, 3}},
		{"negative", []int{1, 2, 3}, -1, []int{1, 2, 3}},
		{"single", []int{1}, 1, []int{}},
		{"empty", []int{}, 3, []int{}},
		{"nil", nil, 3, []int{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Skip(tt.items, tt.n)
			assertSlice(t, result, tt.expected)
		})
	}
}

func TestSkip_DoesNotMutateOriginal(t *testing.T) {
	original := []int{1, 2, 3}
	backup := []int{1, 2, 3}

	result := Skip(original, 1)
	result[0] = 99

	assertSlice(t, original, backup)
}

// --- Pluck ------------------------------------------------------------------

func TestPluck(t *testing.T) {
	t.Run("extract names", func(t *testing.T) {
		users := []user{{Name: "Alice"}, {Name: "Bob"}, {Name: "Charlie"}}
		result := Pluck(users, func(u user) string { return u.Name })
		assertSlice(t, result, []string{"Alice", "Bob", "Charlie"})
	})

	t.Run("empty", func(t *testing.T) {
		result := Pluck([]user{}, func(u user) string { return u.Name })
		assertSlice(t, result, []string{})
	})

	t.Run("nil", func(t *testing.T) {
		result := Pluck(nil, func(u user) string { return u.Name })
		assertSlice(t, result, []string{})
	})
}

// --- Sum --------------------------------------------------------------------

func TestSum(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		result := Sum([]int{1, 2, 3}, func(n int) float64 { return float64(n) })
		if result != 6.0 {
			t.Errorf("got %f; want 6.0", result)
		}
	})

	t.Run("struct field", func(t *testing.T) {
		users := []user{{Age: 25}, {Age: 30}, {Age: 35}}
		result := Sum(users, func(u user) float64 { return float64(u.Age) })
		if result != 90.0 {
			t.Errorf("got %f; want 90.0", result)
		}
	})

	t.Run("empty", func(t *testing.T) {
		result := Sum([]int{}, func(n int) float64 { return float64(n) })
		if result != 0.0 {
			t.Errorf("got %f; want 0.0", result)
		}
	})

	t.Run("nil", func(t *testing.T) {
		result := Sum(nil, func(n int) float64 { return float64(n) })
		if result != 0.0 {
			t.Errorf("got %f; want 0.0", result)
		}
	})
}

// --- Min --------------------------------------------------------------------

func TestMin(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		item, ok := Min([]int{3, 1, 4, 1, 5}, func(n int) int { return n })
		if !ok || item != 1 {
			t.Errorf("got (%d, %v); want (1, true)", item, ok)
		}
	})

	t.Run("by struct field", func(t *testing.T) {
		users := []user{{Name: "Alice", Age: 30}, {Name: "Bob", Age: 25}}
		item, ok := Min(users, func(u user) int { return u.Age })
		if !ok || item.Name != "Bob" {
			t.Errorf("got (%v, %v); want Bob", item, ok)
		}
	})

	t.Run("single", func(t *testing.T) {
		item, ok := Min([]int{42}, func(n int) int { return n })
		if !ok || item != 42 {
			t.Errorf("got (%d, %v); want (42, true)", item, ok)
		}
	})

	t.Run("empty", func(t *testing.T) {
		_, ok := Min([]int{}, func(n int) int { return n })
		if ok {
			t.Error("expected ok to be false for empty slice")
		}
	})

	t.Run("nil", func(t *testing.T) {
		_, ok := Min(nil, func(n int) int { return n })
		if ok {
			t.Error("expected ok to be false for nil slice")
		}
	})

	t.Run("returns first minimum", func(t *testing.T) {
		users := []user{
			{Name: "Alice", Age: 25},
			{Name: "Bob", Age: 25},
		}
		item, _ := Min(users, func(u user) int { return u.Age })
		if item.Name != "Alice" {
			t.Errorf("got %s; want Alice (first with min key)", item.Name)
		}
	})
}

// --- Max --------------------------------------------------------------------

func TestMax(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		item, ok := Max([]int{3, 1, 4, 1, 5}, func(n int) int { return n })
		if !ok || item != 5 {
			t.Errorf("got (%d, %v); want (5, true)", item, ok)
		}
	})

	t.Run("by struct field", func(t *testing.T) {
		users := []user{{Name: "Alice", Age: 30}, {Name: "Bob", Age: 25}}
		item, ok := Max(users, func(u user) int { return u.Age })
		if !ok || item.Name != "Alice" {
			t.Errorf("got (%v, %v); want Alice", item, ok)
		}
	})

	t.Run("single", func(t *testing.T) {
		item, ok := Max([]int{42}, func(n int) int { return n })
		if !ok || item != 42 {
			t.Errorf("got (%d, %v); want (42, true)", item, ok)
		}
	})

	t.Run("empty", func(t *testing.T) {
		_, ok := Max([]int{}, func(n int) int { return n })
		if ok {
			t.Error("expected ok to be false for empty slice")
		}
	})

	t.Run("nil", func(t *testing.T) {
		_, ok := Max(nil, func(n int) int { return n })
		if ok {
			t.Error("expected ok to be false for nil slice")
		}
	})
}

// --- Intersect --------------------------------------------------------------

func TestIntersect(t *testing.T) {
	tests := []struct {
		name     string
		a, b     []int
		expected []int
	}{
		{"overlap", []int{1, 2, 3, 4}, []int{3, 4, 5, 6}, []int{3, 4}},
		{"no overlap", []int{1, 2}, []int{3, 4}, []int{}},
		{"identical", []int{1, 2, 3}, []int{1, 2, 3}, []int{1, 2, 3}},
		{"a empty", []int{}, []int{1, 2}, []int{}},
		{"b empty", []int{1, 2}, []int{}, []int{}},
		{"both empty", []int{}, []int{}, []int{}},
		{"a nil", nil, []int{1, 2}, []int{}},
		{"b nil", []int{1, 2}, nil, []int{}},
		{"duplicates in a", []int{1, 1, 2, 2}, []int{1, 2}, []int{1, 1, 2, 2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Intersect(tt.a, tt.b)
			assertSlice(t, result, tt.expected)
		})
	}
}

// --- Diff -------------------------------------------------------------------

func TestDiff(t *testing.T) {
	tests := []struct {
		name     string
		a, b     []int
		expected []int
	}{
		{"some diff", []int{1, 2, 3, 4}, []int{3, 4, 5, 6}, []int{1, 2}},
		{"no diff", []int{1, 2, 3}, []int{1, 2, 3}, []int{}},
		{"all diff", []int{1, 2}, []int{3, 4}, []int{1, 2}},
		{"a empty", []int{}, []int{1, 2}, []int{}},
		{"b empty", []int{1, 2}, []int{}, []int{1, 2}},
		{"both empty", []int{}, []int{}, []int{}},
		{"a nil", nil, []int{1, 2}, []int{}},
		{"b nil", []int{1, 2}, nil, []int{1, 2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Diff(tt.a, tt.b)
			assertSlice(t, result, tt.expected)
		})
	}
}

// --- Shuffle ----------------------------------------------------------------

func TestShuffle(t *testing.T) {
	t.Run("preserves elements", func(t *testing.T) {
		items := []int{1, 2, 3, 4, 5}
		result := Shuffle(items)
		if len(result) != len(items) {
			t.Fatalf("got %d items; want %d", len(result), len(items))
		}
		sorted := make([]int, len(result))
		copy(sorted, result)
		slices.Sort(sorted)
		assertSlice(t, sorted, []int{1, 2, 3, 4, 5})
	})

	t.Run("does not mutate original", func(t *testing.T) {
		original := []int{1, 2, 3, 4, 5}
		backup := []int{1, 2, 3, 4, 5}
		Shuffle(original)
		assertSlice(t, original, backup)
	})

	t.Run("empty", func(t *testing.T) {
		result := Shuffle([]int{})
		assertSlice(t, result, []int{})
	})

	t.Run("nil", func(t *testing.T) {
		result := Shuffle([]int(nil))
		assertSlice(t, result, []int{})
	})

	t.Run("single element", func(t *testing.T) {
		result := Shuffle([]int{1})
		assertSlice(t, result, []int{1})
	})
}

// --- Partition --------------------------------------------------------------

func TestPartition(t *testing.T) {
	t.Run("basic split", func(t *testing.T) {
		pass, fail := Partition([]int{1, 2, 3, 4, 5}, func(n int) bool { return n%2 == 0 })
		assertSlice(t, pass, []int{2, 4})
		assertSlice(t, fail, []int{1, 3, 5})
	})

	t.Run("all pass", func(t *testing.T) {
		pass, fail := Partition([]int{2, 4}, func(n int) bool { return n%2 == 0 })
		assertSlice(t, pass, []int{2, 4})
		assertSlice(t, fail, []int{})
	})

	t.Run("all fail", func(t *testing.T) {
		pass, fail := Partition([]int{1, 3}, func(n int) bool { return n%2 == 0 })
		assertSlice(t, pass, []int{})
		assertSlice(t, fail, []int{1, 3})
	})

	t.Run("empty", func(t *testing.T) {
		pass, fail := Partition([]int{}, func(n int) bool { return true })
		assertSlice(t, pass, []int{})
		assertSlice(t, fail, []int{})
	})

	t.Run("nil", func(t *testing.T) {
		pass, fail := Partition(nil, func(n int) bool { return true })
		assertSlice(t, pass, []int{})
		assertSlice(t, fail, []int{})
	})
}

// --- Zip --------------------------------------------------------------------

func TestZip(t *testing.T) {
	t.Run("equal length", func(t *testing.T) {
		result := Zip([]int{1, 2, 3}, []string{"a", "b", "c"}, func(n int, s string) string {
			return s + string(rune('0'+n))
		})
		assertSlice(t, result, []string{"a1", "b2", "c3"})
	})

	t.Run("a shorter", func(t *testing.T) {
		result := Zip([]int{1, 2}, []string{"a", "b", "c"}, func(n int, s string) string {
			return s
		})
		assertSlice(t, result, []string{"a", "b"})
	})

	t.Run("b shorter", func(t *testing.T) {
		result := Zip([]int{1, 2, 3}, []string{"a"}, func(n int, s string) string {
			return s
		})
		assertSlice(t, result, []string{"a"})
	})

	t.Run("empty", func(t *testing.T) {
		result := Zip([]int{}, []string{}, func(n int, s string) string { return s })
		assertSlice(t, result, []string{})
	})

	t.Run("a nil", func(t *testing.T) {
		result := Zip(nil, []string{"a"}, func(n int, s string) string { return s })
		assertSlice(t, result, []string{})
	})

	t.Run("b nil", func(t *testing.T) {
		result := Zip([]int{1}, []string(nil), func(n int, s string) string { return s })
		assertSlice(t, result, []string{})
	})
}

// --- Times ------------------------------------------------------------------

func TestTimes(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		result := Times(5, func(i int) int { return i * 2 })
		assertSlice(t, result, []int{0, 2, 4, 6, 8})
	})

	t.Run("zero", func(t *testing.T) {
		result := Times(0, func(i int) int { return i })
		assertSlice(t, result, []int{})
	})

	t.Run("negative", func(t *testing.T) {
		result := Times(-1, func(i int) int { return i })
		assertSlice(t, result, []int{})
	})

	t.Run("single", func(t *testing.T) {
		result := Times(1, func(i int) string { return "hello" })
		assertSlice(t, result, []string{"hello"})
	})
}

// --- Pop --------------------------------------------------------------------

func TestPop(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		remaining, item, ok := Pop([]int{1, 2, 3})
		if !ok || item != 3 {
			t.Errorf("got (%v, %d, %v); want ([1,2], 3, true)", remaining, item, ok)
		}
		assertSlice(t, remaining, []int{1, 2})
	})

	t.Run("single", func(t *testing.T) {
		remaining, item, ok := Pop([]int{42})
		if !ok || item != 42 {
			t.Errorf("got (%v, %d, %v); want ([], 42, true)", remaining, item, ok)
		}
		assertSlice(t, remaining, []int{})
	})

	t.Run("empty", func(t *testing.T) {
		remaining, item, ok := Pop([]int{})
		if ok || item != 0 {
			t.Errorf("got (%v, %d, %v); want ([], 0, false)", remaining, item, ok)
		}
		assertSlice(t, remaining, []int{})
	})

	t.Run("nil", func(t *testing.T) {
		remaining, item, ok := Pop([]int(nil))
		if ok || item != 0 {
			t.Errorf("got (%v, %d, %v); want ([], 0, false)", remaining, item, ok)
		}
		assertSlice(t, remaining, []int{})
	})
}

func TestPop_DoesNotMutateOriginal(t *testing.T) {
	original := []int{1, 2, 3}
	backup := []int{1, 2, 3}

	Pop(original)

	assertSlice(t, original, backup)
}

// --- Push -------------------------------------------------------------------

func TestPush(t *testing.T) {
	t.Run("single item", func(t *testing.T) {
		result := Push([]int{1, 2}, 3)
		assertSlice(t, result, []int{1, 2, 3})
	})

	t.Run("multiple items", func(t *testing.T) {
		result := Push([]int{1}, 2, 3, 4)
		assertSlice(t, result, []int{1, 2, 3, 4})
	})

	t.Run("to empty", func(t *testing.T) {
		result := Push([]int{}, 1, 2)
		assertSlice(t, result, []int{1, 2})
	})

	t.Run("to nil", func(t *testing.T) {
		result := Push(nil, 1, 2)
		assertSlice(t, result, []int{1, 2})
	})

	t.Run("no items", func(t *testing.T) {
		result := Push([]int{1, 2})
		assertSlice(t, result, []int{1, 2})
	})
}

func TestPush_DoesNotMutateOriginal(t *testing.T) {
	original := []int{1, 2, 3}
	backup := []int{1, 2, 3}

	Push(original, 4)

	assertSlice(t, original, backup)
}

// --- Tap --------------------------------------------------------------------

func TestTap(t *testing.T) {
	t.Run("inspects without modifying", func(t *testing.T) {
		var inspected []int
		items := []int{1, 2, 3}
		result := Tap(items, func(s []int) { inspected = s })
		assertSlice(t, result, []int{1, 2, 3})
		assertSlice(t, inspected, []int{1, 2, 3})
	})

	t.Run("empty", func(t *testing.T) {
		called := false
		Tap([]int{}, func(s []int) { called = true })
		if !called {
			t.Error("fn should be called even for empty slice")
		}
	})
}

// --- When -------------------------------------------------------------------

func TestWhen(t *testing.T) {
	t.Run("true condition applies fn", func(t *testing.T) {
		result := When([]int{1, 2, 3}, true, func(items []int) []int {
			return Filter(items, func(n int) bool { return n > 1 })
		})
		assertSlice(t, result, []int{2, 3})
	})

	t.Run("false condition returns unchanged", func(t *testing.T) {
		result := When([]int{1, 2, 3}, false, func(items []int) []int {
			return []int{99}
		})
		assertSlice(t, result, []int{1, 2, 3})
	})

	t.Run("empty", func(t *testing.T) {
		result := When([]int{}, true, func(items []int) []int { return items })
		assertSlice(t, result, []int{})
	})
}
