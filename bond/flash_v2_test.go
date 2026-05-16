package bond

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// renderJSONPage runs Render as an Inertia XHR and returns the parsed Page.
func renderJSONPage(t *testing.T, b *Bond, props Props) (*httptest.ResponseRecorder, Page) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	if err := b.Render(w, r, "Home", props); err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	var page Page
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	return w, page
}

// TestFlashProvider_NotSet_FlashOmitted asserts that a Bond with no
// FlashProvider wired renders no "flash" key in the payload. We assert
// via the rendered JSON (rather than poking flashFor directly) to keep
// the test surface aligned with what callers observe.
func TestFlashProvider_NotSet_FlashOmitted(t *testing.T) {
	b := setupBond(t)

	w, page := renderJSONPage(t, b, Props{"k": "v"})

	if page.Flash != nil {
		t.Errorf("expected Flash nil with no provider, got %#v", page.Flash)
	}
	if strings.Contains(w.Body.String(), `"flash"`) {
		t.Errorf("expected flash key absent from JSON, got %s", w.Body.String())
	}
}

func TestFlashProvider_ReturnsNil_FlashOmitted(t *testing.T) {
	b := setupBond(t)
	b.SetFlashProvider(func(w http.ResponseWriter, r *http.Request) map[string]any {
		return nil
	})

	w, page := renderJSONPage(t, b, Props{})

	if page.Flash != nil {
		t.Errorf("expected Flash nil when provider returns nil, got %#v", page.Flash)
	}
	if strings.Contains(w.Body.String(), `"flash"`) {
		t.Errorf("expected flash key absent, got %s", w.Body.String())
	}
}

func TestFlashProvider_ReturnsEmptyMap_FlashOmitted(t *testing.T) {
	b := setupBond(t)
	b.SetFlashProvider(func(w http.ResponseWriter, r *http.Request) map[string]any {
		return map[string]any{}
	})

	w, page := renderJSONPage(t, b, Props{})

	if page.Flash != nil {
		t.Errorf("expected Flash nil when provider returns empty map, got %#v", page.Flash)
	}
	if strings.Contains(w.Body.String(), `"flash"`) {
		t.Errorf("expected flash key absent, got %s", w.Body.String())
	}
}

func TestFlashProvider_ReturnsNonEmpty_FlashIncluded(t *testing.T) {
	b := setupBond(t)
	bag := map[string]any{
		"success": "Saved!",
		"info":    "Welcome back",
	}
	b.SetFlashProvider(func(w http.ResponseWriter, r *http.Request) map[string]any {
		return bag
	})

	_, page := renderJSONPage(t, b, Props{})

	if page.Flash == nil {
		t.Fatal("expected Flash to be set")
	}
	if page.Flash["success"] != "Saved!" {
		t.Errorf("expected success='Saved!', got %v", page.Flash["success"])
	}
	if page.Flash["info"] != "Welcome back" {
		t.Errorf("expected info='Welcome back', got %v", page.Flash["info"])
	}
}

// TestSetFlashProvider_ConcurrentSafe runs concurrent SetFlashProvider and
// Render to catch any data race on b.flashProvider. The test passes only
// under `-race` (without `-race` the race detector is inactive and a bug
// would be silently masked).
func TestSetFlashProvider_ConcurrentSafe(t *testing.T) {
	b := setupBond(t)

	// Seed with a provider so Render has something non-nil to call at least
	// some of the time.
	b.SetFlashProvider(func(w http.ResponseWriter, r *http.Request) map[string]any {
		return map[string]any{"k": "v"}
	})

	var wg sync.WaitGroup
	const goroutines = 8
	const iters = 200

	// Writer goroutines: keep swapping the provider.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				j := j
				b.SetFlashProvider(func(w http.ResponseWriter, r *http.Request) map[string]any {
					return map[string]any{"writer": id, "iter": j}
				})
			}
		}(i)
	}

	// Reader goroutines: keep rendering.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Set("X-Inertia", "true")
				if err := b.Render(w, r, "Home", Props{}); err != nil {
					t.Errorf("Render failed: %v", err)
					return
				}
			}
		}()
	}

	// Also race SetFlashProvider(nil) to exercise the nil-clear path.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < iters; j++ {
			b.SetFlashProvider(nil)
		}
	}()

	wg.Wait()
}

