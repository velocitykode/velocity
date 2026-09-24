package velocity

import (
	"errors"
	"net/http"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/problem/routerbridge"
	"github.com/velocitykode/velocity/router"
)

// installErrorPipeline connects the router's error boundary to the error
// handler: every handler error and recovered panic that reaches the router
// is reported once and rendered once by a.Services.Errors, resolved per
// request so a handler swapped in by a module is honoured. The router's
// own default logging is suppressed from here on; the handler's
// LogReporter (bound to a.Log) is the single log entry. A consumer calling
// a.Router.SetErrorHandler after New replaces the whole pipeline.
func installErrorPipeline(a *App) {
	routerbridge.Install(a.Router,
		routerbridge.WithHandler(func() contract.ErrorHandler { return a.Services.Errors }),
		routerbridge.WithUserID(appUserIdentifier{a: a}),
	)
}

// installFrameworkErrorRules installs the framework's default mappings on
// h, one call per subsystem. Framework rules rank below every rule the
// application registers, so each default stays overridable.
func installFrameworkErrorRules(h *problem.Handler) {
	installSentinelErrorRules(h)
	installCSRFErrorRules(h)
}

// sentinelStatus maps a framework sentinel to the status it answers with.
type sentinelStatus struct {
	err     error
	status  int
	message string
}

// sentinelStatuses lists the framework sentinels that are client outcomes,
// not failures.
var sentinelStatuses = []sentinelStatus{
	{err: orm.ErrNotFound, status: http.StatusNotFound, message: http.StatusText(http.StatusNotFound)},
	{err: orm.ErrNoRows, status: http.StatusNotFound, message: http.StatusText(http.StatusNotFound)},
	{err: auth.ErrUnauthorized, status: http.StatusForbidden, message: http.StatusText(http.StatusForbidden)},
	{err: csrf.ErrTokenMissing, status: problem.StatusTokenMismatch, message: "CSRF token mismatch"},
	{err: router.ErrBindExtraData, status: http.StatusBadRequest, message: http.StatusText(http.StatusBadRequest)},
}

// installSentinelErrorRules renders each framework sentinel at its status
// (the rendered HTTPError keeps the original as its Cause) and keeps it out
// of the reports.
func installSentinelErrorRules(h *problem.Handler) {
	for _, s := range sentinelStatuses {
		h.AddFrameworkIgnoreRule(contract.IgnoreRule{Key: s.err, Match: matchSentinel(s.err)})
		h.AddFrameworkPrepareRule(contract.MapRule{
			Key:   s.err,
			Match: matchSentinel(s.err),
			Map: func(err error) error {
				return &contract.HTTPError{Status: s.status, Message: s.message, Cause: err}
			},
		})
	}
}

// matchSentinel returns a matcher for errors whose chain holds target.
func matchSentinel(target error) contract.ErrorMatcher {
	return func(err error) bool { return errors.Is(err, target) }
}

// installErrorPageRenderer hands the view engine's ErrorPageRenderer facet
// (when it has one) to the error handler, so an Inertia request that fails
// renders the configured error page. Without the facet the handler keeps
// no error page. Called after the view step in New and again right before
// the errors bootstrap step, because a module may replace the view engine.
func installErrorPageRenderer(a *App) {
	h := a.Services.Errors
	if h == nil {
		return
	}
	page, _ := a.Services.View.(contract.ErrorPageRenderer)
	h.SetErrorPageRenderer(page)
}

// appUserIdentifier resolves the RequestUserIdentifier facet on the auth
// manager for every failed request, so an auth manager replaced after New
// is honoured.
type appUserIdentifier struct {
	a *App
}

// RequestUserID returns the authenticated user of r named by the auth
// manager's facet, or "" when the auth manager has none.
func (u appUserIdentifier) RequestUserID(r *http.Request) string {
	id, ok := u.a.Services.Auth.(contract.RequestUserIdentifier)
	if !ok {
		return ""
	}
	return id.RequestUserID(r)
}
