package problem

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// HandleRequest reports err once and renders one response for it through
// rc. The order is fixed:
//
//  1. A bare contract.ErrResponseWritten ends the pipeline: the response
//     was written on purpose and there is nothing to report. A
//     contract.Handled cause is reported but never rendered. A recovered
//     panic is a reported 500 whatever its value: a response-written or
//     report-once marker the panic value carries counts for nothing.
//  2. User map rules replace the error for both report and render. Whether
//     the error is a recovered panic is decided first, on the error as it
//     arrived (see markRecovered), so a recovered panic stays a reported
//     500 whatever a map rule returns for it.
//  3. The report gate (see ShouldReport), then SelfReporting, ReportFor
//     rules, context merge, level selection and the reporters.
//  4. Rendering: nothing when the response is already written; a
//     context.Canceled on a request the server cut off while shutting
//     down (its context's cause is contract.ErrServerShuttingDown) becomes
//     a 503 with Retry-After: 1 and Connection: close, logged at warn and
//     never reported, while one whose client went away renders nothing; a
//     Renderable error; the framework prepare table (only for an error
//     that names no status); user render rules; framework render rules;
//     content negotiation. A recovered panic skips Renderable, the
//     prepare table and the framework render rules, and reaches the user
//     render rules wrapped in a 500 HTTPError, with every Status rule
//     answering 500. A render that fails or panics
//     falls back to a plain-text 500. The stage negotiates once, on the
//     error as it will be rendered (after the prepare table, or the 500
//     or 503 above), and everything in it reads that answer (see
//     WantsJSON) through rc.WantsJSON.
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
	markRecovered(err, ctx)

	marked := outsidePanic(err, ctx, contract.IsReported)
	written := false
	if outsidePanic(err, ctx, contract.IsResponseWritten) {
		cause := contract.HandledCause(err)
		if cause == nil {
			return
		}
		err, written = cause, true
	}

	err = h.applyMap(s, err)
	if !marked {
		r := requestOf(rc)
		h.report(s, err, ctx, r)
		if serverCancelled(err, r) {
			safeWarn(s.logger, "problem: request cut off by server shutdown", "error", err.Error(), "method", r.Method, "path", requestPath(r))
		}
	}
	if written || rc == nil {
		return
	}
	h.render(s, rc, err, ctx)
}

// Report sends err to the reporters when the report gate allows it. The
// report-once marker is read from err before user map rules apply, as in
// HandleRequest, so a map rule that builds a new error cannot drop it: a
// marked err (outside the value of a recovered panic) is not reported.
// Otherwise the mapped error is reported; a recovered panic is decided on
// err before the map rules (see markRecovered) and always reported. A nil
// ctx is replaced by a new one.
func (h *Handler) Report(err error, ctx *ErrorContext) {
	if err == nil {
		return
	}
	s := h.snap()
	if ctx == nil {
		ctx = NewErrorContext()
	}
	if outsidePanic(err, ctx, contract.IsReported) {
		return
	}
	markRecovered(err, ctx)
	h.report(s, h.applyMap(s, err), ctx, nil)
}

// Render writes the response for err through rc, applying user map rules
// first. It never reports. As in HandleRequest, an err marking the response
// written (a bare contract.ErrResponseWritten or a contract.Handled value)
// writes nothing, checked before the map rules; a marker the value of a
// recovered panic carries counts for nothing, so that panic still renders
// its 500, whatever a map rule returns for it (see markRecovered).
func (h *Handler) Render(rc RenderContext, err error, ctx *ErrorContext) {
	if err == nil || rc == nil || outsidePanic(err, ctx, contract.IsResponseWritten) {
		return
	}
	s := h.snap()
	ctx = fillRequestContext(ctx, rc, s.trustedProxies)
	markRecovered(err, ctx)
	h.render(s, rc, h.applyMap(s, err), ctx)
}

