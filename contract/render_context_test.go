package contract

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestRenderContext(headers map[string]string) (RenderContext, *httptest.ResponseRecorder, *http.Request) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/orders/7", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return NewRenderContext(w, r), w, r
}

func TestNewRenderContext_Accessors(t *testing.T) {
	rc, w, r := newTestRenderContext(map[string]string{"Accept": "application/json"})
	if rc.Request() != r {
		t.Error("Request() did not return the request")
	}
	if rc.Writer() != w {
		t.Error("Writer() did not return the writer")
	}
	if !rc.WantsJSON() {
		t.Error("WantsJSON() = false for Accept application/json")
	}
	if rc.IsInertia() {
		t.Error("IsInertia() = true without X-Inertia")
	}

	inertia, _, _ := newTestRenderContext(map[string]string{"Accept": "application/json", "X-Inertia": "true"})
	if inertia.WantsJSON() || !inertia.IsInertia() {
		t.Error("Inertia request: want WantsJSON false and IsInertia true")
	}
}

func TestNewRenderContext_WriteOnce(t *testing.T) {
	tests := []struct {
		name     string
		run      func(rc RenderContext)
		wantCode int
		wantBody string
	}{
		{"first status wins", func(rc RenderContext) {
			rc.WriteHeader(http.StatusNotFound)
			rc.WriteHeader(http.StatusOK)
			rc.WriteHeader(http.StatusInternalServerError)
		}, 404, ""},
		{"write implies 200", func(rc RenderContext) {
			_, _ = rc.Write([]byte("hi"))
			rc.WriteHeader(http.StatusTeapot)
		}, 200, "hi"},
		{"status then body", func(rc RenderContext) {
			rc.WriteHeader(http.StatusConflict)
			_, _ = rc.Write([]byte("a"))
			_, _ = rc.Write([]byte("b"))
		}, 409, "ab"},
		{"invalid status becomes 500", func(rc RenderContext) {
			rc.WriteHeader(0)
		}, 500, ""},
		{"out of range status becomes 500", func(rc RenderContext) {
			rc.WriteHeader(1000)
		}, 500, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc, w, _ := newTestRenderContext(nil)
			if rc.Written() {
				t.Fatal("Written() = true before any write")
			}
			tt.run(rc)
			if !rc.Written() {
				t.Error("Written() = false after a write")
			}
			if w.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tt.wantCode)
			}
			if w.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", w.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestNewRenderContext_SetHeader(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantSet bool
	}{
		{"valid", "Content-Type", "application/problem+json", true},
		{"CR in value", "X-Test", "a\rb", false},
		{"LF in value", "X-Test", "a\nSet-Cookie: s=1", false},
		{"CRLF in key", "X-Test\r\nSet-Cookie", "v", false},
		{"empty key", "", "v", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc, w, _ := newTestRenderContext(nil)
			rc.SetHeader(tt.key, tt.value)
			_, present := w.Header()[http.CanonicalHeaderKey(tt.key)]
			if present != tt.wantSet {
				t.Errorf("header present = %v, want %v", present, tt.wantSet)
			}
			if !tt.wantSet && len(w.Header()) != 0 {
				t.Errorf("rejected header left entries: %v", w.Header())
			}
		})
	}
}

