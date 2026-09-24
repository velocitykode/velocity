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
//     Renderable error; the framework prepare table (only for an error
//     that names no status); user render rules; framework render rules;
//     content negotiation. A recovered panic skips Renderable and the
//     prepare table and reaches the rules wrapped in a 500 HTTPError,
//     with every Status rule answering 500. A render that fails or panics
//     falls back to a plain-text 500. Everything in this stage reads the
//     handler's negotiation answer (see WantsJSON) through rc.WantsJSON.
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
// forces it through, or it must survive the dead-request cancel ignore
// (applied whatever the error's own ShouldReport says), its own
// ShouldReport, the framework ignores (skipped when ShouldReport says
// true), the user ignores and the IgnoreIf predicates. Throttling is
// decided only when a report is made, so ShouldReport does not consume
// throttle budget.
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
		// A cancel whose request context is dead is a fact about the
		// request (the client went away), not a property of the error, so
		// no ShouldReport answer overrides it: a Timeout 503 wrapping the
		// cancel is dropped like the bare cancel.
		if errors.Is(err, context.Canceled) && requestGone(r) {
			return false
		}
		ownDecision := false
		var rep contract.Reportable
		if errors.As(err, &rep) {
			if !rep.ShouldReport() {
				return false
			}
			ownDecision = true
		}
		if !ownDecision && anyIgnoreMatch(s.frameworkIgnores, err) {
			return false
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
	// Every rule and renderer below reads the handler's negotiation
	// answer through rc.WantsJSON, so a rule that picks between JSON and a
	// browser answer agrees with the negotiation that follows it.
	rc = negotiatedContext{RenderContext: rc, json: wantsJSON(s, rc, err)}

	// A panic is a bug: always a 500, whatever the panic value carries. The
	// panic never renders itself (no Renderable) and skips the prepare
	// table; the render rules see it wrapped in a 500 HTTPError, and a
	// Status rule answers at 500 whatever status it names.
	pinned := 0
	var prepared error
	if isRecovered(err, ctx) {
		pinned = http.StatusInternalServerError
		prepared = contract.NewHTTPError(pinned).WithCause(err)
	} else {
		var renderable contract.Renderable
		if errors.As(err, &renderable) && renderable.RenderError(rc, ctx) {
			return
		}
		if rc.Written() {
			return
		}
		prepared = prepare(s, err)
	}

	if h.applyRenderRules(s, s.renderRules, rc, prepared, ctx, pinned) {
		return
	}
	if h.applyRenderRules(s, s.frameworkRender, rc, prepared, ctx, pinned) {
		return
	}
	status, _, _ := contract.StatusOf(prepared)
	h.negotiate(s, rc, prepared, ctx, status)
}

