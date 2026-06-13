package http_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/router"
	velhttp "github.com/velocitykode/velocity/testing/http"
)

// newTestRouter creates a router with various endpoints for testing.
func newTestRouter() *router.VelocityRouterV2 {
	r := router.NewV2()

	r.Get("/hello", func(c *router.Context) error {
		return c.String(200, "Hello, World!")
	})

	r.Get("/json", func(c *router.Context) error {
		return c.JSON(200, map[string]any{
			"name": "John",
			"age":  30,
			"posts": []map[string]any{
				{"title": "First"},
				{"title": "Second"},
			},
		})
	})

	r.Get("/nested", func(c *router.Context) error {
		return c.JSON(200, map[string]any{
			"user": map[string]any{
				"name":  "Alice",
				"email": "alice@example.com",
				"address": map[string]any{
					"city": "Portland",
				},
			},
		})
	})

	r.Get("/array", func(c *router.Context) error {
		return c.JSON(200, []map[string]any{
			{"id": 1, "name": "One"},
			{"id": 2, "name": "Two"},
			{"id": 3, "name": "Three"},
		})
	})

	r.Post("/echo", func(c *router.Context) error {
		var body map[string]any
		if err := c.Bind(&body); err != nil {
			return c.JSON(400, map[string]any{"error": "invalid body"})
		}
		return c.JSON(200, map[string]any{"received": body})
	})

	r.Put("/update", func(c *router.Context) error {
		var body map[string]any
		if err := c.Bind(&body); err != nil {
			return c.JSON(400, map[string]any{"error": "invalid body"})
		}
		return c.JSON(200, map[string]any{"updated": true, "data": body})
	})

	r.Patch("/patch", func(c *router.Context) error {
		var body map[string]any
		if err := c.Bind(&body); err != nil {
			return c.JSON(400, map[string]any{"error": "invalid body"})
		}
		return c.JSON(200, map[string]any{"patched": true, "data": body})
	})

	r.Delete("/delete", func(c *router.Context) error {
		return c.NoContent()
	})

	r.Get("/redirect", func(c *router.Context) error {
		return c.Redirect(302, "/hello")
	})

	r.Get("/not-found", func(c *router.Context) error {
		return c.JSON(404, map[string]any{"error": "not found"})
	})

	r.Get("/forbidden", func(c *router.Context) error {
		return c.JSON(403, map[string]any{"error": "forbidden"})
	})

	r.Get("/unauthorized", func(c *router.Context) error {
		return c.JSON(401, map[string]any{"error": "unauthorized"})
	})

	r.Get("/unprocessable", func(c *router.Context) error {
		return c.JSON(422, map[string]any{"errors": map[string]any{"email": "required"}})
	})

	r.Post("/created", func(c *router.Context) error {
		return c.JSON(201, map[string]any{"id": 1})
	})

	r.Post("/content-type", func(c *router.Context) error {
		return c.JSON(200, map[string]any{"content-type": c.Header("Content-Type")})
	})

	r.Get("/header-echo", func(c *router.Context) error {
		return c.JSON(200, map[string]any{
			"auth":         c.Header("Authorization"),
			"x-custom":     c.Header("X-Custom"),
			"content-type": c.Header("Content-Type"),
		})
	})

	r.Get("/set-header", func(c *router.Context) error {
		c.SetHeader("X-Custom-Response", "hello")
		return c.String(200, "ok")
	})

	r.Get("/set-cookie", func(c *router.Context) error {
		c.SetCookie(&http.Cookie{
			Name:  "session",
			Value: "abc123",
			Path:  "/",
		})
		return c.String(200, "ok")
	})

	r.Get("/read-cookie", func(c *router.Context) error {
		cookie, err := c.Cookie("token")
		if err != nil {
			return c.JSON(400, map[string]any{"error": "no cookie"})
		}
		return c.JSON(200, map[string]any{"token": cookie.Value})
	})

	r.Get("/empty", func(c *router.Context) error {
		return c.NoContent()
	})

	return r
}

// ---------------------------------------------------------------------------
// Basic request tests
// ---------------------------------------------------------------------------

