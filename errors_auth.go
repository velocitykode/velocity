package velocity

import (
	"errors"
	"reflect"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// installAuthErrorRules installs auth's default render rule on h: an
// *auth.UnauthenticatedError renders through Manager.RenderUnauthenticated
// of the auth manager in the failed request's services (a 303 to the login
// target for a browser request; JSON and Inertia requests fall through to
// the pipeline's 401 problem+json body and Inertia answer). The manager is
// resolved per request, so one replaced after New is honoured; with no
// auth manager the default login target applies.
func installAuthErrorRules(h *problem.Handler) {
	h.AddFrameworkRenderRule(contract.RenderRule{
		Key:   reflect.TypeFor[*auth.UnauthenticatedError](),
		Match: matchUnauthenticated,
		Render: func(rc contract.RenderContext, err error, ctx *contract.ErrorContext) bool {
			m := auth.FromServices(router.ServicesFromRequest(rc.Request()))
			return m.RenderUnauthenticated(rc, err, ctx)
		},
	})
}

// matchUnauthenticated reports whether err's chain holds an
// *auth.UnauthenticatedError.
func matchUnauthenticated(err error) bool {
	var ue *auth.UnauthenticatedError
	return errors.As(err, &ue)
}
