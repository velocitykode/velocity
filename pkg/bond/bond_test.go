package bond

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const validTemplate = `<!DOCTYPE html>
<html>
<head>{{ .inertiaHead }}</head>
<body>{{ .inertia }}</body>
</html>`

func TestNew_ValidConfig(t *testing.T) {
	b, err := New(Config{
		RootTemplate: validTemplate,
		Version:      "1.0.0",
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if b == nil {
		t.Fatal("expected bond instance, got nil")
	}
	if b.Version() != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %s", b.Version())
	}
}

func TestNew_EmptyTemplate_ReturnsError(t *testing.T) {
	_, err := New(Config{
		RootTemplate: "",
	})

	if err != ErrTemplateRequired {
		t.Errorf("expected ErrTemplateRequired, got %v", err)
	}
}

func TestNew_InvalidTemplate_ReturnsError(t *testing.T) {
	_, err := New(Config{
		RootTemplate: "<html><body>no inertia placeholder</body></html>",
	})

	if err != ErrInvalidTemplate {
		t.Errorf("expected ErrInvalidTemplate, got %v", err)
	}
}

func TestNew_DefaultContainerID(t *testing.T) {
	b, err := New(Config{
		RootTemplate: validTemplate,
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if b.ContainerID() != "app" {
		t.Errorf("expected default container ID 'app', got %s", b.ContainerID())
	}
}

func TestNew_CustomContainerID(t *testing.T) {
	b, err := New(Config{
		RootTemplate: validTemplate,
		ContainerID:  "custom-app",
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if b.ContainerID() != "custom-app" {
		t.Errorf("expected container ID 'custom-app', got %s", b.ContainerID())
	}
}

func TestNew_DefaultVersion(t *testing.T) {
	b, err := New(Config{
		RootTemplate: validTemplate,
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if b.Version() != "1" {
		t.Errorf("expected default version '1', got %s", b.Version())
	}
}

func TestNew_EncryptHistoryFlag(t *testing.T) {
	b, err := New(Config{
		RootTemplate:   validTemplate,
		EncryptHistory: true,
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !b.encryptHistory {
		t.Error("expected encryptHistory to be true")
	}
}

func TestInitialize_Success(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	err := Initialize(Config{
		RootTemplate: validTemplate,
		Version:      "2.0.0",
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	b := Get()
	if b.Version() != "2.0.0" {
		t.Errorf("expected version 2.0.0, got %s", b.Version())
	}
}

func TestInitialize_InvalidConfig_ReturnsError(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	err := Initialize(Config{
		RootTemplate: "",
	})

	if err != ErrTemplateRequired {
		t.Errorf("expected ErrTemplateRequired, got %v", err)
	}
}

func TestGet_NotInitialized_Panics(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when Get() called without Initialize()")
		}
	}()

	Get()
}

func TestGet_AfterInitialize_ReturnsInstance(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	err := Initialize(Config{
		RootTemplate: validTemplate,
	})
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	b := Get()
	if b == nil {
		t.Error("expected bond instance, got nil")
	}
}

func TestIsInertiaRequest_True(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	if !isInertiaRequest(r) {
		t.Error("expected isInertiaRequest to return true")
	}
}

func TestIsInertiaRequest_False(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	if isInertiaRequest(r) {
		t.Error("expected isInertiaRequest to return false")
	}
}

func TestIsInertiaRequest_FalseValue(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "false")

	if isInertiaRequest(r) {
		t.Error("expected isInertiaRequest to return false for 'false' value")
	}
}

func TestResetGlobal(t *testing.T) {
	resetGlobal()

	err := Initialize(Config{
		RootTemplate: validTemplate,
	})
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Verify instance exists
	_ = Get()

	// Reset and verify panic
	resetGlobal()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic after resetGlobal")
		}
	}()

	Get()
}

// --- Tests for package-level convenience functions ---

func TestGlobalRender(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	Initialize(Config{RootTemplate: validTemplate})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	err := Render(w, r, "Test", Props{"key": "value"})
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestGlobalShare(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	Initialize(Config{RootTemplate: validTemplate})

	Share("appName", "Velocity")

	if Get().sharedProps["appName"] != "Velocity" {
		t.Error("expected appName to be shared")
	}
}

func TestGlobalShareFunc(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	Initialize(Config{RootTemplate: validTemplate})

	ShareFunc("dynamic", func(r *http.Request) (any, error) {
		return "dynamic value", nil
	})

	if Get().sharedFuncs["dynamic"] == nil {
		t.Error("expected dynamic func to be shared")
	}
}

func TestGlobalShareMultiple(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	Initialize(Config{RootTemplate: validTemplate})

	ShareMultiple(Props{"a": 1, "b": 2})

	if Get().sharedProps["a"] != 1 || Get().sharedProps["b"] != 2 {
		t.Error("expected multiple props to be shared")
	}
}

func TestGlobalRedirect(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	Initialize(Config{RootTemplate: validTemplate})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	Redirect(w, r, "/dashboard")

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d", w.Code)
	}
}

func TestGlobalLocation(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	Initialize(Config{RootTemplate: validTemplate})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	Location(w, r, "https://external.com")

	if w.Code != http.StatusConflict {
		t.Errorf("expected status 409, got %d", w.Code)
	}
}

func TestGlobalBack(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	Initialize(Config{RootTemplate: validTemplate})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Referer", "/previous")

	Back(w, r)

	location := w.Header().Get("Location")
	if location != "/previous" {
		t.Errorf("expected Location '/previous', got %s", location)
	}
}

func TestGlobalMiddleware(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	Initialize(Config{RootTemplate: validTemplate})

	mw := Middleware()
	if mw == nil {
		t.Error("expected middleware to be returned")
	}
}

func TestSetSharePropsFunc(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	Initialize(Config{RootTemplate: validTemplate})

	called := false
	SetSharePropsFunc(func(r *http.Request) (Props, error) {
		called = true
		return Props{
			"auth": map[string]string{"user": "Ali"},
		}, nil
	})

	// Trigger evaluation via render
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	err := Render(w, r, "Test", Props{})
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	if !called {
		t.Error("expected SharePropsFunc to be called")
	}

	// Verify the auth prop was included
	body := w.Body.String()
	if !strings.Contains(body, "auth") {
		t.Error("expected auth prop to be in response")
	}
}

func TestSetSharePropsFunc_Error(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	Initialize(Config{RootTemplate: validTemplate})

	SetSharePropsFunc(func(r *http.Request) (Props, error) {
		return nil, errors.New("share props failed")
	})

	// Errors from ShareFunc are silently ignored in mergeSharedProps
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	err := Render(w, r, "Test", Props{})
	// Should not fail - errors from shared funcs are ignored
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}
