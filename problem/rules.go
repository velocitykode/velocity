package problem

import (
	"context"
	"errors"
	"net/http"
	"reflect"

	"github.com/velocitykode/velocity/contract"
)

// upsert returns rules with rule added: a keyed rule replaces the earlier
// rule with an equal key in place, an anonymous rule is appended. The input
// slice is never modified.
func upsert[R any](rules []R, rule R, key func(R) any) []R {
	if k := key(rule); k != nil {
		for i, existing := range rules {
			if key(existing) == k {
				out := append([]R(nil), rules...)
				out[i] = rule
				return out
			}
		}
	}
	return appendCopy(rules, rule)
}

// removeKey returns rules without the rules keyed k. A nil k removes
// nothing. The input slice is never modified.
func removeKey[R any](rules []R, k any, key func(R) any) []R {
	if k == nil {
		return rules
	}
	out := make([]R, 0, len(rules))
	for _, r := range rules {
		if key(r) != k {
			out = append(out, r)
		}
	}
	return out
}

func mapKey(r contract.MapRule) any           { return r.Key }
func renderKey(r contract.RenderRule) any     { return r.Key }
func reportKey(r contract.ReportRule) any     { return r.Key }
func ignoreKey(r contract.IgnoreRule) any     { return r.Key }
func levelKey(r contract.LevelRule) any       { return r.Key }
func throttleKey(r contract.ThrottleRule) any { return r.Key }

// AddMapRule registers a rule that replaces a matched error before it is
// reported and rendered. The first matching rule applies. A mapped
// recovered panic stays a reported 500 whatever the rule returns: the
// pipeline decides that the error is a panic before the rule runs.
func (h *Handler) AddMapRule(rule contract.MapRule) {
	if rule.Match == nil || rule.Map == nil {
		return
	}
	rule.Key = ruleKey(rule.Key)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mapRules = upsert(h.mapRules, rule, mapKey)
}

// AddRenderRule registers a rule that renders a matched error. User render
// rules run in registration order before the framework's own.
func (h *Handler) AddRenderRule(rule contract.RenderRule) {
	if rule.Match == nil || (rule.Render == nil && rule.Status == 0) {
		return
	}
	rule.Key = ruleKey(rule.Key)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.renderRules = upsert(h.renderRules, rule, renderKey)
}

// AddReportRule registers a rule that reports a matched error.
func (h *Handler) AddReportRule(rule contract.ReportRule) {
	if rule.Match == nil || rule.Report == nil {
		return
	}
	rule.Key = ruleKey(rule.Key)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reportRules = upsert(h.reportRules, rule, reportKey)
}

// AddIgnoreRule registers an ignore rule, or with Unignore set an unignore
// rule. An ignore removes the unignore with the same key and an unignore
// removes the ignore with the same key, so Unignore[T] undoes Ignore[T]. An
// unignore also forces a matched error past the framework's ignores and its
// own ShouldReport.
func (h *Handler) AddIgnoreRule(rule contract.IgnoreRule) {
	if rule.Match == nil {
		return
	}
	rule.Key = ruleKey(rule.Key)
	h.mu.Lock()
	defer h.mu.Unlock()
	if rule.Unignore {
		h.ignoreRules = removeKey(h.ignoreRules, rule.Key, ignoreKey)
		h.unignoreRules = upsert(h.unignoreRules, rule, ignoreKey)
		return
	}
	h.unignoreRules = removeKey(h.unignoreRules, rule.Key, ignoreKey)
	h.ignoreRules = upsert(h.ignoreRules, rule, ignoreKey)
}

// AddLevelRule registers the log level for a matched error.
func (h *Handler) AddLevelRule(rule contract.LevelRule) {
	if rule.Match == nil {
		return
	}
	rule.Key = ruleKey(rule.Key)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.levelRules = upsert(h.levelRules, rule, levelKey)
}

