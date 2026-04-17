package validate

import (
	"strings"

	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
)

// Form binds the request to T, validates using T.Rules(), and returns *T.
// If validation fails, it flashes errors and old input, redirects back, and
// returns router.ErrValidationAborted. The handler should propagate this
// error to the router, which will skip error handling since the redirect
// response has already been written.
//
// Deprecated: Form[T] remains supported because it is the only validation
// entry point that needs *router.Context. Its internals now call
// validation.CheckWithDB directly; new code that does not need the T-rules
// pattern should prefer ctx.Validate(...).
//
//	func (h *Handler) Register(ctx *router.Context) error {
//	    req, err := validate.Form[RegisterRequest](ctx)
//	    if err != nil {
//	        return err
//	    }
//	    // only reaches here if valid
//	}
func Form[T any](ctx *router.Context) (*T, error) {
	req := new(T)

	fr, ok := any(req).(FormRequest)
	if !ok {
		// T doesn't implement FormRequest — just bind and return.
		if err := ctx.BindAuto(req); err != nil {
			return nil, wrapErr("bind failed", err)
		}
		return req, nil
	}

	rules := fr.Rules()

	// Convert []string rules to pipe-separated format.
	vRules := make(validation.Rules, len(rules))
	for field, fieldRules := range rules {
		vRules[field] = strings.Join(fieldRules, "|")
	}

	// Custom messages (optional).
	var msgs []validation.Messages
	if wm, ok := any(req).(WithMessages); ok {
		msgs = append(msgs, validation.Messages(wm.ValidationMessages()))
	}

	// Validate raw request data (DB enables unique/exists rules).
	result := validation.CheckWithDB(ctx.Request, vRules, ctx.DB(), msgs...)
	if !result.HasErrors() {
		// Valid — bind the struct and return.
		if err := ctx.BindAuto(req); err != nil {
			return nil, wrapErr("bind failed", err)
		}
		return req, nil
	}

	// Flash errors + old input, redirect back, return sentinel.
	ctx.WithErrors(result.All())
	ctx.WithInput(result.Old())

	if v := ctx.View(); v != nil {
		v.Back(ctx.Response, ctx.Request)
	}

	return nil, router.ErrValidationAborted
}
