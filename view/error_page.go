package view

import (
	"net/http"

	"github.com/velocitykode/velocity/contract"
)

// The engine is the error pipeline's error page renderer.
var _ contract.ErrorPageRenderer = (*Engine)(nil)

// RenderErrorPage renders the configured error page component (see
// Config.ErrorPage) at status with the props "status" and "message": the
// JSON page object for an Inertia XHR request, the HTML shell for a
// full-page visit. It reports false, having written nothing, when no
// component is configured. A render that fails before writing reports
// false with the error, so the caller can still answer; once anything was
// written it reports true.
func (e *Engine) RenderErrorPage(rc contract.RenderContext, status int, message string) (bool, error) {
	if rc == nil || rc.Request() == nil {
		return false, nil
	}
	component := e.bond.ErrorComponent()
	if component == "" {
		return false, nil
	}
	props := Props{"status": status, "message": message}
	if err := e.bond.RenderWithStatus(renderContextWriter{rc: rc}, rc.Request(), component, props, status); err != nil {
		return rc.Written(), err
	}
	return true, nil
}

// ReloadLocation returns the target an Inertia client reloads when a
// failed request gets no error page: the current URL for GET and HEAD, the
// path and query of a Referer the redirect host allowlist accepts
// otherwise (see bond.Bond.ReloadLocation).
func (e *Engine) ReloadLocation(r *http.Request) string {
	return e.bond.ReloadLocation(r)
}

// renderContextWriter is the http.ResponseWriter the error page renders
// through: headers go to the RenderContext's writer, the status line and
// body through the RenderContext itself, so the status is written once and
// the RenderContext knows the response is written.
type renderContextWriter struct {
	rc contract.RenderContext
}

func (w renderContextWriter) Header() http.Header         { return w.rc.Writer().Header() }
func (w renderContextWriter) WriteHeader(status int)      { w.rc.WriteHeader(status) }
func (w renderContextWriter) Write(p []byte) (int, error) { return w.rc.Write(p) }

// Unwrap returns the underlying writer for http.ResponseController.
func (w renderContextWriter) Unwrap() http.ResponseWriter { return w.rc.Writer() }
