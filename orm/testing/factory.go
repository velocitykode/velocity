package testing

import (
	"github.com/brianvoe/gofakeit/v6"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/factory"
)

// Factory is a type alias for factory.Factory.
// Prefer importing "github.com/velocitykode/velocity/orm/factory" directly.
type Factory = factory.Factory

// NewFactory creates a new factory for generating test data.
// The manager parameter is required for Create() (database persistence).
// Pass nil if you only use Make() (in-memory generation).
//
// Prefer importing "github.com/velocitykode/velocity/orm/factory" directly.
func NewFactory(manager *orm.Manager, tableName string, definition func() map[string]interface{}) *Factory {
	return factory.NewFactory(manager, tableName, definition)
}

// Faker returns the global faker instance.
//
// Prefer importing "github.com/velocitykode/velocity/orm/factory" directly.
func Faker() *gofakeit.Faker {
	return factory.Faker()
}

// F is a convenience alias for Faker().
//
// Prefer importing "github.com/velocitykode/velocity/orm/factory" directly.
func F() *gofakeit.Faker {
	return factory.F()
}
