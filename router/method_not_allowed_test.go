package router

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// probeMethods is every method the tests send. It includes methods no
// test route registers, so "not allowed" is exercised as well as "allowed".
var probeMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

func okHandler(c *Context) error { return c.String(http.StatusOK, "ok") }

func serve(r *VelocityRouterV2, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestMethodNotAllowed(t *testing.T) {
	tests := []struct {
		name      string
		register  func(r *VelocityRouterV2)
		method    string
		target    string
		wantCode  int
		wantAllow string
	}{
		{
			name:      "known path under a method it has no route for",
			register:  func(r *VelocityRouterV2) { r.Post("/mcp", okHandler) },
			method:    "GET",
			target:    "/mcp",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "POST",
		},
		{
			name:      "delete on a post-only path",
			register:  func(r *VelocityRouterV2) { r.Post("/mcp", okHandler) },
			method:    "DELETE",
			target:    "/mcp",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "POST",
		},
		{
			name:     "unknown path stays not found",
			register: func(r *VelocityRouterV2) { r.Post("/mcp", okHandler) },
			method:   "GET",
			target:   "/nope",
			wantCode: http.StatusNotFound,
		},
		{
			name:     "a longer path than a known one is not found",
			register: func(r *VelocityRouterV2) { r.Post("/mcp", okHandler) },
			method:   "GET",
			target:   "/mcp/extra",
			wantCode: http.StatusNotFound,
		},
		{
			name:     "a prefix of a known path is not found",
			register: func(r *VelocityRouterV2) { r.Post("/mcp/messages", okHandler) },
			method:   "GET",
			target:   "/mcp",
			wantCode: http.StatusNotFound,
		},
		{
			name: "every registered method is listed, sorted",
			register: func(r *VelocityRouterV2) {
				r.Post("/users", okHandler)
				r.Get("/users", okHandler)
				r.Delete("/users", okHandler)
			},
			method:    "PATCH",
			target:    "/users",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "DELETE, GET, POST",
		},
		{
			name: "param route",
			register: func(r *VelocityRouterV2) {
				r.Put("/posts/{id}", okHandler)
				r.Delete("/posts/{id}", okHandler)
			},
			method:    "GET",
			target:    "/posts/5",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "DELETE, PUT",
		},
		{
			name: "methods served by different nodes for one path",
			register: func(r *VelocityRouterV2) {
				r.Get("/users/new", okHandler)
				r.Post("/users/{id}", okHandler)
			},
			method:    "DELETE",
			target:    "/users/new",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "GET, POST",
		},
		{
			name:      "regex route whose constraint the path meets",
			register:  func(r *VelocityRouterV2) { r.Get("/items/{id:[0-9]+}", okHandler) },
			method:    "POST",
			target:    "/items/12",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "GET",
		},
		{
			name:     "regex route whose constraint the path fails",
			register: func(r *VelocityRouterV2) { r.Get("/items/{id:[0-9]+}", okHandler) },
			method:   "POST",
			target:   "/items/abc",
			wantCode: http.StatusNotFound,
		},
		{
			name:      "wildcard route",
			register:  func(r *VelocityRouterV2) { r.Get("/files/{path:.*}", okHandler) },
			method:    "POST",
			target:    "/files/a/b/c",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "GET",
		},
		{
			name:      "wildcard route matching nothing after its prefix",
			register:  func(r *VelocityRouterV2) { r.Get("/files/{path:.*}", okHandler) },
			method:    "POST",
			target:    "/files",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "GET",
		},
		{
			name:      "root path",
			register:  func(r *VelocityRouterV2) { r.Get("/", okHandler) },
			method:    "POST",
			target:    "/",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "GET",
		},
		{
			name: "route inside a group",
			register: func(r *VelocityRouterV2) {
				r.Group("/api", func(g Router) { g.Post("/mcp", okHandler) })
			},
			method:    "GET",
			target:    "/api/mcp",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "POST",
		},
		{
			name:      "slashes are normalized as they are for matching",
			register:  func(r *VelocityRouterV2) { r.Post("/users", okHandler) },
			method:    "GET",
			target:    "//users/",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "POST",
		},
		{
			name:      "an encoded slash stays inside its segment",
			register:  func(r *VelocityRouterV2) { r.Post("/posts/{id}", okHandler) },
			method:    "GET",
			target:    "/posts/a%2Fb",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "POST",
		},
		{
			name:      "the query string plays no part",
			register:  func(r *VelocityRouterV2) { r.Post("/mcp", okHandler) },
			method:    "GET",
			target:    "/mcp?x=/nope",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "POST",
		},
		{
			name:      "head is not implied by get",
			register:  func(r *VelocityRouterV2) { r.Get("/page", okHandler) },
			method:    "HEAD",
			target:    "/page",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "GET",
		},
		{
			name:      "options without an options route",
			register:  func(r *VelocityRouterV2) { r.Post("/mcp", okHandler) },
			method:    "OPTIONS",
			target:    "/mcp",
			wantCode:  http.StatusMethodNotAllowed,
			wantAllow: "POST",
		},
		{
			name:     "a route for any method is never refused",
			register: func(r *VelocityRouterV2) { r.Any("/hook", okHandler) },
			method:   "PATCH",
			target:   "/hook",
			wantCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewV2()
			tt.register(r)

			w := serve(r, tt.method, tt.target)

			if w.Code != tt.wantCode {
				t.Fatalf("%s %s: status = %d, want %d", tt.method, tt.target, w.Code, tt.wantCode)
			}
			if got := w.Header().Values("Allow"); tt.wantAllow == "" {
				if len(got) != 0 {
					t.Errorf("%s %s: Allow = %q on a %d, want none", tt.method, tt.target, got, w.Code)
				}
			} else if len(got) != 1 || got[0] != tt.wantAllow {
				t.Errorf("%s %s: Allow = %q, want exactly [%q]", tt.method, tt.target, got, tt.wantAllow)
			}
		})
	}
}

