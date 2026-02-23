package validate

import (
	"github.com/velocitykode/velocity/router"
)

// Form binds the request to T, validates using T.Rules(), and returns *T.
// If validation fails, it flashes errors and old input, redirects back,
// and stops handler execution. The handler code after Form only runs when valid.
//
//	func (h *Handler) Register(ctx *router.Context) error {
//	    req := validate.Form[RegisterRequest](ctx)
//	    // only reaches here if valid
//	}
func Form[T any](ctx *router.Context) *T {
	req := new(T)

	fr, ok := any(req).(FormRequest)
	if !ok {
		// T doesn't implement FormRequest — just bind and return
		ctx.BindAuto(req)
		return req
	}

	rules := fr.Rules()

	// Custom messages (optional)
	var msgs []Messages
	if wm, ok := any(req).(WithMessages); ok {
		msgs = append(msgs, wm.ValidationMessages())
	}

	// Validate raw request data (DB enables unique/exists rules)
	errors := CheckWithDB(ctx.Request, rules, ctx.DB(), msgs...)
	if !errors.HasErrors() {
		// Valid — bind the struct and return
		ctx.BindAuto(req)
		return req
	}

	// Flash errors + old input, redirect back, stop execution
	ctx.WithErrors(errors.All())
	ctx.WithInput(errors.Old())

	if v := ctx.View(); v != nil {
		v.Back(ctx.Response, ctx.Request)
	}

	panic(router.AbortValidation{})
}
