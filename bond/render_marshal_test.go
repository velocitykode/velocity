package bond

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// trickyProps exercises every byte the data-page escaping cares about:
// double quote, single quote, <, >, & and a non-ASCII rune.
func trickyProps() Props {
	return Props{
		"quote":   `she said "hi"`,
		"apos":    "O'Brien",
		"angle":   "<script>alert(1)</script>",
		"amp":     "Tom & Jerry",
		"unicode": "cafe 日本語 😀",
	}
}

func trickyPage() Page {
	return Page{
		Component: "Dashboard",
		Props:     trickyProps(),
		URL:       "/dashboard?q=a&b=c",
		Version:   "1.0.0",
	}
}

// TestRenderHTML_DataPageAttr_ByteIdentical proves the single-marshal path
// produces a byte-identical data-page attribute to the original double-marshal
// path (ToHTMLAttr, which re-marshals the Page internally). Covers quotes,
// angle brackets, ampersands and unicode.
func TestRenderHTML_DataPageAttr_ByteIdentical(t *testing.T) {
	page := trickyPage()

	// Reference: original behavior, marshals the Page a second time.
	want, err := page.ToHTMLAttr()
	if err != nil {
		t.Fatalf("ToHTMLAttr failed: %v", err)
	}

	// New path: marshal once, derive the attribute from the bytes.
	raw, err := page.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}
	got := htmlAttrEscape(raw)

	if got != want {
		t.Fatalf("data-page attr mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// TestRenderHTML_ContainerGolden renders a full page and asserts the emitted
// container holds the exact data-page attribute the original escaping produced,
// so the rendered HTML is unchanged by the single-marshal refactor.
func TestRenderHTML_ContainerGolden(t *testing.T) {
	b := setupBond(t)
	page := trickyPage()

	wantAttr, err := page.ToHTMLAttr()
	if err != nil {
		t.Fatalf("ToHTMLAttr failed: %v", err)
	}

	w := httptest.NewRecorder()
	if err := b.renderHTML(context.Background(), w, page); err != nil {
		t.Fatalf("renderHTML failed: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, `data-page='`+wantAttr+`'`) {
		t.Fatalf("rendered container missing expected data-page attr.\nwant attr: %q\nbody: %s", wantAttr, body)
	}
}

func BenchmarkRenderHTML(b *testing.B) {
	bond := setupBond(&testing.T{})
	page := trickyPage()
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := bond.renderHTML(ctx, discardResponseWriter{}, page); err != nil {
			b.Fatalf("renderHTML failed: %v", err)
		}
	}
}

// discardResponseWriter is a minimal http.ResponseWriter that drops the body,
// keeping the benchmark focused on marshaling/escaping rather than I/O.
type discardResponseWriter struct{}

func (discardResponseWriter) Header() http.Header         { return http.Header{} }
func (discardResponseWriter) Write(p []byte) (int, error) { return io.Discard.Write(p) }
func (discardResponseWriter) WriteHeader(int)             {}

// failingMarshaler is a prop whose MarshalJSON fails.
type failingMarshaler struct{}

func (failingMarshaler) MarshalJSON() ([]byte, error) { return nil, errors.New("cannot marshal") }

// headerRecorder records whether WriteHeader or Write reached it.
type headerRecorder struct {
	*httptest.ResponseRecorder
	wroteHeader bool
}

func (w *headerRecorder) WriteHeader(code int) {
	w.wroteHeader = true
	w.ResponseRecorder.WriteHeader(code)
}

func (w *headerRecorder) Write(p []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseRecorder.Write(p)
}

// TestRenderJSON_EncodeFailureLeavesResponseUntouched checks that an
// Inertia page that fails to encode returns the encoding error having set
// no header (X-Inertia above all: it marks a page object) and written
// nothing, so whatever answers the failure next starts from the headers the
// response had before the render.
func TestRenderJSON_EncodeFailureLeavesResponseUntouched(t *testing.T) {
	tests := []struct {
		name    string
		props   Props
		wantErr any
	}{
		{name: "ChanProp", props: Props{"feed": make(chan int)}, wantErr: new(*json.UnsupportedTypeError)},
		{name: "FuncProp", props: Props{"fn": func() {}}, wantErr: new(*json.UnsupportedTypeError)},
		{name: "NaNProp", props: Props{"ratio": math.NaN()}, wantErr: new(*json.UnsupportedValueError)},
		{name: "MarshalJSONFails", props: Props{"item": failingMarshaler{}}, wantErr: new(*json.MarshalerError)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := setupBond(t)
			r := httptest.NewRequest(http.MethodGet, "/report", nil)
			r.Header.Set(HeaderInertia, "true")
			w := &headerRecorder{ResponseRecorder: httptest.NewRecorder()}
			w.Header().Set("Cache-Control", "private, no-store")
			before := w.Header().Clone()

			err := b.Render(w, r, "Report", tt.props)
			if err == nil {
				t.Fatal("Render returned nil, want the encoding error")
			}
			if !errors.As(err, tt.wantErr) {
				t.Errorf("Render error = %T (%v), want %T", err, err, tt.wantErr)
			}
			if !maps.EqualFunc(w.Header(), before, slices.Equal[[]string]) {
				t.Errorf("headers = %v, want them untouched: %v", w.Header(), before)
			}
			if w.wroteHeader || w.Body.Len() != 0 {
				t.Errorf("wrote a response (status written %v, body %q), want nothing", w.wroteHeader, w.Body.String())
			}
		})
	}
}

// TestRenderJSON_BodyMatchesEncoder checks that the page object is written
// byte for byte as json.Encoder writes it (HTML-escaped, newline
// terminated) with the page marker headers.
func TestRenderJSON_BodyMatchesEncoder(t *testing.T) {
	b := setupBond(t)
	page := trickyPage()
	var want bytes.Buffer
	if err := json.NewEncoder(&want).Encode(page); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	w := httptest.NewRecorder()
	if err := b.renderJSON(w, page); err != nil {
		t.Fatalf("renderJSON: %v", err)
	}
	if got := w.Body.String(); got != want.String() {
		t.Errorf("body = %q, want %q", got, want.String())
	}
	for key, value := range map[string]string{
		"Content-Type":           "application/json",
		"X-Content-Type-Options": "nosniff",
		"X-Inertia":              "true",
		"Vary":                   "X-Inertia",
	} {
		if got := w.Header().Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
}
