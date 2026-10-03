package app_test

import (
	"errors"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
)

type missingThing struct{}

// TestGet_MissingReportsServiceNotConfigured pins the lookup failures to the
// one missing-service error: an unregistered key names the component key, a
// nil container names "services".
func TestGet_MissingReportsServiceNotConfigured(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    *app.Services
		want string
	}{
		{"unregistered", &app.Services{}, "*app_test.missingThing"},
		{"nil services", nil, "services"},
	} {
		v, err := app.Get[*missingThing](tc.s)
		var snc *contract.ServiceNotConfiguredError
		if v != nil || !errors.Is(err, contract.ErrServiceNotConfigured) || !errors.As(err, &snc) || snc.Service != tc.want {
			t.Errorf("%s: Get = %v, %v; want nil and a ServiceNotConfiguredError naming %q", tc.name, v, err, tc.want)
		}
	}
}
