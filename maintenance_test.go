package velocity

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/router"
)

// createMarker is a test helper that creates the .vel/down marker file in the
// current working directory and returns the marker path.
func createMarker(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join(".", ".vel")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create .vel dir: %v", err)
	}
	markerPath := filepath.Join(dir, "down")
	if err := os.WriteFile(markerPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}
	return markerPath
}

func TestPreventRequestsDuringMaintenance_AppIsUp(t *testing.T) {
	t.Chdir(t.TempDir())

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	c, w := router.NewTestContext("GET", "/")
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}
}

func TestPreventRequestsDuringMaintenance_AppIsDown(t *testing.T) {
	t.Chdir(t.TempDir())
	createMarker(t, `{"time":"2024-01-01T00:00:00Z"}`)

	mw := PreventRequestsDuringMaintenance()
	nextCalled := false
	handler := mw(func(c *router.Context) error {
		nextCalled = true
		return nil
	})

	c, w := router.NewTestContext("GET", "/")
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if nextCalled {
		t.Error("next handler should not be called during maintenance")
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body["message"] != "Service Unavailable" {
		t.Errorf("message: got %q, want %q", body["message"], "Service Unavailable")
	}
}

func TestPreventRequestsDuringMaintenance_RecoversAfterUp(t *testing.T) {
	t.Chdir(t.TempDir())
	markerPath := createMarker(t, `{}`)

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Request while down — should get 503.
	c1, w1 := router.NewTestContext("GET", "/")
	if err := handler(c1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w1.Code != http.StatusServiceUnavailable {
		t.Fatalf("while down: got %d, want %d", w1.Code, http.StatusServiceUnavailable)
	}

	// Remove marker (simulate "up" command).
	os.Remove(markerPath)

	// Request after up — should pass through.
	c2, w2 := router.NewTestContext("GET", "/")
	if err := handler(c2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w2.Code != http.StatusOK {
		t.Errorf("after up: got %d, want %d", w2.Code, http.StatusOK)
	}
}

func TestPreventRequestsDuringMaintenance_ContentType(t *testing.T) {
	t.Chdir(t.TempDir())
	createMarker(t, `{}`)

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error { return nil })

	c, w := router.NewTestContext("GET", "/")
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}
}

func TestPreventRequestsDuringMaintenance_Concurrent(t *testing.T) {
	t.Chdir(t.TempDir())
	createMarker(t, `{}`)

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error {
		return c.JSON(http.StatusOK, nil)
	})

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, w := router.NewTestContext("GET", "/")
			if err := handler(c); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("concurrent: got %d, want %d", w.Code, http.StatusServiceUnavailable)
			}
		}()
	}
	wg.Wait()
}

func TestIsDownForMaintenance(t *testing.T) {
	t.Chdir(t.TempDir())

	if isDownForMaintenance() {
		t.Error("should return false when marker does not exist")
	}

	createMarker(t, `{}`)

	if !isDownForMaintenance() {
		t.Error("should return true when marker exists")
	}
}
