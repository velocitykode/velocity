package bond

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLazy_CreatesLazyProp(t *testing.T) {
	evaluated := false
	lp := Lazy(func() (any, error) {
		evaluated = true
		return "lazy value", nil
	})

	// Should not be evaluated on creation
	if evaluated {
		t.Error("lazy prop should not be evaluated on creation")
	}

	// Verify it's the correct type
	if _, ok := any(lp).(LazyProp); !ok {
		t.Error("Lazy() should return LazyProp")
	}
}

func TestLazyProp_Evaluate(t *testing.T) {
	lp := Lazy(func() (any, error) {
		return "lazy value", nil
	})

	val, err := lp.Evaluate()
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}

	if val != "lazy value" {
		t.Errorf("expected 'lazy value', got %v", val)
	}
}

func TestLazyProp_Evaluate_Error(t *testing.T) {
	expectedErr := errors.New("evaluation failed")
	lp := Lazy(func() (any, error) {
		return nil, expectedErr
	})

	_, err := lp.Evaluate()
	if err != expectedErr {
		t.Errorf("expected error '%v', got '%v'", expectedErr, err)
	}
}

func TestLazyProp_EvaluatedOnlyWhenCalled(t *testing.T) {
	callCount := 0
	lp := Lazy(func() (any, error) {
		callCount++
		return callCount, nil
	})

	// Not called yet
	if callCount != 0 {
		t.Error("should not be called before Evaluate()")
	}

	// First call
	val1, _ := lp.Evaluate()
	if val1 != 1 {
		t.Errorf("first call should return 1, got %v", val1)
	}

	// Second call (lazy props are evaluated each time they're requested)
	val2, _ := lp.Evaluate()
	if val2 != 2 {
		t.Errorf("second call should return 2, got %v", val2)
	}
}

func TestDefer_CreatesWithDefaultGroup(t *testing.T) {
	dp := Defer(func() (any, error) {
		return "deferred", nil
	})

	if dp.Group() != "default" {
		t.Errorf("expected default group 'default', got %s", dp.Group())
	}
}

func TestDefer_CreatesWithCustomGroup(t *testing.T) {
	dp := Defer(func() (any, error) {
		return "deferred", nil
	}, "slow")

	if dp.Group() != "slow" {
		t.Errorf("expected group 'slow', got %s", dp.Group())
	}
}

func TestDefer_EmptyGroupUsesDefault(t *testing.T) {
	dp := Defer(func() (any, error) {
		return "deferred", nil
	}, "")

	if dp.Group() != "default" {
		t.Errorf("expected default group when empty string provided, got %s", dp.Group())
	}
}

func TestDeferredProp_Evaluate(t *testing.T) {
	dp := Defer(func() (any, error) {
		return map[string]int{"count": 42}, nil
	})

	val, err := dp.Evaluate()
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}

	m, ok := val.(map[string]int)
	if !ok {
		t.Fatalf("expected map[string]int, got %T", val)
	}
	if m["count"] != 42 {
		t.Errorf("expected count 42, got %d", m["count"])
	}
}

func TestDeferredProp_Evaluate_Error(t *testing.T) {
	expectedErr := errors.New("deferred evaluation failed")
	dp := Defer(func() (any, error) {
		return nil, expectedErr
	})

	_, err := dp.Evaluate()
	if err != expectedErr {
		t.Errorf("expected error '%v', got '%v'", expectedErr, err)
	}
}

func TestDeferredProp_NotEvaluatedOnCreation(t *testing.T) {
	evaluated := false
	_ = Defer(func() (any, error) {
		evaluated = true
		return nil, nil
	})

	if evaluated {
		t.Error("deferred prop should not be evaluated on creation")
	}
}

func TestAlways_CreatesAlwaysProp(t *testing.T) {
	ap := Always("always here")

	if ap.Value() != "always here" {
		t.Errorf("expected 'always here', got %v", ap.Value())
	}
}

