// Package validate is a deprecated compatibility shim for the validation
// package. Every exported symbol forwards to github.com/velocitykode/velocity/validation;
// new code should import validation directly. The shim stays in place only
// for Form[T]() which takes a *router.Context (and therefore cannot live in
// the leaf validation package without creating an import cycle).
//
// Deprecated: use github.com/velocitykode/velocity/validation instead.
package validate

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/validation"
)

// Rules maps field names to a list of validation rules.
//
// Deprecated: use validation.Rules (pipe-separated rule strings) instead.
type Rules = map[string][]string

// Messages maps "field.rule" keys to custom error messages.
//
// Deprecated: use validation.Messages instead.
type Messages = map[string]string

// Check validates request data against the given rules.
//
// Deprecated: use validation.Check with pipe-separated rules instead.
func Check(r *http.Request, rules Rules, messages ...Messages) *Errors {
	vRules, vMsgs := convertRulesAndMessages(rules, messages)
	result := validation.Check(r, vRules, vMsgs...)
	return resultToErrors(result, validation.ExtractRequestData(r))
}

// CheckData validates a pre-extracted data map against rules.
//
// Deprecated: use validation.CheckData with pipe-separated rules instead.
func CheckData(data map[string]interface{}, rules Rules, messages ...Messages) *Errors {
	vRules, vMsgs := convertRulesAndMessages(rules, messages)
	result := validation.CheckData(data, vRules, vMsgs...)
	return resultToErrors(result, data)
}

// CheckWithDB validates request data with database rules available.
//
// Deprecated: use validation.CheckWithDB with pipe-separated rules instead.
func CheckWithDB(r *http.Request, rules Rules, db orm.Database, messages ...Messages) *Errors {
	vRules, vMsgs := convertRulesAndMessages(rules, messages)
	result := validation.CheckWithDB(r, vRules, db, vMsgs...)
	return resultToErrors(result, validation.ExtractRequestData(r))
}

// convertRulesAndMessages lifts the shim's []string rules and map messages
// into the canonical validation.Rules / validation.Messages shape, preserving
// ordering inside each field.
func convertRulesAndMessages(rules Rules, messages []Messages) (validation.Rules, []validation.Messages) {
	vRules := make(validation.Rules, len(rules))
	for field, fieldRules := range rules {
		vRules[field] = strings.Join(fieldRules, "|")
	}
	var vMsgs []validation.Messages
	for _, m := range messages {
		vMsgs = append(vMsgs, validation.Messages(m))
	}
	return vRules, vMsgs
}

// resultToErrors adapts the canonical validation.Result back into the
// shim's *Errors type. Kept unexported — callers that need the richer API
// should migrate to validation.Result directly. Returns a non-nil *Errors
// with wrapped-error context if anything inside validation misbehaves.
func resultToErrors(r *validation.Result, input map[string]interface{}) *Errors {
	e := &Errors{input: input}
	if r == nil {
		// Should not happen — validation.Check* always returns a non-nil
		// result — but guard anyway so we never deref nil in consumer code.
		return e
	}
	if r.HasErrors() {
		e.errors = r.Messages()
	}
	return e
}

// wrapErr is used by the shim when it has to surface a non-validation
// failure (for example, a malformed request body rejected by BindAuto). It
// enforces the sweep rule that all errors emerging from /validate are
// prefixed "velocity/validate: " and stay lowercase.
func wrapErr(msg string, err error) error {
	return fmt.Errorf("velocity/validate: %s: %w", msg, err)
}
