package factory

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/velocitykode/velocity/orm"
)

// ModelFactory is a type-safe factory for creating test models
// Usage:
//
//	func (User) Factory(m *orm.Manager) *ModelFactory[User] {
//	    return NewModelFactory[User](m, func() *User {
//	        return &User{Name: Faker().Name(), Email: Faker().Email()}
//	    })
//	}
//
//	// In tests:
//	user := models.User{}.Factory().Create(nil)
//	admin := models.User{}.Factory().Create(&models.User{Role: "admin"})
//	users := models.User{}.Factory().Count(3).Create(nil)
type ModelFactory[T any] struct {
	manager     *orm.Manager
	definition  func() *T
	states      map[string]func(*T)
	count       int
	activeState string
	// mu is a sync.RWMutex so makeOne can snapshot the active state
	// modifier via RLock while concurrent DefineState writers take Lock.
	// Cross-cutting map mutex sweep, rule #3: previously State()'s
	// presence-check and makeOne()'s state read ran outside the lock,
	// racing the DefineState assignment.
	mu sync.RWMutex
}

// NewModelFactory creates a new type-safe model factory
func NewModelFactory[T any](manager *orm.Manager, definition func() *T) *ModelFactory[T] {
	return &ModelFactory[T]{
		manager:    manager,
		definition: definition,
		states:     make(map[string]func(*T)),
		count:      1,
	}
}

// Count sets the number of records to generate.
// Panics if n <= 0 (programming error, caught at setup time).
func (f *ModelFactory[T]) Count(n int) *ModelFactory[T] {
	if n <= 0 {
		panic("count must be greater than 0")
	}
	f.mu.Lock()
	f.count = n
	f.mu.Unlock()
	return f
}

// State applies a named state modifier to the factory.
// Panics if the state has not been defined via DefineState (programming error).
//
// The presence-check and the activeState write share the same critical
// section so a concurrent DefineState cannot race with the read.
// Previously the presence-check ran without a lock; combined with the
// lock-held write in DefineState this fired "concurrent map read and
// map write" under -race.
func (f *ModelFactory[T]) State(name string) *ModelFactory[T] {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.states[name]; !exists {
		panic("unknown state: " + name)
	}
	f.activeState = name
	return f
}

// DefineState registers a named state modifier
func (f *ModelFactory[T]) DefineState(name string, modifier func(*T)) *ModelFactory[T] {
	f.mu.Lock()
	f.states[name] = modifier
	f.mu.Unlock()
	return f
}

// Make generates model(s) without persisting to database
// Returns *T for single, []*T for multiple (when Count > 1)
func (f *ModelFactory[T]) Make(overrides *T) any {
	f.mu.Lock()
	count := f.count
	activeState := f.activeState
	f.count = 1
	f.activeState = ""
	f.mu.Unlock()

	if count == 1 {
		return f.makeOne(activeState, overrides)
	}

	results := make([]*T, 0, count)
	for i := 0; i < count; i++ {
		results = append(results, f.makeOne(activeState, overrides))
	}
	return results
}

// Create generates and persists model(s) to database. Takes ctx as the
// first argument so writes participate in the caller's transaction
// when ctx carries a *sql.Tx. Returns *T for single, []*T for multiple
// (when Count > 1). Returns an error if database persistence fails.
func (f *ModelFactory[T]) Create(ctx context.Context, overrides *T) (any, error) {
	f.mu.Lock()
	count := f.count
	activeState := f.activeState
	f.count = 1
	f.activeState = ""
	f.mu.Unlock()

	if count == 1 {
		model, err := f.createOne(ctx, activeState, overrides)
		if err != nil {
			return nil, err
		}
		return model, nil
	}

	results := make([]*T, 0, count)
	for i := 0; i < count; i++ {
		model, err := f.createOne(ctx, activeState, overrides)
		if err != nil {
			return results, err
		}
		results = append(results, model)
	}
	return results, nil
}

