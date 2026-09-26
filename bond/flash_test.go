package bond

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/router"
)

// memoryFlashBag is a contract.FlashBag held in memory, standing in for the
// request's session flash bag.
type memoryFlashBag struct {
	mu      sync.Mutex
	entries map[string]any
}

func (b *memoryFlashBag) Flash(key string, value any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entries == nil {
		b.entries = map[string]any{}
	}
	b.entries[key] = value
}

func (b *memoryFlashBag) GetFlash(key string) any {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := b.entries[key]
	delete(b.entries, key)
	return v
}

func (b *memoryFlashBag) FlushFlash() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.entries
	b.entries = nil
	if len(out) == 0 {
		return nil
	}
	return out
}

// left returns a copy of the entries still in the bag.
func (b *memoryFlashBag) left() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]any, len(b.entries))
	for k, v := range b.entries {
		out[k] = v
	}
	return out
}

// bagOf returns a Services.FlashBag that hands out bag.
func bagOf(bag *memoryFlashBag) func(*http.Request) contract.FlashBag {
	return func(*http.Request) contract.FlashBag { return bag }
}

// requestWithFlashBag returns a GET request routed through services whose
// flash bag is bag.
func requestWithFlashBag(bag *memoryFlashBag) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	return router.WithServices(r, &app.Services{FlashBag: bagOf(bag)})
}

// renderPage renders Home with props as an Inertia visit (partial when only
// is non-empty) and returns the page.
func renderPage(t *testing.T, b *Bond, r *http.Request, props Props, only string) Page {
	t.Helper()
	r.Header.Set("X-Inertia", "true")
	if only != "" {
		r.Header.Set(HeaderPartialComponent, "Home")
		r.Header.Set(HeaderPartialOnly, only)
	}
	w := httptest.NewRecorder()
	if err := b.Render(w, r, "Home", props); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if cookies := w.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("Render set cookies %#v; flash rides in the session only", cookies)
	}
	var page Page
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v (%s)", err, w.Body.String())
	}
	return page
}

// TestRender_FlashDrain asserts which entries of the session flash bag a
// render delivers and drains: errors and old input on every render, as
// always props that override component props; messages onto Page.Flash on
// full renders only, left in the bag on partial reloads.
func TestRender_FlashDrain(t *testing.T) {
	errs := map[string]any{"email": "required"}
	old := map[string]any{"email": "bad@"}
	tests := []struct {
		name      string
		only      string
		wantProps map[string]any
		wantFlash map[string]any
		wantLeft  map[string]any
	}{
		{
			name:      "full render",
			wantProps: map[string]any{"k": "v", "errors": errs, "old": old},
			wantFlash: map[string]any{"success": "Saved!"},
			wantLeft:  map[string]any{},
		},
		{
			name:      "partial reload",
			only:      "k",
			wantProps: map[string]any{"k": "v", "errors": errs, "old": old},
			wantLeft:  map[string]any{"success": "Saved!"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bag := &memoryFlashBag{}
			bag.Flash(router.FlashErrorsKey, errs)
			bag.Flash(router.FlashInputKey, old)
			bag.Flash("success", "Saved!")

			page := renderPage(t, setupBond(t), requestWithFlashBag(bag), Props{"k": "v", "errors": "component"}, tt.only)
			if !reflect.DeepEqual(map[string]any(page.Props), tt.wantProps) {
				t.Errorf("props = %#v, want %#v", page.Props, tt.wantProps)
			}
			if !reflect.DeepEqual(page.Flash, tt.wantFlash) {
				t.Errorf("flash = %#v, want %#v", page.Flash, tt.wantFlash)
			}
			if got := bag.left(); !reflect.DeepEqual(got, tt.wantLeft) {
				t.Errorf("left in the bag = %#v, want %#v", got, tt.wantLeft)
			}
		})
	}
}

// TestRender_FlashDeliveredOnce asserts a second render after the one that
// delivered the flash carries none of it.
func TestRender_FlashDeliveredOnce(t *testing.T) {
	b := setupBond(t)
	bag := &memoryFlashBag{}
	bag.Flash(router.FlashErrorsKey, map[string]any{"email": "required"})
	bag.Flash("success", "Saved!")

	_ = renderPage(t, b, requestWithFlashBag(bag), Props{}, "")
	page := renderPage(t, b, requestWithFlashBag(bag), Props{}, "")
	if page.Flash != nil {
		t.Errorf("second render: flash = %#v, want none", page.Flash)
	}
	if _, ok := page.Props["errors"]; ok {
		t.Errorf("second render: errors = %#v, want none", page.Props["errors"])
	}
}

// TestRender_NoFlashBag asserts a render without a session flash bag (no
// routed services, no FlashBag, or a request without a session) omits
// flash and keeps the component's props.
func TestRender_NoFlashBag(t *testing.T) {
	requests := map[string]*http.Request{
		"no services": httptest.NewRequest(http.MethodGet, "/", nil),
		"no FlashBag": router.WithServices(httptest.NewRequest(http.MethodGet, "/", nil), &app.Services{}),
		"no session": router.WithServices(httptest.NewRequest(http.MethodGet, "/", nil), &app.Services{
			FlashBag: func(*http.Request) contract.FlashBag { return nil },
		}),
		"empty bag": requestWithFlashBag(&memoryFlashBag{}),
	}
	for name, r := range requests {
		t.Run(name, func(t *testing.T) {
			page := renderPage(t, setupBond(t), r, Props{"errors": "component"}, "")
			if page.Flash != nil {
				t.Errorf("flash = %#v, want none", page.Flash)
			}
			if page.Props["errors"] != "component" {
				t.Errorf("errors = %#v, want the component prop", page.Props["errors"])
			}
		})
	}
}

// TestRender_FlashEmbeddedInHTMLDataPage asserts a full HTML render embeds
// the drained flash in the data-page JSON, the same shape an Inertia visit
// gets.
func TestRender_FlashEmbeddedInHTMLDataPage(t *testing.T) {
	bag := &memoryFlashBag{}
	bag.Flash("success", "Welcome")
	w := httptest.NewRecorder()
	if err := setupBond(t).Render(w, requestWithFlashBag(bag), "Home", Props{}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	start := strings.Index(body, `<script id="app-page" type="application/json"`)
	if start < 0 {
		t.Fatalf("no inertia page script in HTML: %s", body)
	}
	start += strings.Index(body[start:], ">") + 1
	end := strings.Index(body[start:], "</script>")
	if end < 0 {
		t.Fatalf("inertia page script not closed: %s", body)
	}
	var page Page
	if err := json.Unmarshal([]byte(body[start:start+end]), &page); err != nil {
		t.Fatalf("decode embedded page: %v", err)
	}
	if page.Flash["success"] != "Welcome" {
		t.Errorf("embedded flash = %#v, want success = Welcome", page.Flash)
	}
}
