package orm

// When applies the callback to the query only if the condition is true.
// This enables conditional query building without breaking the chain.
//
// Usage:
//
//	Model[User]{}.Where("active", true).
//	    When(sortByAge, func(q *Query[User]) *Query[User] {
//	        return q.OrderBy("age", "ASC")
//	    }).Get()
func (q *Query[T]) When(condition bool, fn func(*Query[T]) *Query[T]) *Query[T] {
	if condition {
		return fn(q)
	}
	return q
}
