package router

import (
	"errors"
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/nilval"
)

var benchSinkAuth any

func BenchmarkContextAccessor(b *testing.B) {
	s := &app.Services{Auth: &mockAuthAccessChecker{allows: map[string]bool{}}, Errors: &fakeErrorHandler{}}
	b.Run("Auth", func(b *testing.B) {
		c := &Context{services: s}
		b.ReportAllocs()
		for b.Loop() {
			benchSinkAuth, _ = c.Auth()
		}
	})
	b.Run("AuthParallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			c := &Context{services: s}
			var v any
			for pb.Next() {
				v, _ = c.Auth()
			}
			benchSinkAuth = v
		})
	})
}

// accessorCalls drives every service accessor of c, by the name its
// missing-service error carries.
func accessorCalls(c *Context) map[string]func() (any, error) {
	wrap := func(v any, err error) (any, error) { return v, err }
	return map[string]func() (any, error){
		"database":     func() (any, error) { return wrap(c.DB()) },
		"cache":        func() (any, error) { return wrap(c.Cache()) },
		"queue":        func() (any, error) { return wrap(c.Queue()) },
		"storage":      func() (any, error) { return wrap(c.Storage()) },
		"mail":         func() (any, error) { return wrap(c.Mail()) },
		"notification": func() (any, error) { return wrap(c.Notification()) },
		"events":       func() (any, error) { return wrap(c.Events()) },
		"crypto":       func() (any, error) { return wrap(c.Crypto()) },
		"errors":       func() (any, error) { return wrap(c.Errors()) },
		"scheduler":    func() (any, error) { return wrap(c.Scheduler()) },
		"auth":         func() (any, error) { return wrap(c.Auth()) },
		"csrf":         func() (any, error) { return wrap(c.CSRF()) },
		"view":         func() (any, error) { return wrap(c.View()) },
	}
}

// TestContextAccessors_ReportMissingServices pins that no accessor panics:
// a nil context and a context without services name "services", and an
// empty container names the service; the value is always nil. Log never
// fails: it binds the fallback logger.
func TestContextAccessors_ReportMissingServices(t *testing.T) {
	bare, _ := NewTestContext(http.MethodGet, "/")
	empty, _ := NewTestContext(http.MethodGet, "/")
	empty.services = &app.Services{}
	for _, tc := range []struct {
		name string
		c    *Context
		want func(service string) string
	}{
		{"nil context", nil, func(string) string { return "services" }},
		{"no services", bare, func(string) string { return "services" }},
		{"empty services", empty, func(s string) string { return s }},
	} {
		for service, call := range accessorCalls(tc.c) {
			v, err := call()
			var snc *contract.ServiceNotConfiguredError
			if !nilval.Is(v) || !errors.Is(err, contract.ErrServiceNotConfigured) || !errors.As(err, &snc) || snc.Service != tc.want(service) {
				t.Errorf("%s: %s accessor = %v, %v; want nil and a ServiceNotConfiguredError naming %q", tc.name, service, v, err, tc.want(service))
			}
		}
		if s, err := tc.c.Services(); s != nil || !errors.As(err, new(*contract.ServiceNotConfiguredError)) || err.Error() != "velocity: services service not configured" {
			if tc.name != "empty services" {
				t.Errorf("%s: Services() = %v, %v; want nil and the services error", tc.name, s, err)
			}
		}
		if l := tc.c.Log(); l == nil {
			t.Errorf("%s: Log() = nil, want the fallback logger", tc.name)
		}
		if tc.c.Can("x") || !tc.c.Cannot("x") {
			t.Errorf("%s: Can/Cannot granted an ability with auth missing", tc.name)
		}
		err := tc.c.Authorize("x")
		var he *contract.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusForbidden || !errors.Is(err, contract.ErrServiceNotConfigured) {
			t.Errorf("%s: Authorize = %v, want a 403 caused by the missing-service error", tc.name, err)
		}
		if err := tc.c.Validate(nil); !errors.Is(err, contract.ErrServiceNotConfigured) {
			t.Errorf("%s: Validate = %v, want the missing-service error", tc.name, err)
		}
	}
	if s, err := empty.Services(); err != nil || s != empty.services {
		t.Errorf("empty services: Services() = %v, %v; want the container", s, err)
	}
}
