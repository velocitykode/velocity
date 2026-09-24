package bond

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/router"
)

// statusRecorder counts WriteHeader calls on top of a ResponseRecorder.
type statusRecorder struct {
	*httptest.ResponseRecorder
	headerWrites int
}

func (w *statusRecorder) WriteHeader(code int) {
	w.headerWrites++
	w.ResponseRecorder.WriteHeader(code)
}

func newStatusRecorder() *statusRecorder {
	return &statusRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func TestBond_RenderWithStatus(t *testing.T) {
	tests := []struct {
		name        string
		inertia     bool
		status      int
		wantContent string
	}{
		{name: "InertiaXHR", inertia: true, status: http.StatusNotFound, wantContent: "application/json"},
		{name: "FullPage", status: http.StatusNotFound, wantContent: "text/html; charset=utf-8"},
		{name: "ServerErrorXHR", inertia: true, status: http.StatusInternalServerError, wantContent: "application/json"},
		{name: "ServerErrorFullPage", status: http.StatusServiceUnavailable, wantContent: "text/html; charset=utf-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := New(Config{RootTemplate: validTemplate})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			r := httptest.NewRequest(http.MethodGet, "/missing", nil)
			if tt.inertia {
				r.Header.Set(HeaderInertia, "true")
			}
			w := newStatusRecorder()
			props := Props{"status": tt.status, "message": "Not Found"}
			if err := b.RenderWithStatus(w, r, "Error", props, tt.status); err != nil {
				t.Fatalf("RenderWithStatus: %v", err)
			}
			if w.Code != tt.status {
				t.Errorf("status = %d, want %d", w.Code, tt.status)
			}
			if w.headerWrites != 1 {
				t.Errorf("WriteHeader called %d times, want 1", w.headerWrites)
			}
			if got := w.Header().Get("Content-Type"); got != tt.wantContent {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantContent)
			}
			page := decodePage(t, w.Body.String(), tt.inertia)
			if page.Component != "Error" {
				t.Errorf("component = %q, want Error", page.Component)
			}
			if got, _ := page.Props["status"].(float64); int(got) != tt.status {
				t.Errorf("props.status = %v, want %d", page.Props["status"], tt.status)
			}
			if page.URL != "/missing" {
				t.Errorf("url = %q, want /missing", page.URL)
			}
		})
	}
}

// decodePage extracts the page object from a JSON body or from the
// data-page script of an HTML body.
func decodePage(t *testing.T, body string, inertia bool) Page {
	t.Helper()
	raw := body
	if !inertia {
		start := strings.Index(body, `type="application/json" data-page="app">`)
		if start < 0 {
			t.Fatalf("no page script in body: %s", body)
		}
		raw = body[start+len(`type="application/json" data-page="app">`):]
		raw = raw[:strings.Index(raw, "</script>")]
	}
	var page Page
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatalf("decode page: %v (%s)", err, raw)
	}
	return page
}

func TestBond_RenderWithStatus_InvalidStatus(t *testing.T) {
	for _, status := range []int{0, 99, 100, 199, 1000, -1} {
		b, err := New(Config{RootTemplate: validTemplate})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		w := newStatusRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		err = b.RenderWithStatus(w, r, "Error", nil, status)
		if !errors.Is(err, ErrInvalidStatus) {
			t.Errorf("status %d: err = %v, want ErrInvalidStatus", status, err)
		}
		if w.headerWrites != 0 || w.Body.Len() != 0 {
			t.Errorf("status %d: response written", status)
		}
	}
}

func TestBond_RenderWithStatus_FailureWritesNothing(t *testing.T) {
	b, err := New(Config{RootTemplate: validTemplate})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ssrErr := errors.New("ssr down")
	b.SetSSRGateway(&fakeGateway{err: ssrErr})
	w := newStatusRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := b.RenderWithStatus(w, r, "Error", nil, http.StatusNotFound); !errors.Is(err, ssrErr) {
		t.Fatalf("err = %v, want the SSR failure", err)
	}
	if w.headerWrites != 0 || w.Body.Len() != 0 {
		t.Errorf("failed render wrote a response (status writes %d, body %q)", w.headerWrites, w.Body.String())
	}
}

