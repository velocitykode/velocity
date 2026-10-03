package view

import (
	"errors"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/router"
)

// TestRender_NoEngine_EntryPointsAgree asserts every Render and redirect
// entry point answers a context without a view engine the same way: the
// missing-service error and nothing written, so the handler's return
// reaches the error pipeline.
func TestRender_NoEngine_EntryPointsAgree(t *testing.T) {
	tests := []struct {
		name     string
		services *app.Services
		want     string
	}{
		{name: "bare context", want: "services"},
		{name: "services without view", services: &app.Services{}, want: "view"},
		{name: "typed-nil view", services: &app.Services{View: (*Engine)(nil)}, want: "view"},
	}
	renders := map[string]func(*router.Context) error{
		"view.Render":           func(c *router.Context) error { return Render(c, "Comp") },
		"For":                   func(c *router.Context) error { _, err := For(c); return err },
		"view.Redirect":         func(c *router.Context) error { return Redirect(c, "/x") },
		"view.Location":         func(c *router.Context) error { return Location(c, "/x") },
		"view.LocationExternal": func(c *router.Context) error { return LocationExternal(c, "https://x.example") },
		"view.Back":             func(c *router.Context) error { return Back(c) },
	}
	for _, tt := range tests {
		for entry, render := range renders {
			t.Run(tt.name+"/"+entry, func(t *testing.T) {
				ctx, rec := router.NewTestContext("GET", "/")
				if tt.services != nil {
					ctx.SetServices(tt.services)
				}
				var snc *contract.ServiceNotConfiguredError
				if err := render(ctx); !errors.Is(err, contract.ErrServiceNotConfigured) || !errors.As(err, &snc) || snc.Service != tt.want {
					t.Fatalf("err = %v, want a ServiceNotConfiguredError naming %q", err, tt.want)
				}
				if rec.Body.Len() != 0 || len(rec.Header()) != 0 {
					t.Errorf("wrote body %q headers %v, want nothing", rec.Body.String(), rec.Header())
				}
			})
		}
	}
}

// TestHelpers_NilContextReports pins that a nil *Context argument reports
// the missing-service error naming "services" instead of panicking.
func TestHelpers_NilContextReports(t *testing.T) {
	for name, call := range map[string]func() error{
		"Render":   func() error { return Render(nil, "Comp") },
		"Redirect": func() error { return Redirect(nil, "/x") },
		"Back":     func() error { return Back(nil) },
		"For":      func() error { _, err := For(nil); return err },
	} {
		var snc *contract.ServiceNotConfiguredError
		if err := call(); !errors.As(err, &snc) || snc.Service != "services" {
			t.Errorf("%s(nil) = %v, want a ServiceNotConfiguredError naming services", name, err)
		}
	}
}
