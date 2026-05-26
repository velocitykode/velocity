package csrf

import "net/http"

// testCookieResolver returns a SessionIDResolver that reads the raw value
// of the named cookie. Tests opt in to this legacy "cookie-value-is-the-
// session-id" binding under their own control: production code (app.go)
// installs an encrypted-session resolver instead. Returns ErrNoSession
// when the cookie is missing or empty so the middleware refuses to mint
// or validate tokens for cookie-less requests.
func testCookieResolver(name string) func(*http.Request) (string, error) {
	return func(r *http.Request) (string, error) {
		c, err := r.Cookie(name)
		if err != nil || c.Value == "" {
			return "", ErrNoSession
		}
		return c.Value, nil
	}
}

// testConfig returns a DefaultConfig wired with testCookieResolver so the
// test exercises the same code path production runs with, just keyed by
// a raw cookie value instead of an encrypted-session decrypt.
func testConfig() *Config {
	cfg := DefaultConfig()
	cfg.SessionIDResolver = testCookieResolver("session_id")
	return cfg
}
