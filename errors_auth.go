package velocity

import (
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// installAuthErrorRules installs auth's default render rule on h: an
// *auth.UnauthenticatedError that owns the response status renders through
// Manager.RenderUnauthenticated of the auth manager in the failed request's
// services (a 303 to the login target for a browser or Inertia request; a
// request that wants JSON falls through to the pipeline's 401 problem+json
// body). The manager is resolved per request, so one replaced after New is
// honoured; with no auth manager the default login target applies.
func installAuthErrorRules(h *problem.Handler) {
	problem.FrameworkRenderFor[*auth.UnauthenticatedError](h, func(rc contract.RenderContext, err error, ctx *contract.ErrorContext) bool {
		m := auth.FromServices(router.ServicesFromRequest(rc.Request()))
		return m.RenderUnauthenticated(rc, err, ctx)
	})
}
