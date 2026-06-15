package http_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/velocitykode/velocity/router"
	velhttp "github.com/velocitykode/velocity/testing/http"
)

// newCoreRouter builds a router with form, redirect-chain, and array endpoints
// used by the form/redirect/array-index tests.
func newCoreRouter() *router.VelocityRouterV2 {
	r := router.NewV2()

	// Echo submitted form fields back as JSON, plus the received Content-Type.
	formEcho := func(c *router.Context) error {
		return c.JSON(200, map[string]any{
			"name":         c.FormValue("name"),
			"email":        c.FormValue("email"),
			"role":         c.FormValue("role"),
			"content-type": c.Header("Content-Type"),
		})
	}
	r.Post("/form", formEcho)
	r.Put("/form", formEcho)

	// Redirect chain: /hop1 -> /hop2 -> /done (200).
	r.Get("/hop1", func(c *router.Context) error { return c.Redirect(302, "/hop2") })
	r.Get("/hop2", func(c *router.Context) error { return c.Redirect(302, "/done") })
	r.Get("/done", func(c *router.Context) error { return c.String(200, "arrived") })

	// Infinite self-redirect to exercise the hop cap.
	r.Get("/loop", func(c *router.Context) error { return c.Redirect(302, "/loop") })

	// /login sets a session cookie then redirects to /me, which echoes it back.
	r.Get("/login", func(c *router.Context) error {
		c.SetCookie(&http.Cookie{Name: "session", Value: "carried", Path: "/"})
		return c.Redirect(302, "/me")
	})
	r.Get("/me", func(c *router.Context) error {
		cookie, err := c.Cookie("session")
		if err != nil {
			return c.JSON(200, map[string]any{"session": ""})
		}
		return c.JSON(200, map[string]any{"session": cookie.Value})
	})

	// Multi-hop chain: /login2 sets a session cookie and redirects to /relay,
	// which redirects again to /me without touching the cookie. The cookie set
	// at the first hop must still reach /me at the third hop.
	r.Get("/login2", func(c *router.Context) error {
		c.SetCookie(&http.Cookie{Name: "session", Value: "carried", Path: "/"})
		return c.Redirect(302, "/relay")
	})
	r.Get("/relay", func(c *router.Context) error { return c.Redirect(302, "/me") })

	// Array payloads for array-index path assertions.
	r.Get("/items", func(c *router.Context) error {
		return c.JSON(200, map[string]any{
			"items": []map[string]any{
				{"name": "alpha"},
				{"name": "beta"},
			},
		})
	})

	// Object keyed by numeric strings: a numeric path segment must resolve as a
	// map key here, NOT be treated as an array index.
	r.Get("/numkeys", func(c *router.Context) error {
		return c.JSON(200, map[string]any{
			"counts": map[string]any{"0": "zero", "42": "answer"},
		})
	})
	return r
}

// ---------------------------------------------------------------------------
// Form helpers
// ---------------------------------------------------------------------------

func TestFormHelpers(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		send   func(c *velhttp.TestClient, path string, v url.Values) *velhttp.TestResponse
	}{
		{
			name:   "PostForm",
			method: "POST",
			path:   "/form",
			send:   func(c *velhttp.TestClient, p string, v url.Values) *velhttp.TestResponse { return c.PostForm(p, v) },
		},
		{
			name:   "PutForm",
			method: "PUT",
			path:   "/form",
			send:   func(c *velhttp.TestClient, p string, v url.Values) *velhttp.TestResponse { return c.PutForm(p, v) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := velhttp.NewTestClient(t, newCoreRouter())
			values := url.Values{}
			values.Set("name", "Ada")
			values.Set("email", "ada@example.com")

			resp := tt.send(client, tt.path, values)
			resp.AssertOk().
				AssertJSON("content-type", "application/x-www-form-urlencoded").
				AssertJSON("name", "Ada").
				AssertJSON("email", "ada@example.com")
		})
	}
}

