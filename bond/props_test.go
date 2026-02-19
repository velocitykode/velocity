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
	if _, ok := any(op).(*OptionalProp); !ok {
		t.Error("Optional() should return *OptionalProp")
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
	if _, ok := props["deferred"].(*DeferredProp); !ok {
		t.Error("expected deferred to be *DeferredProp")
	}
	if _, ok := props["always"].(AlwaysProp); !ok {
		t.Error("expected always to be AlwaysProp")
	}
	if _, ok := props["optional"].(*OptionalProp); !ok {
		t.Error("expected optional to be *OptionalProp")
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

// --- DeferredProp builder tests ---

func TestDeferredProp_Merge(t *testing.T) {
	dp := Defer(func() (any, error) { return nil, nil }).Merge()

	if !dp.ShouldMerge() {
		t.Error("expected ShouldMerge to be true")
	}
	if dp.ShouldPrepend() {
		t.Error("expected ShouldPrepend to be false")
	}
}

func TestDeferredProp_DeepMerge(t *testing.T) {
	dp := Defer(func() (any, error) { return nil, nil }).DeepMerge()

	if !dp.ShouldMerge() {
		t.Error("expected ShouldMerge to be true")
	}
	if !dp.ShouldDeepMerge() {
		t.Error("expected ShouldDeepMerge to be true")
	}
}

func TestDeferredProp_Prepend(t *testing.T) {
	dp := Defer(func() (any, error) { return nil, nil }).Prepend("items")

	if !dp.ShouldMerge() {
		t.Error("expected ShouldMerge to be true")
	}
	if !dp.ShouldPrepend() {
		t.Error("expected ShouldPrepend to be true")
	}
	if len(dp.PrependPaths()) != 1 || dp.PrependPaths()[0] != "items" {
		t.Errorf("expected PrependPaths [items], got %v", dp.PrependPaths())
	}
}

func TestDeferredProp_Append(t *testing.T) {
	dp := Defer(func() (any, error) { return nil, nil }).Append("data")

	if !dp.ShouldMerge() {
		t.Error("expected ShouldMerge to be true")
	}
	if len(dp.AppendPaths()) != 1 || dp.AppendPaths()[0] != "data" {
		t.Errorf("expected AppendPaths [data], got %v", dp.AppendPaths())
	}
}

func TestDeferredProp_MatchOn(t *testing.T) {
	dp := Defer(func() (any, error) { return nil, nil }).MatchOn("id")

	if len(dp.MatchesOn()) != 1 || dp.MatchesOn()[0] != "id" {
		t.Errorf("expected MatchesOn [id], got %v", dp.MatchesOn())
	}
}

func TestDeferredProp_Once(t *testing.T) {
	dp := Defer(func() (any, error) { return nil, nil }).Once()

	if !dp.IsOnce() {
		t.Error("expected IsOnce to be true")
	}
}

func TestDeferredProp_Chaining(t *testing.T) {
	dp := Defer(func() (any, error) { return nil, nil }).
		Merge().
		Append("items").
		MatchOn("id").
		Once()

	if !dp.ShouldMerge() {
		t.Error("expected ShouldMerge")
	}
	if !dp.IsOnce() {
		t.Error("expected IsOnce")
	}
	if len(dp.AppendPaths()) != 1 {
		t.Error("expected 1 append path")
	}
	if len(dp.MatchesOn()) != 1 {
		t.Error("expected 1 match key")
	}
}

// --- OptionalProp builder tests ---

func TestOptionalProp_Once(t *testing.T) {
	op := Optional(func() (any, error) { return nil, nil }).Once()

	if !op.IsOnce() {
		t.Error("expected IsOnce to be true")
	}
}

func TestOptionalProp_As(t *testing.T) {
	op := Optional(func() (any, error) { return nil, nil }).As("custom-key")

	if op.OnceKey() != "custom-key" {
		t.Errorf("expected OnceKey 'custom-key', got %s", op.OnceKey())
	}
}

func TestOptionalProp_OnceAs(t *testing.T) {
	op := Optional(func() (any, error) { return nil, nil }).Once().As("custom")

	if !op.IsOnce() {
		t.Error("expected IsOnce")
	}
	if op.OnceKey() != "custom" {
		t.Errorf("expected OnceKey 'custom', got %s", op.OnceKey())
	}
}

// --- MergeProp tests ---

func TestMerge_CreatesWithStaticValue(t *testing.T) {
	mp := Merge([]string{"a", "b"})

	val, err := mp.Evaluate()
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}

	items, ok := val.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", val)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
}

func TestMergeFunc_CreatesWithFunction(t *testing.T) {
	evaluated := false
	mp := MergeFunc(func() (any, error) {
		evaluated = true
		return "dynamic", nil
	})

	if evaluated {
		t.Error("should not be evaluated on creation")
	}

	val, err := mp.Evaluate()
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if val != "dynamic" {
		t.Errorf("expected 'dynamic', got %v", val)
	}
	if !evaluated {
		t.Error("should be evaluated after Evaluate()")
	}
}

func TestMergeProp_ShouldMerge(t *testing.T) {
	mp := Merge("value")

	if !mp.ShouldMerge() {
		t.Error("expected ShouldMerge to be true")
	}
}

func TestMergeProp_DeepMerge(t *testing.T) {
	mp := Merge("value").DeepMerge()

	if !mp.ShouldDeepMerge() {
		t.Error("expected ShouldDeepMerge to be true")
	}
}

func TestMergeProp_Prepend(t *testing.T) {
	mp := Merge("value").Prepend("items")

	if !mp.ShouldPrepend() {
		t.Error("expected ShouldPrepend to be true")
	}
	if len(mp.PrependPaths()) != 1 || mp.PrependPaths()[0] != "items" {
		t.Errorf("expected PrependPaths [items], got %v", mp.PrependPaths())
	}
}

func TestMergeProp_Append(t *testing.T) {
	mp := Merge("value").Append("data")

	if len(mp.AppendPaths()) != 1 || mp.AppendPaths()[0] != "data" {
		t.Errorf("expected AppendPaths [data], got %v", mp.AppendPaths())
	}
}

func TestMergeProp_MatchOn(t *testing.T) {
	mp := Merge("value").MatchOn("id", "slug")

	if len(mp.MatchesOn()) != 2 {
		t.Errorf("expected 2 match keys, got %d", len(mp.MatchesOn()))
	}
}

func TestMergeProp_Once(t *testing.T) {
	mp := Merge("value").Once()

	if !mp.IsOnce() {
		t.Error("expected IsOnce to be true")
	}
}

func TestMergeProp_As(t *testing.T) {
	mp := Merge("value").As("custom")

	if mp.OnceKey() != "custom" {
		t.Errorf("expected OnceKey 'custom', got %s", mp.OnceKey())
	}
}

func TestMergeProp_Chaining(t *testing.T) {
	mp := Merge([]int{1, 2, 3}).
		DeepMerge().
		Append("items").
		MatchOn("id").
		Once().
		As("my-merge")

	if !mp.ShouldMerge() {
		t.Error("expected ShouldMerge")
	}
	if !mp.ShouldDeepMerge() {
		t.Error("expected ShouldDeepMerge")
	}
	if !mp.IsOnce() {
		t.Error("expected IsOnce")
	}
	if mp.OnceKey() != "my-merge" {
		t.Errorf("expected OnceKey 'my-merge', got %s", mp.OnceKey())
	}
}

func TestMergeProp_Evaluate_Error(t *testing.T) {
	expectedErr := errors.New("merge failed")
	mp := MergeFunc(func() (any, error) {
		return nil, expectedErr
	})

	_, err := mp.Evaluate()
	if err != expectedErr {
		t.Errorf("expected error '%v', got '%v'", expectedErr, err)
	}
}

// --- OnceProp tests ---

func TestOnce_Creates(t *testing.T) {
	evaluated := false
	op := Once(func() (any, error) {
		evaluated = true
		return "once value", nil
	})

	if evaluated {
		t.Error("should not be evaluated on creation")
	}

	val, err := op.Evaluate()
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if val != "once value" {
		t.Errorf("expected 'once value', got %v", val)
	}
}

func TestOnceProp_As(t *testing.T) {
	op := Once(func() (any, error) { return nil, nil }).As("custom-key")

	if op.OnceKey() != "custom-key" {
		t.Errorf("expected OnceKey 'custom-key', got %s", op.OnceKey())
	}
}

func TestOnceProp_DefaultKey(t *testing.T) {
	op := Once(func() (any, error) { return nil, nil })

	if op.OnceKey() != "" {
		t.Errorf("expected empty default OnceKey, got %s", op.OnceKey())
	}
}

func TestOnceProp_Evaluate_Error(t *testing.T) {
	expectedErr := errors.New("once failed")
	op := Once(func() (any, error) {
		return nil, expectedErr
	})

	_, err := op.Evaluate()
	if err != expectedErr {
		t.Errorf("expected error '%v', got '%v'", expectedErr, err)
	}
}

// --- ScrollProp tests ---

func TestScroll_CreatesWithStaticValue(t *testing.T) {
	sp := Scroll([]int{1, 2, 3}, "data")

	val, err := sp.Evaluate()
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}

	items, ok := val.([]int)
	if !ok {
		t.Fatalf("expected []int, got %T", val)
	}
	if len(items) != 3 {
		t.Errorf("expected 3 items, got %d", len(items))
	}
	if sp.Wrapper() != "data" {
		t.Errorf("expected wrapper 'data', got %s", sp.Wrapper())
	}
}