// The Allow header is a promise: every method it names must be served,
// and every method it leaves out must be refused. Checked against the
// router itself rather than an expected list, over trees where methods
// for one path live on different nodes.
func TestMethodNotAllowed_AllowAgreesWithRouting(t *testing.T) {
	r := NewV2()
	r.Get("/users/new", okHandler)
	r.Post("/users/{id}", okHandler)
	r.Put("/users/{id:[0-9]+}", okHandler)
	r.Delete("/users/{rest:.*}", okHandler)
	r.Patch("/users/{id}/posts", okHandler)
	r.Get("/", okHandler)

	for _, target := range []string{"/", "/users", "/users/new", "/users/7", "/users/7/posts", "/users/a/b/c", "/other"} {
		codes := make(map[string]int, len(probeMethods))
		for _, m := range probeMethods {
			codes[m] = serve(r, m, target).Code
		}

		var served []string
		for _, m := range probeMethods {
			if codes[m] == http.StatusOK {
				served = append(served, m)
			}
		}
		slices.Sort(served)

		for _, m := range probeMethods {
			if codes[m] == http.StatusOK {
				continue
			}
			w := serve(r, m, target)
			if len(served) == 0 {
				if w.Code != http.StatusNotFound || w.Header().Get("Allow") != "" {
					t.Errorf("%s %s: no method is served, got %d Allow=%q, want 404 without Allow", m, target, w.Code, w.Header().Get("Allow"))
				}
				continue
			}
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: status = %d, want 405 (served: %v)", m, target, w.Code, served)
			}
			if got, want := w.Header().Get("Allow"), strings.Join(served, ", "); got != want {
				t.Errorf("%s %s: Allow = %q, want %q", m, target, got, want)
			}
		}
	}
}

func TestMethodNotAllowed_HandlerIsNotRun(t *testing.T) {
	r := NewV2()
	called := false
	r.Post("/mcp", func(c *Context) error {
		called = true
		return nil
	})

	serve(r, "GET", "/mcp")

	if called {
		t.Error("the POST handler ran for a GET request")
	}
}

func TestMethodNotAllowed_BodyCarriesNoRouteDetail(t *testing.T) {
	r := NewV2()
	r.Post("/secret/{token}", okHandler)

	w := serve(r, "GET", "/secret/abc")

	if got := strings.TrimSpace(w.Body.String()); got != "Method Not Allowed" {
		t.Errorf("body = %q, want the bare status text", got)
	}
}