func TestGet(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.Get("/hello")
	resp.AssertOk().AssertBodyContains("Hello, World!")
}

func TestPost(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	body := strings.NewReader(`{"name":"test"}`)
	resp := client.Post("/echo", body)
	resp.AssertOk().AssertJSON("received", map[string]any{"name": "test"})
}

func TestPostJSON(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.PostJSON("/echo", map[string]any{"name": "velocity"})
	resp.AssertOk().AssertJSON("received", map[string]any{"name": "velocity"})
}

func TestPut(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	body := strings.NewReader(`{"status":"active"}`)
	resp := client.Put("/update", body)
	resp.AssertOk().AssertJSON("updated", true)
}

func TestPutJSON(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.PutJSON("/update", map[string]any{"status": "active"})
	resp.AssertOk().
		AssertJSON("updated", true).
		AssertJSONPath("data.status", "active")
}

func TestPatch(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	body := strings.NewReader(`{"field":"value"}`)
	resp := client.Patch("/patch", body)
	resp.AssertOk().AssertJSON("patched", true)
}

func TestPatchJSON(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.PatchJSON("/patch", map[string]any{"field": "value"})
	resp.AssertOk().
		AssertJSON("patched", true).
		AssertJSONPath("data.field", "value")
}

func TestDelete(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.Delete("/delete")
	resp.AssertNoContent()
}

// ---------------------------------------------------------------------------
// Status assertion tests
// ---------------------------------------------------------------------------

func TestAssertStatus(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/hello").AssertStatus(200)
	client.Get("/not-found").AssertStatus(404)
	client.Get("/forbidden").AssertStatus(403)
}

func TestAssertOk(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/hello").AssertOk()
}

func TestAssertCreated(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Post("/created", nil).AssertCreated()
}

func TestAssertNoContent(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Delete("/delete").AssertNoContent()
}

func TestAssertNotFound(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/not-found").AssertNotFound()
}

func TestAssertForbidden(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/forbidden").AssertForbidden()
}

func TestAssertUnauthorized(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/unauthorized").AssertUnauthorized()
}

func TestAssertUnprocessable(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/unprocessable").AssertUnprocessable()
}

func TestAssertRedirect(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/redirect").AssertRedirect()
}

func TestAssertRedirect_WithLocation(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/redirect").AssertRedirect("/hello")
}

// ---------------------------------------------------------------------------
// Header assertion tests
// ---------------------------------------------------------------------------

func TestAssertHeader(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/set-header").AssertHeader("X-Custom-Response", "hello")
}

func TestAssertHeaderMissing(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/hello").AssertHeaderMissing("X-Custom-Response")
}

// ---------------------------------------------------------------------------
// JSON assertion tests
// ---------------------------------------------------------------------------

func TestAssertJSON(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.Get("/json")
	resp.AssertOk().
		AssertJSON("name", "John").
		AssertJSON("age", 30)
}

func TestAssertJSONPath(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.Get("/nested")
	resp.AssertOk().
		AssertJSONPath("user.name", "Alice").
		AssertJSONPath("user.email", "alice@example.com").
		AssertJSONPath("user.address.city", "Portland")
}

func TestAssertJSONCount_TopLevel(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/array").AssertOk().AssertJSONCount(3)
}

func TestAssertJSONCount_Nested(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/json").AssertOk().AssertJSONCount(2, "posts")
}

func TestAssertJSONStructure(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/json").AssertOk().AssertJSONStructure([]string{"name", "age", "posts"})
}

// ---------------------------------------------------------------------------
// Body assertion tests
// ---------------------------------------------------------------------------

func TestAssertBodyContains(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/hello").AssertBodyContains("Hello")
}

func TestAssertBodyEmpty(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Delete("/delete").AssertBodyEmpty()
}

// ---------------------------------------------------------------------------
// Client configuration tests
// ---------------------------------------------------------------------------

func TestWithHeader(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.WithHeader("X-Custom", "test-value")
	resp := client.Get("/header-echo")
	resp.AssertOk().AssertJSON("x-custom", "test-value")
}