// AddThrottleRule registers report throttling for a matched error. The
// first matching rule throttles. A rule without a usable Key is anonymous:
// it never replaces another rule, and it gets a Key of its own (see
// anonymousThrottleKey), so its MaxPerWindow buckets are its own even when
// another rule was built from the same function literal.
func (h *Handler) AddThrottleRule(rule contract.ThrottleRule) {
	if rule.Match == nil {
		return
	}
	rule.Key = ruleKey(rule.Key)
	if rule.Key == nil {
		rule.Key = new(anonymousThrottleKey)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.throttleRules = upsert(h.throttleRules, rule, throttleKey)
}

// anonymousThrottleKey is the Key of a throttle rule registered without
// one: AddThrottleRule allocates one per registration, and its buckets are
// keyed by that pointer. It is not zero-size, so two allocations never
// share an address.
type anonymousThrottleKey struct{ _ byte }

// IgnoreIf registers a predicate; an error for which it returns true is not
// reported.
func (h *Handler) IgnoreIf(pred func(err error, ctx *ErrorContext) bool) {
	if pred == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ignorePredicates = appendCopy(h.ignorePredicates, pred)
}

// ContextUsing registers a provider whose fields are merged into
// ErrorContext.Extra for every report.
func (h *Handler) ContextUsing(fn func(err error, ctx *ErrorContext) map[string]any) {
	if fn == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.contextProviders = appendCopy(h.contextProviders, fn)
}

// JSONWhen replaces the negotiation predicate: when set, its answer alone
// decides whether a response renders as JSON. Nil restores the default (API
// mode, API prefixes, then contract.WantsJSON). The pipeline asks it once
// per failure, with the error as it will be rendered: after the framework
// prepare table (a bare context.DeadlineExceeded arrives as its 503
// HTTPError, orm.ErrNotFound as its 404), a request cut off by shutdown as
// its 503, a recovered panic as a 500 HTTPError wrapping it.
func (h *Handler) JSONWhen(fn func(r *http.Request, err error) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.jsonWhen = fn
}

// WantsJSON reports whether the response to r for err renders as JSON, in
// the order negotiation uses: the JSONWhen predicate alone when set,
// otherwise API mode, an API prefix of r's path, then contract.WantsJSON.
// Render rules read the pipeline's answer through RenderContext.WantsJSON;
// the pipeline asks with the error as it will be rendered (see JSONWhen),
// so passing that error here gives the same answer. err may be nil.
func (h *Handler) WantsJSON(r *http.Request, err error) bool {
	asJSON, _ := negotiatesJSON(h.snap(), r, err, func() bool { return contract.WantsJSON(r) })
	return asJSON
}

// BeforeRender registers a hook run once, right before the pipeline's own
// negotiated write, with the resolved status. It may set headers through rc
// and returns the status to write; hooks chain in registration order.
func (h *Handler) BeforeRender(fn func(rc RenderContext, err error, status int) int) {
	if fn == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.beforeRender = appendCopy(h.beforeRender, fn)
}

// SetErrorPageRenderer installs the error page renderer. Outside debug mode
// it answers Inertia requests at the real status (one it declines gets a
// 409 reload) and full-page HTML requests for which the application
// supplied no page of its own (a custom "html" renderer, or a status, class
// or fallback template on the HTMLRenderer), ahead of the built-in
// template. Nil removes it.
func (h *Handler) SetErrorPageRenderer(r contract.ErrorPageRenderer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errorPage = r
}

// AddFrameworkIgnoreRule registers a framework ignore: a matched error is
// not reported unless an unignore matches it or the error's own
// ShouldReport says true.
func (h *Handler) AddFrameworkIgnoreRule(rule contract.IgnoreRule) {
	if rule.Match == nil || rule.Unignore {
		return
	}
	rule.Key = ruleKey(rule.Key)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.frameworkIgnores = upsert(h.frameworkIgnores, rule, ignoreKey)
}

// AddFrameworkPrepareRule registers a framework prepare rule. Prepare rules
// run at render time only (the report sees the original error) and turn a
// matched error into the error that is rendered, normally an HTTPError whose
// Cause is the original so errors.Is still reaches it. They apply only to an
// error that names no status (contract.StatusOf reports false): an error
// that already names one keeps its own status, message and headers.
func (h *Handler) AddFrameworkPrepareRule(rule contract.MapRule) {
	if rule.Match == nil || rule.Map == nil {
		return
	}
	rule.Key = ruleKey(rule.Key)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.frameworkPrepare = upsert(h.frameworkPrepare, rule, mapKey)
}

// AddFrameworkRenderRule registers a framework render rule, consulted after
// every user render rule and never for a recovered panic. Match alone
// decides whether the rule applies; FrameworkRenderFor registers a rule for
// a subsystem error that applies only while that error owns the status.
func (h *Handler) AddFrameworkRenderRule(rule contract.RenderRule) {
	if rule.Match == nil || (rule.Render == nil && rule.Status == 0) {
		return
	}
	rule.Key = ruleKey(rule.Key)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.frameworkRender = upsert(h.frameworkRender, rule, renderKey)
}

// AddFrameworkLevelRule registers a framework level rule, consulted after
// every user level rule.
func (h *Handler) AddFrameworkLevelRule(rule contract.LevelRule) {
	if rule.Match == nil {
		return
	}
	rule.Key = ruleKey(rule.Key)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.frameworkLevels = upsert(h.frameworkLevels, rule, levelKey)
}

// belowServerErrorKey keys the framework ignore for status errors below 500.
type belowServerErrorKey struct{}

// clientGoneKey keys the framework render rule for a cancelled request whose
// client has gone.
type clientGoneKey struct{}

// registerStdlibRules installs the framework rules that need only the
// standard library.
func registerStdlibRules(h *Handler) {
	h.AddFrameworkIgnoreRule(contract.IgnoreRule{
		Key: belowServerErrorKey{},
		Match: func(err error) bool {
			status, _, ok := contract.StatusOf(err)
			return ok && status < http.StatusInternalServerError
		},
	})
	h.AddFrameworkIgnoreRule(contract.IgnoreRule{Key: typeKey[*http.MaxBytesError](), Match: matchAs[*http.MaxBytesError]()})
	h.AddFrameworkPrepareRule(contract.MapRule{
		Key:   typeKey[*http.MaxBytesError](),
		Match: matchAs[*http.MaxBytesError](),
		Map: func(err error) error {
			return contract.NewHTTPError(http.StatusRequestEntityTooLarge).WithCause(err)
		},
	})
	h.AddFrameworkPrepareRule(contract.MapRule{
		Key:   context.DeadlineExceeded,
		Match: matchIs(context.DeadlineExceeded),
		Map: func(err error) error {
			return contract.NewHTTPError(http.StatusServiceUnavailable).WithCause(err)
		},
	})
	h.AddFrameworkLevelRule(contract.LevelRule{
		Key:   context.DeadlineExceeded,
		Match: matchIs(context.DeadlineExceeded),
		Level: contract.LogLevelWarn,
	})
	h.AddFrameworkRenderRule(contract.RenderRule{
		Key:   clientGoneKey{},
		Match: matchIs(context.Canceled),
		Render: func(rc RenderContext, _ error, _ *ErrorContext) bool {
			r := requestOf(rc)
			return requestGone(r) && !shuttingDown(r)
		},
	})
}

// requestGone reports whether r's context is done: the client went away or
// the server cut the request off while shutting down (see shuttingDown).
// Either way the outcome belongs to the client, and it is not reported.
func requestGone(r *http.Request) bool {
	return r != nil && r.Context().Err() != nil
}

// shuttingDown reports whether r's context is done because the server is
// shutting down: its cause is contract.ErrServerShuttingDown.
func shuttingDown(r *http.Request) bool {
	return requestGone(r) && errors.Is(context.Cause(r.Context()), contract.ErrServerShuttingDown)
}

// serverCancelled reports whether err is a context.Canceled for a request
// the server cut off while shutting down.
func serverCancelled(err error, r *http.Request) bool {
	return shuttingDown(r) && errors.Is(err, context.Canceled)
}

// serverShutdownError is the answer to a request the server cut off while
// shutting down: 503, retry after a second, on a new connection.
func serverShutdownError(cause error) *contract.HTTPError {
	return (&contract.HTTPError{Status: http.StatusServiceUnavailable, Message: http.StatusText(http.StatusServiceUnavailable)}).
		WithHeader("Retry-After", "1").
		WithHeader("Connection", "close").
		WithCause(cause)
}

// requestPath returns r's URL path, or "".
func requestPath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return r.URL.Path
}

