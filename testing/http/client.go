package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
)

// TestClient sends requests through a router for integration testing.
type TestClient struct {
	t       TestingT
	router  http.Handler
	headers map[string]string
	cookies []*http.Cookie
}

// NewTestClient creates a test client that sends requests through the given router.
func NewTestClient(t TestingT, router http.Handler) *TestClient {
	return &TestClient{
		t:       t,
		router:  router,
		headers: make(map[string]string),
		cookies: make([]*http.Cookie, 0),
	}
}

// WithHeader sets a default header for all requests.
func (c *TestClient) WithHeader(key, value string) *TestClient {
	c.headers[key] = value
	return c
}

// WithCookie sets a cookie for all requests.
func (c *TestClient) WithCookie(cookie *http.Cookie) *TestClient {
	c.cookies = append(c.cookies, cookie)
	return c
}

// WithToken sets a Bearer token header.
func (c *TestClient) WithToken(token string) *TestClient {
	c.headers["Authorization"] = "Bearer " + token
	return c
}

// Get sends a GET request to the given path.
func (c *TestClient) Get(path string) *TestResponse {
	return c.call(http.MethodGet, path, nil)
}

// Post sends a POST request with an optional body.
func (c *TestClient) Post(path string, body io.Reader) *TestResponse {
	return c.call(http.MethodPost, path, body)
}

// PostJSON sends a POST request with a JSON-encoded body.
func (c *TestClient) PostJSON(path string, data any) *TestResponse {
	return c.callJSON(http.MethodPost, path, data)
}

// Put sends a PUT request with an optional body.
func (c *TestClient) Put(path string, body io.Reader) *TestResponse {
	return c.call(http.MethodPut, path, body)
}

// PutJSON sends a PUT request with a JSON-encoded body.
func (c *TestClient) PutJSON(path string, data any) *TestResponse {
	return c.callJSON(http.MethodPut, path, data)
}

// Patch sends a PATCH request with an optional body.
func (c *TestClient) Patch(path string, body io.Reader) *TestResponse {
	return c.call(http.MethodPatch, path, body)
}

// PatchJSON sends a PATCH request with a JSON-encoded body.
func (c *TestClient) PatchJSON(path string, data any) *TestResponse {
	return c.callJSON(http.MethodPatch, path, data)
}

// Delete sends a DELETE request to the given path.
func (c *TestClient) Delete(path string) *TestResponse {
	return c.call(http.MethodDelete, path, nil)
}

// callJSON marshals data to JSON, sets the Content-Type header, and performs the request.
// Content-Type is set after client headers are applied so that PostJSON always sends
// application/json even if the client has a default Content-Type via WithHeader.
func (c *TestClient) callJSON(method, path string, data any) *TestResponse {
	body, err := json.Marshal(data)
	if err != nil {
		c.t.Helper()
		c.t.Errorf("TestClient: failed to marshal JSON body: %v", err)
		return &TestResponse{t: c.t, recorder: httptest.NewRecorder()}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	// Apply client headers first, then force Content-Type for JSON so a
	// WithHeader("Content-Type", ...) default cannot override it.
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.send(req)
}

// call builds the request and sends it through the router.
func (c *TestClient) call(method, path string, body io.Reader) *TestResponse {
	req := httptest.NewRequest(method, path, body)
	return c.doRequest(req)
}

// doRequest applies the client's default headers and executes the request.
func (c *TestClient) doRequest(req *http.Request) *TestResponse {
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}
	return c.send(req)
}

// send applies cookies, runs the request through the router, and wraps the
// recorder. Header application is left to the caller because callJSON must
// force its Content-Type after the client's default headers (see callJSON).
func (c *TestClient) send(req *http.Request) *TestResponse {
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}

	rec := httptest.NewRecorder()
	c.router.ServeHTTP(rec, req)

	return &TestResponse{
		t:        c.t,
		recorder: rec,
	}
}

// TestResponse wraps an httptest.ResponseRecorder with assertion methods.
type TestResponse struct {
	t        TestingT
	recorder *httptest.ResponseRecorder
}

// StatusCode returns the HTTP status code of the response.
func (r *TestResponse) StatusCode() int {
	return r.recorder.Code
}

// Body returns the response body as a string.
func (r *TestResponse) Body() string {
	return r.recorder.Body.String()
}

// Header returns the value of the given response header.
func (r *TestResponse) Header(key string) string {
	return r.recorder.Header().Get(key)
}

// Cookies returns the response cookies.
func (r *TestResponse) Cookies() []*http.Cookie {
	return r.recorder.Result().Cookies()
}

// Recorder returns the underlying httptest.ResponseRecorder.
func (r *TestResponse) Recorder() *httptest.ResponseRecorder {
	return r.recorder
}

// ---------------------------------------------------------------------------
// Status assertions
// ---------------------------------------------------------------------------

// AssertStatus asserts the response has the given status code.
func (r *TestResponse) AssertStatus(status int) *TestResponse {
	r.t.Helper()
	if r.recorder.Code != status {
		r.t.Errorf("expected status %d, got %d", status, r.recorder.Code)
	}
	return r
}

