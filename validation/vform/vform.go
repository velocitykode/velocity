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
	"github.com/velocitykode/velocity/validation/dbrules"
)

// FormRequest defines validation rules for a request. Implement this on a
// struct to make it self-validating; rules are built with the validation
// package's rule constructors.
//
//	func (r *SignupRequest) Rules() validation.Rules {
//	    return validation.Rules{
//	        "email":    {validation.Required(), validation.Email()},
//	        "password": {validation.Required(), validation.Min(8)},
//	    }
//	}
//
// The return type is validation.Rules so the same value can be passed
// straight into validation.Check / dbrules.CheckWithDB without intermediate
// conversion.
//
// It is an alias for router.Validatable, the same contract read by
// ctx.BindValid: one form struct serves both entry points, and there is one
// declaration to satisfy rather than two identical ones.
type FormRequest = router.Validatable

// WithMessages can be implemented alongside FormRequest to override the
// message a given field+rule pair produces.
//
//	func (r *SignupRequest) ValidationMessages() validation.Messages {
//	    return validation.Messages{
//	        {Field: "email", Rule: "required"}: "We need your email.",
//	    }
//	}
type WithMessages interface {
	ValidationMessages() contract.ValidationMessages
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
//   - error:   non-nil only for non-validation failures: a body the check
//     cannot use (the *http.MaxBytesError of one over the limit, a
//     400 *contract.HTTPError for a malformed one), a malformed rule
//     set, or a decode/bind error. Validation errors travel through
//     *Result, never the error return.
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
					"change the signature to `Rules() validation.Rules`",
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
		msgs = append(msgs, wm.ValidationMessages())
	}

	// ctx.Response is threaded into the body-read path so
	// http.MaxBytesReader can signal a connection-close hint on
	// oversized bodies (rule 5). A body the check cannot use is returned
	// as is: the *http.MaxBytesError of one over the limit (answered 413)
	// or the 400 for a malformed one.
	data, err := validation.ExtractRequestDataLimited(ctx.Response, ctx.Request, validation.DefaultMaxBodyBytes)
	if err != nil {
		return nil, nil, err
	}
	result, err := dbrules.CheckDataWithDBCtx(ctx.Request.Context(), data, rules, safeDB(ctx), msgs...)
	if err != nil {
		return nil, nil, fmt.Errorf("velocity/vform: %T rule set is invalid: %w", req, err)
	}
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
// or before service wiring completes. A rule set that names Unique or Exists
// with no database attached has no handler for them, which Validate reports
// as an error rather than failing the field.
func safeDB(ctx *router.Context) orm.Database {
	s := ctx.ServicesIfSet()
	if s == nil || s.DB == nil {
		return nil
	}
	// s.DB is the stdlib-only contract.Database; the framework always stores
	// the concrete *orm.Manager, which also satisfies the richer orm.Database.
	// An adopter could install a different contract.Database, so assert with
	// comma-ok and treat a mismatch as "no DB" rather than panicking in
	// library code.
	db, ok := s.DB.(orm.Database)
	if !ok {
		return nil
	}
	return db
}

// Form binds the request body into a fresh *T, validates using T.Rules() if
// T implements FormRequest, and returns *T on success. On validation
// failure it writes nothing and returns a *validation.Failure; the handler
// returns it and the error pipeline answers: the errors and old input
// flashed plus a redirect back for a browser when a view engine is wired,
// 422 application/problem+json with the per-field errors otherwise, unless
// an application map or render rule for the failure answers first.
//
// Adopters that want to render a custom error view instead of redirecting
// back can register a render rule for *validation.Failure, or call
// Validate[T] directly and inspect the returned *Result.
func Form[T any](ctx *router.Context) (*T, error) {
	req, result, err := Validate[T](ctx)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return req, nil
	}
	return nil, validation.NewFailure(result)
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
	// output of type validation.Rules.
	if t.NumIn() == 0 && t.NumOut() == 1 && t.Out(0) == reflect.TypeOf(validation.Rules(nil)) {
		return "", false
	}
	return t.String(), true
}