// typeKey returns the rule key for type T.
func typeKey[T error]() any {
	return reflect.TypeOf((*T)(nil)).Elem()
}

// matchAs returns a matcher that reports whether an error's chain holds a T
// (errors.As).
func matchAs[T error]() contract.ErrorMatcher {
	return func(err error) bool {
		var target T
		return errors.As(err, &target)
	}
}

// matchStatusOwner returns a matcher that reports whether an error's chain
// holds a T (errors.As) that owns the error's status: the status
// contract.StatusOf resolves for the whole error equals the T's own. An
// outer error naming another status (a map result such as
// Internal().WithCause(err)) owns the answer instead.
func matchStatusOwner[T contract.StatusError]() contract.ErrorMatcher {
	return func(err error) bool {
		var target T
		if !errors.As(err, &target) {
			return false
		}
		status, _, _ := contract.StatusOf(err)
		own, _, _ := contract.StatusOf(target)
		return status == own
	}
}

// matchIs returns a matcher that reports whether an error's chain holds
// target (errors.Is).
func matchIs(target error) contract.ErrorMatcher {
	return func(err error) bool { return errors.Is(err, target) }
}

// RenderFor registers fn to render errors whose chain holds a T. fn receives
// the T found by errors.As and returns true when it wrote the response, or
// false to fall through to the next rule. A recovered panic reaches the
// rule too (T = contract.RecoveredPanic matches every one); a panic always
// answers 500, so fn should write that status.
func RenderFor[T error](h contract.ErrorHandler, fn func(rc RenderContext, err T, ctx *ErrorContext) bool) {
	if h == nil || fn == nil {
		return
	}
	h.AddRenderRule(contract.RenderRule{
		Key:   typeKey[T](),
		Match: matchAs[T](),
		Render: func(rc RenderContext, err error, ctx *ErrorContext) bool {
			var target T
			if !errors.As(err, &target) {
				return false
			}
			return fn(rc, target, ctx)
		},
	})
}

