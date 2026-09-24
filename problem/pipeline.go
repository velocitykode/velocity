package problem

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// HandleRequest reports err once and renders one response for it through
// rc. The order is fixed:
//
//  1. A bare contract.ErrResponseWritten ends the pipeline: the response
//     was written on purpose and there is nothing to report. A
//     contract.Handled cause is reported but never rendered.
//  2. User map rules replace the error for both report and render.
//  3. The report gate (see ShouldReport), then SelfReporting, ReportFor
//     rules, context merge, level selection and the reporters.
//  4. Rendering: nothing when the response is already written; a
//     Renderable error; the framework prepare table; user render rules;
//     framework render rules; content negotiation. A render that fails or
//     panics falls back to a plain-text 500.
//
// A nil ctx is replaced by one carrying the request facts; a ctx missing
// them is filled in.
func (h *Handler) HandleRequest(rc RenderContext, err error, ctx *ErrorContext) {
	if err == nil {
		return
	}
	s := h.snap()
	ctx = fillRequestContext(ctx, rc, s.trustedProxies)
	if s.debug && ctx.StackTrace == nil {
		ctx.StackTrace = contract.CaptureStackTrace(1)
	}

	marked := contract.IsReported(err)
	written := false
	if errors.Is(err, contract.ErrResponseWritten) {
		cause := contract.HandledCause(err)
		if cause == nil {
			return
		}
		err, written = cause, true
	}

	err = h.applyMap(s, err)
	if !marked {
		h.report(s, err, ctx, requestOf(rc))
	}
	if written || rc == nil {
		return
	}
	h.render(s, rc, err, ctx)
}

// Report sends err to the reporters when the report gate allows it. User
// map rules apply first. A nil ctx is replaced by a new one.
func (h *Handler) Report(err error, ctx *ErrorContext) {
	if err == nil {
		return
	}
	s := h.snap()
	if ctx == nil {
		ctx = NewErrorContext()
	}
	h.report(s, h.applyMap(s, err), ctx, nil)
}

// Render writes the response for err through rc, applying user map rules
// first. It never reports.
func (h *Handler) Render(rc RenderContext, err error, ctx *ErrorContext) {
	if err == nil || rc == nil {
		return
	}
	s := h.snap()
	ctx = fillRequestContext(ctx, rc, s.trustedProxies)
	h.render(s, rc, h.applyMap(s, err), ctx)
}

// ShouldReport reports whether err passes the report gate: not already
// reported; a recovered panic always passes; otherwise an unignore rule
// forces it through, or it must survive its own ShouldReport, the
// framework ignores (skipped when ShouldReport says true), the user ignores
// and the IgnoreIf predicates. Throttling is decided only when a report is
// made, so ShouldReport does not consume throttle budget.
func (h *Handler) ShouldReport(err error) bool {
	return h.passes(h.snap(), err, nil, nil, false)
}

// applyMap returns the replacement from the first matching user map rule,
// or err. A panicking rule is logged and leaves err unchanged.
func (h *Handler) applyMap(s *snapshot, err error) (out error) {
	out = err
	defer func() {
		if p := recover(); p != nil {
			safeLog(s.logger, "problem: map rule panicked", "panic", fmt.Sprint(p))
			out = err
		}
	}()
	for _, rule := range s.mapRules {
		if !rule.Match(err) {
			continue
		}
		if mapped := rule.Map(err); mapped != nil {
			return mapped
		}
		return err
	}
	return err
}

// isRecovered reports whether err came from a recovered panic.
func isRecovered(err error, ctx *ErrorContext) bool {
	if ctx != nil && ctx.Recovered {
		return true
	}
	return panicerr.AsTyped(err) != nil
}

// passes runs the report gate. r, when non-nil, lets a cancelled request
// with a dead context be ignored; consume enables throttling.
func (h *Handler) passes(s *snapshot, err error, ctx *ErrorContext, r *http.Request, consume bool) bool {
	if err == nil || contract.IsReported(err) {
		return false
	}
	if isRecovered(err, ctx) {
		return true
	}
	if !anyIgnoreMatch(s.unignoreRules, err) {
		ownDecision := false
		var rep contract.Reportable
		if errors.As(err, &rep) {
			if !rep.ShouldReport() {
				return false
			}
			ownDecision = true
		}
		if !ownDecision {
			if anyIgnoreMatch(s.frameworkIgnores, err) {
				return false
			}
			if errors.Is(err, context.Canceled) && requestGone(r) {
				return false
			}
		}
		if anyIgnoreMatch(s.ignoreRules, err) {
			return false
		}
		for _, pred := range s.ignorePredicates {
			if pred(err, ctx) {
				return false
			}
		}
	}
	if consume {
		for _, rule := range s.throttleRules {
			if rule.Match(err) {
				return h.throttle.allow(rule, err)
			}
		}
	}
	return true
}

func anyIgnoreMatch(rules []contract.IgnoreRule, err error) bool {
	for _, rule := range rules {
		if rule.Match(err) {
			return true
		}
	}
	return false
}