func TestStatusWriter(t *testing.T) {
	tests := []struct {
		name       string
		explicit   int
		wantStatus int
	}{
		{name: "StatusBeforeFirstByte", wantStatus: http.StatusNotFound},
		{name: "ExplicitStatusWins", explicit: http.StatusTeapot, wantStatus: http.StatusTeapot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newStatusRecorder()
			w := &statusWriter{ResponseWriter: rec, status: http.StatusNotFound}
			if tt.explicit != 0 {
				w.WriteHeader(tt.explicit)
				w.WriteHeader(http.StatusOK)
			}
			if _, err := w.Write([]byte("a")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if _, err := w.Write([]byte("b")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if rec.Code != tt.wantStatus || rec.headerWrites != 1 || rec.Body.String() != "ab" {
				t.Errorf("status %d writes %d body %q", rec.Code, rec.headerWrites, rec.Body.String())
			}
			if w.Unwrap() != http.ResponseWriter(rec) {
				t.Error("Unwrap did not return the underlying writer")
			}
		})
	}
}

func TestBond_ErrorComponent(t *testing.T) {
	b, err := New(Config{RootTemplate: validTemplate})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := b.ErrorComponent(); got != "" {
		t.Errorf("default ErrorComponent = %q, want empty", got)
	}
	b.SetErrorComponent("Errors/Show")
	if got := b.ErrorComponent(); got != "Errors/Show" {
		t.Errorf("ErrorComponent = %q, want Errors/Show", got)
	}
	b.SetErrorComponent("")
	if got := b.ErrorComponent(); got != "" {
		t.Errorf("cleared ErrorComponent = %q, want empty", got)
	}
}

func TestBond_ReloadLocation(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		target    string
		host      string
		referer   string
		allowlist []string
		mutate    func(r *http.Request)
		want      string
	}{
		{name: "GETCurrentURL", method: http.MethodGet, target: "/posts/1?tab=a", want: "/posts/1?tab=a"},
		{name: "HEADCurrentURL", method: http.MethodHead, target: "/posts", want: "/posts"},
		{name: "GETIgnoresReferer", method: http.MethodGet, target: "/a", referer: "/b", want: "/a"},
		{name: "GETProtocolRelativePath", method: http.MethodGet, target: "/x", mutate: func(r *http.Request) { r.URL.Path = "//evil.test" }, want: "/"},
		{name: "GETNilURL", method: http.MethodGet, target: "/x", mutate: func(r *http.Request) { r.URL = nil }, want: "/"},
		{name: "POSTRelativeReferer", method: http.MethodPost, target: "/posts", referer: "/posts/new?draft=1", want: "/posts/new?draft=1"},
		{name: "POSTSameHostReferer", method: http.MethodPost, target: "/posts", host: "example.com", referer: "http://example.com/posts/new", want: "http://example.com/posts/new"},
		{name: "POSTCrossOriginReferer", method: http.MethodPost, target: "/posts", host: "example.com", referer: "https://evil.test/phish", want: "/"},
		{name: "POSTAllowlistedReferer", method: http.MethodPut, target: "/posts", host: "internal:8080", referer: "https://app.example.com/posts/2/edit", allowlist: []string{"app.example.com"}, want: "https://app.example.com/posts/2/edit"},
		{name: "POSTHostNotInAllowlist", method: http.MethodPost, target: "/posts", host: "evil.test", referer: "http://evil.test/x", allowlist: []string{"app.example.com"}, want: "/"},
		{name: "POSTJavascriptReferer", method: http.MethodPost, target: "/posts", referer: "javascript:alert(1)", want: "/"},
		{name: "POSTProtocolRelativeReferer", method: http.MethodPost, target: "/posts", referer: "//evil.test/x", want: "/"},
		{name: "DELETENoReferer", method: http.MethodDelete, target: "/posts/1", want: "/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetHostFallbackLatch(t)
			b, err := New(Config{RootTemplate: validTemplate})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			r := httptest.NewRequest(tt.method, tt.target, nil)
			if tt.host != "" {
				r.Host = tt.host
			}
			if tt.referer != "" {
				r.Header.Set("Referer", tt.referer)
			}
			if tt.allowlist != nil {
				r = router.WithServices(r, &app.Services{RedirectAllowlist: &stubAllowlist{hosts: tt.allowlist}})
			}
			if tt.mutate != nil {
				tt.mutate(r)
			}
			if got := b.ReloadLocation(r); got != tt.want {
				t.Errorf("ReloadLocation = %q, want %q", got, tt.want)
			}
		})
	}
	b, err := New(Config{RootTemplate: validTemplate})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := b.ReloadLocation(nil); got != "/" {
		t.Errorf("ReloadLocation(nil) = %q, want /", got)
	}
}

