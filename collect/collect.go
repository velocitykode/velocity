// Package collect provides generic, type-safe operations on slices.
//
// It offers two complementary APIs:
//
// Package-level functions for one-off operations:
//
//	active := collect.Filter(users, func(u User) bool {
//	    return u.Active
//	})
//
//	names := collect.Map(users, func(u User) string {
//	    return u.Name
//	})
//
//	grouped := collect.GroupBy(orders, func(o Order) string {
//	    return o.Status
//	})
//
// A fluent [Collection] type for chaining same-type operations:
//
//	result := collect.From(users).
//	    Filter(func(u User) bool { return u.Active }).
//	    Sort(func(a, b User) bool { return a.Name < b.Name }).
//	    Take(10).
//	    All()
//
// The [Collection] type works with any slice, including ORM query results:
//
//	users, _ := User{}.Where("active = ?", true).Get()
//	top := collect.From(users).
//	    Sort(func(a, b User) bool { return a.Score > b.Score }).
//	    Take(5).
//	    All()
//
// [Collection] is immutable: [From] copies the input slice, and every method
// returns a new [Collection] without modifying the original. This prevents
// accidental aliasing bugs.
//
// Functions that change the element type ([Map], [Reduce], [GroupBy], [KeyBy],
// [FlatMap], [Pluck], [Zip]) are only available as package-level functions,
// because Go methods cannot introduce new type parameters. Similarly,
// functions requiring a [comparable] constraint ([Unique], [UniqueBy],
// [Intersect], [Diff]) are package-level only, since [Collection] uses the
// broader any constraint.
package collect

import (
	"cmp"
	"math/rand/v2"
	"slices"
)

// Filter returns items for which fn returns true.
func Filter[T any](items []T, fn func(T) bool) []T {
	result := make([]T, 0)
	for _, item := range items {
		if fn(item) {
			result = append(result, item)
		}
	}
	return result
}

// Reject returns items for which fn returns false.
func Reject[T any](items []T, fn func(T) bool) []T {
	result := make([]T, 0)
	for _, item := range items {
		if !fn(item) {
			result = append(result, item)
		}
	}
	return result
}

// Map transforms each item using the given function.
func Map[T, R any](items []T, fn func(T) R) []R {
	result := make([]R, len(items))
	for i, item := range items {
		result[i] = fn(item)
	}
	return result
}

// Each calls fn for each item in the slice.
func Each[T any](items []T, fn func(T)) {
	for _, item := range items {
		fn(item)
	}
}

// Reduce reduces the slice to a single value starting from initial.
func Reduce[T, R any](items []T, initial R, fn func(R, T) R) R {
	result := initial
	for _, item := range items {
		result = fn(result, item)
	}
	return result
}

// Contains returns true if fn returns true for any item.
func Contains[T any](items []T, fn func(T) bool) bool {
	for _, item := range items {
		if fn(item) {
			return true
		}
	}
	return false
}

// Every returns true if fn returns true for all items. Returns true for empty slices.
func Every[T any](items []T, fn func(T) bool) bool {
	for _, item := range items {
		if !fn(item) {
			return false
		}
	}
	return true
}

// None returns true if fn returns false for all items. Returns true for empty slices.
func None[T any](items []T, fn func(T) bool) bool {
	for _, item := range items {
		if fn(item) {
			return false
		}
	}
	return true
}

// First returns the first item for which fn returns true.
func First[T any](items []T, fn func(T) bool) (T, bool) {
	for _, item := range items {
		if fn(item) {
			return item, true
		}
	}
	var zero T
	return zero, false
}

// Last returns the last item for which fn returns true.
func Last[T any](items []T, fn func(T) bool) (T, bool) {
	for i := len(items) - 1; i >= 0; i-- {
		if fn(items[i]) {
			return items[i], true
		}
	}
	var zero T
	return zero, false
}

// FirstWhere returns the first item for which fn returns true.
// It is an alias for [First], provided for readability.
func FirstWhere[T any](items []T, fn func(T) bool) (T, bool) {
	return First(items, fn)
}

// CountBy returns the number of items for which fn returns true.
func CountBy[T any](items []T, fn func(T) bool) int {
	count := 0
	for _, item := range items {
		if fn(item) {
			count++
		}
	}
	return count
}