// ShouldReport reports whether err passes the report gate: not already
// reported (a marker inside a recovered panic's value does not count); a
// recovered panic always passes; otherwise an unignore rule
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

// markRecovered sets ctx.Recovered when err came from a recovered panic
// (see isRecovered). The entry points call it on the error as it arrived,
// before the user map rules, as the router boundary decides it: MapIs and
// MapFor reach the panic value through Unwrap, so a rule can replace the
// panic with an error that no longer carries it, and the report gate and
// the render stage must still see a recovered panic (reported, answered
// with a 500). Marker checks on err give the same answer before and after
// it, because err itself carries the panic when ctx did not flag it.
func markRecovered(err error, ctx *ErrorContext) {
	if isRecovered(err, ctx) {
		ctx.Recovered = true
	}
}

// isRecovered reports whether err came from a recovered panic: ctx flags
// it, or err's chain holds a contract.RecoveredPanic node (the framework's
// own recovered-panic errors, or any other error implementing the facet).
func isRecovered(err error, ctx *ErrorContext) bool {
	if ctx != nil && ctx.Recovered {
		return true
	}
	return carriesRecoveredPanic(err)
}

// outsidePanic reports whether the marker predicate match (a contract
// predicate such as contract.IsReported or contract.IsResponseWritten)
// holds for err outside the value of a recovered panic. The contract
// predicates never look inside a contract.RecoveredPanic node, so a marker
// wrapped around the panic (a middleware that rendered or reported it)
// counts and one inside the panic value does not. When ctx flags the error
// recovered but err carries no RecoveredPanic node, the whole of err is
// the panic value and no marker counts.
func outsidePanic(err error, ctx *ErrorContext, match func(error) bool) bool {
	if ctx != nil && ctx.Recovered && !carriesRecoveredPanic(err) {
		return false
	}
	return match(err)
}

// carriesRecoveredPanic reports whether err's chain holds a
// contract.RecoveredPanic node.
func carriesRecoveredPanic(err error) bool {
	var rp contract.RecoveredPanic
	return errors.As(err, &rp)
}

