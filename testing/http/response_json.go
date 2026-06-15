package http

import "reflect"

// AssertJSONFragment asserts that the decoded JSON response contains the given
// subset. Extra keys in the response are allowed; only the keys present in
// subset must exist and match. Nested objects recurse, and arrays match
// positionally (the response array may be longer than the expected one).
func (r *TestResponse) AssertJSONFragment(subset map[string]any) *TestResponse {
	r.t.Helper()
	decoded := r.decodeJSON()
	if decoded == nil {
		return r
	}
	if !jsonFragmentMatch(subset, decoded) {
		r.t.Errorf("JSON fragment mismatch: subset %+v not found in %+v", subset, decoded)
	}
	return r
}

// jsonFragmentMatch reports whether actual contains expected as a subset.
// Maps require every expected key to exist in actual and match recursively.
// Slices require actual to be at least as long as expected, with each
// expected element matching the actual element at the same index. Scalars
// reuse jsonEqual for JSON number coercion.
func jsonFragmentMatch(expected, actual any) bool {
	switch e := expected.(type) {
	case map[string]any:
		a, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		for k, ev := range e {
			av, ok := a[k]
			if !ok {
				return false
			}
			if !jsonFragmentMatch(ev, av) {
				return false
			}
		}
		return true
	default:
		// A nil subset value (e.g. JSON null) has no reflect.Kind, so guard
		// it before reflection and compare directly to avoid a panic.
		if expected == nil {
			return jsonEqual(expected, actual)
		}
		// Handle any slice/array kind (e.g. []map[string]any, []string)
		// generically so natural fragment literals recurse element-by-element
		// against the decoded []any. Scalars fall through to jsonEqual.
		ev := reflect.ValueOf(expected)
		if ev.Kind() == reflect.Slice || ev.Kind() == reflect.Array {
			a, ok := actual.([]any)
			if !ok || len(a) < ev.Len() {
				return false
			}
			for i := 0; i < ev.Len(); i++ {
				if !jsonFragmentMatch(ev.Index(i).Interface(), a[i]) {
					return false
				}
			}
			return true
		}
		return jsonEqual(expected, actual)
	}
}
