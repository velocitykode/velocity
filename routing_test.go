package velocity

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/router"
)

// trackingMiddleware returns a middleware that sets a response header.
func trackingMiddleware(header, value string) router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			c.Response.Header().Set(header, value)
			return next(c)
		}
	}
}

// orderTrackingMiddleware appends a value to a response header to verify execution order.
func orderTrackingMiddleware(header, value string) router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			existing := c.Response.Header().Get(header)
			if existing != "" {
				existing += ","
			}
			c.Response.Header().Set(header, existing+value)
			return next(c)
		}
	}
}

func okHandler(c *router.Context) error {
	c.Response.WriteHeader(http.StatusOK)
	_, err := c.Response.Write([]byte("ok"))
	return err
}

func TestRouting_Web(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatal(err)
	}

	mwStack := &MiddlewareStack{services: a.Services}
	mwStack.Web(trackingMiddleware("X-Web", "true"))

	routing := &Routing{router: a.Router, middleware: mwStack}
	routing.Web(func(r router.Router) {
		r.Get("/home", okHandler)
	})

	req := httptest.NewRequest(http.MethodGet, "/home", nil)
	rec := httptest.NewRecorder()
	a.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("X-Web"); got != "true" {
		t.Fatalf("expected X-Web header 'true', got %q", got)
	}
}

func TestRouting_Web_MultipleRoutes(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatal(err)
	}

	mwStack := &MiddlewareStack{services: a.Services}
	mwStack.Web(trackingMiddleware("X-Web", "multi"))

	routing := &Routing{router: a.Router, middleware: mwStack}
	routing.Web(func(r router.Router) {
		r.Get("/page1", okHandler)
		r.Get("/page2", okHandler)
	})

	for _, path := range []string{"/page1", "/page2"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		a.Router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, rec.Code)
		}
		if got := rec.Header().Get("X-Web"); got != "multi" {
			t.Fatalf("%s: expected X-Web header 'multi', got %q", path, got)
		}
	}
}

func TestRouting_API(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatal(err)
	}

	mwStack := &MiddlewareStack{services: a.Services}
	mwStack.API(trackingMiddleware("X-API", "true"))

	routing := &Routing{router: a.Router, middleware: mwStack}
	routing.API("/api", func(r router.Router) {
		r.Get("/users", okHandler)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	rec := httptest.NewRecorder()
	a.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("X-API"); got != "true" {
		t.Fatalf("expected X-API header 'true', got %q", got)
	}
}

func TestRouting_API_CustomPrefix(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatal(err)
	}

	mwStack := &MiddlewareStack{services: a.Services}
	mwStack.API(trackingMiddleware("X-V2", "true"))

	routing := &Routing{router: a.Router, middleware: mwStack}
	routing.API("/v2", func(r router.Router) {
		r.Get("/items", okHandler)
	})

	req := httptest.NewRequest(http.MethodGet, "/v2/items", nil)
	rec := httptest.NewRecorder()
	a.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("X-V2"); got != "true" {
		t.Fatalf("expected X-V2 header 'true', got %q", got)
	}
}

func TestRouting_Health(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatal(err)
	}

	mwStack := &MiddlewareStack{services: a.Services}
	routing := &Routing{router: a.Router, middleware: mwStack}
	routing.Health("/health")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	a.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != "OK" {
		t.Fatalf("expected body 'OK', got %q", body)
	}
}

func TestRouting_Services(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatal(err)
	}

	mwStack := &MiddlewareStack{services: a.Services}
	routing := &Routing{router: a.Router, middleware: mwStack}

	if got := routing.Services(); got != a.Services {
		t.Fatal("Services() did not return the expected services pointer")
	}
}

func TestRouting_Router(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatal(err)
	}

	mwStack := &MiddlewareStack{services: a.Services}
	routing := &Routing{router: a.Router, middleware: mwStack}

	if got := routing.Router(); got != a.Router {
		t.Fatal("Router() did not return the expected router pointer")
	}
}

func TestRouting_EmptyMiddleware(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatal(err)
	}

	mwStack := &MiddlewareStack{services: a.Services}
	// No middleware added — empty web and api stacks.
	routing := &Routing{router: a.Router, middleware: mwStack}

	routing.Web(func(r router.Router) {
		r.Get("/web-empty", okHandler)
	})
	routing.API("/api", func(r router.Router) {
		r.Get("/empty", okHandler)
	})

	for _, path := range []string{"/web-empty", "/api/empty"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		a.Router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, rec.Code)
		}
	}
}

func TestRouting_MultipleWebCalls(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatal(err)
	}

	mwStack := &MiddlewareStack{services: a.Services}
	mwStack.Web(trackingMiddleware("X-Web", "yes"))

	routing := &Routing{router: a.Router, middleware: mwStack}

	routing.Web(func(r router.Router) {
		r.Get("/first", okHandler)
	})
	routing.Web(func(r router.Router) {
		r.Get("/second", okHandler)
	})

	for _, path := range []string{"/first", "/second"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		a.Router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, rec.Code)
		}
		if got := rec.Header().Get("X-Web"); got != "yes" {
			t.Fatalf("%s: expected X-Web 'yes', got %q", path, got)
		}
	}
}

func TestRouting_MultipleAPICalls(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatal(err)
	}

	mwStack := &MiddlewareStack{services: a.Services}
	mwStack.API(trackingMiddleware("X-API", "multi"))

	routing := &Routing{router: a.Router, middleware: mwStack}

	routing.API("/v1", func(r router.Router) {
		r.Get("/data", okHandler)
	})
	routing.API("/v2", func(r router.Router) {
		r.Get("/data", okHandler)
	})

	for _, path := range []string{"/v1/data", "/v2/data"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		a.Router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, rec.Code)
		}
		if got := rec.Header().Get("X-API"); got != "multi" {
			t.Fatalf("%s: expected X-API 'multi', got %q", path, got)
		}
	}
}

func TestRouting_MiddlewareExecutionOrder(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatal(err)
	}

	mwStack := &MiddlewareStack{services: a.Services}
	mwStack.Web(
		orderTrackingMiddleware("X-Order", "first"),
		orderTrackingMiddleware("X-Order", "second"),
		orderTrackingMiddleware("X-Order", "third"),
	)

	routing := &Routing{router: a.Router, middleware: mwStack}
	routing.Web(func(r router.Router) {
		r.Get("/ordered", okHandler)
	})

	req := httptest.NewRequest(http.MethodGet, "/ordered", nil)
	rec := httptest.NewRecorder()
	a.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("X-Order"); got != "first,second,third" {
		t.Fatalf("expected middleware order 'first,second,third', got %q", got)
	}
}