// TestPostJSON_OverridesContentTypeHeader pins the ordering subtlety in
// callJSON: a WithHeader("Content-Type", ...) default must not override the
// application/json content type that JSON requests require.
func TestPostJSON_OverridesContentTypeHeader(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter()).
		WithHeader("Content-Type", "text/plain")
	resp := client.PostJSON("/content-type", map[string]any{"k": "v"})
	resp.AssertOk().AssertJSON("content-type", "application/json")
}

func TestWithToken(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.WithToken("my-secret-token")
	resp := client.Get("/header-echo")
	resp.AssertOk().AssertJSON("auth", "Bearer my-secret-token")
}

func TestWithCookie(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.WithCookie(&http.Cookie{Name: "token", Value: "xyz789"})
	resp := client.Get("/read-cookie")
	resp.AssertOk().AssertJSON("token", "xyz789")
}

func TestWithHeader_Chaining(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter()).
		WithHeader("X-Custom", "chained").
		WithToken("chain-token")
	resp := client.Get("/header-echo")
	resp.AssertOk().
		AssertJSON("x-custom", "chained").
		AssertJSON("auth", "Bearer chain-token")
}

// ---------------------------------------------------------------------------
// Chaining tests
// ---------------------------------------------------------------------------

func TestChainedAssertions(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/json").
		AssertOk().
		AssertHeader("Content-Type", "application/json").
		AssertJSON("name", "John").
		AssertJSON("age", 30).
		AssertJSONStructure([]string{"name", "age", "posts"}).
		AssertBodyContains("John")
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestJSONWithIntComparison(t *testing.T) {
	// JSON numbers are float64; ensure int comparison works
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Post("/created", nil).AssertCreated().AssertJSON("id", 1)
}

func TestJSONWithFloat64Comparison(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	client.Get("/json").AssertJSON("age", float64(30))
}

func TestPostWithNilBody(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.Post("/created", nil)
	resp.AssertCreated()
}

func TestRawAccessors(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.Get("/hello")

	if resp.StatusCode() != 200 {
		t.Errorf("expected StatusCode() 200, got %d", resp.StatusCode())
	}
	if resp.Body() != "Hello, World!" {
		t.Errorf("expected Body() %q, got %q", "Hello, World!", resp.Body())
	}
	if resp.Header("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("unexpected Content-Type: %s", resp.Header("Content-Type"))
	}
	if resp.Recorder() == nil {
		t.Error("expected Recorder() to not be nil")
	}
}

func TestCookiesAccessor(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.Get("/set-cookie")
	cookies := resp.Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == "session" && c.Value == "abc123" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected cookie 'session' with value 'abc123'")
	}
}

func TestPostJSON_WithStruct(t *testing.T) {
	type Payload struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.PostJSON("/echo", Payload{Name: "Ali", Email: "ali@example.com"})
	resp.AssertOk().
		AssertJSONPath("received.name", "Ali").
		AssertJSONPath("received.email", "ali@example.com")
}

func TestPutJSON_WithStruct(t *testing.T) {
	type Payload struct {
		Status string `json:"status"`
	}
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.PutJSON("/update", Payload{Status: "inactive"})
	resp.AssertOk().AssertJSONPath("data.status", "inactive")
}

func TestPatchJSON_WithStruct(t *testing.T) {
	type Payload struct {
		Field string `json:"field"`
	}
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.PatchJSON("/patch", Payload{Field: "updated"})
	resp.AssertOk().AssertJSONPath("data.field", "updated")
}

// ---------------------------------------------------------------------------
// Failure case tests (verify t.Errorf is called, not panic)
// ---------------------------------------------------------------------------

// mockT records calls to Errorf for testing assertion failures.
type mockT struct {
	errors []string
}

func (m *mockT) Helper() {}
func (m *mockT) Errorf(format string, args ...interface{}) {
	m.errors = append(m.errors, fmt.Sprintf(format, args...))
}

func TestAssertOk_Failure(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/not-found").AssertOk()
	if len(mt.errors) == 0 {
		t.Error("expected AssertOk to record an error for 404 response")
	}
}

func TestAssertJSON_NonJSONBody(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/hello").AssertJSON("key", "value")
	if len(mt.errors) == 0 {
		t.Error("expected AssertJSON to record an error for non-JSON body")
	}
}

