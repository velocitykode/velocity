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
// reported and rendered. The first matching rule applies.
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
// first matching rule throttles.
func (h *Handler) AddThrottleRule(rule contract.ThrottleRule) {
	if rule.Match == nil {
		return
	}
	rule.Key = ruleKey(rule.Key)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.throttleRules = upsert(h.throttleRules, rule, throttleKey)
}

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
// mode, API prefixes, then contract.WantsJSON).
func (h *Handler) JSONWhen(fn func(r *http.Request, err error) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.jsonWhen = fn
}

// WantsJSON reports whether the response to r for err renders as JSON, in
// the order negotiation uses: the JSONWhen predicate alone when set,
// otherwise API mode, an API prefix of r's path, then contract.WantsJSON.
// Render rules read the same answer through RenderContext.WantsJSON. err
// may be nil.
func (h *Handler) WantsJSON(r *http.Request, err error) bool {
	return negotiatesJSON(h.snap(), r, err, func() bool { return contract.WantsJSON(r) })
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
// it answers Inertia and full-page HTML requests at the real status; an
// Inertia request it declines gets a 409 reload. Nil removes it.
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
// every user render rule.
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
			return requestGone(requestOf(rc))
		},
	})
}

// requestGone reports whether r's context is done: the client went away or
// the server is cancelling the request.
func requestGone(r *http.Request) bool {
	return r != nil && r.Context().Err() != nil
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

// matchIs returns a matcher that reports whether an error's chain holds
// target (errors.Is).
func matchIs(target error) contract.ErrorMatcher {
	return func(err error) bool { return errors.Is(err, target) }
}

// RenderFor registers fn to render errors whose chain holds a T. fn receives
// the T found by errors.As and returns true when it wrote the response, or
// false to fall through to the next rule.
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

// RenderStatus registers status as the response status for errors whose
// chain holds a T; the body is negotiated as usual.
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
// are reported and rendered. A nil result keeps the original error.
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
// result keeps it.
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
