package velocity

import (
	"errors"
	"net/http"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
)

// installValidationErrorRules installs the default rendering of a
// *validation.Failure that owns the response status. A request the
// handler answers with JSON (its
// negotiation: JSONWhen, API mode, API prefixes, then the Accept header)
// falls through to negotiation, which answers 422
// application/problem+json with the per-field "errors". Any other request
// with a view engine and a session gets the errors and old input flashed
// into the session flash bag and a redirect back (or to
// Failure.RedirectTo). With no view engine, or no session to carry the
// errors across the redirect, the failure still renders as problem+json. Every validation entry point (ctx.Validate,
// vform.Form, ctx.BindValid) returns its *validation.Failure with nothing
// written, so this rule is the one browser answer for all of them.
func installValidationErrorRules(h *problem.Handler) {
	problem.FrameworkRenderFor[*validation.Failure](h, func(rc contract.RenderContext, err error, ctx *contract.ErrorContext) bool {
		return renderValidationFailure(h, rc, err, ctx)
	})
}

// renderValidationFailure is the render rule for a *validation.Failure. It
// returns false for a request the handler answers with JSON (rc.WantsJSON
// carries the handler's negotiation), so negotiation renders it. With no
// view engine, or when the request carries no session whose flash bag
// could take the errors to the next page (a redirect back would lose them),
// it renders the failure as JSON through h.RenderJSON, which keeps the
// configured JSON renderer and BeforeRender hooks.
func renderValidationFailure(h *problem.Handler, rc contract.RenderContext, err error, ctx *contract.ErrorContext) bool {
	var f *validation.Failure
	if !errors.As(err, &f) || rc.WantsJSON() {
		return false
	}
	r := rc.Request()
	services := router.ServicesFromRequest(r)
	view := viewEngineOf(services)
	if view == nil || !carriesFlashBag(services, r) {
		return h.RenderJSON(rc, err, ctx)
	}
	flashFailure(router.NewContext(rc.Writer(), r), rc, view, f)
	return true
}

// flashFailure writes the browser answer to a validation failure: the
// errors (router.Context.FlashErrors flashes field -> first message, also
// under the error bag's name when f.Bag is set) and the redacted old input
// into the session flash bag, then a 303 to f.RedirectTo when it is a safe
// same-origin target, else view.Back.
func flashFailure(c *router.Context, rc contract.RenderContext, view contract.ViewEngine, f *validation.Failure) {
	result := f.Result
	if result == nil {
		result = &validation.Result{}
	}
	c.FlashErrors(f)
	c.FlashInput(result.Old())
	if f.RedirectTo != "" && rc.Redirect(http.StatusSeeOther, f.RedirectTo) == nil {
		return
	}
	view.Back(c.Response, c.Request)
}

// viewEngineOf returns the view engine in s, or nil when s or its View is
// unset. An app without a view engine is supported (API-only).
func viewEngineOf(s *app.Services) contract.ViewEngine {
	if s == nil {
		return nil
	}
	return s.View
}

// carriesFlashBag reports whether r carries a session flash bag in s.
func carriesFlashBag(s *app.Services, r *http.Request) bool {
	return s != nil && s.FlashBag != nil && s.FlashBag(r) != nil
}
