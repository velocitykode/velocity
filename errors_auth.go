package velocity

import (
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// installAuthErrorRules installs auth's default render rules on h. An
// *auth.UnauthenticatedError that owns the response status renders through
// Manager.RenderUnauthenticated of the auth manager in the failed request's
// services (a 303 to the login target for a browser or Inertia request; a
// request that wants JSON, or one denied only by stateless schemes, falls
// through to the pipeline's 401 with the checked schemes' WWW-Authenticate
// challenges); an error auth's middleware returned
// resolves the checked schemes through, stashes the intended URL
// through, and falls back to the login target of, the manager that denied
// the request instead. An *auth.AlreadyAuthenticatedError from the guest guard renders
// through Manager.RenderAlreadyAuthenticated (a 303 to its redirect target
// for a browser or Inertia request; a request that wants JSON, or a
// refused target, falls through to the pipeline's 403 problem+json body).
// The manager is resolved per request, so one replaced after New is
// honoured; with no auth manager the default login target applies and a
// refused guest redirect is not logged.
func installAuthErrorRules(h *problem.Handler) {
	problem.FrameworkRenderFor[*auth.UnauthenticatedError](h, func(rc contract.RenderContext, err error, ctx *contract.ErrorContext) bool {
		return requestAuthManager(rc).RenderUnauthenticated(rc, err, ctx)
	})
	problem.FrameworkRenderFor[*auth.AlreadyAuthenticatedError](h, func(rc contract.RenderContext, err error, ctx *contract.ErrorContext) bool {
		return requestAuthManager(rc).RenderAlreadyAuthenticated(rc, err, ctx)
	})
}

// requestAuthManager returns the *auth.Manager in the failed request's
// services, or nil when there is none (no services, no auth, or an auth
// manager of another type); the render methods take a nil manager.
func requestAuthManager(rc contract.RenderContext) *auth.Manager {
	s := router.ServicesFromRequest(rc.Request())
	if s == nil {
		return nil
	}
	m, _ := s.Auth.(*auth.Manager)
	return m
}
