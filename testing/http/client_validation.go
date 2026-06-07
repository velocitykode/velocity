package http

import (
	"net/http"
)

// ---------------------------------------------------------------------------
// Validation assertions
// ---------------------------------------------------------------------------
//
// A validation failure renders as HTTP 422 with the JSON shape:
//
//	{"message": "...", "errors": {"field": ["msg1", "msg2"]}}
//
// The value of each field under "errors" is an ARRAY of messages.

// validationErrors decodes the response body and returns the "errors" object as
// a map of field name to its list of messages. It reports a decode/shape error
// via r.t.Errorf and returns nil if the body is not the expected shape.
func (r *TestResponse) validationErrors() map[string][]string {
	body := r.decodeJSON()
	if body == nil {
		return nil
	}

	raw, ok := body["errors"]
	if !ok {
		r.t.Errorf("expected response body to contain an \"errors\" object, but none found")
		return nil
	}

	errsObj, ok := raw.(map[string]any)
	if !ok {
		r.t.Errorf("expected \"errors\" to be a JSON object, got %T", raw)
		return nil
	}

	out := make(map[string][]string, len(errsObj))
	for field, v := range errsObj {
		arr, ok := v.([]any)
		if !ok {
			r.t.Errorf("expected \"errors\"[%q] to be an array of strings, got %T", field, v)
			return nil
		}
		msgs := make([]string, 0, len(arr))
		for _, m := range arr {
			s, ok := m.(string)
			if !ok {
				r.t.Errorf("expected \"errors\"[%q] to contain only strings, got %T", field, m)
				return nil
			}
			msgs = append(msgs, s)
		}
		out[field] = msgs
	}
	return out
}

// AssertInvalid asserts the response is a 422 validation failure and that each
// named field has at least one error message.
func (r *TestResponse) AssertInvalid(fields ...string) *TestResponse {
	r.t.Helper()
	r.AssertUnprocessable()

	errs := r.validationErrors()
	if errs == nil {
		return r
	}

	for _, field := range fields {
		msgs, ok := errs[field]
		if !ok || len(msgs) == 0 {
			r.t.Errorf("expected validation error for field %q, but none found", field)
		}
	}
	return r
}

// AssertValidationErrors asserts each field maps to error messages containing
// the expected ones. Each expected message must appear (as an exact match)
// among the field's actual messages.
func (r *TestResponse) AssertValidationErrors(expected map[string][]string) *TestResponse {
	r.t.Helper()
	r.AssertUnprocessable()

	errs := r.validationErrors()
	if errs == nil {
		return r
	}

	for field, wantMsgs := range expected {
		gotMsgs, ok := errs[field]
		if !ok {
			r.t.Errorf("expected validation error for field %q, but none found", field)
			continue
		}
		for _, want := range wantMsgs {
			if !containsString(gotMsgs, want) {
				r.t.Errorf("expected field %q to contain error message %q, got %v", field, want, gotMsgs)
			}
		}
	}
	return r
}

// AssertValid asserts the response is not a 422 and carries no non-empty errors
// object.
func (r *TestResponse) AssertValid() *TestResponse {
	r.t.Helper()

	if r.recorder.Code == http.StatusUnprocessableEntity {
		r.t.Errorf("expected a valid response, got status %d", r.recorder.Code)
	}

	body := r.decodeJSON()
	if body == nil {
		return r
	}
	if raw, ok := body["errors"]; ok {
		if errsObj, ok := raw.(map[string]any); ok && len(errsObj) > 0 {
			r.t.Errorf("expected no validation errors, got %v", errsObj)
		}
	}
	return r
}

// containsString reports whether s is present in list.
func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
