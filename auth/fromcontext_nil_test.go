package auth

import (
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/router"
)

// FromContext documents "returns nil if auth is not configured"; that
// contract must hold on a bare context with no service container at
// all (previously ctx.Auth() panicked via mustServices).
func TestFromContext_NoServices_ReturnsNil(t *testing.T) {
	ctx, _ := router.NewTestContext("GET", "/")
	if m := FromContext(ctx); m != nil {
		t.Errorf("expected nil manager on bare context, got %v", m)
	}
}

func TestFromContext_ServicesWithoutAuth_ReturnsNil(t *testing.T) {
	ctx, _ := router.NewTestContext("GET", "/")
	ctx.SetServices(&app.Services{})
	if m := FromContext(ctx); m != nil {
		t.Errorf("expected nil manager when services.Auth is unset, got %v", m)
	}
}
