package view

import (
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/router"
)

// FromContext documents "returns nil if view is not configured"; that
// contract must hold on a bare context with no service container at
// all (previously ctx.View() panicked via mustServices). For inherits
// the same contract.
func TestFromContext_NoServices_ReturnsNil(t *testing.T) {
	ctx, _ := router.NewTestContext("GET", "/")
	if e := FromContext(ctx); e != nil {
		t.Errorf("expected nil engine on bare context, got %v", e)
	}
	if re := For(ctx); re != nil {
		t.Errorf("expected nil ReqEngine on bare context, got %v", re)
	}
}

func TestFromContext_ServicesWithoutView_ReturnsNil(t *testing.T) {
	ctx, _ := router.NewTestContext("GET", "/")
	ctx.SetServices(&app.Services{})
	if e := FromContext(ctx); e != nil {
		t.Errorf("expected nil engine when services.View is unset, got %v", e)
	}
}
