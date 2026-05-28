// Package auth implements authentication (Guards), authorization
// (Gate), and user lookup (UserProvider) for the Velocity framework.
//
// # Optional guard capabilities
//
// Guards MAY implement any of these to opt into framework features.
// Manager.RegisterGuard and the various Manager setters walk every
// registered guard, type-assert against each interface, and call the
// matching method on guards that implement it; guards that do not are
// skipped silently.
//
//	SessionAware                Session(r) returns the request-scoped
//	                            Session when the guard is session-backed.
//	                            JWT/bearer guards omit this;
//	                            Manager.Session returns nil for guards
//	                            that do not implement it.
//
//	ServerSessionStoreReceiver  SetServerSessionStore(store) opts the
//	                            guard into server-side session
//	                            revocation. Manager.SetServerSessionStore
//	                            propagates the store to every implementer.
//
//	TrustedProxiesReceiver      SetTrustedProxies(proxies) receives the
//	                            parsed proxy-network allowlist used for
//	                            client-IP resolution. Manager.SetTrustedProxies
//	                            propagates the list to every implementer.
//
//	CSRFTokenRotatorReceiver    SetCSRFTokenRotator(rotator) wires a
//	                            contract.CSRFTokenRotator so the guard
//	                            can rotate / revoke CSRF tokens across
//	                            Login / Logout / remember-recall.
//	                            Manager.SetCSRFTokenRotator propagates.
//
//	EventDispatcherReceiver     SetEventDispatcher(fn) wires the
//	                            framework event dispatcher into the
//	                            guard so its auth events surface
//	                            through the same channel as the rest
//	                            of the framework. Manager.SetEventDispatcher
//	                            propagates.
//
//	RememberTokenClearer        ClearRememberTokensForUser(ctx, userID)
//	                            invalidates persistent "remember me"
//	                            cookies for a user during
//	                            Manager.RevokeAllSessions. Without this
//	                            hook a revoked browser could resurrect
//	                            via its remember cookie.
//
//	RefreshTokenRevoker         RevokeAllRefreshTokensForUser(ctx, userID)
//	                            invalidates persistent refresh tokens
//	                            (bearer/JWT) for a user during
//	                            Manager.RevokeAllSessions. Without this
//	                            hook a phished refresh token survives
//	                            the administrative purge for up to
//	                            RefreshTTL (audit M-10).
//
// Guard implementations may install their own per-guard collaborators
// (e.g. SessionGuard.SetLoginThrottler) that the Manager-level walk does
// not propagate; consult the concrete guard's godoc for those.
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
//	                     throttler via the guard's SetLoginThrottler
//	                     (e.g. SessionGuard.SetLoginThrottler) to
//	                     short-circuit credential checks under attack.
//
//	contract.RedirectAllowlist
//	                     Operator-configured allowlist of cross-origin
//	                     hosts treated as same-origin-equivalent by
//	                     redirect helpers.
//
// # Lifecycle hooks
//
// Cross-cutting lifecycle hooks (contract.ShutdownAware) are defined in
// the contract package and apply uniformly to every Velocity manager
// that holds background resources; they are not duplicated in each
// package's capability table. The framework's bootstrap and shutdown
// paths type-assert services against contract.ShutdownAware to drive
// graceful teardown.
//
// Capability detection is a plain type assertion at the call site.
package auth