// report runs the gate and, when it passes, SelfReporting, the ReportFor
// rules, the context merge, level selection and the reporters. A panic in
// any of them is logged and ends the report; it never reaches the caller.
func (h *Handler) report(s *snapshot, err error, ctx *ErrorContext, r *http.Request) {
	defer func() {
		if p := recover(); p != nil {
			safeLog(s.logger, "problem: report failed", "panic", fmt.Sprint(p), "error", err.Error())
		}
	}()
	if !h.passes(s, err, ctx, r, true) {
		return
	}
	if isRecovered(err, ctx) {
		ctx.Recovered = true
	}

	var self contract.SelfReporting
	if errors.As(err, &self) && self.ReportError(ctx) {
		return
	}
	for _, rule := range s.reportRules {
		if rule.Match(err) && rule.Report(err, ctx) {
			return
		}
	}

	var contextual contract.Contextual
	if errors.As(err, &contextual) {
		for k, v := range contextual.Context() {
			ctx.WithExtra(k, v)
		}
	}
	for _, provider := range s.contextProviders {
		for k, v := range provider(err, ctx) {
			ctx.WithExtra(k, v)
		}
	}

	ctx.Level = selectLevel(s, err, ctx.Level)

	for _, reporter := range s.reporters {
		callReporter(s.logger, reporter, err, ctx)
	}
}

// selectLevel returns the level of the first matching user level rule, then
// framework level rule; otherwise current, or error when current is unset.
func selectLevel(s *snapshot, err error, current contract.LogLevel) contract.LogLevel {
	for _, rule := range s.levelRules {
		if rule.Match(err) {
			return rule.Level
		}
	}
	for _, rule := range s.frameworkLevels {
		if rule.Match(err) {
			return rule.Level
		}
	}
	if current == contract.LogLevelUnset {
		return contract.LogLevelError
	}
	return current
}

// callReporter calls one reporter; a panic is logged so the remaining
// reporters still run.
func callReporter(logger contract.Logger, reporter Reporter, err error, ctx *ErrorContext) {
	defer func() {
		if p := recover(); p != nil {
			safeLog(logger, "problem: reporter panicked", "panic", fmt.Sprint(p), "error", err.Error())
		}
	}()
	reporter.Report(err, ctx)
}

// render runs the render stage under one recover: a panic anywhere in it is
// logged through the handler logger and answered with the plain-text 500
// when nothing was written yet.
func (h *Handler) render(s *snapshot, rc RenderContext, err error, ctx *ErrorContext) {
	defer func() {
		if p := recover(); p != nil {
			safeLog(s.logger, "problem: rendering panicked", "panic", fmt.Sprint(p), "error", err.Error())
			lastResort(s.logger, rc)
		}
	}()
	if rc.Written() {
		return
	}
	if isRecovered(err, ctx) {
		// A panic is a bug: always a 500, whatever the panic value carries.
		h.negotiate(s, rc, contract.NewHTTPError(http.StatusInternalServerError).WithCause(err), ctx, http.StatusInternalServerError)
		return
	}

	var renderable contract.Renderable
	if errors.As(err, &renderable) && renderable.RenderError(rc, ctx) {
		return
	}
	if rc.Written() {
		return
	}

	prepared := prepare(s, err)
	if h.applyRenderRules(s, s.renderRules, rc, prepared, ctx) {
		return
	}
	if h.applyRenderRules(s, s.frameworkRender, rc, prepared, ctx) {
		return
	}
	status, _, _ := contract.StatusOf(prepared)
	h.negotiate(s, rc, prepared, ctx, status)
}

// prepare returns the replacement from the first matching framework prepare
// rule, or err.
func prepare(s *snapshot, err error) error {
	for _, rule := range s.frameworkPrepare {
		if rule.Match(err) {
			if mapped := rule.Map(err); mapped != nil {
				return mapped
			}
			return err
		}
	}
	return err
}

// applyRenderRules runs rules in order and reports whether one handled the
// response. A rule with Render returning false (and writing nothing) falls
// through; a Status rule always handles it through negotiation.
func (h *Handler) applyRenderRules(s *snapshot, rules []contract.RenderRule, rc RenderContext, err error, ctx *ErrorContext) bool {
	for _, rule := range rules {
		if !rule.Match(err) {
			continue
		}
		if rule.Render == nil {
			h.negotiate(s, rc, err, ctx, rule.Status)
			return true
		}
		if rule.Render(rc, err, ctx) || rc.Written() {
			return true
		}
	}
	return false
}

