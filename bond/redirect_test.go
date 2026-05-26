package bond

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRedirect_Uses303Status(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)

	b.Redirect(w, r, "/dashboard")

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d", w.Code)
	}
}

func TestRedirect_SetsLocationHeader(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)

	b.Redirect(w, r, "/dashboard")

	location := w.Header().Get("Location")
	if location != "/dashboard" {
		t.Errorf("expected Location '/dashboard', got %s", location)
	}
}

func TestRedirect_InertiaRequest_SetsXInertiaLocation(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.Header.Set("X-Inertia", "true")

	b.Redirect(w, r, "/dashboard")

	xInertiaLocation := w.Header().Get("X-Inertia-Location")
	if xInertiaLocation != "/dashboard" {
		t.Errorf("expected X-Inertia-Location '/dashboard', got %s", xInertiaLocation)
	}
}

func TestRedirect_NonInertiaRequest_NoXInertiaLocation(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)

	b.Redirect(w, r, "/dashboard")

	xInertiaLocation := w.Header().Get("X-Inertia-Location")
	if xInertiaLocation != "" {
		t.Errorf("expected no X-Inertia-Location header, got %s", xInertiaLocation)
	}
}

func TestRedirectWithStatus_CustomStatus(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	b.RedirectWithStatus(w, r, "/new-location", http.StatusMovedPermanently)

	if w.Code != http.StatusMovedPermanently {
		t.Errorf("expected status 301, got %d", w.Code)
	}
}

func TestRedirectWithStatus_302(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	b.RedirectWithStatus(w, r, "/temporary", http.StatusFound)

	if w.Code != http.StatusFound {
		t.Errorf("expected status 302, got %d", w.Code)
	}
}

func TestLocation_InertiaRequest_Returns409(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Location(w, r, "https://external.com")

	if w.Code != http.StatusConflict {
		t.Errorf("expected status 409, got %d", w.Code)
	}
}

func TestLocation_InertiaRequest_SetsXInertiaLocation(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Location(w, r, "https://external.com/page")

	xInertiaLocation := w.Header().Get("X-Inertia-Location")
	if xInertiaLocation != "https://external.com/page" {
		t.Errorf("expected X-Inertia-Location 'https://external.com/page', got %s", xInertiaLocation)
	}
}

func TestLocation_NonInertiaRequest_Returns302(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	b.Location(w, r, "https://external.com")

	if w.Code != http.StatusFound {
		t.Errorf("expected status 302, got %d", w.Code)
	}
}

func TestLocation_NonInertiaRequest_SetsLocationHeader(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	b.Location(w, r, "https://external.com")

	location := w.Header().Get("Location")
	if location != "https://external.com" {
		t.Errorf("expected Location 'https://external.com', got %s", location)
	}
}

func TestBack_UsesReferer(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/submit", nil)
	r.Header.Set("Referer", "/form")

	b.Back(w, r)

	location := w.Header().Get("Location")
	if location != "/form" {
		t.Errorf("expected Location '/form', got %s", location)
	}
}

func TestBack_FallsBackToRoot(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/submit", nil)
	// No Referer header

	b.Back(w, r)

	location := w.Header().Get("Location")
	if location != "/" {
		t.Errorf("expected Location '/', got %s", location)
	}
}

func TestBack_Uses303Status(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/submit", nil)

	b.Back(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d", w.Code)
	}
}

func TestBack_InertiaRequest_SetsXInertiaLocation(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/submit", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("Referer", "/form")

	b.Back(w, r)

	xInertiaLocation := w.Header().Get("X-Inertia-Location")
	if xInertiaLocation != "/form" {
		t.Errorf("expected X-Inertia-Location '/form', got %s", xInertiaLocation)
	}
}

func TestRedirect_ExternalURL(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	b.Redirect(w, r, "https://example.com/path?query=1")

	location := w.Header().Get("Location")
	if location != "https://example.com/path?query=1" {
		t.Errorf("expected external URL, got %s", location)
	}
}

func TestRedirect_RelativeURL(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/current/page", nil)

	b.Redirect(w, r, "../other")

	location := w.Header().Get("Location")
	// http.Redirect resolves relative URLs against the request URL
	if location != "/other" {
		t.Errorf("expected resolved relative URL '/other', got %s", location)
	}
}

func TestSanitizeRedirectURL(t *testing.T) {
	const host = "same.com"

	tests := []struct {
		name   string
		target string
		want   string
	}{
		{
			name:   "javascript scheme rejected",
			target: "javascript:alert(1)",
			want:   "/",
		},
		{
			name:   "javascript scheme mixed case rejected",
			target: "JavaScript:alert(1)",
			want:   "/",
		},
		{
			name:   "data scheme rejected",
			target: "data:text/html,<script>alert(1)</script>",
			want:   "/",
		},
		{
			name:   "vbscript scheme rejected",
			target: "vbscript:msgbox(1)",
			want:   "/",
		},
		{
			name:   "file scheme rejected",
			target: "file:///etc/passwd",
			want:   "/",
		},
		{
			name:   "cross-host http rejected",
			target: "http://other.com/path",
			want:   "/",
		},
		{
			name:   "same-host http allowed",
			target: "http://same.com/path",
			want:   "http://same.com/path",
		},
		{
			name:   "same-host https allowed",
			target: "https://same.com/path",
			want:   "https://same.com/path",
		},
		{
			name:   "relative path allowed",
			target: "/relative",
			want:   "/relative",
		},
		{
			name:   "relative deep path allowed",
			target: "/users/42/edit",
			want:   "/users/42/edit",
		},
		{
			name:   "protocol-relative URL rejected",
			target: "//evil.com/path",
			want:   "/",
		},
		{
			name:   "triple-slash network-path rejected",
			target: "///evil.com/path",
			want:   "/",
		},
		{
			name:   "quad-slash network-path rejected",
			target: "////evil.com",
			want:   "/",
		},
		{
			name:   "backslash variant rejected",
			target: "/\\evil.com/path",
			want:   "/",
		},
		{
			name:   "double-backslash variant rejected",
			target: "\\\\evil.com",
			want:   "/",
		},
		{
			name:   "unicode fullwidth solidus rejected",
			target: "/／evil.com/path",
			want:   "/",
		},
		{
			name:   "unicode big solidus rejected",
			target: "/⧸evil.com/path",
			want:   "/",
		},
		{
			name:   "unicode fraction slash rejected",
			target: "/⁄evil.com/path",
			want:   "/",
		},
		{
			name:   "unicode division slash rejected",
			target: "/∕evil.com/path",
			want:   "/",
		},
		{
			name:   "safe relative path with multi-segment allowed",
			target: "/safe/path",
			want:   "/safe/path",
		},
		{
			name:   "empty string falls through to parse and returns empty",
			target: "",
			want:   "",
		},
	}

	allowed := []string{host}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeRedirectURL(tt.target, allowed)
			if got != tt.want {
				t.Errorf("sanitizeRedirectURL(%q, %v) = %q, want %q", tt.target, allowed, got, tt.want)
			}
		})
	}
}