// AssertOk asserts the response has status 200.
func (r *TestResponse) AssertOk() *TestResponse {
	r.t.Helper()
	return r.AssertStatus(http.StatusOK)
}

// AssertCreated asserts the response has status 201.
func (r *TestResponse) AssertCreated() *TestResponse {
	r.t.Helper()
	return r.AssertStatus(http.StatusCreated)
}

// AssertNoContent asserts the response has status 204.
func (r *TestResponse) AssertNoContent() *TestResponse {
	r.t.Helper()
	return r.AssertStatus(http.StatusNoContent)
}

// AssertNotFound asserts the response has status 404.
func (r *TestResponse) AssertNotFound() *TestResponse {
	r.t.Helper()
	return r.AssertStatus(http.StatusNotFound)
}

// AssertForbidden asserts the response has status 403.
func (r *TestResponse) AssertForbidden() *TestResponse {
	r.t.Helper()
	return r.AssertStatus(http.StatusForbidden)
}

// AssertUnauthorized asserts the response has status 401.
func (r *TestResponse) AssertUnauthorized() *TestResponse {
	r.t.Helper()
	return r.AssertStatus(http.StatusUnauthorized)
}

// AssertUnprocessable asserts the response has status 422.
func (r *TestResponse) AssertUnprocessable() *TestResponse {
	r.t.Helper()
	return r.AssertStatus(http.StatusUnprocessableEntity)
}

// AssertRedirect asserts the response has a 3xx status code. If location is
// provided, also asserts the Location header matches.
func (r *TestResponse) AssertRedirect(location ...string) *TestResponse {
	r.t.Helper()
	code := r.recorder.Code
	if code < 300 || code >= 400 {
		r.t.Errorf("expected redirect status (3xx), got %d", code)
	}
	if len(location) > 0 {
		loc := r.recorder.Header().Get("Location")
		if loc != location[0] {
			r.t.Errorf("expected redirect location %q, got %q", location[0], loc)
		}
	}
	return r
}

// ---------------------------------------------------------------------------
// Header assertions
// ---------------------------------------------------------------------------

// AssertHeader asserts the response contains the given header with the expected value.
func (r *TestResponse) AssertHeader(key, value string) *TestResponse {
	r.t.Helper()
	actual := r.recorder.Header().Get(key)
	if actual != value {
		r.t.Errorf("expected header %q to be %q, got %q", key, value, actual)
	}
	return r
}

// AssertHeaderMissing asserts the response does not contain the given header.
func (r *TestResponse) AssertHeaderMissing(key string) *TestResponse {
	r.t.Helper()
	if val := r.recorder.Header().Get(key); val != "" {
		r.t.Errorf("expected header %q to be missing, but got %q", key, val)
	}
	return r
}

// ---------------------------------------------------------------------------
// Cookie assertions
// ---------------------------------------------------------------------------

// AssertCookie asserts the response contains a cookie with the given name and value.
func (r *TestResponse) AssertCookie(name, value string) *TestResponse {
	r.t.Helper()
	for _, c := range r.recorder.Result().Cookies() {
		if c.Name == name {
			if c.Value != value {
				r.t.Errorf("expected cookie %q to have value %q, got %q", name, value, c.Value)
			}
			return r
		}
	}
	r.t.Errorf("expected cookie %q to exist in response", name)
	return r
}

// AssertCookieMissing asserts the response does not contain a cookie with the given name.
func (r *TestResponse) AssertCookieMissing(name string) *TestResponse {
	r.t.Helper()
	for _, c := range r.recorder.Result().Cookies() {
		if c.Name == name {
			r.t.Errorf("expected cookie %q to be missing, but found value %q", name, c.Value)
			return r
		}
	}
	return r
}

// ---------------------------------------------------------------------------
// JSON assertions
// ---------------------------------------------------------------------------

// AssertJSON asserts that the top-level JSON key equals the expected value.
func (r *TestResponse) AssertJSON(key string, expected any) *TestResponse {
	r.t.Helper()
	decoded := r.decodeJSON()
	if decoded == nil {
		return r
	}
	actual, ok := decoded[key]
	if !ok {
		r.t.Errorf("JSON key %q not found in response", key)
		return r
	}
	if !jsonEqual(expected, actual) {
		r.t.Errorf("JSON key %q: expected %v (%T), got %v (%T)", key, expected, expected, actual, actual)
	}
	return r
}

// AssertJSONPath asserts a value at a dot-notation path (e.g. "user.name").
func (r *TestResponse) AssertJSONPath(path string, expected any) *TestResponse {
	r.t.Helper()
	decoded := r.decodeJSON()
	if decoded == nil {
		return r
	}
	parts := strings.Split(path, ".")
	var current any = decoded
	for _, part := range parts {
		switch v := current.(type) {
		case map[string]any:
			val, ok := v[part]
			if !ok {
				r.t.Errorf("JSON path %q: key %q not found", path, part)
				return r
			}
			current = val
		default:
			r.t.Errorf("JSON path %q: cannot traverse into %T at segment %q", path, current, part)
			return r
		}
	}
	if !jsonEqual(expected, current) {
		r.t.Errorf("JSON path %q: expected %v (%T), got %v (%T)", path, expected, expected, current, current)
	}
	return r
}