func TestScrollFunc_CreatesWithFunction(t *testing.T) {
	evaluated := false
	sp := ScrollFunc(func() (any, error) {
		evaluated = true
		return "scroll data", nil
	}, "items")

	if evaluated {
		t.Error("should not be evaluated on creation")
	}

	val, err := sp.Evaluate()
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if val != "scroll data" {
		t.Errorf("expected 'scroll data', got %v", val)
	}
}

func TestScrollProp_Defer(t *testing.T) {
	sp := Scroll("data", "items").Defer()

	if !sp.IsDeferred() {
		t.Error("expected IsDeferred to be true")
	}
	if sp.Group() != "default" {
		t.Errorf("expected default group, got %s", sp.Group())
	}
}

func TestScrollProp_DeferWithGroup(t *testing.T) {
	sp := Scroll("data", "items").Defer("slow")

	if !sp.IsDeferred() {
		t.Error("expected IsDeferred to be true")
	}
	if sp.Group() != "slow" {
		t.Errorf("expected group 'slow', got %s", sp.Group())
	}
}

func TestScrollProp_PrependData(t *testing.T) {
	sp := Scroll("data", "items").PrependData("rows")

	if !sp.ShouldPrepend() {
		t.Error("expected ShouldPrepend to be true")
	}
}

