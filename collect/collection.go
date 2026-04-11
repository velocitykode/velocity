package collect

// Collection wraps a slice of T with chainable, immutable operations.
// Every method returns a new Collection without modifying the original.
//
// Methods that would require additional type parameters (Map, Reduce, GroupBy,
// KeyBy, FlatMap, Pluck, Zip) or a comparable constraint (Unique, Intersect,
// Diff) are only available as package-level functions.
type Collection[T any] struct {
	items []T
}

// From creates a new Collection from the given slice. The input is copied
// to prevent aliasing.
func From[T any](items []T) *Collection[T] {
	cp := make([]T, len(items))
	copy(cp, items)
	return &Collection[T]{items: cp}
}

// All returns the underlying slice.
func (c *Collection[T]) All() []T {
	return c.items
}

// Count returns the number of items.
func (c *Collection[T]) Count() int {
	return len(c.items)
}

// IsEmpty returns true if the collection has no items.
func (c *Collection[T]) IsEmpty() bool {
	return len(c.items) == 0
}

// IsNotEmpty returns true if the collection has at least one item.
func (c *Collection[T]) IsNotEmpty() bool {
	return len(c.items) > 0
}

// Filter returns a new Collection containing items for which fn returns true.
func (c *Collection[T]) Filter(fn func(T) bool) *Collection[T] {
	return &Collection[T]{items: Filter(c.items, fn)}
}

// Reject returns a new Collection containing items for which fn returns false.
func (c *Collection[T]) Reject(fn func(T) bool) *Collection[T] {
	return &Collection[T]{items: Reject(c.items, fn)}
}

// Each calls fn for each item and returns the same Collection for chaining.
func (c *Collection[T]) Each(fn func(T)) *Collection[T] {
	Each(c.items, fn)
	return c
}

// Contains returns true if fn returns true for any item.
func (c *Collection[T]) Contains(fn func(T) bool) bool {
	return Contains(c.items, fn)
}

// Every returns true if fn returns true for all items.
func (c *Collection[T]) Every(fn func(T) bool) bool {
	return Every(c.items, fn)
}

// None returns true if fn returns false for all items.
func (c *Collection[T]) None(fn func(T) bool) bool {
	return None(c.items, fn)
}

// First returns the first item for which fn returns true.
func (c *Collection[T]) First(fn func(T) bool) (T, bool) {
	return First(c.items, fn)
}

// Last returns the last item for which fn returns true.
func (c *Collection[T]) Last(fn func(T) bool) (T, bool) {
	return Last(c.items, fn)
}

// FirstWhere returns the first item for which fn returns true.
// It is an alias for [Collection.First], provided for readability.
func (c *Collection[T]) FirstWhere(fn func(T) bool) (T, bool) {
	return First(c.items, fn)
}

// Reverse returns a new Collection with items in reverse order.
func (c *Collection[T]) Reverse() *Collection[T] {
	return &Collection[T]{items: Reverse(c.items)}
}

// Sort returns a new Collection sorted using the given comparison function.
func (c *Collection[T]) Sort(less func(a, b T) bool) *Collection[T] {
	return &Collection[T]{items: Sort(c.items, less)}
}

// Chunk splits items into groups of the given size.
func (c *Collection[T]) Chunk(size int) [][]T {
	return Chunk(c.items, size)
}

// Take returns a new Collection with the first n items.
func (c *Collection[T]) Take(n int) *Collection[T] {
	return &Collection[T]{items: Take(c.items, n)}
}

// Skip returns a new Collection with items after skipping the first n.
func (c *Collection[T]) Skip(n int) *Collection[T] {
	return &Collection[T]{items: Skip(c.items, n)}
}

// Shuffle returns a new Collection with items in random order.
func (c *Collection[T]) Shuffle() *Collection[T] {
	return &Collection[T]{items: Shuffle(c.items)}
}

// Pop removes the last item and returns a new Collection with the remaining
// items, the removed item, and a boolean indicating success.
func (c *Collection[T]) Pop() (*Collection[T], T, bool) {
	remaining, item, ok := Pop(c.items)
	return &Collection[T]{items: remaining}, item, ok
}

// Push returns a new Collection with the given items appended.
func (c *Collection[T]) Push(items ...T) *Collection[T] {
	return &Collection[T]{items: Push(c.items, items...)}
}

// Tap calls fn with the underlying slice for inspection and returns the
// same Collection for chaining.
func (c *Collection[T]) Tap(fn func([]T)) *Collection[T] {
	fn(c.items)
	return c
}

// When applies fn if condition is true, otherwise returns the Collection unchanged.
func (c *Collection[T]) When(condition bool, fn func([]T) []T) *Collection[T] {
	result := When(c.items, condition, fn)
	return &Collection[T]{items: result}
}
