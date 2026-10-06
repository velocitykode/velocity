package router

import (
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

type nilProtector struct{ contract.CSRFProtector }

// CSRFMiddleware with a typed-nil protector protects nothing, as with nil.
func TestCSRFMiddleware_TypedNilProtectorPassesThrough(t *testing.T) {
	var typed *nilProtector
	for name, protector := range map[string]contract.CSRFProtector{"nil": nil, "typed nil": typed} {
		t.Run(name, func(t *testing.T) {
			called := false
			h := CSRFMiddleware(protector)(func(*Context) error { called = true; return nil })
			c, _ := NewTestContext(http.MethodPost, "/x")
			if err := h(c); err != nil {
				t.Fatalf("handler = %v, want nil", err)
			}
			if !called {
				t.Error("next was not called")
			}
		})
	}
}