func TestNewRenderContext_Redirect(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		target     string
		wantErr    bool
		wantStatus int // StatusOf status of the returned error
	}{
		{"relative path", http.StatusFound, "/dashboard", false, 0},
		{"see other with query", http.StatusSeeOther, "/orders?page=2#top", false, 0},
		{"root", http.StatusMovedPermanently, "/", false, 0},
		{"empty target", http.StatusFound, "", true, 400},
		{"no leading slash", http.StatusFound, "dashboard", true, 400},
		{"protocol relative", http.StatusFound, "//evil.com", true, 400},
		{"triple slash", http.StatusFound, "///evil.com", true, 400},
		{"absolute url", http.StatusFound, "https://evil.com/x", true, 400},
		{"javascript scheme", http.StatusFound, "javascript:alert(1)", true, 400},
		{"backslash", http.StatusFound, "/\\evil.com", true, 400},
		{"leading backslash", http.StatusFound, "\\\\evil.com", true, 400},
		{"fullwidth solidus", http.StatusFound, "/／evil.com", true, 400},
		{"division slash", http.StatusFound, "/∕evil.com", true, 400},
		{"CRLF injection", http.StatusFound, "/a\r\nSet-Cookie: s=1", true, 400},
		{"tab", http.StatusFound, "/\t/evil.com", true, 400},
		{"DEL byte", http.StatusFound, "/a\x7f", true, 400},
		{"trailing space", http.StatusFound, "/a ", true, 400},
		{"leading space", http.StatusFound, " /a", true, 400},
		{"status 200", http.StatusOK, "/ok", true, 500},
		{"status 400", http.StatusBadRequest, "/ok", true, 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc, w, _ := newTestRenderContext(nil)
			err := rc.Redirect(tt.status, tt.target)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("Redirect() error = %v", err)
				}
				if w.Code != tt.status {
					t.Errorf("status = %d, want %d", w.Code, tt.status)
				}
				if got := w.Header().Get("Location"); got != tt.target {
					t.Errorf("Location = %q, want %q", got, tt.target)
				}
				if !rc.Written() {
					t.Error("Written() = false after redirect")
				}
				return
			}
			if !errors.Is(err, ErrInvalidRedirect) {
				t.Fatalf("Redirect() error = %v, want ErrInvalidRedirect", err)
			}
			if status, _, _ := StatusOf(err); status != tt.wantStatus {
				t.Errorf("StatusOf(err) = %d, want %d", status, tt.wantStatus)
			}
			if rc.Written() {
				t.Error("rejected redirect marked the response written")
			}
			if w.Header().Get("Location") != "" {
				t.Error("rejected redirect set Location")
			}
		})
	}
}

func TestNewRenderContext_Redirect_AfterWrite(t *testing.T) {
	rc, w, _ := newTestRenderContext(nil)
	rc.WriteHeader(http.StatusNotFound)
	err := rc.Redirect(http.StatusFound, "/login")
	if !errors.Is(err, ErrInvalidRedirect) {
		t.Fatalf("Redirect() after write error = %v, want ErrInvalidRedirect", err)
	}
	if w.Code != http.StatusNotFound || w.Header().Get("Location") != "" {
		t.Errorf("redirect after write changed the response: %d %q", w.Code, w.Header().Get("Location"))
	}
}

// commitWriter is a recorder that reports its own commitment.
type commitWriter struct {
	*httptest.ResponseRecorder
	committed bool
}

func (w *commitWriter) Committed() bool { return w.committed }

func TestNewRenderContext_HonoursCommitReporter(t *testing.T) {
	tests := []struct {
		name      string
		committed bool
		wantW     bool
	}{
		{"committed writer", true, true},
		{"uncommitted writer", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &commitWriter{ResponseRecorder: httptest.NewRecorder(), committed: tt.committed}
			rc := NewRenderContext(w, httptest.NewRequest(http.MethodPost, "/x", nil))
			if rc.Written() != tt.wantW {
				t.Fatalf("Written() = %v, want %v", rc.Written(), tt.wantW)
			}
			err := rc.Redirect(http.StatusSeeOther, "/back")
			if tt.committed != errors.Is(err, ErrInvalidRedirect) {
				t.Errorf("Redirect() = %v, want refused=%v", err, tt.committed)
			}
			if tt.committed {
				rc.WriteHeader(http.StatusTeapot)
				if w.Code != http.StatusOK || w.Header().Get("Location") != "" {
					t.Errorf("committed writer got status %d Location %q, want nothing written", w.Code, w.Header().Get("Location"))
				}
			}
		})
	}
}