// TestPostForm_OverridesContentTypeHeader pins that a WithHeader default cannot
// override the form Content-Type (mirrors the JSON ordering subtlety).
func TestPostForm_OverridesContentTypeHeader(t *testing.T) {
	client := velhttp.NewTestClient(t, newCoreRouter()).
		WithHeader("Content-Type", "text/plain")
	resp := client.PostForm("/form", url.Values{"name": {"x"}})
	resp.AssertOk().AssertJSON("content-type", "application/x-www-form-urlencoded")
}

// TestWithForm_Defaults checks that WithForm values are sent and per-call values
// overwrite the defaults.
func TestWithForm_Defaults(t *testing.T) {
	client := velhttp.NewTestClient(t, newCoreRouter()).
		WithForm(url.Values{"role": {"admin"}, "name": {"default"}})

	resp := client.PostForm("/form", url.Values{"name": {"override"}})
	resp.AssertOk().
		AssertJSON("role", "admin").   // default carried through
		AssertJSON("name", "override") // per-call value wins
}

// ---------------------------------------------------------------------------
// Redirect following
// ---------------------------------------------------------------------------

func TestFollowingRedirects_Chain(t *testing.T) {
	client := velhttp.NewTestClient(t, newCoreRouter()).FollowingRedirects()
	resp := client.Get("/hop1")
	resp.AssertOk().AssertBodyContains("arrived")
}

func TestFollowingRedirects_Disabled(t *testing.T) {
	client := velhttp.NewTestClient(t, newCoreRouter())
	resp := client.Get("/hop1")
	resp.AssertRedirect("/hop2")
}

func TestFollowingRedirects_Cap(t *testing.T) {
	// An infinite loop must stop at the 10-hop cap and return the final 3xx.
	client := velhttp.NewTestClient(t, newCoreRouter()).FollowingRedirects()
	resp := client.Get("/loop")
	resp.AssertRedirect("/loop")
}

func TestFollowingRedirects_CookieCarry(t *testing.T) {
	client := velhttp.NewTestClient(t, newCoreRouter()).FollowingRedirects()
	resp := client.Get("/login")
	resp.AssertOk().AssertJSON("session", "carried")
}

func TestFollowingRedirects_CookieCarryMultiHop(t *testing.T) {
	// Cookie set at the first hop must survive an intermediate redirect that
	// does not reset it and reach the final hop that reads it.
	client := velhttp.NewTestClient(t, newCoreRouter()).FollowingRedirects()
	resp := client.Get("/login2")
	resp.AssertOk().AssertJSON("session", "carried")
}

// ---------------------------------------------------------------------------
// Array-index JSON paths
// ---------------------------------------------------------------------------

func TestAssertJSONPath_ArrayIndex(t *testing.T) {
	client := velhttp.NewTestClient(t, newCoreRouter())
	client.Get("/items").AssertOk().
		AssertJSONPath("items.0.name", "alpha").
		AssertJSONPath("items.1.name", "beta")
}

func TestAssertJSONPath_ArrayIndexOutOfRange(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newCoreRouter())
	client.Get("/items").AssertJSONPath("items.5.name", "x")
	if len(mt.errors) == 0 {
		t.Error("expected out-of-range array index to record an error")
	}
}

func TestAssertJSONPath_IndexIntoNonArray(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newCoreRouter())
	// "items" is an array; indexing its element's "name" string with [0] fails.
	client.Get("/items").AssertJSONPath("items.0.name.0", "x")
	if len(mt.errors) == 0 {
		t.Error("expected indexing into non-array to record an error")
	}
}

// Regression: a numeric path segment against a map keyed by numeric strings must
// resolve as a key lookup, not be misread as an array index.
func TestAssertJSONPath_NumericMapKey(t *testing.T) {
	mt := &mockT{}
	client := velhttp.NewTestClient(mt, newCoreRouter())
	client.Get("/numkeys").
		AssertJSONPath("counts.0", "zero").
		AssertJSONPath("counts.42", "answer")
	if len(mt.errors) != 0 {
		t.Errorf("numeric map keys must resolve, got errors: %v", mt.errors)
	}
}