// FrameworkRenderFor registers fn as the framework render rule for errors
// whose chain holds a T that owns the status the error resolves to (see
// contract.StatusOf): a subsystem default answers its own error, never one
// an outer error gave another status. Like every framework render rule it
// runs after the user render rules and never for a recovered panic. fn
// receives the whole error and returns true when it wrote the response, or
// false to fall through to negotiation.
func FrameworkRenderFor[T contract.StatusError](h *Handler, fn func(rc RenderContext, err error, ctx *ErrorContext) bool) {
	if h == nil || fn == nil {
		return
	}
	h.AddFrameworkRenderRule(contract.RenderRule{Key: typeKey[T](), Match: matchStatusOwner[T](), Render: fn})
}

// RenderStatus registers status as the response status for errors whose
// chain holds a T; the body is negotiated as usual. A recovered panic
// answers 500 whatever status is registered.
func RenderStatus[T error](h contract.ErrorHandler, status int) {
	if h == nil {
		return
	}
	h.AddRenderRule(contract.RenderRule{Key: typeKey[T](), Match: matchAs[T](), Status: status})
}

// ReportFor registers fn to report errors whose chain holds a T. Returning
// true stops the configured reporters from also seeing the error.
func ReportFor[T error](h contract.ErrorHandler, fn func(err T, ctx *ErrorContext) bool) {
	if h == nil || fn == nil {
		return
	}
	h.AddReportRule(contract.ReportRule{
		Key:   typeKey[T](),
		Match: matchAs[T](),
		Report: func(err error, ctx *ErrorContext) bool {
			var target T
			if !errors.As(err, &target) {
				return false
			}
			return fn(target, ctx)
		},
	})
}

