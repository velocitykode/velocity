// Package vform offers Form[T], a request-binding helper that pairs the
// validation package with router.Context. It is a leaf package: it imports
// both validation and router, so neither needs to import the other.
package vform

import (
	"fmt"

	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
)

// FormRequest defines validation rules for a request. Implement this on a
// struct to make it self-validating; rules use the canonical
// validation.Rules type (map[string][]string).
//
// The return type is validation.Rules so the same value can be passed
// straight into validation.Check / CheckWithDB without intermediate
// conversion.
type FormRequest interface {
	Rules() validation.Rules
}

// WithMessages can be implemented alongside FormRequest to provide custom
// error messages keyed by "field.rule".
type WithMessages interface {
	ValidationMessages() map[string]string
}

// Form binds the request body into a fresh *T, validates using T.Rules() if
// T implements FormRequest, and returns *T on success. On validation failure
// it flashes errors plus old input, redirects back, and returns
// router.ErrValidationAborted so the handler can return early without the
// router emitting an error response.
func Form[T any](ctx *router.Context) (*T, error) {
	req := new(T)

	fr, ok := any(req).(FormRequest)
	if !ok {
		if err := ctx.BindAuto(req); err != nil {
			return nil, fmt.Errorf("velocity/vform: bind failed: %w", err)
		}
		return req, nil
	}

	rules := fr.Rules()

	var msgs []validation.Messages
	if wm, ok := any(req).(WithMessages); ok {
		msgs = append(msgs, validation.Messages(wm.ValidationMessages()))
	}

	result := validation.CheckWithDB(ctx.Request, rules, ctx.DB(), msgs...)
	if !result.HasErrors() {
		if err := ctx.BindAuto(req); err != nil {
			return nil, fmt.Errorf("velocity/vform: bind failed: %w", err)
		}
		return req, nil
	}

	ctx.WithErrors(result.All())
	ctx.WithInput(result.Old())

	if v := ctx.View(); v != nil {
		v.Back(ctx.Response, ctx.Request)
	}

	return nil, router.ErrValidationAborted
}
