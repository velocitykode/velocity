package velocity

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/problem"
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

	mwStack := chain.NewMiddlewareStack(a.Services)
	mwStack.Web(trackingMiddleware("X-Web", "true"))

	routing := chain.NewRouting(a.Router, mwStack)
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

	mwStack := chain.NewMiddlewareStack(a.Services)
	mwStack.Web(trackingMiddleware("X-Web", "multi"))

	routing := chain.NewRouting(a.Router, mwStack)
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

	mwStack := chain.NewMiddlewareStack(a.Services)
	mwStack.API(trackingMiddleware("X-API", "true"))

	routing := chain.NewRouting(a.Router, mwStack)
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

	mwStack := chain.NewMiddlewareStack(a.Services)
	mwStack.API(trackingMiddleware("X-V2", "true"))

	routing := chain.NewRouting(a.Router, mwStack)
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

	mwStack := chain.NewMiddlewareStack(a.Services)
	routing := chain.NewRouting(a.Router, mwStack)
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

	mwStack := chain.NewMiddlewareStack(a.Services)
	routing := chain.NewRouting(a.Router, mwStack)

	if got := routing.Services(); got != a.Services {
		t.Fatal("Services() did not return the expected services pointer")
	}
}

func TestRouting_Router(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatal(err)
	}

	mwStack := chain.NewMiddlewareStack(a.Services)
	routing := chain.NewRouting(a.Router, mwStack)

	if got := routing.Router(); got != a.Router {
		t.Fatal("Router() did not return the expected router pointer")
	}
}

func TestRouting_EmptyMiddleware(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatal(err)
	}

	mwStack := chain.NewMiddlewareStack(a.Services)
	// No middleware added — empty web and api stacks.
	routing := chain.NewRouting(a.Router, mwStack)

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

	mwStack := chain.NewMiddlewareStack(a.Services)
	mwStack.Web(trackingMiddleware("X-Web", "yes"))

	routing := chain.NewRouting(a.Router, mwStack)

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

	mwStack := chain.NewMiddlewareStack(a.Services)
	mwStack.API(trackingMiddleware("X-API", "multi"))

	routing := chain.NewRouting(a.Router, mwStack)

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

	mwStack := chain.NewMiddlewareStack(a.Services)
	mwStack.Web(
		orderTrackingMiddleware("X-Order", "first"),
		orderTrackingMiddleware("X-Order", "second"),
		orderTrackingMiddleware("X-Order", "third"),
	)

	routing := chain.NewRouting(a.Router, mwStack)
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

// TestRouting_API_PrefixAnswersProblemJSON drives unmatched paths through a
// bootstrapped app over a real server: every request under a Routing.API
// prefix answers problem+json whatever its Accept header (HEAD with the
// same headers and no body), while a path outside every prefix still
// negotiates HTML for a browser.
func TestRouting_API_PrefixAnswersProblemJSON(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	a.Routes(func(r *chain.Routing) {
		r.API("/api", func(rt router.Router) { rt.Get("/data", okHandler) })
		r.API("/v2", func(rt router.Router) { rt.Get("/data", okHandler) })
	})
	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() error: %v", err)
	}
	if got, want := a.Services.Errors.GetAPIPrefixes(), []string{"/api", "/v2"}; !slices.Equal(got, want) {
		t.Fatalf("GetAPIPrefixes() = %q, want %q", got, want)
	}
	srv := httptest.NewServer(a.Router)
	defer srv.Close()

	tests := []struct {
		name            string
		method          string
		path            string
		accept          string
		wantContentType string
		wantEmptyBody   bool
	}{
		{name: "no accept", method: http.MethodGet, path: "/api/nope", wantContentType: problem.ProblemTypeContent},
		{name: "accept any", method: http.MethodGet, path: "/api/nope", accept: "*/*", wantContentType: problem.ProblemTypeContent},
		{name: "accept html", method: http.MethodGet, path: "/api/nope", accept: "text/html", wantContentType: problem.ProblemTypeContent},
		{name: "head", method: http.MethodHead, path: "/api/nope", accept: "text/html", wantContentType: problem.ProblemTypeContent, wantEmptyBody: true},
		{name: "second group", method: http.MethodGet, path: "/v2/nope", accept: "text/html", wantContentType: problem.ProblemTypeContent},
		{name: "outside every prefix", method: http.MethodGet, path: "/nope", accept: "text/html", wantContentType: "text/html; charset=utf-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, srv.URL+tt.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}

			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %q)", resp.StatusCode, body)
			}
			if got := resp.Header.Get("Content-Type"); got != tt.wantContentType {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantContentType)
			}
			if tt.wantEmptyBody && len(body) != 0 {
				t.Errorf("body = %q, want empty", body)
			}
			if !tt.wantEmptyBody && len(body) == 0 {
				t.Error("body is empty")
			}
		})
	}
}