func TestAssertJSON_MissingKey(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/json").AssertJSON("nonexistent", "value")
	if len(mt.errors) == 0 {
		t.Error("expected AssertJSON to record an error for missing key")
	}
}

func TestAssertJSON_WrongValue(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/json").AssertJSON("name", "Wrong")
	if len(mt.errors) == 0 {
		t.Error("expected AssertJSON to record an error for wrong value")
	}
}

func TestAssertJSONPath_InvalidPath(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/nested").AssertJSONPath("user.nonexistent.deep", "value")
	if len(mt.errors) == 0 {
		t.Error("expected AssertJSONPath to record an error for invalid path")
	}
}

func TestAssertJSONCount_WrongCount(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/array").AssertJSONCount(99)
	if len(mt.errors) == 0 {
		t.Error("expected AssertJSONCount to record an error for wrong count")
	}
}

func TestAssertJSONStructure_MissingKey(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/json").AssertJSONStructure([]string{"name", "nonexistent"})
	if len(mt.errors) == 0 {
		t.Error("expected AssertJSONStructure to record an error for missing key")
	}
}

func TestAssertRedirect_NotRedirect(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/hello").AssertRedirect()
	if len(mt.errors) == 0 {
		t.Error("expected AssertRedirect to record an error for non-redirect response")
	}
}

func TestAssertHeader_WrongValue(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/set-header").AssertHeader("X-Custom-Response", "wrong")
	if len(mt.errors) == 0 {
		t.Error("expected AssertHeader to record an error for wrong value")
	}
}

func TestAssertHeaderMissing_Present(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/set-header").AssertHeaderMissing("X-Custom-Response")
	if len(mt.errors) == 0 {
		t.Error("expected AssertHeaderMissing to record an error when header is present")
	}
}

func TestAssertBodyContains_Missing(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/hello").AssertBodyContains("not in body")
	if len(mt.errors) == 0 {
		t.Error("expected AssertBodyContains to record an error for missing substring")
	}
}

func TestAssertBodyEmpty_NotEmpty(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/hello").AssertBodyEmpty()
	if len(mt.errors) == 0 {
		t.Error("expected AssertBodyEmpty to record an error for non-empty body")
	}
}

// ---------------------------------------------------------------------------
// Multiple requests with same client
// ---------------------------------------------------------------------------

func TestMultipleRequests(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter()).
		WithHeader("X-Custom", "persistent")

	// First request
	client.Get("/header-echo").AssertOk().AssertJSON("x-custom", "persistent")

	// Second request - headers should still be applied
	client.Get("/header-echo").AssertOk().AssertJSON("x-custom", "persistent")
}

// ---------------------------------------------------------------------------
// Post with io.Reader body
// ---------------------------------------------------------------------------

func TestPost_WithReader(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	payload := `{"message":"hello"}`
	resp := client.Post("/echo", strings.NewReader(payload))
	resp.AssertOk().AssertJSONPath("received.message", "hello")
}

// ---------------------------------------------------------------------------
// Cookie assertions
// ---------------------------------------------------------------------------

func TestAssertCookie(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.Get("/set-cookie")
	resp.AssertOk().AssertCookie("session", "abc123")
}

func TestAssertCookieMissing(t *testing.T) {
	client := velhttp.NewTestClient(t, newTestRouter())
	resp := client.Get("/hello")
	resp.AssertOk().AssertCookieMissing("session")
}

func TestAssertCookie_WrongValue(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/set-cookie").AssertCookie("session", "wrong-value")

	if len(mt.errors) == 0 {
		t.Error("expected assertion to fail for wrong cookie value")
	}
}

func TestAssertCookie_Missing(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/hello").AssertCookie("nonexistent", "value")

	if len(mt.errors) == 0 {
		t.Error("expected assertion to fail for missing cookie")
	}
}

func TestAssertCookieMissing_Present(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newTestRouter())
	client.Get("/set-cookie").AssertCookieMissing("session")

	if len(mt.errors) == 0 {
		t.Error("expected assertion to fail for present cookie")
	}
}
