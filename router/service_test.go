package router_test

import (
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/router"
)

// fixture is a concrete component type registered for retrieval in these tests.
type fixture struct{ name string }

// altQualifier is a marker type for the qualified-retrieval variant.
type altQualifier struct{}

func TestService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func() *router.Context
		wantErr bool
		want    string
	}{
		{
			name: "default qualifier retrieval",
			setup: func() *router.Context {
				c, _ := router.NewTestContext(http.MethodGet, "/")
				s := &app.Services{}
				if err := app.Register(s, &fixture{name: "primary"}); err != nil {
					t.Fatalf("register: %v", err)
				}
				c.SetServices(s)
				return c
			},
			want: "primary",
		},
		{
			name: "services never set",
			setup: func() *router.Context {
				c, _ := router.NewTestContext(http.MethodGet, "/")
				return c
			},
			wantErr: true,
		},
		{
			name: "component missing",
			setup: func() *router.Context {
				c, _ := router.NewTestContext(http.MethodGet, "/")
				c.SetServices(&app.Services{})
				return c
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := router.Service[*fixture](tt.setup())
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got value %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.name != tt.want {
				t.Fatalf("got %q, want %q", got.name, tt.want)
			}
		})
	}
}

func TestServiceFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func() *router.Context
		wantErr bool
		want    string
	}{
		{
			name: "qualified retrieval",
			setup: func() *router.Context {
				c, _ := router.NewTestContext(http.MethodGet, "/")
				s := &app.Services{}
				if err := app.RegisterFor[*fixture, altQualifier](s, &fixture{name: "alt"}); err != nil {
					t.Fatalf("register: %v", err)
				}
				c.SetServices(s)
				return c
			},
			want: "alt",
		},
		{
			name: "qualifier mismatch is a miss",
			setup: func() *router.Context {
				c, _ := router.NewTestContext(http.MethodGet, "/")
				s := &app.Services{}
				// Registered under the default qualifier, not altQualifier.
				if err := app.Register(s, &fixture{name: "default"}); err != nil {
					t.Fatalf("register: %v", err)
				}
				c.SetServices(s)
				return c
			},
			wantErr: true,
		},
		{
			name: "services never set",
			setup: func() *router.Context {
				c, _ := router.NewTestContext(http.MethodGet, "/")
				return c
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := router.ServiceFor[*fixture, altQualifier](tt.setup())
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got value %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.name != tt.want {
				t.Fatalf("got %q, want %q", got.name, tt.want)
			}
		})
	}
}
