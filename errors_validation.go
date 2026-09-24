package velocity

import (
	"errors"
	"net/http"
	"reflect"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
)

// installValidationErrorRules installs the default rendering of a
// *validation.Failure. A request that wants JSON falls through to
// negotiation, which answers 422 application/problem+json with the
// per-field "errors". A browser request with a view engine gets the errors
// and old input flashed and a redirect back (or to Failure.RedirectTo),
// the same answer the validator callback writes. With no view engine the
// failure still renders as problem+json.
func installValidationErrorRules(h *problem.Handler) {
	h.AddFrameworkRenderRule(contract.RenderRule{
		Key: reflect.TypeFor[*validation.Failure](),
		Match: func(err error) bool {
			var f *validation.Failure
			return errors.As(err, &f)
		},
		Render: func(rc contract.RenderContext, err error, ctx *contract.ErrorContext) bool {
			return renderValidationFailure(h, rc, err, ctx)
		},
	})
}

// renderValidationFailure is the render rule for a *validation.Failure. It
// returns false for a request that wants JSON so negotiation renders it.
// With no view engine it renders through h again with a RenderContext that
// wants JSON: problem exposes no forced-JSON negotiation, and going through
// h keeps the configured JSON renderer and BeforeRender hooks. That second
// pass re-applies the user map and render rules to the failure; this rule
// returns false in it, so it recurses at most once.
func renderValidationFailure(h *problem.Handler, rc contract.RenderContext, err error, ctx *contract.ErrorContext) bool {
	var f *validation.Failure
	if !errors.As(err, &f) || rc.WantsJSON() {
		return false
	}
	r := rc.Request()
	view := viewEngineOf(router.ServicesFromRequest(r))
	if view == nil {
		h.Render(jsonRenderContext{RenderContext: rc}, err, ctx)
		return true
	}
	flashFailure(router.NewContext(rc.Writer(), r), rc, view, f)
	return true
}

// flashFailure writes the browser answer to a validation failure: the
// errors (nested under f.Bag when set) and the redacted old input as flash
// cookies on c, then a 303 to f.RedirectTo when it is a safe same-origin
// target, else view.Back.
func flashFailure(c *router.Context, rc contract.RenderContext, view contract.ViewEngine, f *validation.Failure) {
	result := f.Result
	if result == nil {
		result = &validation.Result{}
	}
	var errs any = result.All()
	if f.Bag != "" {
		errs = map[string]map[string]string{f.Bag: result.All()}
	}
	c.FlashErrors(errs)
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

// jsonRenderContext is a RenderContext that asks for JSON and is never an
// Inertia request, so negotiation takes the JSON branch.
type jsonRenderContext struct {
	contract.RenderContext
}

// WantsJSON always reports true.
func (jsonRenderContext) WantsJSON() bool { return true }

// IsInertia always reports false.
func (jsonRenderContext) IsInertia() bool { return false }