// AssertJSONCount asserts the number of items in a JSON array. If key is
// provided, looks up that key first; otherwise the top-level body must be an array.
func (r *TestResponse) AssertJSONCount(count int, key ...string) *TestResponse {
	r.t.Helper()
	var target any

	if len(key) > 0 {
		decoded := r.decodeJSON()
		if decoded == nil {
			return r
		}
		val, ok := decoded[key[0]]
		if !ok {
			r.t.Errorf("JSON key %q not found for count assertion", key[0])
			return r
		}
		target = val
	} else {
		arr := r.decodeJSONArray()
		if arr == nil {
			return r
		}
		target = arr
	}

	arr, ok := target.([]any)
	if !ok {
		r.t.Errorf("expected JSON array for count assertion, got %T", target)
		return r
	}
	if len(arr) != count {
		r.t.Errorf("expected JSON array count %d, got %d", count, len(arr))
	}
	return r
}

// AssertJSONStructure asserts that the given keys exist in the top-level JSON object.
func (r *TestResponse) AssertJSONStructure(keys []string) *TestResponse {
	r.t.Helper()
	decoded := r.decodeJSON()
	if decoded == nil {
		return r
	}
	for _, key := range keys {
		if _, ok := decoded[key]; !ok {
			r.t.Errorf("expected JSON key %q to exist in response", key)
		}
	}
	return r
}

// ---------------------------------------------------------------------------
// Body assertions
// ---------------------------------------------------------------------------

// AssertBodyContains asserts the response body contains the given substring.
func (r *TestResponse) AssertBodyContains(substring string) *TestResponse {
	r.t.Helper()
	body := r.recorder.Body.String()
	if !strings.Contains(body, substring) {
		r.t.Errorf("expected body to contain %q, got %q", substring, body)
	}
	return r
}

// AssertBodyEmpty asserts the response body is empty.
func (r *TestResponse) AssertBodyEmpty() *TestResponse {
	r.t.Helper()
	body := r.recorder.Body.String()
	if body != "" {
		r.t.Errorf("expected empty body, got %q", body)
	}
	return r
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// decodeJSON parses the response body as a JSON object.
func (r *TestResponse) decodeJSON() map[string]any {
	var result map[string]any
	if err := json.Unmarshal(r.recorder.Body.Bytes(), &result); err != nil {
		r.t.Helper()
		r.t.Errorf("failed to decode response body as JSON object: %v\nbody: %s", err, r.recorder.Body.String())
		return nil
	}
	return result
}

// decodeJSONArray parses the response body as a JSON array.
func (r *TestResponse) decodeJSONArray() []any {
	var result []any
	if err := json.Unmarshal(r.recorder.Body.Bytes(), &result); err != nil {
		r.t.Helper()
		r.t.Errorf("failed to decode response body as JSON array: %v\nbody: %s", err, r.recorder.Body.String())
		return nil
	}
	return result
}

// jsonEqual compares expected and actual values, handling JSON number coercion.
// JSON numbers are always float64, so if expected is an integer type, we compare
// as float64.
func jsonEqual(expected, actual any) bool {
	// Handle nil
	if expected == nil && actual == nil {
		return true
	}
	if expected == nil || actual == nil {
		return false
	}

	// Handle integer-to-float64 coercion (JSON unmarshals numbers as float64)
	switch e := expected.(type) {
	case int:
		if af, ok := actual.(float64); ok {
			return af == float64(e)
		}
	case int8:
		if af, ok := actual.(float64); ok {
			return af == float64(e)
		}
	case int16:
		if af, ok := actual.(float64); ok {
			return af == float64(e)
		}
	case int32:
		if af, ok := actual.(float64); ok {
			return af == float64(e)
		}
	case int64:
		if af, ok := actual.(float64); ok {
			return af == float64(e)
		}
	case uint:
		if af, ok := actual.(float64); ok {
			return af == float64(e)
		}
	case uint8:
		if af, ok := actual.(float64); ok {
			return af == float64(e)
		}
	case uint16:
		if af, ok := actual.(float64); ok {
			return af == float64(e)
		}
	case uint32:
		if af, ok := actual.(float64); ok {
			return af == float64(e)
		}
	case uint64:
		if af, ok := actual.(float64); ok {
			return af == float64(e)
		}
	case float32:
		if af, ok := actual.(float64); ok {
			return af == float64(e)
		}
	}

	// Handle string comparison
	if es, ok := expected.(string); ok {
		if as, ok := actual.(string); ok {
			return es == as
		}
		return false
	}

	// Handle bool comparison
	if eb, ok := expected.(bool); ok {
		if ab, ok := actual.(bool); ok {
			return eb == ab
		}
		return false
	}

	// Fallback to reflect.DeepEqual
	return reflect.DeepEqual(expected, actual)
}