func TestFlashErrorsProp(t *testing.T) {
	fields := map[string]any{"email": []any{"required"}}
	tests := []struct {
		name  string
		value any
		want  any
	}{
		{name: "PlainErrors", value: fields, want: fields},
		{
			name:  "BaggedErrors",
			value: map[string]any{router.FlashErrorBagKey: "login", router.FlashBaggedErrorsKey: fields},
			want:  map[string]any{"email": []any{"required"}, "login": fields},
		},
		{
			name:  "EmptyBag",
			value: map[string]any{router.FlashErrorBagKey: "", router.FlashBaggedErrorsKey: fields},
			want:  map[string]any{router.FlashErrorBagKey: "", router.FlashBaggedErrorsKey: fields},
		},
		{
			name:  "NonStringBag",
			value: map[string]any{router.FlashErrorBagKey: 3.0, router.FlashBaggedErrorsKey: fields},
			want:  map[string]any{router.FlashErrorBagKey: 3.0, router.FlashBaggedErrorsKey: fields},
		},
		{
			name:  "NonObjectErrors",
			value: map[string]any{router.FlashErrorBagKey: "login", router.FlashBaggedErrorsKey: "oops"},
			want:  map[string]any{router.FlashErrorBagKey: "login", router.FlashBaggedErrorsKey: "oops"},
		},
		{
			name:  "ExtraMember",
			value: map[string]any{router.FlashErrorBagKey: "login", router.FlashBaggedErrorsKey: fields, "x": 1.0},
			want:  map[string]any{router.FlashErrorBagKey: "login", router.FlashBaggedErrorsKey: fields, "x": 1.0},
		},
		{name: "StringValue", value: "error", want: "error"},
		{name: "NilValue", value: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := flashErrorsProp(tt.value); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("flashErrorsProp = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// baggedFailure is a flashed validation error that names its error bag.
type baggedFailure struct {
	bag    string
	fields map[string][]string
}

func (f *baggedFailure) Error() string               { return "validation failed" }
func (f *baggedFailure) ErrorBag() string            { return f.bag }
func (f *baggedFailure) Errors() map[string][]string { return f.fields }

// TestApplyFlashData_ErrorBag drives the write path (router's FlashErrors)
// into the read path: errors flashed from a value naming a bag render at
// the top level and under errors.{bag}; any other value renders as sealed.
func TestApplyFlashData_ErrorBag(t *testing.T) {
	fields := map[string][]string{"email": {"The email field is required."}}
	message := []any{"The email field is required."}
	tests := []struct {
		name    string
		flashed any
		want    map[string]any
	}{
		{
			name:    "NamedBag",
			flashed: &baggedFailure{bag: "login", fields: fields},
			want:    map[string]any{"email": message, "login": map[string]any{"email": message}},
		},
		{
			name:    "WrappedNamedBag",
			flashed: fmt.Errorf("store: %w", &baggedFailure{bag: "signup", fields: fields}),
			want:    map[string]any{"email": message, "signup": map[string]any{"email": message}},
		},
		{
			// Without a bag the error value itself is sealed; its JSON
			// form carries no exported field.
			name:    "EmptyBag",
			flashed: &baggedFailure{fields: fields},
			want:    map[string]any{},
		},
		{name: "PlainErrors", flashed: fields, want: map[string]any{"email": message}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc := testFlashEncryptor(t)
			wc := httptest.NewRecorder()
			c := router.NewContext(wc, httptest.NewRequest(http.MethodPost, "/login", nil))
			c.SetServices(&app.Services{Crypto: enc})
			c.FlashErrors(tt.flashed)
			cookies := wc.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("FlashErrors set %d cookies, want 1", len(cookies))
			}

			r := requestWithServices(t, enc)
			r.AddCookie(cookies[0])
			props := Props{}
			applyFlashData(httptest.NewRecorder(), r, props)

			if !reflect.DeepEqual(props["errors"], tt.want) {
				t.Errorf("errors prop = %#v, want %#v", props["errors"], tt.want)
			}
		})
	}
}
