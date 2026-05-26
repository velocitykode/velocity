// Package vform offers Form[T], a request-binding helper that pairs the
// validation package with router.Context. It is a leaf package: it imports
// both validation and router, so neither needs to import the other.
package vform

import (
	"fmt"
	"reflect"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm"
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

// Result is re-exported from the validation package for callers using the
// lower-level Validate[T] entry point. It carries per-field errors and the
// original input data; see validation.Result for full method documentation.
type Result = validation.Result

// Validate is the lower-level entry point: it binds the request body into a
// fresh *T, runs the same validation Form[T] does, and returns the result
// without flashing errors or redirecting back. Use this when you need to
// render a custom view that carries view-specific props (e.g. an invitation
// query parameter) instead of relying on the framework's flash + Back flow.
//
// Returns:
//   - *T:     populated form on validation success; zero-value *T on
//     validation failure. Callers SHOULD only consume *T when the
//     returned *Result is nil.
//   - *Result: nil on validation success, non-nil with errors keyed by field
//     on validation failure.
//   - error:   non-nil only for non-validation failures (decode/bind error).
//     Validation errors travel through *Result, never the error
//     return.
//
// If T does not implement FormRequest, Validate skips validation entirely
// and returns the bound *T with a nil *Result and any bind error.
func Validate[T any](ctx *router.Context) (*T, *Result, error) {
	req := new(T)

	fr, ok := any(req).(FormRequest)
	if !ok {
		if sig, has := mismatchedRulesMethod(req); has {
			return nil, nil, fmt.Errorf(
				"velocity/vform: %T has a Rules method but its signature %s does not satisfy vform.FormRequest; "+
					"change the signature to `Rules() validation.Rules` (alias for map[string][]string) or `Rules() map[string][]string`",
				req, sig,
			)
		}
		if err := ctx.BindAuto(req); err != nil {
			return nil, nil, fmt.Errorf("velocity/vform: bind failed: %w", err)
		}
		return req, nil, nil
	}

	rules := fr.Rules()

	var msgs []validation.Messages
	if wm, ok := any(req).(WithMessages); ok {
		msgs = append(msgs, validation.Messages(wm.ValidationMessages()))
	}

	// CheckWithDBW threads ctx.Response into the body-read path so
	// http.MaxBytesReader can signal a connection-close hint on
	// oversized bodies (rule 5).
	result := validation.CheckWithDBW(ctx.Response, ctx.Request, rules, safeDB(ctx), msgs...)
	if result.HasErrors() {
		return new(T), result, nil
	}

	if err := ctx.BindAuto(req); err != nil {
		return nil, nil, fmt.Errorf("velocity/vform: bind failed: %w", err)
	}
	return req, nil, nil
}

// safeDB returns the ORM database from ctx.Services without panicking when
// either the services container or the DB field is nil. This lets adopters
// run vform in test contexts (no DB), in API-only handlers (no DB rules),
// or before service wiring completes; database rules (unique/exists) simply
// short-circuit when no DB is attached.
func safeDB(ctx *router.Context) orm.Database {
	s := ctx.ServicesIfSet()
	if s == nil {
		return nil
	}
	return s.DB
}

// Form binds the request body into a fresh *T, validates using T.Rules() if
// T implements FormRequest, and returns *T on success. On validation failure
// it flashes errors plus old input, redirects back, and returns
// router.ErrValidationAborted so the handler can return early without the
// router emitting an error response.
//
// Adopters that want to render a custom error view instead of redirecting
// back should call Validate[T] directly and inspect the returned *Result.
func Form[T any](ctx *router.Context) (*T, error) {
	req, result, err := Validate[T](ctx)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return req, nil
	}

	ctx.WithErrors(result.All())
	ctx.WithInput(result.Old())

	if v := safeView(ctx); v != nil {
		v.Back(ctx.Response, ctx.Request)
	}

	return nil, router.ErrValidationAborted
}

// mismatchedRulesMethod inspects req for a method literally named "Rules"
// whose signature does not satisfy vform.FormRequest. It is the guardrail
// that turns a silent-skip footgun into a loud error: if an adopter typed
// the Rules method with an incompatible return type (e.g. a future shape
// change, or a stray helper named Rules), Validate[T] reports it instead
// of skipping validation. Returns the offending signature string and true
// when a mismatch is detected.
func mismatchedRulesMethod(req any) (string, bool) {
	v := reflect.ValueOf(req)
	if !v.IsValid() {
		return "", false
	}
	m := v.MethodByName("Rules")
	if !m.IsValid() {
		return "", false
	}
	t := m.Type()
	// Compatible: zero inputs (method value already binds receiver), one
	// output of type validation.Rules (which is map[string][]string).
	if t.NumIn() == 0 && t.NumOut() == 1 && t.Out(0) == reflect.TypeOf(validation.Rules(nil)) {
		return "", false
	}
	return t.String(), true
}

// safeView mirrors safeDB: returns the view engine without panicking when
// the services container or View field is unset. View.Back is the
// redirect-back hook used by Form[T] on validation failure; when no view
// engine is wired, the caller already received ErrValidationAborted and
// can render its own response.
func safeView(ctx *router.Context) contract.ViewEngine {
	s := ctx.ServicesIfSet()
	if s == nil || s.View == nil {
		return nil
	}
	return s.View
}
