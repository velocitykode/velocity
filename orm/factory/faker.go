package factory

import (
	"sync"

	"github.com/brianvoe/gofakeit/v6"
)

var (
	fakerInstance *gofakeit.Faker
	fakerOnce     sync.Once
)

// Faker returns the global faker instance
func Faker() *gofakeit.Faker {
	fakerOnce.Do(func() {
		fakerInstance = gofakeit.New(0) // Seed with 0 for consistent testing, or use time for randomness
	})
	return fakerInstance
}

// F is a convenience alias for Faker()
func F() *gofakeit.Faker {
	return Faker()
}
