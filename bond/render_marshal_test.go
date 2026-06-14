package bond

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
