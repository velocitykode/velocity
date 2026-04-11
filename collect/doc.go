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
