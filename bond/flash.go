package bond

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
)

const (
	flashErrorsCookie = "_velocity_errors"
	flashInputCookie  = "_velocity_old"
)

// applyFlashData reads flash cookies (validation errors + old input) from the
// request, merges them into props, and clears the cookies on the response.
// Flash data overrides any existing "errors" or "old" props so that
// redirect-back-with-errors always wins.
func applyFlashData(w http.ResponseWriter, r *http.Request, props Props) {
	applied := false

	if errors, ok := readFlashCookie(r, flashErrorsCookie); ok {
		props["errors"] = errors
		applied = true
	}
	if old, ok := readFlashCookie(r, flashInputCookie); ok {
		props["old"] = old
		applied = true
	}

	if applied {
		clearFlashCookies(w)
	}
}

// readFlashCookie reads a base64+JSON-encoded flash cookie and returns the
// decoded value. Returns false if the cookie is absent or cannot be decoded.
func readFlashCookie(r *http.Request, name string) (any, bool) {
	cookie, err := r.Cookie(name)
	if err != nil || cookie.Value == "" {
		return nil, false
	}

	data, err := base64.URLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return nil, false
	}

	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, false
	}
	return result, true
}

// clearFlashCookies expires the flash cookies so they are consumed only once.
func clearFlashCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   flashErrorsCookie,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name:   flashInputCookie,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}
