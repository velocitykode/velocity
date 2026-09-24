package bond

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrInvalidStatus is matched (errors.Is) by the error RenderWithStatus
// returns for a status outside 200-999. Nothing is written in that case.
var ErrInvalidStatus = errors.New("bond: invalid status code")

// RenderWithStatus renders component with props exactly like Render (the
// JSON page object for an Inertia XHR request, the HTML shell for a
// full-page visit) but answers with status instead of 200. The status is
// written once, right before the first body byte, so a render that fails
// before writing leaves the response untouched.
func (b *Bond) RenderWithStatus(w http.ResponseWriter, r *http.Request, component string, props Props, status int) error {
	if status < http.StatusOK || status > 999 {
		return fmt.Errorf("%w: %d", ErrInvalidStatus, status)
	}
	return b.Render(&statusWriter{ResponseWriter: w, status: status}, r, component, props)
}

// SetErrorComponent sets the component rendered as the error page (for
// example "Error"). An empty name turns the error page off.
func (b *Bond) SetErrorComponent(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.errorComponent = name
}

// ErrorComponent returns the error page component, or "" when none is set.
func (b *Bond) ErrorComponent() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.errorComponent
}

// ReloadLocation returns the target an Inertia client reloads as a full
// visit (X-Inertia-Location) when a request cannot be answered with a page:
// the current URL for GET and HEAD, the Referer otherwise. Both pass the
// same redirect host allowlist as Redirect and Back, so an unsafe or
// foreign target, or a missing one, collapses to "/".
func (b *Bond) ReloadLocation(r *http.Request) string {
	if r == nil {
		return "/"
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		if r.URL == nil {
			return "/"
		}
		// The request URI is always relative, so no host list is needed.
		return sanitizeRedirectURL(r.URL.RequestURI(), nil)
	}
	referer := r.Header.Get("Referer")
	if referer == "" {
		return "/"
	}
	return sanitizeRedirectURL(referer, b.allowedHostsFor(r))
}

// statusWriter writes status before the first body byte unless a status
// was already written through it.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(w.status)
	}
	return w.ResponseWriter.Write(p)
}

// Unwrap returns the underlying writer for http.ResponseController.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
