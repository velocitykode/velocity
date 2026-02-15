package testing

import (
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/factory"
)

// ModelFactory is a type alias for factory.ModelFactory.
// Prefer importing "github.com/velocitykode/velocity/orm/factory" directly.
type ModelFactory[T any] = factory.ModelFactory[T]

// NewModelFactory creates a new type-safe model factory.
//
// Prefer importing "github.com/velocitykode/velocity/orm/factory" directly.
func NewModelFactory[T any](manager *orm.Manager, definition func() *T) *ModelFactory[T] {
	return factory.NewModelFactory[T](manager, definition)
}
