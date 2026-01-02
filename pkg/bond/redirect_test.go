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
