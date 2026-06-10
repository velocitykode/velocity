package csrf

import (
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/router"
)

// FromContext documents "returns nil if CSRF is not configured"; that
// contract must hold on a bare context with no service container at
// all (previously ctx.CSRF() panicked via mustServices).
func TestFromContext_NoServices_ReturnsNil(t *testing.T) {
	ctx, _ := router.NewTestContext("GET", "/")
	if c := FromContext(ctx); c != nil {
		t.Errorf("expected nil CSRF on bare context, got %v", c)
	}
}

func TestFromContext_ServicesWithoutCSRF_ReturnsNil(t *testing.T) {
	ctx, _ := router.NewTestContext("GET", "/")
	ctx.SetServices(&app.Services{})
	if c := FromContext(ctx); c != nil {
		t.Errorf("expected nil CSRF when services.CSRF is unset, got %v", c)
	}
}