// TestFlashProvider_ReceivesActualWriterAndRequest asserts the provider is
// called with the exact w and r passed to Render, not copies or wrappers.
// This is the contract that lets a provider use the request to read its
// session cookie and the writer to set a Set-Cookie clearing the bag.
func TestFlashProvider_ReceivesActualWriterAndRequest(t *testing.T) {
	b := setupBond(t)

	var capturedW http.ResponseWriter
	var capturedR *http.Request
	b.SetFlashProvider(func(w http.ResponseWriter, r *http.Request) map[string]any {
		capturedW = w
		capturedR = r
		return map[string]any{"ok": true}
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/path?x=1", nil)
	r.Header.Set("X-Inertia", "true")

	if err := b.Render(w, r, "Home", Props{}); err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	if capturedR != r {
		t.Errorf("expected provider to receive the exact *Request passed to Render")
	}
	if capturedW != w {
		t.Errorf("expected provider to receive the exact ResponseWriter passed to Render")
	}
}

// TestFlashProvider_HeadersPropagateToResponse asserts that a provider which
// writes response headers (the typical "I just consumed the flash, clear
// the session cookie" pattern) sees those headers land on the wire. This
// confirms Render does not pass a copy / shadow writer to the provider.
func TestFlashProvider_HeadersPropagateToResponse(t *testing.T) {
	b := setupBond(t)
	b.SetFlashProvider(func(w http.ResponseWriter, r *http.Request) map[string]any {
		w.Header().Set("X-Flash-Cleared", "yes")
		http.SetCookie(w, &http.Cookie{Name: "flash_session", Value: "", MaxAge: -1, Path: "/"})
		return map[string]any{"info": "consumed"}
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	if err := b.Render(w, r, "Home", Props{}); err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	if got := w.Header().Get("X-Flash-Cleared"); got != "yes" {
		t.Errorf("expected provider header to land on response, got %q", got)
	}
	if cookie := w.Header().Get("Set-Cookie"); !strings.Contains(cookie, "flash_session=") {
		t.Errorf("expected Set-Cookie from provider on response, got %q", cookie)
	}

	// Flash data still rendered.
	var page Page
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if page.Flash["info"] != "consumed" {
		t.Errorf("expected flash.info='consumed', got %v", page.Flash["info"])
	}
}

// TestFlashProvider_EmbeddedInHTMLDataPage asserts that for a non-Inertia
// (full HTML) response the flash payload is embedded in the data-page
// JSON, so the client sees the same shape on first paint as on
// subsequent visits. We extract the JSON from both the v3 <script
// data-page="app"> block and the legacy data-page attribute and check
// both carry the flash.
func TestFlashProvider_EmbeddedInHTMLDataPage(t *testing.T) {
	b := setupBond(t)
	b.SetFlashProvider(func(w http.ResponseWriter, r *http.Request) map[string]any {
		return map[string]any{"success": "Welcome"}
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// No X-Inertia header, this is a full HTML render.

	if err := b.Render(w, r, "Home", Props{}); err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	body := w.Body.String()

	// The v3 <script type="application/json"> block carries raw JSON.
	scriptStart := strings.Index(body, `<script id="app-page" type="application/json"`)
	if scriptStart < 0 {
		t.Fatalf("expected v3 inertia script tag in HTML, got: %s", body)
	}
	jsonStart := strings.Index(body[scriptStart:], `>`)
	if jsonStart < 0 {
		t.Fatalf("malformed inertia script tag: %s", body)
	}
	jsonStart += scriptStart + 1
	jsonEnd := strings.Index(body[jsonStart:], `</script>`)
	if jsonEnd < 0 {
		t.Fatalf("inertia script not closed: %s", body)
	}
	rawJSON := body[jsonStart : jsonStart+jsonEnd]

	var page Page
	if err := json.Unmarshal([]byte(rawJSON), &page); err != nil {
		t.Fatalf("failed to parse embedded page JSON: %v (raw=%s)", err, rawJSON)
	}
	if page.Flash == nil || page.Flash["success"] != "Welcome" {
		t.Errorf("expected flash.success='Welcome' in HTML data-page, got %#v", page.Flash)
	}
}

// TestFlashProvider_PartialReload_NotDrained asserts that on a partial
// reload request the flash provider is NOT invoked, so the bag stays in
// the session for the next full render. Inertia v2 clients skip the
// `flash` event on deferred-prop and partial requests, so consuming the
// bag here would silently lose the message.
func TestFlashProvider_PartialReload_NotDrained(t *testing.T) {
	b := setupBond(t)

	var called int
	b.SetFlashProvider(func(w http.ResponseWriter, r *http.Request) map[string]any {
		called++
		return map[string]any{"error": "x"}
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set(HeaderPartialComponent, "Home")
	r.Header.Set(HeaderPartialOnly, "k")

	if err := b.Render(w, r, "Home", Props{"k": "v"}); err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	if called != 0 {
		t.Errorf("expected flashFor not called on partial reload, got %d call(s)", called)
	}

	var page Page
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if page.Flash != nil {
		t.Errorf("expected Flash nil on partial reload, got %#v", page.Flash)
	}
}