// Global middleware guards unmatched requests too (rate limits, security
// headers). A 405 must run the chain, and run it once.
func TestMethodNotAllowed_RunsGlobalMiddlewareOnce(t *testing.T) {
	r := NewV2()
	runs := 0
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			runs++
			c.Response.Header().Set("X-Guard", "on")
			return next(c)
		}
	})
	r.Post("/mcp", okHandler)

	w := serve(r, "GET", "/mcp")

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
	if runs != 1 {
		t.Errorf("global middleware ran %d times, want 1", runs)
	}
	if w.Header().Get("X-Guard") != "on" {
		t.Error("global middleware header missing from the 405")
	}
}

// A middleware that answers the request itself (a CORS preflight, a rate
// limiter) still wins over the 405.
func TestMethodNotAllowed_MiddlewareMayAnswerFirst(t *testing.T) {
	r := NewV2()
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			if c.Request.Method == http.MethodOptions {
				c.Response.WriteHeader(http.StatusNoContent)
				return nil
			}
			return next(c)
		}
	})
	r.Post("/mcp", okHandler)

	if w := serve(r, "OPTIONS", "/mcp"); w.Code != http.StatusNoContent {
		t.Errorf("OPTIONS status = %d, want the middleware's 204", w.Code)
	}
	if w := serve(r, "GET", "/mcp"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", w.Code)
	}
}

// A middleware may run the rest of the chain on a Context of its own for
// the same request. Timeout does (it clones the pooled Context for the
// handler goroutine). The 405 must survive that: what the terminal
// handler needs travels with the request, not with one Context value.
func TestMethodNotAllowed_SurvivesAMiddlewareThatSwapsTheContext(t *testing.T) {
	tests := []struct {
		name string
		mw   MiddlewareFunc
	}{
		{name: "Timeout", mw: Timeout(time.Second)},
		{
			name: "fresh Context for the same request",
			mw: func(next HandlerFunc) HandlerFunc {
				return func(c *Context) error { return next(NewContext(c.Response, c.Request)) }
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewV2()
			r.Use(tt.mw)
			r.Post("/mcp", okHandler)

			w := serve(r, "GET", "/mcp")
			if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
				t.Errorf("GET /mcp = %d Allow=%q, want 405 Allow=POST", w.Code, w.Header().Get("Allow"))
			}
			if w := serve(r, "GET", "/nope"); w.Code != http.StatusNotFound || w.Header().Get("Allow") != "" {
				t.Errorf("GET /nope = %d Allow=%q, want 404 without Allow", w.Code, w.Header().Get("Allow"))
			}
			if w := serve(r, "POST", "/mcp"); w.Code != http.StatusOK {
				t.Errorf("POST /mcp = %d, want 200", w.Code)
			}
		})
	}
}