// MapFor registers fn to replace errors whose chain holds a T before they
// are reported and rendered. A nil result keeps the original error. A T
// reached through a recovered panic (the panic value, or inside it) is
// still replaced, but the result stays a reported 500 whatever fn returns.
func MapFor[T error](h contract.ErrorHandler, fn func(err T) error) {
	if h == nil || fn == nil {
		return
	}
	h.AddMapRule(contract.MapRule{
		Key:   typeKey[T](),
		Match: matchAs[T](),
		Map: func(err error) error {
			var target T
			if !errors.As(err, &target) {
				return nil
			}
			return fn(target)
		},
	})
}

// MapIs registers fn to replace errors whose chain holds target (errors.Is)
// before they are reported and rendered. fn receives the whole error; a nil
// result keeps it. A target reached through a recovered panic (the panic
// value, or inside it) is still replaced, but the result stays a reported
// 500 whatever fn returns.
func MapIs(h contract.ErrorHandler, target error, fn func(err error) error) {
	if h == nil || target == nil || fn == nil {
		return
	}
	h.AddMapRule(contract.MapRule{Key: target, Match: matchIs(target), Map: fn})
}

// Ignore stops errors whose chain holds a T from being reported.
func Ignore[T error](h contract.ErrorHandler) {
	if h == nil {
		return
	}
	h.AddIgnoreRule(contract.IgnoreRule{Key: typeKey[T](), Match: matchAs[T]()})
}

// IgnoreIs stops errors whose chain holds target from being reported.
func IgnoreIs(h contract.ErrorHandler, target error) {
	if h == nil || target == nil {
		return
	}
	h.AddIgnoreRule(contract.IgnoreRule{Key: target, Match: matchIs(target)})
}

// Unignore removes Ignore[T] and forces errors whose chain holds a T to be
// reported even when a framework ignore or the error's own ShouldReport
// would drop them.
func Unignore[T error](h contract.ErrorHandler) {
	if h == nil {
		return
	}
	h.AddIgnoreRule(contract.IgnoreRule{Key: typeKey[T](), Match: matchAs[T](), Unignore: true})
}

// UnignoreIs removes IgnoreIs(target) and forces errors whose chain holds
// target to be reported.
func UnignoreIs(h contract.ErrorHandler, target error) {
	if h == nil || target == nil {
		return
	}
	h.AddIgnoreRule(contract.IgnoreRule{Key: target, Match: matchIs(target), Unignore: true})
}

// LevelFor sets the log level for errors whose chain holds a T.
func LevelFor[T error](h contract.ErrorHandler, level contract.LogLevel) {
	if h == nil {
		return
	}
	h.AddLevelRule(contract.LevelRule{Key: typeKey[T](), Match: matchAs[T](), Level: level})
}

// LevelIs sets the log level for errors whose chain holds target.
func LevelIs(h contract.ErrorHandler, target error, level contract.LogLevel) {
	if h == nil || target == nil {
		return
	}
	h.AddLevelRule(contract.LevelRule{Key: target, Match: matchIs(target), Level: level})
}

// ThrottleFor throttles reports of errors whose chain holds a T.
func ThrottleFor[T error](h contract.ErrorHandler, throttle contract.Throttle) {
	if h == nil {
		return
	}
	h.AddThrottleRule(contract.ThrottleRule{Key: typeKey[T](), Match: matchAs[T](), Throttle: throttle})
}
