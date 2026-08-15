package factory

import (
	"math/rand"
	"sync"

	"github.com/brianvoe/gofakeit/v6"
)

var (
	fakerMu       sync.RWMutex
	fakerInstance *gofakeit.Faker
)

// lockedSource makes a math/rand source safe for concurrent use. Unlike
// gofakeit.New, it never special-cases a seed of 0, so every seed value
// produces a deterministic stream.
type lockedSource struct {
	mu  sync.Mutex
	src rand.Source64
}

func (s *lockedSource) Int63() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.src.Int63()
}

func (s *lockedSource) Uint64() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.src.Uint64()
}

func (s *lockedSource) Seed(seed int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.src.Seed(seed)
}

// NewFaker returns a faker seeded with seed for reproducible data. Every
// seed value, including 0, is deterministic. Safe for concurrent use.
func NewFaker(seed int64) *gofakeit.Faker {
	return gofakeit.NewCustom(&lockedSource{src: rand.NewSource(seed).(rand.Source64)})
}

// SetSeed replaces the global faker with a deterministic one seeded with
// seed, so subsequent Faker()/F() calls produce a reproducible stream.
// Call it before generating data; references obtained from Faker() earlier
// keep their previous stream.
func SetSeed(seed int64) {
	fakerMu.Lock()
	fakerInstance = NewFaker(seed)
	fakerMu.Unlock()
}

// Faker returns the global faker instance. It is randomly seeded (from
// crypto/rand), so output differs between runs; call SetSeed for
// reproducible data, or NewFaker for an independent seeded instance.
func Faker() *gofakeit.Faker {
	fakerMu.RLock()
	f := fakerInstance
	fakerMu.RUnlock()
	if f != nil {
		return f
	}

	fakerMu.Lock()
	defer fakerMu.Unlock()
	if fakerInstance == nil {
		fakerInstance = gofakeit.New(0)
	}
	return fakerInstance
}

// F is a convenience alias for Faker()
func F() *gofakeit.Faker {
	return Faker()
}
