package http

import (
	"net/http/httptest"
	"net/url"
	"strings"
)

// WithForm merges the given form values into the client's default form values,
// overwriting any existing keys. The values are sent as the request body by
// PostForm and PutForm.
func (c *TestClient) WithForm(values url.Values) *TestClient {
	for key, vals := range values {
		// Copy the slice so a later mutation of the caller's url.Values cannot
		// corrupt the client's stored defaults.
		c.form[key] = append([]string(nil), vals...)
	}
	return c
}

// PostForm sends a POST request with a urlencoded form body.
func (c *TestClient) PostForm(path string, values url.Values) *TestResponse {
	return c.callForm("POST", path, values)
}

// PutForm sends a PUT request with a urlencoded form body.
func (c *TestClient) PutForm(path string, values url.Values) *TestResponse {
	return c.callForm("PUT", path, values)
}

// callForm encodes values as application/x-www-form-urlencoded and performs the
// request. The client's default form values (set via WithForm) are applied
// first, then the per-call values overwrite them. The Content-Type is set after
// client headers are applied so a WithHeader("Content-Type", ...) default cannot
// override it (mirrors callJSON).
func (c *TestClient) callForm(method, path string, values url.Values) *TestResponse {
	merged := make(url.Values, len(c.form)+len(values))
	for key, vals := range c.form {
		merged[key] = vals
	}
	for key, vals := range values {
		merged[key] = vals
	}
	body := strings.NewReader(merged.Encode())
	req := httptest.NewRequest(method, path, body)
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.send(req)
}