// A request can pass through more than one router: a middleware or a
// handler of one hands it to another. Each router answers for its own
// routes only; what an outer router knew about the path is not the inner
// router's business, in either direction.
func TestMethodNotAllowed_IsDecidedByTheRouterThatAnswers(t *testing.T) {
	delegateTo := func(inner *VelocityRouterV2) MiddlewareFunc {
		return func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				inner.ServeHTTP(c.Response, c.Request)
				return nil
			}
		}
	}

	t.Run("outer knows the path, inner does not", func(t *testing.T) {
		inner := NewV2()
		inner.Get("/other", okHandler)
		outer := NewV2()
		outer.Use(delegateTo(inner))
		outer.Post("/mcp", okHandler)

		w := serve(outer, "GET", "/mcp")
		if w.Code != http.StatusNotFound || w.Header().Get("Allow") != "" {
			t.Errorf("GET /mcp = %d Allow=%q, want the inner router's 404 without Allow", w.Code, w.Header().Get("Allow"))
		}
	})

	t.Run("inner knows the path under other methods than outer", func(t *testing.T) {
		inner := NewV2()
		inner.Put("/mcp", okHandler)
		outer := NewV2()
		outer.Use(delegateTo(inner))
		outer.Post("/mcp", okHandler)

		w := serve(outer, "GET", "/mcp")
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "PUT" {
			t.Errorf("GET /mcp = %d Allow=%q, want the inner router's 405 Allow=PUT", w.Code, w.Header().Get("Allow"))
		}
	})

	t.Run("outer does not know the path, inner does", func(t *testing.T) {
		inner := NewV2()
		inner.Post("/mcp", okHandler)
		outer := NewV2()
		outer.Use(delegateTo(inner))

		w := serve(outer, "GET", "/mcp")
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
			t.Errorf("GET /mcp = %d Allow=%q, want the inner router's 405 Allow=POST", w.Code, w.Header().Get("Allow"))
		}
	})

	t.Run("a route of the outer router mounts the inner router", func(t *testing.T) {
		inner := NewV2()
		inner.Post("/api/mcp", okHandler)
		outer := NewV2()
		outer.Any("/api/{rest:.*}", func(c *Context) error {
			inner.ServeHTTP(c.Response, c.Request)
			return nil
		})

		if w := serve(outer, "GET", "/api/mcp"); w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
			t.Errorf("GET /api/mcp = %d Allow=%q, want 405 Allow=POST", w.Code, w.Header().Get("Allow"))
		}
		if w := serve(outer, "GET", "/api/nope"); w.Code != http.StatusNotFound || w.Header().Get("Allow") != "" {
			t.Errorf("GET /api/nope = %d Allow=%q, want 404 without Allow", w.Code, w.Header().Get("Allow"))
		}
	})

	t.Run("two routers serving in turn do not see each other", func(t *testing.T) {
		a := NewV2()
		a.Post("/mcp", okHandler)
		b := NewV2()
		b.Get("/other", okHandler)

		for i := 0; i < 20; i++ {
			if w := serve(a, "GET", "/mcp"); w.Code != http.StatusMethodNotAllowed {
				t.Fatalf("round %d: router a GET /mcp = %d, want 405", i, w.Code)
			}
			if w := serve(b, "GET", "/mcp"); w.Code != http.StatusNotFound || w.Header().Get("Allow") != "" {
				t.Fatalf("round %d: router b GET /mcp = %d Allow=%q, want 404 without Allow", i, w.Code, w.Header().Get("Allow"))
			}
		}
	})
}

// The answer is for the request as the terminal handler receives it. A
// middleware that rewrites the method or the path after matching must
// never produce a 405 whose Allow names the very method being refused.
func TestMethodNotAllowed_NeverListsTheRefusedMethod(t *testing.T) {
	rewrite := func(fn func(*http.Request)) MiddlewareFunc {
		return func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				fn(c.Request)
				return next(c)
			}
		}
	}

	t.Run("method rewritten to one the path serves", func(t *testing.T) {
		r := NewV2()
		r.Use(rewrite(func(req *http.Request) { req.Method = "POST" }))
		r.Post("/mcp", okHandler)

		w := serve(r, "GET", "/mcp")
		if w.Code != http.StatusNotFound || w.Header().Get("Allow") != "" {
			t.Errorf("got %d Allow=%q, want 404 without Allow", w.Code, w.Header().Get("Allow"))
		}
	})

	t.Run("path rewritten to one served for any method", func(t *testing.T) {
		r := NewV2()
		r.Use(rewrite(func(req *http.Request) { req.URL.Path = "/hook" }))
		r.Any("/hook", okHandler)

		w := serve(r, "GET", "/nope")
		if w.Code != http.StatusNotFound || w.Header().Get("Allow") != "" {
			t.Errorf("got %d Allow=%q, want 404 without Allow", w.Code, w.Header().Get("Allow"))
		}
	})

	t.Run("path rewritten to one that serves other methods", func(t *testing.T) {
		r := NewV2()
		r.Use(rewrite(func(req *http.Request) { req.URL.Path = "/mcp" }))
		r.Post("/mcp", okHandler)

		w := serve(r, "GET", "/nope")
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
			t.Errorf("got %d Allow=%q, want 405 Allow=POST for the request as rewritten", w.Code, w.Header().Get("Allow"))
		}
	})
}

