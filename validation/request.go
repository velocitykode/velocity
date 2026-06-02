package validation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// DefaultMaxBodyBytes is the hard cap applied to JSON / form bodies the
// validation package extracts via ExtractRequestData / CheckW / CheckWithDBW.
// It mirrors router.DefaultMaxBodySize (10 MiB) so the two body-read paths
// agree on a single limit. Adopters with stricter or laxer needs can pass
// a custom limit through ExtractRequestDataLimited.
const DefaultMaxBodyBytes int64 = 10 * 1024 * 1024

// sensitiveFieldSubstrings is the best-effort list of field-name substrings
// that Result.Old redacts before returning input for client-side form replay.
// Keep this list auditable and extend it when new sensitive naming conventions
// are identified.
var sensitiveFieldSubstrings = []string{
	"password",
	"passwd",
	"passcode",
	"secret",
	"token",
	"pin",
	"cvv",
	"cvc",
	"card",
	"ssn",
	"otp",
	"credential",
	"credentials",
	"api_key",
	"apikey",
	"private_key",
	"privatekey",
}

// Check validates request data against the given rules.
// It extracts form values or JSON body from the request automatically.
//
// Prefer CheckW(w, r, ...) when a *http.ResponseWriter is available so the
// body read is wrapped with http.MaxBytesReader and an oversized body can
// signal the connection to close. This shim passes a nil writer; the
// MaxBytesReader call still enforces the read cap, it just can't trigger
// the optional requestTooLarge connection-close optimisation.
func Check(r *http.Request, rules Rules, messages ...Messages) *Result {
	return CheckW(nil, r, rules, messages...)
}

// CheckW is Check but with a *http.ResponseWriter so the body extraction
// can wrap r.Body with http.MaxBytesReader properly. Returns a result with
// a sentinel field-level error on oversized body (rather than truncating
// silently the way io.LimitReader did).
func CheckW(w http.ResponseWriter, r *http.Request, rules Rules, messages ...Messages) *Result {
	return CheckWithRulesW(w, r, rules, nil, messages...)
}

// CheckData validates a pre-extracted data map against rules.
func CheckData(data map[string]interface{}, rules Rules, messages ...Messages) *Result {
	return CheckDataWithRules(data, rules, nil, messages...)
}

// DB-backed validation (the unique / exists rules, plus the driver-error
// mapper AsValidationError) now lives in the validation/dbrules subpackage so
// this core package stays free of orm and SQL-driver dependencies. New code
// should call the dbrules variants:
//
//	validation.CheckWithDB      -> dbrules.CheckWithDB
//	validation.CheckWithDBW     -> dbrules.CheckWithDBW
//	validation.CheckDataWithDB  -> dbrules.CheckDataWithDB
//	validation.CheckDataWithDBCtx -> dbrules.CheckDataWithDBCtx
//	validation.UniqueRule/Ctx   -> dbrules.UniqueRule/Ctx
//	validation.ExistsRule/Ctx   -> dbrules.ExistsRule/Ctx
//	validation.AsValidationError -> dbrules.AsValidationError
//
// The old validation.* names are retained as DEPRECATED, orm-free
// compatibility shims in dbrules_compat.go (they reach the database
// structurally via reflection) so existing callers keep compiling and
// working. See CHANGELOG "DB-backed validation API moved out of validation"
// for the rationale (decoupling orm/driver deps from the core package).

// CheckWithRulesW is the engine behind DB-backed Check helpers that live in
// subpackages (e.g. validation/dbrules) so the core validation package need
// not import orm or a SQL driver. It extracts the request body via the same
// MaxBytesReader-wrapped path as CheckW, then runs validation with the
// supplied extra rule handlers registered on the validator.
//
// extra maps rule name -> handler; a nil or empty map runs the standard,
// orm-free rule set only. Subpackages pass DB-backed handlers (unique,
// exists) here so cancellation and quoting live with the orm dependency.
func CheckWithRulesW(w http.ResponseWriter, r *http.Request, rules Rules, extra map[string]RuleHandler, messages ...Messages) *Result {
	data, bodyErr := extractRequestDataW(w, r, DefaultMaxBodyBytes)
	if bodyErr != nil {
		return resultForBodyError(bodyErr)
	}
	return runWithRules(data, rules, extra, messages...)
}

