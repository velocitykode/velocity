package validate

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/validation"
)

// Rules maps field names to a list of validation rules.
//
//	validate.Rules{
//	    "title": {"required", "min:3"},
//	    "email": {"required", "email"},
//	}
type Rules map[string][]string

// Messages maps "field.rule" keys to custom error messages.
//
//	validate.Messages{
//	    "title.required": "Please enter a title",
//	    "email.email":    "Please enter a valid email",
//	}
type Messages map[string]string

// Check validates request data against the given rules.
// It extracts form values or JSON body from the request automatically.
func Check(r *http.Request, rules Rules, messages ...Messages) *Errors {
	data := extractRequestData(r)
	return run(data, rules, nil, messages...)
}

// CheckData validates a pre-extracted data map against rules.
// Use this when you've already parsed the request body (e.g. via ctx.Bind).
func CheckData(data map[string]interface{}, rules Rules, messages ...Messages) *Errors {
	return run(data, rules, nil, messages...)
}

// checkWithDB validates request data with database rules (unique, exists) available.
func checkWithDB(r *http.Request, rules Rules, db *orm.Manager, messages ...Messages) *Errors {
	data := extractRequestData(r)
	return run(data, rules, db, messages...)
}

// run validates data against rules using the framework's validator.
func run(data map[string]interface{}, rules Rules, db *orm.Manager, messages ...Messages) *Errors {
	v := validation.NewValidator()

	// Register database rules when DB is available
	if db != nil {
		v.RegisterRule("unique", uniqueRule(db))
		v.RegisterRule("exists", existsRule(db))
	}

	// Convert []string rules to pipe-separated format
	vRules := make(validation.Rules, len(rules))
	for field, fieldRules := range rules {
		vRules[field] = strings.Join(fieldRules, "|")
	}

	if len(messages) > 0 {
		v.SetMessages(validation.Messages(messages[0]))
	}

	result := &Errors{input: data}

	_, err := v.Validate(data, vRules)
	if err != nil {
		if ve, ok := err.(validation.ValidationErrors); ok {
			result.errors = ve.Errors
		}
	}

	return result
}

// extractRequestData reads form values or JSON body from the request.
func extractRequestData(r *http.Request) map[string]interface{} {
	ct := r.Header.Get("Content-Type")

	// Try JSON first for application/json
	if strings.HasPrefix(ct, "application/json") {
		var data map[string]interface{}
		body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10MB limit
		if err == nil && len(body) > 0 {
			// Restore body so ctx.Bind() can read it again
			r.Body = io.NopCloser(bytes.NewReader(body))
			if json.Unmarshal(body, &data) == nil {
				return data
			}
		}
	}

	// Fall back to form data (idempotent after first parse)
	if err := r.ParseForm(); err == nil && len(r.Form) > 0 {
		data := make(map[string]interface{}, len(r.Form))
		for key, values := range r.Form {
			if len(values) == 1 {
				data[key] = values[0]
			} else {
				data[key] = values
			}
		}
		return data
	}

	return make(map[string]interface{})
}