func TestMethodNotAllowed_Events(t *testing.T) {
	collector := newTestEventCollector()
	r := NewV2()
	r.SetEventDispatcher(collector.dispatch)
	r.Post("/mcp", okHandler)

	serve(r, "GET", "/mcp")

	var routed *RequestRouted
	var handled *RequestHandled
	for _, e := range collector.getEvents() {
		switch ev := e.(type) {
		case *RequestRouted:
			routed = ev
		case *RequestHandled:
			handled = ev
		}
	}
	if routed == nil || routed.Matched {
		t.Errorf("RequestRouted = %+v, want one with Matched=false: no route handled the request", routed)
	}
	if handled == nil || handled.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("RequestHandled = %+v, want one with status 405", handled)
	}
}

func TestMethodNotAllowed_AfterClearRoutes(t *testing.T) {
	r := NewV2()
	r.Post("/old", okHandler)
	serve(r, "GET", "/old")

	r.ClearRoutes()
	r.Put("/new", okHandler)

	if w := serve(r, "GET", "/old"); w.Code != http.StatusNotFound {
		t.Errorf("GET /old after clear = %d, want 404", w.Code)
	}
	w := serve(r, "GET", "/new")
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "PUT" {
		t.Errorf("GET /new = %d Allow=%q, want 405 Allow=PUT", w.Code, w.Header().Get("Allow"))
	}
}

func TestWriteUnmatched(t *testing.T) {
	t.Run("no allowed methods is a 404 without Allow", func(t *testing.T) {
		w := httptest.NewRecorder()
		writeUnmatched(w, httptest.NewRequest("GET", "/x", nil), nil)
		if w.Code != http.StatusNotFound || len(w.Header().Values("Allow")) != 0 {
			t.Errorf("got %d Allow=%q", w.Code, w.Header().Values("Allow"))
		}
	})
	t.Run("allowed methods is a 405 naming them", func(t *testing.T) {
		w := httptest.NewRecorder()
		writeUnmatched(w, httptest.NewRequest("GET", "/x", nil), []string{"POST", "PUT"})
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST, PUT" {
			t.Errorf("got %d Allow=%q", w.Code, w.Header().Get("Allow"))
		}
	})
}

// AllowedMethods must name exactly the methods Match finds a route for.
func TestTree_AllowedMethodsAgreesWithMatch(t *testing.T) {
	tree := NewTree()
	routes := []struct{ method, path string }{
		{"GET", "/"},
		{"GET", "/users/new"},
		{"POST", "/users/{id}"},
		{"PUT", "/users/{id:[0-9]+}"},
		{"DELETE", "/users/{rest:.*}"},
		{"PATCH", "/users/{id}/posts"},
		{"ANY", "/hook"},
		{"GET", "/a/{x}/c"},
		{"POST", "/a/b/{y}"},
	}
	for _, rt := range routes {
		if err := tree.Insert(rt.method, rt.path, dummyHandler); err != nil {
			t.Fatalf("insert %s %s: %v", rt.method, rt.path, err)
		}
	}
	methods := append([]string{"ANY"}, probeMethods...)

	paths := []string{
		"/", "", "/users", "/users/", "/users/new", "/users/7", "/users/x", "/users/7/posts",
		"/users/a/b/c", "//users//7", "/hook", "/hook/x", "/a/b/c", "/a/z/c", "/a/b/z", "/a/b",
		"/users/a%2Fb", "/missing", "/missing/deeper",
	}
	for _, p := range paths {
		var want []string
		for _, m := range methods {
			if tree.Match(m, p) != nil {
				want = append(want, m)
			}
		}
		slices.Sort(want)

		got := tree.AllowedMethods(p)
		if !slices.Equal(got, want) {
			t.Errorf("AllowedMethods(%q) = %v, want %v (what Match serves)", p, got, want)
		}
	}
}

func TestTree_AllowedMethodsIsSortedEveryTime(t *testing.T) {
	tree := NewTree()
	for _, m := range []string{"PUT", "GET", "DELETE", "POST", "PATCH"} {
		tree.Insert(m, "/r", dummyHandler)
	}
	for i := 0; i < 100; i++ {
		if got := tree.AllowedMethods("/r"); !slices.IsSorted(got) || len(got) != 5 {
			t.Fatalf("run %d: %v is not the five methods in order", i, got)
		}
	}
}
