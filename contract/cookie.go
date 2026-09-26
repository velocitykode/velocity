package contract

import "net/http"

// CookiePolicy is the one set of cookie attributes every cookie the
// framework writes or clears carries: the session cookie, the remember
// cookie, the XSRF token cookie, the maintenance
// bypass cookie and every deletion of them. It is derived once from the
// validated session configuration and carried on app.Services, so a
// SameSite, Domain, Path or Secure setting reaches every framework cookie
// and a deletion always matches the attributes of the write it undoes (a
// browser keeps a cookie whose deletion names a different Path or Domain).
//
// The zero value is the secure default: Path "/", no Domain, Secure,
// SameSite=Lax. A hand-built app.Services or csrf.Config that never sets
// a policy therefore still writes Secure cookies.
//
// Cookie is the only constructor of framework cookies. Context.SetCookie
// stays a raw pass-through for cookies an application builds itself.
type CookiePolicy struct {
	path     string
	domain   string
	sameSite http.SameSite
	insecure bool
}

// NewCookiePolicy returns the policy for the given attributes. An empty
// path becomes "/" and a zero or SameSiteDefaultMode sameSite becomes
// SameSite=Lax. secure=false is a dev/test opt-out the caller must
// already have validated (the session configuration rejects it outside
// dev and test profiles).
func NewCookiePolicy(path, domain string, secure bool, sameSite http.SameSite) CookiePolicy {
	return CookiePolicy{
		path:     path,
		domain:   domain,
		sameSite: sameSite,
		insecure: !secure,
	}
}

// Path is the Path attribute of every framework cookie ("/" by default).
func (p CookiePolicy) Path() string {
	if p.path == "" {
		return "/"
	}
	return p.path
}

// Domain is the Domain attribute of every framework cookie; empty means a
// host-only cookie.
func (p CookiePolicy) Domain() string { return p.domain }

// Secure reports whether framework cookies carry the Secure attribute.
func (p CookiePolicy) Secure() bool { return !p.insecure }

// SameSite is the SameSite attribute of every framework cookie
// (SameSite=Lax by default).
func (p CookiePolicy) SameSite() http.SameSite {
	// The zero value (no SameSite at all) and SameSiteDefaultMode (a bare
	// "SameSite" attribute, which browsers read differently) both mean
	// Lax here.
	if p.sameSite == 0 || p.sameSite == http.SameSiteDefaultMode {
		return http.SameSiteLaxMode
	}
	return p.sameSite
}

// Cookie builds a framework cookie carrying the policy's Path, Domain,
// Secure and SameSite. maxAge follows net/http: positive is a lifetime in
// seconds, zero omits Max-Age (a browser-session cookie), negative deletes
// the cookie. httpOnly is the one per-cookie attribute: the XSRF token
// cookie is readable by scripts by design, every other framework cookie is
// HttpOnly.
func (p CookiePolicy) Cookie(name, value string, maxAge int, httpOnly bool) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     p.Path(),
		Domain:   p.domain,
		MaxAge:   maxAge,
		HttpOnly: httpOnly,
		Secure:   p.Secure(),
		SameSite: p.SameSite(),
	}
}
