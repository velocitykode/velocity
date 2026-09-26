package bond

import (
	"net/http"

	"github.com/velocitykode/velocity/contract"
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
	// Clear whenever the request CARRIED either flash cookie, not only
	// when a read succeeded: a tampered, oversized, or undecryptable
	// cookie must still be expired, or the client replays the garbage
	// value on every subsequent request.
	carried := hasCookie(r, flashErrorsCookie) || hasCookie(r, flashInputCookie)

	if errors, ok := readFlashCookie(r, flashErrorsCookie); ok {
		props["errors"] = errors
	}
	if old, ok := readFlashCookie(r, flashInputCookie); ok {
		props["old"] = old
	}

	if carried {
		clearFlashCookies(w, r)
	}
}

// hasCookie reports whether the request carried the named cookie,
// regardless of whether its value decodes.
func hasCookie(r *http.Request, name string) bool {
	_, err := r.Cookie(name)
	return err == nil
}

// readFlashCookie reads an authenticated flash cookie produced by
// router.Context.FlashErrors / FlashInput and returns the decoded value.
// The errors cookie opens through router.OpenFlashErrors, which also
// unwraps an error bag envelope into the page's errors prop.
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

	var value any
	if name == flashErrorsCookie {
		value, err = router.OpenFlashErrors(enc, cookie.Value)
	} else {
		value, err = router.OpenFlash(enc, name, cookie.Value)
	}
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

// clearFlashCookies expires the flash cookies so they are consumed only
// once. The deletions are built by the app's cookie policy, the same one
// the router's write path uses, so Path, Domain, Secure and SameSite match
// the write and the browser drops the cookies. Without routed services the
// secure zero-value policy applies, matching the write path's default.
func clearFlashCookies(w http.ResponseWriter, r *http.Request) {
	var policy contract.CookiePolicy
	if services := router.ServicesFromRequest(r); services != nil {
		policy = services.CookiePolicy
	}
	http.SetCookie(w, policy.Cookie(flashErrorsCookie, "", -1, true))
	http.SetCookie(w, policy.Cookie(flashInputCookie, "", -1, true))
}