// CheckDataWithRules is the pre-extracted-data variant of CheckWithRulesW.
// extra maps rule name -> handler; a nil or empty map runs the standard
// rule set only.
func CheckDataWithRules(data map[string]interface{}, rules Rules, extra map[string]RuleHandler, messages ...Messages) *Result {
	return runWithRules(data, rules, extra, messages...)
}

// Result holds the outcome of a validation check.
// It carries both error messages and the original input data so that
// view helpers can re-populate forms with old values automatically.
type Result struct {
	errors map[string][]string
	input  map[string]interface{}
}

// HasErrors returns true if validation failed.
func (r *Result) HasErrors() bool {
	return len(r.errors) > 0
}

// First returns the first error message for a field, or "".
func (r *Result) First(field string) string {
	if msgs, ok := r.errors[field]; ok && len(msgs) > 0 {
		return msgs[0]
	}
	return ""
}

// All returns the first error per field as a flat map.
// This matches the Inertia convention where errors is { field: "message" }.
func (r *Result) All() map[string]string {
	result := make(map[string]string, len(r.errors))
	for field, msgs := range r.errors {
		if len(msgs) > 0 {
			result[field] = msgs[0]
		}
	}
	return result
}

// Messages returns all error messages grouped by field.
func (r *Result) Messages() map[string][]string {
	return r.errors
}

// Err returns nil when validation passed, or an error wrapping
// ErrValidationFailed when one or more fields failed. The returned error
// also satisfies errors.As(&ValidationErrors{}) so callers can access the
// per-field message map.
func (r *Result) Err() error {
	if r == nil || !r.HasErrors() {
		return nil
	}
	ve := ValidationErrors{Errors: r.errors}
	return &resultError{ve: ve}
}

// resultError wraps ValidationErrors and ErrValidationFailed so both
// errors.Is(err, ErrValidationFailed) and errors.As(&ValidationErrors{})
// succeed.
type resultError struct {
	ve ValidationErrors
}

func (e *resultError) Error() string { return e.ve.Error() }
func (e *resultError) Unwrap() error { return ErrValidationFailed }
func (e *resultError) As(target interface{}) bool {
	if p, ok := target.(*ValidationErrors); ok {
		*p = e.ve
		return true
	}
	return false
}

// Old returns the original input data with sensitive fields removed.
// Field names containing password, passwd, passcode, secret, token, pin, cvv,
// cvc, card, ssn, otp, credential, credentials, api_key, apikey, private_key,
// or privatekey are stripped automatically (case-insensitive). This is
// best-effort substring redaction, not an allowlist; adopters with unusually
// named sensitive fields should redact those values server-side before form
// replay.
func (r *Result) Old() map[string]interface{} {
	old := make(map[string]interface{}, len(r.input))
	for k, v := range r.input {
		lower := strings.ToLower(k)
		redacted := false
		for _, sensitive := range sensitiveFieldSubstrings {
			if strings.Contains(lower, sensitive) {
				redacted = true
				break
			}
		}
		if redacted {
			continue
		}

		old[k] = v
	}
	return old
}

// runWithRules validates data against rules using a fresh validator. Any
// handlers in extra are registered on the validator before validation runs.
// This is how DB-backed rules (unique, exists), which live in the
// validation/dbrules subpackage so the core package stays orm-free, get
// wired in without core taking an orm dependency.
func runWithRules(data map[string]interface{}, rules Rules, extra map[string]RuleHandler, messages ...Messages) *Result {
	v := NewValidator()

	for name, handler := range extra {
		v.RegisterRule(name, handler)
	}

	if len(messages) > 0 {
		v.SetMessages(messages[0])
	}

	result := &Result{input: data}

	_, err := v.Validate(data, rules)
	if err != nil {
		if ve, ok := err.(ValidationErrors); ok {
			result.errors = ve.Errors
		}
	}

	return result
}

// ExtractRequestData reads form values or JSON body from the request.
//
// Both branches are wrapped with http.MaxBytesReader (limit:
// DefaultMaxBodyBytes) instead of the legacy io.LimitReader that silently
// truncated oversize JSON bodies and left form bodies completely
// unbounded. When a *http.ResponseWriter is available, prefer
// ExtractRequestDataLimited(w, r, n) so the MaxBytesReader can also signal
// the server to close the connection on overrun.
//
// On oversized body the function still returns a (possibly empty) map
// instead of an error to keep the legacy signature; for the error-surfacing
// path used by Check / CheckWithDB, the package's CheckW / CheckWithDBW
// helpers call extractRequestDataW directly so callers can react with a
// proper field-level "body too large" validation error.
func ExtractRequestData(r *http.Request) map[string]interface{} {
	data, _ := extractRequestDataW(nil, r, DefaultMaxBodyBytes)
	return data
}

