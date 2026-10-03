package router_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/router"
)

type unregisteredThing struct{}

// TestService_MissingReportsServiceNotConfigured pins that Service never
// panics and reports the one missing-service error: a nil context or one
// without services names "services", an unregistered component its key.
func TestService_MissingReportsServiceNotConfigured(t *testing.T) {
	bare, _ := router.NewTestContext(http.MethodGet, "/")
	wired, _ := router.NewTestContext(http.MethodGet, "/")
	wired.SetServices(&app.Services{})
	for _, tc := range []struct {
		name string
		c    *router.Context
		want string
	}{
		{"nil context", nil, "services"},
		{"no services", bare, "services"},
		{"unregistered", wired, "*router_test.unregisteredThing"},
	} {
		v, err := router.Service[*unregisteredThing](tc.c)
		var snc *contract.ServiceNotConfiguredError
		if v != nil || !errors.Is(err, contract.ErrServiceNotConfigured) || !errors.As(err, &snc) || snc.Service != tc.want {
			t.Errorf("%s: Service = %v, %v; want nil and a ServiceNotConfiguredError naming %q", tc.name, v, err, tc.want)
		}
	}
}
