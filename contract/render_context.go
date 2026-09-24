package contract

import (
	"errors"
	"net/http"
	"strings"
)

// RenderContext is the surface the error pipeline renders through. The
// router supplies its own implementation; NewRenderContext is the bare
// net/http one.
type RenderContext interface {
	// Request returns the request being answered.
	Request() *http.Request
	// Writer returns the underlying response writer.
	Writer() http.ResponseWriter
	// WriteHeader writes the status line once; later calls are no-ops.
	WriteHeader(status int)
	// Write writes body bytes, writing a 200 status first when none was
	// written.
	Write(p []byte) (int, error)
	// SetHeader sets a response header. A key or value containing CR or
	// LF is dropped.
	SetHeader(key, value string)
	// Written reports whether the status line has been written.
	Written() bool
	// WantsJSON reports whether the request asks for JSON (see WantsJSON).
	WantsJSON() bool
	// IsInertia reports whether the request is an Inertia request.
	IsInertia() bool
	// Redirect answers with a redirect to target. An unsafe target or a
	// non-3xx status writes nothing and returns an error.
	Redirect(status int, target string) error
}

// ErrInvalidRedirect is matched (errors.Is) by the error a RenderContext
// returns from Redirect for an unsafe target, a non-3xx status, or a
// response already written.
var ErrInvalidRedirect = errors.New("velocity: invalid redirect")

// CommitReporter is a response writer that knows whether its response is
// committed: the status line (or a body byte, or a flush) already went
// out, so a second response cannot be written.
type CommitReporter interface {
	Committed() bool
}

// NewRenderContext returns the net/http RenderContext for w and r.
// WriteHeader is idempotent, SetHeader drops CR/LF, and Redirect accepts
// only a path with a single leading slash that SanitizeRedirect accepts
// with no allowed hosts (no "//", no backslash or slash lookalike, no
// control bytes, no edge space).
//
// When w implements CommitReporter, Written also reports true while
// w.Committed does, so a response the handler already committed through w
// counts as written: WriteHeader and Redirect then write nothing, and the
// error pipeline renders nothing over it. Any other writer counts as
// written only after this RenderContext writes to it.
func NewRenderContext(w http.ResponseWriter, r *http.Request) RenderContext {
	return &httpRenderContext{w: w, r: r}
}

// httpRenderContext is the RenderContext over a bare ResponseWriter. It is
// used by one goroutine for one error, like the ResponseWriter it wraps.
type httpRenderContext struct {
	w       http.ResponseWriter
	r       *http.Request
	written bool
}

func (c *httpRenderContext) Request() *http.Request      { return c.r }
func (c *httpRenderContext) Writer() http.ResponseWriter { return c.w }
func (c *httpRenderContext) WantsJSON() bool             { return WantsJSON(c.r) }
func (c *httpRenderContext) IsInertia() bool             { return IsInertia(c.r) }

// Written reports whether this RenderContext wrote the status line, or
// the writer reports its response committed (see CommitReporter).
func (c *httpRenderContext) Written() bool {
	if c.written {
		return true
	}
	cr, ok := c.w.(CommitReporter)
	return ok && cr.Committed()
}

// WriteHeader writes status once, and never over a committed response. A
// status outside 100-999 is written as 500 because net/http rejects it.
// The write is recorded once w returns, so a WriteHeader that panics
// before committing (a panicking pre-commit hook) leaves the response
// unwritten for a fallback.
func (c *httpRenderContext) WriteHeader(status int) {
	if c.Written() {
		return
	}
	c.w.WriteHeader(validStatus(status))
	c.written = true
}

// Write writes p, writing a 200 status first when none was written.
func (c *httpRenderContext) Write(p []byte) (int, error) {
	if !c.Written() {
		c.WriteHeader(http.StatusOK)
	}
	return c.w.Write(p)
}

// SetHeader sets key to value, dropping an empty key or any CR or LF.
func (c *httpRenderContext) SetHeader(key, value string) {
	if key == "" || hasCRLF(key) || hasCRLF(value) {
		return
	}
	c.w.Header().Set(key, value)
}

// Redirect writes a Location header and status. The status must be 3xx
// (else a 500 HTTPError) and the target must be a path starting with "/"
// that SanitizeRedirect returns unchanged with no allowed hosts (else a
// 400 HTTPError); both wrap ErrInvalidRedirect and write nothing, as does
// a call after the response was written. The Location header stays only
// once the status write is recorded: a status write that panics before
// committing restores the header as it was, so a fallback response never
// carries the refused redirect's target.
func (c *httpRenderContext) Redirect(status int, target string) error {
	if c.Written() {
		return &HTTPError{Status: http.StatusInternalServerError, Cause: ErrInvalidRedirect}
	}
	if status < 300 || status > 399 {
		return &HTTPError{Status: http.StatusInternalServerError, Cause: ErrInvalidRedirect}
	}
	if !strings.HasPrefix(target, "/") || SanitizeRedirect(target, nil) != target {
		return &HTTPError{Status: http.StatusBadRequest, Cause: ErrInvalidRedirect}
	}
	h := c.w.Header()
	prior, hadPrior := h["Location"]
	defer func() {
		if !c.written {
			restoreLocation(h, prior, hadPrior)
		}
	}()
	h.Set("Location", target)
	c.WriteHeader(status)
	return nil
}

// restoreLocation puts h's Location header back to prior, or removes it
// when there was none.
func restoreLocation(h http.Header, prior []string, hadPrior bool) {
	if hadPrior {
		h["Location"] = prior
		return
	}
	h.Del("Location")
}
