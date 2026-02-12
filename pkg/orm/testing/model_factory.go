package testing

import (
	"reflect"
	"sync"

	"github.com/velocitykode/velocity/pkg/orm"
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
	mu          sync.Mutex
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

// Count sets the number of records to generate
func (f *ModelFactory[T]) Count(n int) *ModelFactory[T] {
	if n <= 0 {
		panic("count must be greater than 0")
	}
	f.mu.Lock()
	f.count = n
	f.mu.Unlock()
	return f
}

// State applies a named state modifier to the factory
func (f *ModelFactory[T]) State(name string) *ModelFactory[T] {
	if _, exists := f.states[name]; !exists {
		panic("unknown state: " + name)
	}
	f.mu.Lock()
	f.activeState = name
	f.mu.Unlock()
	return f
}

// DefineState registers a named state modifier
func (f *ModelFactory[T]) DefineState(name string, modifier func(*T)) *ModelFactory[T] {
	f.states[name] = modifier
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

// Create generates and persists model(s) to database
// Returns *T for single, []*T for multiple (when Count > 1)
func (f *ModelFactory[T]) Create(overrides *T) any {
	f.mu.Lock()
	count := f.count
	activeState := f.activeState
	f.count = 1
	f.activeState = ""
	f.mu.Unlock()

	if count == 1 {
		return f.createOne(activeState, overrides)
	}

	results := make([]*T, 0, count)
	for i := 0; i < count; i++ {
		results = append(results, f.createOne(activeState, overrides))
	}
	return results
}

// CreateOne is a convenience method that always returns *T (not any)
func (f *ModelFactory[T]) CreateOne(overrides *T) *T {
	f.mu.Lock()
	activeState := f.activeState
	f.count = 1
	f.activeState = ""
	f.mu.Unlock()

	return f.createOne(activeState, overrides)
}

// CreateMany is a convenience method that always returns []*T (not any)
func (f *ModelFactory[T]) CreateMany(count int, overrides *T) []*T {
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
		results = append(results, f.createOne(activeState, overrides))
	}
	return results
}

// makeOne generates a single model without persisting
func (f *ModelFactory[T]) makeOne(activeState string, overrides *T) *T {
	model := f.definition()

	// Apply state modifier if set
	if activeState != "" {
		if modifier, exists := f.states[activeState]; exists {
			modifier(model)
		}
	}

	// Apply overrides
	if overrides != nil {
		mergeNonZero(model, overrides)
	}

	return model
}

// createOne generates and persists a single model
func (f *ModelFactory[T]) createOne(activeState string, overrides *T) *T {
	model := f.makeOne(activeState, overrides)

	if err := orm.Save(f.manager, model); err != nil {
		panic("failed to create model: " + err.Error())
	}

	return model
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

// isZeroValue checks if a reflect.Value is the zero value for its type
func isZeroValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		return v.IsNil()
	case reflect.Struct:
		// For structs, check if all fields are zero
		// But skip embedded orm.Model since it has non-zero timestamps
		t := v.Type()
		if t.Name() != "" && t.PkgPath() != "" {
			// Named struct from a package - check if it looks like orm.Model
			if t.Name() == "Model" || (t.NumField() > 0 && t.Field(0).Name == "ID") {
				return true // Treat as zero to avoid overwriting base model
			}
		}
		return reflect.DeepEqual(v.Interface(), reflect.Zero(v.Type()).Interface())
	default:
		return reflect.DeepEqual(v.Interface(), reflect.Zero(v.Type()).Interface())
	}
}