// CreateOne is a convenience method that always returns *T (not any).
// Takes ctx as the first argument. Returns an error if database
// persistence fails.
func (f *ModelFactory[T]) CreateOne(ctx context.Context, overrides *T) (*T, error) {
	f.mu.Lock()
	activeState := f.activeState
	f.count = 1
	f.activeState = ""
	f.mu.Unlock()

	return f.createOne(ctx, activeState, overrides)
}

// CreateMany is a convenience method that always returns []*T (not any).
// Takes ctx as the first argument. Returns an error if database
// persistence fails.
func (f *ModelFactory[T]) CreateMany(ctx context.Context, count int, overrides *T) ([]*T, error) {
	if count <= 0 {
		panic("count must be greater than 0")
	}

	f.mu.Lock()
	activeState := f.activeState
	f.activeState = ""
	f.count = 1
	f.mu.Unlock()

	results := make([]*T, 0, count)
	for i := 0; i < count; i++ {
		model, err := f.createOne(ctx, activeState, overrides)
		if err != nil {
			return results, err
		}
		results = append(results, model)
	}
	return results, nil
}

// MakeOne is a convenience method that always returns *T (not any)
func (f *ModelFactory[T]) MakeOne(overrides *T) *T {
	f.mu.Lock()
	activeState := f.activeState
	f.count = 1
	f.activeState = ""
	f.mu.Unlock()

	return f.makeOne(activeState, overrides)
}

// makeOne generates a single model without persisting.
//
// The state lookup runs under f.mu.RLock so a concurrent DefineState
// cannot race the map read. The modifier closure itself fires outside
// the lock so user code (which may take its own locks) cannot deadlock
// against the factory's mutex.
func (f *ModelFactory[T]) makeOne(activeState string, overrides *T) *T {
	model := f.definition()

	// Apply state modifier if set. Snapshot the modifier under RLock
	// so map iteration does not race a concurrent DefineState write.
	if activeState != "" {
		f.mu.RLock()
		modifier, exists := f.states[activeState]
		f.mu.RUnlock()
		if exists {
			modifier(model)
		}
	}

	// Apply overrides
	if overrides != nil {
		mergeNonZero(model, overrides)
	}

	return model
}

// createOne generates and persists a single model. Threads ctx
// through orm.Save so a caller-supplied *sql.Tx in ctx enrolls the
// insert in the surrounding transaction.
func (f *ModelFactory[T]) createOne(ctx context.Context, activeState string, overrides *T) (*T, error) {
	model := f.makeOne(activeState, overrides)

	if err := orm.Save(ctx, f.manager, model); err != nil {
		return nil, fmt.Errorf("factory: failed to create model: %w", err)
	}

	return model, nil
}

// mergeNonZero copies non-zero values from src to dst
func mergeNonZero[T any](dst, src *T) {
	dstVal := reflect.ValueOf(dst).Elem()
	srcVal := reflect.ValueOf(src).Elem()

	for i := 0; i < srcVal.NumField(); i++ {
		srcField := srcVal.Field(i)
		dstField := dstVal.Field(i)

		if !dstField.CanSet() {
			continue
		}

		// Skip zero values
		if isZeroValue(srcField) {
			continue
		}

		dstField.Set(srcField)
	}
}

// ormPkgPath is the import path of the velocity orm package,
// used to identify embedded orm.Model structs in isZeroValue.
const ormPkgPath = "github.com/velocitykode/velocity/orm"

// isZeroValue checks if a reflect.Value is the zero value for its type
func isZeroValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		return v.IsNil()
	case reflect.Struct:
		// Skip embedded orm.Model types to avoid overwriting base model fields
		t := v.Type()
		if t.PkgPath() == ormPkgPath {
			return true
		}
		return reflect.DeepEqual(v.Interface(), reflect.Zero(v.Type()).Interface())
	default:
		return reflect.DeepEqual(v.Interface(), reflect.Zero(v.Type()).Interface())
	}
}
