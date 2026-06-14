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

// --- Tests for instance-level methods ---

func TestBond_Render(t *testing.T) {
	b, err := New(Config{RootTemplate: validTemplate})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	err = b.Render(w, r, "Test", Props{"key": "value"})
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestBond_Share(t *testing.T) {
	b, err := New(Config{RootTemplate: validTemplate})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	b.Share("appName", "Velocity")

	if b.sharedProps["appName"] != "Velocity" {
		t.Error("expected appName to be shared")
	}
}

func TestBond_ShareFunc(t *testing.T) {
	b, err := New(Config{RootTemplate: validTemplate})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	b.ShareFunc("dynamic", func(r *http.Request) (any, error) {
		return "dynamic value", nil
	})

	if b.sharedFuncs["dynamic"] == nil {
		t.Error("expected dynamic func to be shared")
	}
}

func TestBond_ShareMultiple(t *testing.T) {
	b, err := New(Config{RootTemplate: validTemplate})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	b.ShareMultiple(Props{"a": 1, "b": 2})

	if b.sharedProps["a"] != 1 || b.sharedProps["b"] != 2 {
		t.Error("expected multiple props to be shared")
	}
}

func TestBond_Redirect(t *testing.T) {
	b, err := New(Config{RootTemplate: validTemplate})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	b.Redirect(w, r, "/dashboard")

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d", w.Code)
	}
}

func TestBond_Location(t *testing.T) {
	b, err := New(Config{RootTemplate: validTemplate})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.LocationExternal(w, r, "https://external.com")

	if w.Code != http.StatusConflict {
		t.Errorf("expected status 409, got %d", w.Code)
	}
}

func TestBond_Back(t *testing.T) {
	b, err := New(Config{RootTemplate: validTemplate})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Referer", "/previous")

	b.Back(w, r)

	location := w.Header().Get("Location")
	if location != "/previous" {
		t.Errorf("expected Location '/previous', got %s", location)
	}
}

func TestBond_MiddlewareFunc(t *testing.T) {
	b, err := New(Config{RootTemplate: validTemplate})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	mw := b.MiddlewareFunc()
	if mw == nil {
		t.Error("expected middleware to be returned")
	}
}

func TestBond_SetSharePropsFunc(t *testing.T) {
	b, err := New(Config{RootTemplate: validTemplate})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	called := false
	b.SetSharePropsFunc(func(r *http.Request) (Props, error) {
		called = true
		return Props{
			"auth": map[string]string{"user": "Ali"},
		}, nil
	})

	// Trigger evaluation via render
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	err = b.Render(w, r, "Test", Props{})
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

func TestBond_SetSharePropsFunc_Error(t *testing.T) {
	b, err := New(Config{RootTemplate: validTemplate})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	b.SetSharePropsFunc(func(r *http.Request) (Props, error) {
		return nil, errors.New("share props failed")
	})

	// Errors from ShareFunc are silently ignored in mergeSharedProps
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	err = b.Render(w, r, "Test", Props{})
	// Should not fail - errors from shared funcs are ignored
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}
