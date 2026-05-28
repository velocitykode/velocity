// Package auth implements authentication (Guards), authorization
// (Gate), and user lookup (UserProvider) for the Velocity framework.
//
// # Optional guard capabilities
//
// Guards MAY implement any of these to opt into framework features:
//
//	SessionAware         Session(r) returns the request-scoped Session
//	                     when the guard is session-backed. JWT/bearer
//	                     guards omit this; Manager.Session returns nil
//	                     for guards that do not implement it.
//
// # Optional Manager collaborators (set via Manager setters)
//
//	contract.CSRFTokenRotator
//	                     Aligns CSRF token lifecycle with session
//	                     regenerate/invalidate. Session guards call
//	                     RotateToken on Login and RevokeToken on Logout
//	                     when a rotator is wired.
//
//	contract.LoginThrottler
//	                     Rate-limits login attempts. The default is a
//	                     no-op (NoopLoginThrottler); install a real
//	                     throttler via Manager.SetLoginThrottler to
//	                     short-circuit credential checks under attack.
//
//	contract.RedirectAllowlist
//	                     Operator-configured allowlist of cross-origin
//	                     hosts treated as same-origin-equivalent by
//	                     redirect helpers.
//
// Capability detection is a plain type assertion at the call site.
package auth