func TestScrollProp_WithMetadata(t *testing.T) {
	sp := Scroll("data", "items").WithMetadata(func() ScrollMeta {
		return ScrollMeta{
			PageName:    "page",
			CurrentPage: 2,
			NextPage:    3,
		}
	})

	meta := sp.Metadata()
	if meta == nil {
		t.Fatal("expected metadata to be set")
	}
	if meta.PageName != "page" {
		t.Errorf("expected PageName 'page', got %s", meta.PageName)
	}
	if meta.CurrentPage != 2 {
		t.Errorf("expected CurrentPage 2, got %v", meta.CurrentPage)
	}
}

func TestScrollProp_MetadataDefaultNil(t *testing.T) {
	sp := Scroll("data", "items")

	if sp.Metadata() != nil {
		t.Error("expected nil metadata by default")
	}
}

func TestScrollProp_Evaluate_Error(t *testing.T) {
	expectedErr := errors.New("scroll failed")
	sp := ScrollFunc(func() (any, error) {
		return nil, expectedErr
	}, "items")

	_, err := sp.Evaluate()
	if err != expectedErr {
		t.Errorf("expected error '%v', got '%v'", expectedErr, err)
	}
}

// --- Props map with new types ---

func TestProps_CanContainNewTypes(t *testing.T) {
	props := Props{
		"merge":  Merge([]int{1, 2}),
		"once":   Once(func() (any, error) { return "once", nil }),
		"scroll": Scroll([]int{1}, "data"),
	}

	if _, ok := props["merge"].(*MergeProp); !ok {
		t.Error("expected merge to be *MergeProp")
	}
	if _, ok := props["once"].(*OnceProp); !ok {
		t.Error("expected once to be *OnceProp")
	}
	if _, ok := props["scroll"].(*ScrollProp); !ok {
		t.Error("expected scroll to be *ScrollProp")
	}
}