// ExtractRequestDataLimited is the ResponseWriter-aware, configurable-limit
// variant of ExtractRequestData. n is the maximum number of bytes that
// will be read from r.Body across the JSON and form branches. Returns
// nil, *http.MaxBytesError when the body exceeds n.
func ExtractRequestDataLimited(w http.ResponseWriter, r *http.Request, n int64) (map[string]interface{}, error) {
	return extractRequestDataW(w, r, n)
}

// extractRequestDataW is the implementation behind ExtractRequestData,
// ExtractRequestDataLimited, CheckW, and CheckWithDBW. The body is wrapped
// with http.MaxBytesReader so callers cannot silently truncate (the JSON
// io.LimitReader bug) and forms are no longer unbounded.
//
// The returned error is non-nil only for body-size overruns; everything
// else (malformed JSON, missing content type, etc.) falls back to an
// empty map for compatibility with the legacy ExtractRequestData
// signature.
func extractRequestDataW(w http.ResponseWriter, r *http.Request, n int64) (map[string]interface{}, error) {
	ct := r.Header.Get("Content-Type")

	// Try JSON first for application/json
	if strings.HasPrefix(ct, "application/json") {
		// Wrap the body. http.MaxBytesReader tolerates a nil w; the
		// optional requestTooLarge connection-close hint is skipped via
		// the type-assertion in maxBytesReader.Read when w is nil.
		r.Body = http.MaxBytesReader(w, r.Body, n)
		var data map[string]interface{}
		body, err := io.ReadAll(r.Body) //nolint:forbidigo // bounded by http.MaxBytesReader installed on r.Body above
		if err != nil {
			// MaxBytesError or any other read failure: surface up so
			// CheckW can flip the result into a validation error
			// instead of silently truncating the way the legacy
			// io.LimitReader path did.
			return nil, err
		}
		if len(body) > 0 {
			// Restore body so ctx.Bind() can read it again
			r.Body = io.NopCloser(bytes.NewReader(body))
			if json.Unmarshal(body, &data) == nil {
				return data, nil
			}
		}
		return make(map[string]interface{}), nil
	}

	// Fall back to form data. Wrap the body with MaxBytesReader BEFORE
	// ParseForm so an attacker cannot stream an unbounded
	// application/x-www-form-urlencoded body and exhaust memory. ParseForm
	// has its own 10 MiB default for non-POST queries but does not cap
	// POST body reads, so the wrapper is load-bearing here.
	r.Body = http.MaxBytesReader(w, r.Body, n)
	if err := r.ParseForm(); err != nil {
		// MaxBytesError surfaces through ParseForm's body read.
		if isMaxBytesError(err) {
			return nil, err
		}
		// Any other parse error (e.g. malformed urlencoding) falls
		// through to the empty-map return below, matching legacy
		// behaviour.
		return make(map[string]interface{}), nil
	}
	if len(r.Form) > 0 {
		data := make(map[string]interface{}, len(r.Form))
		for key, values := range r.Form {
			if len(values) == 1 {
				data[key] = values[0]
			} else {
				data[key] = values
			}
		}
		return data, nil
	}

	return make(map[string]interface{}), nil
}

// isMaxBytesError checks whether err (possibly wrapped) is an
// *http.MaxBytesError. ParseForm wraps the underlying body Read error
// in a multipart / url.ParseQuery layer, so we walk the chain.
func isMaxBytesError(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

// resultForBodyError builds a *Result that surfaces an oversized body
// as a field-level validation error keyed on "_body" so callers see a
// clear "request body too large" message instead of an empty validation
// pass that would have followed silent truncation. The 413-style
// connection-close hint, when supported, is fired by MaxBytesReader
// itself via the response writer (CheckW / CheckWithDBW path).
func resultForBodyError(err error) *Result {
	return &Result{
		errors: map[string][]string{
			"_body": {"The request body is too large."},
		},
		input: nil,
	}
}
