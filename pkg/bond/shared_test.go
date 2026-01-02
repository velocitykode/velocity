package bond

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setupBond(t *testing.T) *Bond {
	t.Helper()
	b, err := New(Config{
		RootTemplate: validTemplate,
		Version:      "1.0.0",
	})
	if err != nil {
		t.Fatalf("failed to create Bond: %v", err)
	}
	return b
}

func TestShare_AddsStaticProp(t *testing.T) {
	b := setupBond(t)

	b.Share("appName", "Velocity")

	if b.sharedProps["appName"] != "Velocity" {
		t.Errorf("expected appName 'Velocity', got %v", b.sharedProps["appName"])
	}
}

func TestShare_OverwritesExisting(t *testing.T) {
	b := setupBond(t)

	b.Share("key", "value1")
	b.Share("key", "value2")

	if b.sharedProps["key"] != "value2" {
		t.Errorf("expected key 'value2', got %v", b.sharedProps["key"])
	}
}

func TestShareFunc_AddsDynamicProp(t *testing.T) {
	b := setupBond(t)

	b.ShareFunc("path", func(r *http.Request) (any, error) {
		return r.URL.Path, nil
	})

	if b.sharedFuncs["path"] == nil {
		t.Error("expected path function to be set")
	}
}

func TestShareMultiple_AddsMultipleProps(t *testing.T) {
	b := setupBond(t)

	b.ShareMultiple(Props{
		"app":     "Velocity",
		"version": "1.0",
		"debug":   true,
	})

	if b.sharedProps["app"] != "Velocity" {
		t.Errorf("expected app 'Velocity', got %v", b.sharedProps["app"])
	}
	if b.sharedProps["version"] != "1.0" {
		t.Errorf("expected version '1.0', got %v", b.sharedProps["version"])
	}
	if b.sharedProps["debug"] != true {
		t.Errorf("expected debug true, got %v", b.sharedProps["debug"])
	}
}

func TestClearShared_RemovesAllProps(t *testing.T) {
	b := setupBond(t)

	b.Share("key1", "value1")
	b.ShareFunc("key2", func(r *http.Request) (any, error) {
		return "value2", nil
	})

	b.ClearShared()

	if len(b.sharedProps) != 0 {
		t.Errorf("expected empty sharedProps, got %d items", len(b.sharedProps))
	}
	if len(b.sharedFuncs) != 0 {
		t.Errorf("expected empty sharedFuncs, got %d items", len(b.sharedFuncs))
	}
}

func TestMergeSharedProps_MergesStaticProps(t *testing.T) {
	b := setupBond(t)

	b.Share("shared", "sharedValue")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	componentProps := Props{"component": "componentValue"}

	merged := b.mergeSharedProps(r, componentProps)

	if merged["shared"] != "sharedValue" {
		t.Errorf("expected shared 'sharedValue', got %v", merged["shared"])
	}
	if merged["component"] != "componentValue" {
		t.Errorf("expected component 'componentValue', got %v", merged["component"])
	}
}

func TestMergeSharedProps_EvaluatesDynamicProps(t *testing.T) {
	b := setupBond(t)

	b.ShareFunc("path", func(r *http.Request) (any, error) {
		return r.URL.Path, nil
	})

	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	merged := b.mergeSharedProps(r, Props{})

	if merged["path"] != "/test" {
		t.Errorf("expected path '/test', got %v", merged["path"])
	}
}

func TestMergeSharedProps_ComponentOverridesShared(t *testing.T) {
	b := setupBond(t)

	b.Share("key", "sharedValue")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	componentProps := Props{"key": "componentValue"}

	merged := b.mergeSharedProps(r, componentProps)

	if merged["key"] != "componentValue" {
		t.Errorf("expected component to override shared, got %v", merged["key"])
	}
}

func TestMergeSharedProps_DynamicOverridesStatic(t *testing.T) {
	b := setupBond(t)

	b.Share("key", "staticValue")
	b.ShareFunc("key", func(r *http.Request) (any, error) {
		return "dynamicValue", nil
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	merged := b.mergeSharedProps(r, Props{})

	if merged["key"] != "dynamicValue" {
		t.Errorf("expected dynamic to override static, got %v", merged["key"])
	}
}

func TestMergeSharedProps_IgnoresDynamicErrors(t *testing.T) {
	b := setupBond(t)

	b.ShareFunc("failing", func(r *http.Request) (any, error) {
		return nil, errors.New("evaluation failed")
	})
	b.ShareFunc("working", func(r *http.Request) (any, error) {
		return "works", nil
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	merged := b.mergeSharedProps(r, Props{})

	// Failing func should not add key
	if _, ok := merged["failing"]; ok {
		t.Error("expected failing func to not add key")
	}

	// Working func should still work
	if merged["working"] != "works" {
		t.Errorf("expected working 'works', got %v", merged["working"])
	}
}

func TestMergeSharedProps_EmptyComponentProps(t *testing.T) {
	b := setupBond(t)

	b.Share("shared", "value")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	merged := b.mergeSharedProps(r, Props{})

	if merged["shared"] != "value" {
		t.Errorf("expected shared 'value', got %v", merged["shared"])
	}
}

func TestMergeSharedProps_NilComponentProps(t *testing.T) {
	b := setupBond(t)

	b.Share("shared", "value")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	merged := b.mergeSharedProps(r, nil)

	if merged["shared"] != "value" {
		t.Errorf("expected shared 'value', got %v", merged["shared"])
	}
}

func TestMergeSharedProps_ThreadSafe(t *testing.T) {
	b := setupBond(t)

	// Run concurrent reads and writes
	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			b.Share("counter", i)
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			_ = b.mergeSharedProps(r, Props{})
		}
		done <- true
	}()

	<-done
	<-done
}

func TestShare_InitializesNilMap(t *testing.T) {
	b := &Bond{}

	b.Share("key", "value")

	if b.sharedProps["key"] != "value" {
		t.Errorf("expected key 'value', got %v", b.sharedProps["key"])
	}
}

func TestShareFunc_InitializesNilMap(t *testing.T) {
	b := &Bond{}

	b.ShareFunc("key", func(r *http.Request) (any, error) {
		return "value", nil
	})

	if b.sharedFuncs["key"] == nil {
		t.Error("expected key function to be set")
	}
}

func TestShareMultiple_InitializesNilMap(t *testing.T) {
	b := &Bond{}

	b.ShareMultiple(Props{"key": "value"})

	if b.sharedProps["key"] != "value" {
		t.Errorf("expected key 'value', got %v", b.sharedProps["key"])
	}
}
