package view

import (
	"errors"
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

// TestRender_NoEngine_EntryPointsAgree asserts both Render entry points
// answer a context without a view engine the same way: ErrNoEngine and
// nothing written, so the handler's return reaches the error pipeline.
func TestRender_NoEngine_EntryPointsAgree(t *testing.T) {
	tests := []struct {
		name     string
		services *app.Services
	}{
		{name: "bare context"},
		{name: "services without view", services: &app.Services{}},
		{name: "view not an engine", services: &app.Services{View: stubViewEngine{}}},
	}
	renders := map[string]func(*router.Context) error{
		"view.Render":     func(c *router.Context) error { return Render(c, "Comp") },
		"For(ctx).Render": func(c *router.Context) error { return For(c).Render("Comp") },
	}
	for _, tt := range tests {
		for entry, render := range renders {
			t.Run(tt.name+"/"+entry, func(t *testing.T) {
				ctx, rec := router.NewTestContext("GET", "/")
				if tt.services != nil {
					ctx.SetServices(tt.services)
				}
				if err := render(ctx); !errors.Is(err, ErrNoEngine) {
					t.Fatalf("err = %v, want ErrNoEngine", err)
				}
				if rec.Body.Len() != 0 || len(rec.Header()) != 0 {
					t.Errorf("wrote body %q headers %v, want nothing", rec.Body.String(), rec.Header())
				}
			})
		}
	}
}