// passes runs the report gate. r, when non-nil, lets a cancelled request
// with a dead context be ignored; consume enables throttling.
func (h *Handler) passes(s *snapshot, err error, ctx *ErrorContext, r *http.Request, consume bool) bool {
	if err == nil || outsidePanic(err, ctx, contract.IsReported) {
		return false
	}
	if isRecovered(err, ctx) {
		return true
	}
	if !anyIgnoreMatch(s.unignoreRules, err) {
		// A cancel whose request context is dead is a fact about the
		// request (the client went away, or the server cut it off while
		// shutting down), not a property of the error, so no ShouldReport
		// answer overrides it: a Timeout 503 wrapping the cancel is
		// dropped like the bare cancel.
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

// stage runs one render through rc: nothing when a response was
// already written; otherwise the Content-Length the handler staged is
// dropped, then write runs. Every write the pipeline makes (Renderable
// errors, render rules, renderers, the error page and the last resort)
// happens inside a stage, after that drop: the pipeline replaces the
// body, and a server enforcing the stale length would reject it.
// Content-Encoding is left to whoever wraps the writer, as http.Error
// does: a compressing middleware that set it up front compresses the
// rendered body too.
//
// A panic in write (a renderer, a rule, or a pre-commit hook the response
// writer fires) is a bug of its own: it is reported through the reporter
// chain as a recovered panic (see reportRenderPanic) and answered with the
// plain-text 500 when nothing was written yet. RequestFailed is the
// router's event: the router decides it from the status written once the
// pipeline returned, so that 500 dispatches it with the error the
// pipeline was rendering, not flagged recovered. A panic with net/http's
// http.ErrAbortHandler is not a bug: it is passed on, unreported, so
// net/http aborts the response.
func (h *Handler) stage(s *snapshot, rc RenderContext, ctx *ErrorContext, write func()) {
	defer func() {
		if p := recover(); p != nil {
			if pe, ok := p.(error); ok && errors.Is(pe, http.ErrAbortHandler) {
				panic(p)
			}
			h.reportRenderPanic(s, rc, ctx, p)
			lastResort(s.logger, rc)
		}
	}()
	if rc.Written() {
		return
	}
	if w := rc.Writer(); w != nil {
		// The key is a canonical constant, so the map delete equals Header.Del.
		delete(w.Header(), "Content-Length")
	}
	write()
}

// reportRenderPanic reports p, a panic recovered while rendering, through
// the report gate and the reporters as a recovered panic: the error is the
// recovered value as a contract.RecoveredPanic, and a new ErrorContext
// carries the request facts of ctx, Recovered set and both stacks. It must
// be called by the deferred function that recovered p, so the stacks it
// captures still hold the panicking frames. The gate honours a report-once
// marker as for any report (one outside the panic value counts).
func (h *Handler) reportRenderPanic(s *snapshot, rc RenderContext, ctx *ErrorContext, p any) {
	pctx := NewErrorContext()
	if ctx != nil {
		pctx.RequestID, pctx.TraceID, pctx.SpanID, pctx.UserID = ctx.RequestID, ctx.TraceID, ctx.SpanID, ctx.UserID
		pctx.URL, pctx.Method, pctx.IP, pctx.UserAgent = ctx.URL, ctx.Method, ctx.IP, ctx.UserAgent
	}
	pctx = fillRequestContext(pctx, rc, s.trustedProxies)
	pctx.Timestamp = time.Now()
	pctx.Recovered = true
	pctx.PanicStack = string(debug.Stack())
	// Skip this function and the deferred one so the trace starts at the
	// panic site.
	pctx.StackTrace = contract.CaptureStackTrace(2)
	h.report(s, panicerr.FromRecovered(p), pctx, requestOf(rc))
}

// render runs the render stage (see stage).
func (h *Handler) render(s *snapshot, rc RenderContext, err error, ctx *ErrorContext) {
	h.stage(s, rc, ctx, func() { h.renderStage(s, rc, err, ctx) })
}

// renderStage renders err through rc inside a stage.
//
// It first builds the error the stage renders (a recovered panic wrapped
// in a pinned 500 HTTPError, a shutdown cancel as its 503, otherwise the
// framework prepare table's replacement) and negotiates once, on that
// error. Every rule and renderer below reads the answer through
// rc.WantsJSON and the final negotiation reuses it, so a JSONWhen
// predicate is asked once per failure and a rule that picks between JSON
// and a browser answer agrees with the response that follows it.
func (h *Handler) renderStage(s *snapshot, rc RenderContext, err error, ctx *ErrorContext) {
	// A panic is a bug: always a 500, whatever the panic value carries. The
	// panic never renders itself (no Renderable) and skips the prepare
	// table and the framework render rules (a subsystem default would
	// answer the error the panic carries, not the 500); the user render
	// rules see it wrapped in a 500 HTTPError, and a Status rule answers
	// at 500 whatever status it names.
	pinned := 0
	dispatch := false
	var prepared error
	switch {
	case isRecovered(err, ctx):
		pinned = http.StatusInternalServerError
		prepared = contract.NewHTTPError(pinned).WithCause(err)
	case serverCancelled(err, rc.Request()):
		// Checked before the error's own status, as the router does: a
		// Timeout 503 wrapping the cancel answers as the shutdown.
		prepared = serverShutdownError(err)
	default:
		dispatch = true
		prepared = prepare(s, err)
	}
	asJSON, byRequest := wantsJSON(s, rc, prepared)
	rc = negotiatedContext{RenderContext: rc, json: asJSON, byRequest: byRequest}

	if dispatch {
		// A Renderable error answers for itself, checked on the error as
		// the handler returned it.
		var renderable contract.Renderable
		if errors.As(err, &renderable) && renderable.RenderError(rc, ctx) {
			return
		}
		if rc.Written() {
			return
		}
	}

	if h.applyRenderRules(s, s.renderRules, rc, prepared, ctx, pinned) {
		return
	}
	if pinned == 0 && h.applyRenderRules(s, s.frameworkRender, rc, prepared, ctx, 0) {
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
	h.stage(s, rc, ctx, func() {
		status, _, _ := contract.StatusOf(err)
		h.respond(s, rc, err, ctx, status, true)
	})
	return rc.Written()
}

// respond is negotiate with the JSON branch forced when forceJSON is set.
//
// An answer other than the JSON body is a page (see pagePolicy): its
// Cache-Control is set to pageCacheControl after the error's own headers
// are copied and before the BeforeRender hooks run, unless the error's
// headers carry a Cache-Control.
func (h *Handler) respond(s *snapshot, rc RenderContext, err error, ctx *ErrorContext, status int, forceJSON bool) {
	_, headers, _ := contract.StatusOf(err)
	setHeaders(rc, headers)

	asJSON, byRequest := forceJSON, false
	if !forceJSON {
		// The render stage negotiated once, on the error it renders; only
		// a RenderContext that did not come from it negotiates here.
		if nc, ok := rc.(negotiatedContext); ok {
			asJSON, byRequest = nc.json, nc.byRequest
		} else {
			asJSON, byRequest = wantsJSON(s, rc, err)
		}
	}
	var policy pagePolicy
	if !asJSON {
		policy.apply(rc, headers)
		// A panic on the way (a hook, a renderer) leaves the answer to
		// the plain-text last resort, which is not a page.
		defer policy.undo(rc)
	}

	for _, hook := range s.beforeRender {
		status = hook(rc, err, status)
	}
	if status < 100 || status > 999 {
		status = http.StatusInternalServerError
	}

	var renderErr error
	switch {
	case asJSON:
		varyOnNegotiation(rc, !forceJSON, byRequest)
		if rc.IsInertia() {
			dropPageMarker(rc)
		}
		renderErr = rendererFor(s, "json").Render(rc, err, ctx, status, s.debug)
	case rc.IsInertia():
		varyOnNegotiation(rc, true, false)
		renderErr = h.renderInertia(s, rc, err, ctx, status, &policy)
	default:
		varyOnNegotiation(rc, true, byRequest)
		renderErr = h.renderHTML(s, rc, err, ctx, status)
	}
	if renderErr != nil {
		policy.undo(rc)
		safeLog(s.logger, "problem: rendering failed", "render_error", renderErr.Error(), "error", err.Error())
		lastResort(s.logger, rc)
		return
	}
	policy.keep()
}

// pageCacheControl is the Cache-Control of the error answers that embed
// application or request content (see pagePolicy).
const pageCacheControl = "private, no-store"

// pagePolicy is the cache policy the pipeline gives the error answers
// that embed application or request content: the Inertia error page (its
// props include the application's shared props, the signed-in user among
// them), the HTML branch's answer (the error page renderer's full-page
// shell, the HTML templates, an application "html" renderer) and the debug
// page (the request and the error in detail). Each is sent with
// "Cache-Control: private, no-store" whatever policy the failed attempt
// left on the response, so no shared cache stores one user's error page
// and serves it to another. The error's own headers win: an error whose
// headers (contract.HTTPError.Header) carry a Cache-Control keeps it. The
// policy is set before the BeforeRender hooks run, so a hook can still
// replace it.
//
// The problem+json body, the Inertia 409 reload and the plain-text last
// resort embed nothing personal and set no policy: they keep the
// Cache-Control the response already held. An answer that was to be a
// page and turns into one of them (the error page renderer declines, a
// renderer fails or panics) gets that value back, unless a hook replaced
// the pipeline's.
type pagePolicy struct {
	value []string // the Cache-Control the pipeline set; nil when none is pending
	prior []string // the value it replaced
	had   bool     // whether there was one
}

// apply sets pageCacheControl on the response unless errHeaders carries a
// Cache-Control.
func (p *pagePolicy) apply(rc RenderContext, errHeaders http.Header) {
	for k, vs := range errHeaders {
		if len(vs) > 0 && http.CanonicalHeaderKey(k) == "Cache-Control" {
			return
		}
	}
	w := rc.Writer()
	if w == nil {
		return
	}
	h := w.Header()
	p.prior, p.had = h["Cache-Control"]
	p.value = []string{pageCacheControl}
	h["Cache-Control"] = p.value
}

// keep settles the policy on the page answer that was written.
func (p *pagePolicy) keep() { p.value = nil }

// undo puts back the Cache-Control apply replaced, unless the response
// was written or a hook replaced the pipeline's value. It settles the
// policy, so a second call does nothing.
func (p *pagePolicy) undo(rc RenderContext) {
	value := p.value
	p.value = nil
	if value == nil || rc.Written() {
		return
	}
	w := rc.Writer()
	if w == nil {
		return
	}
	h := w.Header()
	if cur, ok := h["Cache-Control"]; !ok || len(cur) != 1 || &cur[0] != &value[0] {
		return
	}
	if p.had {
		h["Cache-Control"] = p.prior
	} else {
		delete(h, "Cache-Control")
	}
}

// renderInertia answers an Inertia request: the debug page at status in
// debug mode; otherwise the configured error page at status; failing that,
// a 409 with X-Inertia-Location so the client reloads the page as a full
// visit. Only the error page is a page object: the debug page and the 409
// go out without the page marker (see dropPageMarker). The 409 is not a
// page and goes out without the page policy (see pagePolicy).
func (h *Handler) renderInertia(s *snapshot, rc RenderContext, err error, ctx *ErrorContext, status int, policy *pagePolicy) error {
	if s.debug {
		dropPageMarker(rc)
		return rendererFor(s, "html").Render(rc, err, ctx, status, true)
	}
	if answered, pageErr := renderErrorPage(s, rc, err, status); answered {
		return pageErr
	}
	policy.undo(rc)
	dropPageMarker(rc)
	rc.SetHeader("X-Inertia-Location", reloadLocation(s.errorPage, rc.Request()))
	rc.WriteHeader(http.StatusConflict)
	return nil
}

// renderHTML answers a full-page request through the HTML renderer. In
// debug mode that is the debug page. Otherwise a page the application
// supplied wins (a custom "html" renderer, or a status, class or fallback
// template registered on the HTMLRenderer), then the configured error
// page, then the built-in template.
func (h *Handler) renderHTML(s *snapshot, rc RenderContext, err error, ctx *ErrorContext, status int) error {
	html := rendererFor(s, "html")
	if !s.debug && !appOwnsHTMLPage(html, status) {
		if answered, pageErr := renderErrorPage(s, rc, err, status); answered {
			return pageErr
		}
	}
	return html.Render(rc, err, ctx, status, s.debug)
}

// appOwnsHTMLPage reports whether r answers status with a page the
// application supplied: any renderer other than an *HTMLRenderer, or an
// *HTMLRenderer holding its own template for status.
func appOwnsHTMLPage(r Renderer, status int) bool {
	hr, ok := r.(*HTMLRenderer)
	if !ok {
		return true
	}
	return hr.ownsPage(status)
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
// negotiation. byRequest reports that the request's negotiation decided.
func wantsJSON(s *snapshot, rc RenderContext, err error) (asJSON, byRequest bool) {
	return negotiatesJSON(s, rc.Request(), err, rc.WantsJSON)
}

// negotiatesJSON is the negotiation order shared by the pipeline and
// Handler.WantsJSON; fallback answers when neither JSONWhen, API mode nor
// an API prefix decides, and byRequest reports that it did.
func negotiatesJSON(s *snapshot, r *http.Request, err error, fallback func() bool) (asJSON, byRequest bool) {
	if s.jsonWhen != nil {
		return s.jsonWhen(r, err), false
	}
	if s.apiMode {
		return true, false
	}
	if r != nil && r.URL != nil {
		for _, prefix := range s.apiPrefixes {
			if underAPIPrefix(r.URL.Path, prefix) {
				return true, false
			}
		}
	}
	return fallback(), true
}

// underAPIPrefix reports whether path lies under the API prefix prefix,
// segment by segment: "/api" matches "/api" and "/api/users" but not
// "/apiary". A prefix ending in "/" matches every path starting with it.
// An empty prefix matches nothing.
func underAPIPrefix(path, prefix string) bool {
	if prefix == "" || !strings.HasPrefix(path, prefix) {
		return false
	}
	return len(path) == len(prefix) || strings.HasSuffix(prefix, "/") || path[len(prefix)] == '/'
}

// varyOnNegotiation lists in the response's Vary header the request
// headers that chose its format, so a cache keyed on the URL does not
// serve a JSON error to a browser, an HTML one to an API client or an
// XHR, or either to an Inertia client. negotiated is false for a format a
// render rule forced (RenderJSON), which lists nothing. Otherwise
// X-Inertia is always listed: it picks the Inertia answer over the HTML
// one even when JSONWhen, API mode or an API prefix decided against JSON.
// byRequest adds the headers contract.WantsJSON reads (Accept and
// X-Requested-With) when the request's own negotiation chose JSON or HTML;
// a format fixed by JSONWhen, API mode or an API prefix does not vary
// with them.
func varyOnNegotiation(rc RenderContext, negotiated, byRequest bool) {
	if !negotiated {
		return
	}
	h := rc.Writer().Header()
	if byRequest {
		for _, name := range contract.JSONNegotiationHeaders() {
			contract.AppendVary(h, name)
		}
	}
	contract.AppendVary(h, contract.InertiaNegotiationHeader)
}

// negotiatedContext is the RenderContext the render stage hands to
// Renderable errors, render rules and renderers: WantsJSON reports the
// handler's negotiation answer (JSONWhen, API mode, API prefixes, then the
// request) for the error the stage renders, instead of the request's
// Accept header alone. byRequest records that the request's own
// negotiation decided, for the Vary header.
type negotiatedContext struct {
	RenderContext
	json      bool
	byRequest bool
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

// dropPageMarker removes from the response the headers that mark it an
// Inertia page object: X-Inertia, and the application/json Content-Type
// set beside it. The pipeline calls it before an answer to an Inertia
// request that is not a page object (the 409 reload, the debug page, the
// JSON branch, the plain-text 500): a page render that set them and then
// failed having written nothing must not make the client read that answer
// as a page, which would skip the reload and the debug handling.
func dropPageMarker(rc RenderContext) {
	w := rc.Writer()
	if w == nil {
		return
	}
	h := w.Header()
	h.Del("X-Inertia")
	if mt, _, _ := mime.ParseMediaType(h.Get("Content-Type")); mt == "application/json" {
		h.Del("Content-Type")
	}
}

// lastResort writes the plain-text 500 when nothing was written, never
// marked as an Inertia page object (see dropPageMarker). It runs under its
// own recover: a failure is logged and never re-panics.
func lastResort(logger contract.Logger, rc RenderContext) {
	defer func() {
		if p := recover(); p != nil {
			safeLog(logger, "problem: last-resort response failed", "panic", fmt.Sprint(p))
		}
	}()
	if rc.Written() {
		return
	}
	dropPageMarker(rc)
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

// safeWarn is safeLog at warn level.
func safeWarn(logger contract.Logger, msg string, kvs ...any) {
	if logger == nil {
		return
	}
	defer func() { _ = recover() }()
	logger.Warn(msg, kvs...)
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
		return contract.StatusTitle(status)
	}
	var me contract.MessageError
	if errors.As(err, &me) && me.StatusCode() == status {
		if msg := me.ClientMessage(); msg != "" {
			return msg
		}
	}
	return contract.StatusTitle(status)
}