func TestAlways_WithComplexValue(t *testing.T) {
	data := map[string]any{
		"user":  "Ali",
		"admin": true,
	}
	ap := Always(data)

	val := ap.Value()
	m, ok := val.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", val)
	}
	if m["user"] != "Ali" {
		t.Errorf("expected user 'Ali', got %v", m["user"])
	}
}

func TestAlways_WithNilValue(t *testing.T) {
	ap := Always(nil)

	if ap.Value() != nil {
		t.Errorf("expected nil, got %v", ap.Value())
	}
}

func TestOptional_CreatesOptionalProp(t *testing.T) {
	evaluated := false
	op := Optional(func() (any, error) {
		evaluated = true
		return "optional value", nil
	})

	// Should not be evaluated on creation
	if evaluated {
		t.Error("optional prop should not be evaluated on creation")
	}

	// Verify it's the correct type
	if _, ok := any(op).(OptionalProp); !ok {
		t.Error("Optional() should return OptionalProp")
	}
}

func TestOptionalProp_Evaluate(t *testing.T) {
	op := Optional(func() (any, error) {
		return "optional value", nil
	})

	val, err := op.Evaluate()
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}

	if val != "optional value" {
		t.Errorf("expected 'optional value', got %v", val)
	}
}

func TestOptionalProp_Evaluate_Error(t *testing.T) {
	expectedErr := errors.New("optional evaluation failed")
	op := Optional(func() (any, error) {
		return nil, expectedErr
	})

	_, err := op.Evaluate()
	if err != expectedErr {
		t.Errorf("expected error '%v', got '%v'", expectedErr, err)
	}
}

func TestProps_IsMap(t *testing.T) {
	props := Props{
		"name":  "Ali",
		"count": 42,
		"items": []string{"a", "b"},
	}

	if props["name"] != "Ali" {
		t.Errorf("expected name 'Ali', got %v", props["name"])
	}
	if props["count"] != 42 {
		t.Errorf("expected count 42, got %v", props["count"])
	}
}

func TestProps_CanContainSpecialTypes(t *testing.T) {
	props := Props{
		"lazy":     Lazy(func() (any, error) { return "lazy", nil }),
		"deferred": Defer(func() (any, error) { return "deferred", nil }),
		"always":   Always("always"),
		"optional": Optional(func() (any, error) { return "optional", nil }),
	}

	if _, ok := props["lazy"].(LazyProp); !ok {
		t.Error("expected lazy to be LazyProp")
	}
	if _, ok := props["deferred"].(DeferredProp); !ok {
		t.Error("expected deferred to be DeferredProp")
	}
	if _, ok := props["always"].(AlwaysProp); !ok {
		t.Error("expected always to be AlwaysProp")
	}
	if _, ok := props["optional"].(OptionalProp); !ok {
		t.Error("expected optional to be OptionalProp")
	}
}

func TestSharedPropFunc_Type(t *testing.T) {
	var fn SharedPropFunc = func(r *http.Request) (any, error) {
		return r.URL.Path, nil
	}

	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	val, err := fn(r)
	if err != nil {
		t.Fatalf("SharedPropFunc failed: %v", err)
	}

	if val != "/test" {
		t.Errorf("expected '/test', got %v", val)
	}
}

func TestSharedPropFunc_WithRequestData(t *testing.T) {
	var fn SharedPropFunc = func(r *http.Request) (any, error) {
		return map[string]any{
			"method": r.Method,
			"path":   r.URL.Path,
			"query":  r.URL.RawQuery,
		}, nil
	}

	r := httptest.NewRequest(http.MethodPost, "/submit?id=123", nil)
	val, err := fn(r)
	if err != nil {
		t.Fatalf("SharedPropFunc failed: %v", err)
	}

	m := val.(map[string]any)
	if m["method"] != http.MethodPost {
		t.Errorf("expected method POST, got %v", m["method"])
	}
	if m["path"] != "/submit" {
		t.Errorf("expected path '/submit', got %v", m["path"])
	}
	if m["query"] != "id=123" {
		t.Errorf("expected query 'id=123', got %v", m["query"])
	}
}