// Reverse returns a new slice with items in reverse order.
func Reverse[T any](items []T) []T {
	result := make([]T, len(items))
	for i, item := range items {
		result[len(items)-1-i] = item
	}
	return result
}

// Unique returns a new slice with duplicate values removed, preserving order.
func Unique[T comparable](items []T) []T {
	seen := make(map[T]struct{}, len(items))
	result := make([]T, 0)
	for _, item := range items {
		if _, ok := seen[item]; !ok {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

// UniqueBy returns a new slice with duplicates removed, where uniqueness is
// determined by the key returned by fn. The first item with each key is kept.
func UniqueBy[T any, K comparable](items []T, fn func(T) K) []T {
	seen := make(map[K]struct{}, len(items))
	result := make([]T, 0)
	for _, item := range items {
		key := fn(item)
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

// SortBy returns a new slice sorted by the key returned by fn in ascending order.
func SortBy[T any, K cmp.Ordered](items []T, fn func(T) K) []T {
	result := make([]T, len(items))
	copy(result, items)
	slices.SortStableFunc(result, func(a, b T) int {
		ka, kb := fn(a), fn(b)
		if ka < kb {
			return -1
		}
		if ka > kb {
			return 1
		}
		return 0
	})
	return result
}

// SortByDesc returns a new slice sorted by the key returned by fn in descending order.
func SortByDesc[T any, K cmp.Ordered](items []T, fn func(T) K) []T {
	result := make([]T, len(items))
	copy(result, items)
	slices.SortStableFunc(result, func(a, b T) int {
		ka, kb := fn(a), fn(b)
		if ka > kb {
			return -1
		}
		if ka < kb {
			return 1
		}
		return 0
	})
	return result
}

// Sort returns a new slice sorted using the given comparison function.
func Sort[T any](items []T, less func(a, b T) bool) []T {
	result := make([]T, len(items))
	copy(result, items)
	slices.SortStableFunc(result, func(a, b T) int {
		if less(a, b) {
			return -1
		}
		if less(b, a) {
			return 1
		}
		return 0
	})
	return result
}

// GroupBy groups items by the key returned by fn.
func GroupBy[T any, K comparable](items []T, fn func(T) K) map[K][]T {
	result := make(map[K][]T)
	for _, item := range items {
		key := fn(item)
		result[key] = append(result[key], item)
	}
	return result
}

// KeyBy indexes items by the key returned by fn. If multiple items produce
// the same key, the last one wins.
func KeyBy[T any, K comparable](items []T, fn func(T) K) map[K]T {
	result := make(map[K]T, len(items))
	for _, item := range items {
		result[fn(item)] = item
	}
	return result
}

// Chunk splits items into groups of the given size. Returns nil for size <= 0.
func Chunk[T any](items []T, size int) [][]T {
	if size <= 0 {
		return nil
	}
	result := make([][]T, 0, (len(items)+size-1)/size)
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		chunk := make([]T, end-i)
		copy(chunk, items[i:end])
		result = append(result, chunk)
	}
	return result
}

// Flatten flattens a slice of slices into a single slice.
func Flatten[T any](items [][]T) []T {
	result := make([]T, 0)
	for _, group := range items {
		result = append(result, group...)
	}
	return result
}

// FlatMap maps each item to a slice and flattens the results.
func FlatMap[T, R any](items []T, fn func(T) []R) []R {
	result := make([]R, 0)
	for _, item := range items {
		result = append(result, fn(item)...)
	}
	return result
}

// Take returns the first n items. Returns an empty slice for n <= 0.
func Take[T any](items []T, n int) []T {
	if n <= 0 {
		return []T{}
	}
	if n > len(items) {
		n = len(items)
	}
	result := make([]T, n)
	copy(result, items[:n])
	return result
}

// Skip returns items after skipping the first n. Returns all items for n <= 0.
func Skip[T any](items []T, n int) []T {
	if n <= 0 {
		result := make([]T, len(items))
		copy(result, items)
		return result
	}
	if n >= len(items) {
		return []T{}
	}
	result := make([]T, len(items)-n)
	copy(result, items[n:])
	return result
}

// Pluck extracts a value from each item using fn. It is an alias for [Map],
// provided for readability when extracting struct fields.
func Pluck[T any, R any](items []T, fn func(T) R) []R {
	return Map(items, fn)
}

// Sum returns the sum of values extracted from each item by fn.
func Sum[T any](items []T, fn func(T) float64) float64 {
	var total float64
	for _, item := range items {
		total += fn(item)
	}
	return total
}

// Min returns the item with the smallest key as determined by fn.
func Min[T any, K cmp.Ordered](items []T, fn func(T) K) (T, bool) {
	if len(items) == 0 {
		var zero T
		return zero, false
	}
	minItem := items[0]
	minKey := fn(items[0])
	for _, item := range items[1:] {
		key := fn(item)
		if key < minKey {
			minKey = key
			minItem = item
		}
	}
	return minItem, true
}

// Max returns the item with the largest key as determined by fn.
func Max[T any, K cmp.Ordered](items []T, fn func(T) K) (T, bool) {
	if len(items) == 0 {
		var zero T
		return zero, false
	}
	maxItem := items[0]
	maxKey := fn(items[0])
	for _, item := range items[1:] {
		key := fn(item)
		if key > maxKey {
			maxKey = key
			maxItem = item
		}
	}
	return maxItem, true
}

// Intersect returns items present in both a and b, preserving order from a.
func Intersect[T comparable](a, b []T) []T {
	set := make(map[T]struct{}, len(b))
	for _, item := range b {
		set[item] = struct{}{}
	}
	result := make([]T, 0)
	for _, item := range a {
		if _, ok := set[item]; ok {
			result = append(result, item)
		}
	}
	return result
}

// Diff returns items in a that are not in b, preserving order from a.
func Diff[T comparable](a, b []T) []T {
	set := make(map[T]struct{}, len(b))
	for _, item := range b {
		set[item] = struct{}{}
	}
	result := make([]T, 0)
	for _, item := range a {
		if _, ok := set[item]; !ok {
			result = append(result, item)
		}
	}
	return result
}

// Shuffle returns a new slice with items in random order.
func Shuffle[T any](items []T) []T {
	result := make([]T, len(items))
	copy(result, items)
	rand.Shuffle(len(result), func(i, j int) {
		result[i], result[j] = result[j], result[i]
	})
	return result
}

// Partition splits items into two slices: the first contains items for which
// fn returns true, the second contains the rest.
func Partition[T any](items []T, fn func(T) bool) ([]T, []T) {
	pass := make([]T, 0)
	fail := make([]T, 0)
	for _, item := range items {
		if fn(item) {
			pass = append(pass, item)
		} else {
			fail = append(fail, item)
		}
	}
	return pass, fail
}

// Zip combines two slices element-wise using fn. The result length equals
// the shorter of the two input slices.
func Zip[T, U, R any](a []T, b []U, fn func(T, U) R) []R {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	result := make([]R, n)
	for i := 0; i < n; i++ {
		result[i] = fn(a[i], b[i])
	}
	return result
}

// Times generates a slice of n items by calling fn with each index (0-based).
func Times[T any](n int, fn func(int) T) []T {
	if n <= 0 {
		return []T{}
	}
	result := make([]T, n)
	for i := 0; i < n; i++ {
		result[i] = fn(i)
	}
	return result
}

// Pop removes the last item from the slice and returns the remaining items,
// the removed item, and a boolean indicating success.
func Pop[T any](items []T) ([]T, T, bool) {
	if len(items) == 0 {
		var zero T
		return []T{}, zero, false
	}
	last := items[len(items)-1]
	result := make([]T, len(items)-1)
	copy(result, items[:len(items)-1])
	return result, last, true
}

// Push appends one or more items to the slice and returns a new slice.
func Push[T any](items []T, item ...T) []T {
	result := make([]T, len(items), len(items)+len(item))
	copy(result, items)
	return append(result, item...)
}

// Tap calls fn with the items for inspection and returns the items unchanged.
func Tap[T any](items []T, fn func([]T)) []T {
	fn(items)
	return items
}

// When applies fn to items if condition is true, otherwise returns items unchanged.
func When[T any](items []T, condition bool, fn func([]T) []T) []T {
	if condition {
		return fn(items)
	}
	return items
}
