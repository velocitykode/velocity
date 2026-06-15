package http

import "strings"

// ---------------------------------------------------------------------------
// Body presence aliases
// ---------------------------------------------------------------------------

// AssertSee asserts the response body contains the given text.
// It is a thin alias over AssertBodyContains.
func (r *TestResponse) AssertSee(text string) *TestResponse {
	r.t.Helper()
	return r.AssertBodyContains(text)
}

// AssertDontSee asserts the response body does not contain the given text.
func (r *TestResponse) AssertDontSee(text string) *TestResponse {
	r.t.Helper()
	body := r.recorder.Body.String()
	if strings.Contains(body, text) {
		r.t.Errorf("expected body NOT to contain %q, got %q", text, body)
	}
	return r
}
