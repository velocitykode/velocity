package chain

import (
	"slices"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

func TestRouting_API_RegistersPrefix(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		groups   []string
		want     []string
	}{
		{name: "one group", groups: []string{"/api"}, want: []string{"/api"}},
		{name: "second group adds a second prefix", groups: []string{"/api", "/v2"}, want: []string{"/api", "/v2"}},
		{name: "same prefix twice registers once", groups: []string{"/api", "/api"}, want: []string{"/api"}},
		{name: "kept beside configured prefixes", existing: []string{"/rpc"}, groups: []string{"/api"}, want: []string{"/rpc", "/api"}},
		{name: "configured prefix not duplicated", existing: []string{"/api"}, groups: []string{"/api"}, want: []string{"/api"}},
		{name: "exact string, no normalisation", groups: []string{"/api/"}, want: []string{"/api/"}},
		{name: "empty prefix registers nothing", groups: []string{""}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := problem.NewHandler()
			h.SetAPIPrefixes(tt.existing...)
			routing := NewRouting(router.New(), NewMiddlewareStack(&app.Services{Errors: h}))

			for _, prefix := range tt.groups {
				routing.API(prefix, func(router.Router) {})
			}

			if got := h.GetAPIPrefixes(); !slices.Equal(got, tt.want) {
				t.Errorf("GetAPIPrefixes() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRouting_API_NoErrorHandler(t *testing.T) {
	tests := []struct {
		name     string
		services *app.Services
	}{
		{name: "nil services", services: nil},
		{name: "nil error handler", services: &app.Services{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routing := NewRouting(router.New(), NewMiddlewareStack(tt.services))
			called := false
			routing.API("/api", func(router.Router) { called = true })
			if !called {
				t.Error("group callback not run")
			}
		})
	}
}