// negotiate copies the error's headers, runs the BeforeRender hooks with
// status, and renders JSON, the Inertia branch or HTML. A renderer error
// with nothing written falls back to the plain-text 500.
func (h *Handler) negotiate(s *snapshot, rc RenderContext, err error, ctx *ErrorContext, status int) {
	_, headers, _ := contract.StatusOf(err)
	setHeaders(rc, headers)
	for _, hook := range s.beforeRender {
		status = hook(rc, err, status)
	}
	if status < 100 || status > 999 {
		status = http.StatusInternalServerError
	}

	var renderErr error
	switch {
	case wantsJSON(s, rc, err):
		renderErr = rendererFor(s, "json").Render(rc, err, ctx, status, s.debug)
	case rc.IsInertia():
		renderErr = h.renderInertia(s, rc, err, ctx, status)
	default:
		renderErr = rendererFor(s, "html").Render(rc, err, ctx, status, s.debug)
	}
	if renderErr != nil {
		safeLog(s.logger, "problem: rendering failed", "render_error", renderErr.Error(), "error", err.Error())
		lastResort(s.logger, rc)
	}
}

// renderInertia answers an Inertia request: the configured error page at
// status; failing that, the debug page at status in debug mode; otherwise a
// 409 with X-Inertia-Location so the client reloads the page as a full
// visit.
func (h *Handler) renderInertia(s *snapshot, rc RenderContext, err error, ctx *ErrorContext, status int) error {
	if s.errorPage != nil {
		ok, pageErr := s.errorPage.RenderErrorPage(rc, status, clientMessage(err, status, s.debug))
		if ok || rc.Written() {
			return pageErr
		}
		if pageErr != nil {
			safeLog(s.logger, "problem: inertia error page failed", "render_error", pageErr.Error())
		}
	}
	if s.debug {
		return rendererFor(s, "html").Render(rc, err, ctx, status, true)
	}
	rc.SetHeader("X-Inertia-Location", inertiaLocation(rc.Request()))
	rc.WriteHeader(http.StatusConflict)
	return nil
}

// inertiaLocation returns the same-origin path an Inertia client reloads:
// the current URL for GET and HEAD, the Referer's path and query when it is
// same-origin, and "/" otherwise.
func inertiaLocation(r *http.Request) string {
	if r == nil {
		return "/"
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		if r.URL != nil && isLocalPath(r.URL.RequestURI()) {
			return r.URL.RequestURI()
		}
		return "/"
	}
	if loc := sameOriginReferer(r); loc != "" {
		return loc
	}
	return "/"
}

// wantsJSON decides the JSON branch: the JSONWhen predicate alone when set,
// otherwise API mode, an API prefix, then the request's own negotiation.
func wantsJSON(s *snapshot, rc RenderContext, err error) bool {
	r := rc.Request()
	if s.jsonWhen != nil {
		return s.jsonWhen(r, err)
	}
	if s.apiMode {
		return true
	}
	if r != nil && r.URL != nil {
		for _, prefix := range s.apiPrefixes {
			if prefix != "" && strings.HasPrefix(r.URL.Path, prefix) {
				return true
			}
		}
	}
	return rc.WantsJSON()
}

// rendererFor returns the configured renderer for key, or the built-in one.
func rendererFor(s *snapshot, key string) Renderer {
	if r, ok := s.renderers[key]; ok && r != nil {
		return r
	}
	if key == "json" {
		return NewJSONRenderer()
	}
	return NewHTMLRenderer()
}

// setHeaders copies headers onto the response through rc, which drops any
// key or value containing CR or LF.
func setHeaders(rc RenderContext, headers http.Header) {
	for k, vs := range headers {
		for i, v := range vs {
			if i == 0 {
				rc.SetHeader(k, v)
				continue
			}
			if !strings.ContainsAny(k, "\r\n") && !strings.ContainsAny(v, "\r\n") {
				rc.Writer().Header().Add(k, v)
			}
		}
	}
}

// lastResort writes the plain-text 500 when nothing was written. It runs
// under its own recover: a failure is logged and never re-panics.
func lastResort(logger contract.Logger, rc RenderContext) {
	defer func() {
		if p := recover(); p != nil {
			safeLog(logger, "problem: last-resort response failed", "panic", fmt.Sprint(p))
		}
	}()
	if rc.Written() {
		return
	}
	rc.SetHeader("Content-Type", "text/plain; charset=utf-8")
	rc.SetHeader("X-Content-Type-Options", "nosniff")
	rc.WriteHeader(http.StatusInternalServerError)
	_, _ = rc.Write([]byte(http.StatusText(http.StatusInternalServerError)))
}

// safeLog logs at error level and swallows a panicking logger.
func safeLog(logger contract.Logger, msg string, kvs ...any) {
	if logger == nil {
		return
	}
	defer func() { _ = recover() }()
	logger.Error(msg, kvs...)
}

// clientMessage returns the client-facing message for err at status: the
// full Error() in debug; the status title for 5xx; for 4xx the Message of
// the HTTPError in err's chain when it names the same status, else the
// status title.
func clientMessage(err error, status int, debug bool) string {
	if debug {
		return err.Error()
	}
	if status >= http.StatusInternalServerError {
		return statusTitle(status)
	}
	var he *contract.HTTPError
	if errors.As(err, &he) && he.StatusCode() == status && he.Message != "" {
		return he.Message
	}
	return statusTitle(status)
}
