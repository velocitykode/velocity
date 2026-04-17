// Package validate provides router-aware validation helpers.
//
// Deprecated: Use the validation package directly for Check/CheckData/CheckWithDB.
// This package remains for Form[T]() which requires router.Context.
package validate

import (
	"net/http"
	"strings"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/validation"
)

// Rules maps field names to a list of validation rules.
//
// Deprecated: Use validation.Rules with pipe-separated rules instead.
type Rules = map[string][]string

// Messages maps "field.rule" keys to custom error messages.
//
// Deprecated: Use validation.Messages directly.
type Messages = map[string]string

// Check validates request data against the given rules.
//
// Deprecated: Use validation.Check with pipe-separated rules.
func Check(r *http.Request, rules Rules, messages ...Messages) *Errors {
	vRules := make(validation.Rules, len(rules))
	for field, fieldRules := range rules {
		vRules[field] = strings.Join(fieldRules, "|")
	}
	var vMsgs []validation.Messages
	for _, m := range messages {
		vMsgs = append(vMsgs, validation.Messages(m))
	}
	result := validation.Check(r, vRules, vMsgs...)
	return resultToErrors(result, validation.ExtractRequestData(r))
}

// CheckData validates a pre-extracted data map against rules.
//
// Deprecated: Use validation.CheckData with pipe-separated rules.
func CheckData(data map[string]interface{}, rules Rules, messages ...Messages) *Errors {
	vRules := make(validation.Rules, len(rules))
	for field, fieldRules := range rules {
		vRules[field] = strings.Join(fieldRules, "|")
	}
	var vMsgs []validation.Messages
	for _, m := range messages {
		vMsgs = append(vMsgs, validation.Messages(m))
	}
	result := validation.CheckData(data, vRules, vMsgs...)
	return resultToErrors(result, data)
}

// CheckWithDB validates request data with database rules available.
//
// Deprecated: Use validation.CheckWithDB with pipe-separated rules.
func CheckWithDB(r *http.Request, rules Rules, db orm.Database, messages ...Messages) *Errors {
	vRules := make(validation.Rules, len(rules))
	for field, fieldRules := range rules {
		vRules[field] = strings.Join(fieldRules, "|")
	}
	var vMsgs []validation.Messages
	for _, m := range messages {
		vMsgs = append(vMsgs, validation.Messages(m))
	}
	result := validation.CheckWithDB(r, vRules, db, vMsgs...)
	return resultToErrors(result, validation.ExtractRequestData(r))
}

func resultToErrors(r *validation.Result, input map[string]interface{}) *Errors {
	e := &Errors{input: input}
	if r != nil && r.HasErrors() {
		e.errors = r.Messages()
	}
	return e
}
