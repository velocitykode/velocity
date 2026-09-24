package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// TestContext_AcceptsAgreesWithWantsJSON asserts ctx.Accepts and
// ctx.WantsJSON read a q-weighted Accept header the same way: the
// preferred range wins, the first listed among equal q, q=0 excluded.
func TestContext_AcceptsAgreesWithWantsJSON(t *testing.T) {
	tests := []struct {
		accept   string
		wantJSON bool
	}{
		{"application/json", true},
		{"text/html", false},
		{"text/html;q=0.9, application/json", true},
		{"application/json;q=0.9, text/html", false},
		{"application/json, text/html", true},
		{"text/html, application/json", false},
		{"text/html;q=0.5, application/json;q=0.5", false},
		{"application/json;q=0, text/html;q=0.1", false},
		{"text/html;q=0, application/json;q=0.1", true},
		{"TEXT/HTML;Q=0.2, Application/JSON;q=0.8", true},
	}
	for _, tt := range tests {
		t.Run(tt.accept, func(t *testing.T) {
			c, _ := NewTestContext(http.MethodGet, "/")
			c.Request.Header.Set("Accept", tt.accept)
			accepted := c.Accepts("application/json", "text/html")
			if (accepted == "application/json") != tt.wantJSON {
				t.Errorf("Accepts = %q, want JSON %v", accepted, tt.wantJSON)
			}
			if got := c.WantsJSON(); got != tt.wantJSON {
				t.Errorf("WantsJSON = %v, want %v", got, tt.wantJSON)
			}
		})
	}
}

// TestContext_IsAjax_AnyCase asserts IsAjax matches X-Requested-With
// without case, as WantsJSON does.
func TestContext_IsAjax_AnyCase(t *testing.T) {
	for value, want := range map[string]bool{
		"XMLHttpRequest": true,
		"xmlhttprequest": true,
		"XMLHTTPREQUEST": true,
		"fetch":          false,
		"":               false,
	} {
		c, _ := NewTestContext(http.MethodGet, "/")
		if value != "" {
			c.Request.Header.Set("X-Requested-With", value)
		}
		if got := c.IsAjax(); got != want {
			t.Errorf("IsAjax(%q) = %v, want %v", value, got, want)
		}
	}
}

// TestContext_IsInertia_MatchesContract asserts ctx.IsInertia accepts only
// "true" in any case, like contract.IsInertia and bond.
func TestContext_IsInertia_MatchesContract(t *testing.T) {
	for value, want := range map[string]bool{
		"true":  true,
		"TRUE":  true,
		"1":     false,
		"false": false,
		"":      false,
	} {
		c, _ := NewTestContext(http.MethodGet, "/")
		if value != "" {
			c.Request.Header.Set("X-Inertia", value)
		}
		if got := c.IsInertia(); got != want || got != contract.IsInertia(c.Request) {
			t.Errorf("IsInertia(%q) = %v, want %v", value, got, want)
		}
	}
}

// TestDefaultErrorHandler_VaryAccept asserts the standalone error writer
// lists Accept in Vary when the Accept header chose the body (JSON or
// plain text), keeps a Vary the handler set, and adds nothing for an
// Inertia request, whose answer X-Inertia fixes.
func TestDefaultErrorHandler_VaryAccept(t *testing.T) {
	tests := []struct {
		name     string
		accept   string
		inertia  bool
		preVary  string
		wantVary []string
	}{
		{name: "plain text", wantVary: []string{"Accept"}},
		{name: "json", accept: "application/json", wantVary: []string{"Accept"}},
		{name: "keeps handler vary", accept: "application/json", preVary: "Origin", wantVary: []string{"Origin", "Accept"}},
		{name: "no duplicate", preVary: "accept", wantVary: []string{"accept"}},
		{name: "inertia", inertia: true, wantVary: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New()
			r.Get("/x", func(c *Context) error {
				if tt.preVary != "" {
					c.Response.Header().Set("Vary", tt.preVary)
				}
				return errors.New("boom")
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}
			if tt.inertia {
				req.Header.Set("X-Inertia", "true")
			}
			r.ServeHTTP(w, req)
			got := w.Header().Values("Vary")
			if len(got) != len(tt.wantVary) {
				t.Fatalf("Vary = %v, want %v", got, tt.wantVary)
			}
			for i := range got {
				if got[i] != tt.wantVary[i] {
					t.Errorf("Vary = %v, want %v", got, tt.wantVary)
				}
			}
		})
	}
}

// TestDefaultErrorHandler_SharedVaryIsNotMutated asserts a later Add on
// one response's Vary never changes the next response's value.
func TestDefaultErrorHandler_SharedVaryIsNotMutated(t *testing.T) {
	h := http.Header{}
	varyOnAccept(h)
	h.Add("Vary", "Origin")
	h2 := http.Header{}
	varyOnAccept(h2)
	if got := h2.Values("Vary"); len(got) != 1 || got[0] != "Accept" || varyAccept[0] != "Accept" || len(varyAccept) != 1 {
		t.Errorf("second response Vary = %v (shared %v), want [Accept]", got, varyAccept)
	}
}
