package bond

import (
	"net/http"

	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/router"
)

const (
	flashErrorsCookie = router.FlashErrorsCookie
	flashInputCookie  = router.FlashInputCookie
)

// applyFlashData reads flash cookies (validation errors + old input) from the
// request, merges them into props, and clears the cookies on the response.
// Flash data overrides any existing "errors" or "old" props so that
// redirect-back-with-errors always wins.
//
// Cookies are authenticated via the app's crypto.Encryptor (same key as
// the session cookie). A cookie with an invalid signature, wrong AAD
// binding, oversized payload, or absent encryptor is treated as if it
// were missing: no prop is set and no error reaches the client. This
// is the only safe handling for unauthenticated user-supplied state.
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

// readFlashCookie reads an authenticated flash cookie produced by
// router.Context.WithErrors / WithInput and returns the decoded value.
// Returns false when the cookie is absent, the app key is unavailable,
// the cookie exceeds router.MaxFlashCookieSize, or authentication
// fails for any reason (wrong key, tampered payload, AAD mismatch,
// rotated-out previous key). Crucially this NEVER returns
// (non-nil, false): a partial decode is reported as a clean miss so
// downstream render code cannot accidentally treat the attacker's
// partial bytes as trusted state.
func readFlashCookie(r *http.Request, name string) (any, bool) {
	cookie, err := r.Cookie(name)
	if err != nil || cookie.Value == "" {
		return nil, false
	}

	enc := flashEncryptorFor(r)
	if enc == nil {
		return nil, false
	}

	value, err := router.OpenFlash(enc, name, cookie.Value)
	if err != nil {
		return nil, false
	}
	return value, true
}

// flashEncryptorFor returns the crypto.Encryptor attached to r via the
// router pipeline, or nil when the request was not routed through
// velocity.New() (typical for unit tests that build a *Bond directly).
// Nil encryptor disables flash reads so that misconfigured environments
// degrade safely instead of trusting an unauthenticated cookie.
func flashEncryptorFor(r *http.Request) crypto.Encryptor {
	services := router.ServicesFromRequest(r)
	if services == nil {
		return nil
	}
	return services.Crypto
}

// clearFlashCookies expires the flash cookies so they are consumed only once.
func clearFlashCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashErrorsCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     flashInputCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}