// prepare returns the replacement from the first matching framework prepare
// rule, or err. The table only gives a status to an error that names none:
// an error whose chain already holds a StatusError (an application
// HTTPError, a user map result, a typed subsystem error) keeps itself, so
// its status, message and headers are never replaced by the framework's.
func prepare(s *snapshot, err error) error {
	if _, _, ok := contract.StatusOf(err); ok {
		return err
	}
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
// through; a Status rule always handles it through negotiation, at pinned
// instead of its own status when pinned is non-zero.
func (h *Handler) applyRenderRules(s *snapshot, rules []contract.RenderRule, rc RenderContext, err error, ctx *ErrorContext, pinned int) bool {
	for _, rule := range rules {
		if !rule.Match(err) {
			continue
		}
		if rule.Render == nil {
			status := rule.Status
			if pinned != 0 {
				status = pinned
			}
			h.negotiate(s, rc, err, ctx, status)
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
	h.respond(s, rc, err, ctx, status, false)
}

// RenderJSON renders err as JSON through the configured JSON renderer at
// the status contract.StatusOf resolves, after copying the error's headers
// and running the BeforeRender hooks, whatever the request negotiates. It
// applies no map or render rule and never reports. It returns true when
// the response was written, false when err or rc is nil or a response was
// already written. A render rule uses it to answer with JSON a request the
// negotiation would answer otherwise; a render failure falls back to the
// plain-text 500.
func (h *Handler) RenderJSON(rc RenderContext, err error, ctx *ErrorContext) bool {
	if err == nil || rc == nil || rc.Written() {
		return false
	}
	s := h.snap()
	ctx = fillRequestContext(ctx, rc, s.trustedProxies)
	defer func() {
		if p := recover(); p != nil {
			safeLog(s.logger, "problem: rendering panicked", "panic", fmt.Sprint(p), "error", err.Error())
			lastResort(s.logger, rc)
		}
	}()
	status, _, _ := contract.StatusOf(err)
	h.respond(s, rc, err, ctx, status, true)
	return rc.Written()
}

// respond is negotiate with the JSON branch forced when forceJSON is set.
func (h *Handler) respond(s *snapshot, rc RenderContext, err error, ctx *ErrorContext, status int, forceJSON bool) {
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
	case forceJSON || wantsJSON(s, rc, err):
		renderErr = rendererFor(s, "json").Render(rc, err, ctx, status, s.debug)
	case rc.IsInertia():
		renderErr = h.renderInertia(s, rc, err, ctx, status)
	default:
		renderErr = h.renderHTML(s, rc, err, ctx, status)
	}
	if renderErr != nil {
		safeLog(s.logger, "problem: rendering failed", "render_error", renderErr.Error(), "error", err.Error())
		lastResort(s.logger, rc)
	}
}

// renderInertia answers an Inertia request: the debug page at status in
// debug mode; otherwise the configured error page at status; failing that,
// a 409 with X-Inertia-Location so the client reloads the page as a full
// visit.
func (h *Handler) renderInertia(s *snapshot, rc RenderContext, err error, ctx *ErrorContext, status int) error {
	if s.debug {
		return rendererFor(s, "html").Render(rc, err, ctx, status, true)
	}
	if answered, pageErr := renderErrorPage(s, rc, err, status); answered {
		return pageErr
	}
	rc.SetHeader("X-Inertia-Location", reloadLocation(s.errorPage, rc.Request()))
	rc.WriteHeader(http.StatusConflict)
	return nil
}

// renderHTML answers a full-page request: the configured error page at
// status outside debug mode, else the HTML renderer (the debug page in
// debug mode).
func (h *Handler) renderHTML(s *snapshot, rc RenderContext, err error, ctx *ErrorContext, status int) error {
	if !s.debug {
		if answered, pageErr := renderErrorPage(s, rc, err, status); answered {
			return pageErr
		}
	}
	return rendererFor(s, "html").Render(rc, err, ctx, status, s.debug)
}

// renderErrorPage asks the error page renderer to answer at status and
// reports whether it did (or wrote anything). A renderer that declines
// with an error, having written nothing, is logged and the caller answers.
func renderErrorPage(s *snapshot, rc RenderContext, err error, status int) (bool, error) {
	if s.errorPage == nil {
		return false, nil
	}
	ok, pageErr := s.errorPage.RenderErrorPage(rc, status, clientMessage(err, status, s.debug))
	if ok || rc.Written() {
		return true, pageErr
	}
	if pageErr != nil {
		safeLog(s.logger, "problem: error page failed", "render_error", pageErr.Error())
	}
	return false, nil
}

// wantsJSON decides the JSON branch for rc: the JSONWhen predicate alone
// when set, otherwise API mode, an API prefix, then the request's own
// negotiation.
func wantsJSON(s *snapshot, rc RenderContext, err error) bool {
	return negotiatesJSON(s, rc.Request(), err, rc.WantsJSON)
}

// negotiatesJSON is the negotiation order shared by the pipeline and
// Handler.WantsJSON; fallback answers when neither JSONWhen, API mode nor
// an API prefix decides.
func negotiatesJSON(s *snapshot, r *http.Request, err error, fallback func() bool) bool {
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
	return fallback()
}

// negotiatedContext is the RenderContext the render stage hands to
// Renderable errors, render rules and renderers: WantsJSON reports the
// handler's negotiation answer (JSONWhen, API mode, API prefixes, then the
// request) instead of the request's Accept header alone.
type negotiatedContext struct {
	RenderContext
	json bool
}

// WantsJSON reports the handler's negotiation answer.
func (c negotiatedContext) WantsJSON() bool { return c.json }

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
// full Error() in debug; the status title for 5xx; for 4xx the
// ClientMessage of the first contract.MessageError in err's chain when it
// names the same status, else the status title.
func clientMessage(err error, status int, debug bool) string {
	if debug {
		return err.Error()
	}
	if status >= http.StatusInternalServerError {
		return statusTitle(status)
	}
	var me contract.MessageError
	if errors.As(err, &me) && me.StatusCode() == status {
		if msg := me.ClientMessage(); msg != "" {
			return msg
		}
	}
	return statusTitle(status)
}
