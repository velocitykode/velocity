package resource

import "reflect"

// When returns a key-value pair that should be included in the resource
// only when the condition is true. The third return value indicates inclusion.
func When(condition bool, key string, value any) (string, any, bool) {
	return key, value, condition
}

// WhenNotNil returns a key-value pair that should be included in the resource
// only when the value is not nil (including typed nils like (*string)(nil)).
// The third return value indicates inclusion.
func WhenNotNil(key string, value any) (string, any, bool) {
	return key, value, !isNil(value)
}

// isNil checks whether value is nil, including typed nils (e.g. (*string)(nil)).
func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		return v.IsNil()
	}
	return false
}

// WhenFunc returns a key-value pair that should be included in the resource
// only when the condition is true. The value is computed lazily via fn,
// which is only called when the condition is true.
func WhenFunc(condition bool, key string, fn func() any) (string, any, bool) {
	if !condition {
		return key, nil, false
	}
	return key, fn(), true
}

// Merge adds conditional fields to a base map. Each conditional function
// receives the map and may add keys to it.
func Merge(base map[string]any, conditionals ...func(m map[string]any)) {
	for _, fn := range conditionals {
		fn(base)
	}
}
