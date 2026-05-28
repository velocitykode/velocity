// Package cache provides a unified cache abstraction with pluggable
// drivers (memory, file, redis, database) configured via the
// CACHE_DRIVER environment variable.
//
// # Optional store capabilities
//
// Stores MAY implement any of these to opt into framework features:
//
//	ContextStore        Context-aware variants of every core Store method
//	                    (GetCtx, PutCtx, ForgetCtx, ...). The Manager
//	                    threads request context through to the underlying
//	                    backend when the active store implements this.
//	                    Stores that do not implement it fall back to the
//	                    non-context methods with ctx-cancellation lost at
//	                    the boundary.
//
//	drivers.Locker      Lock(key, ttl...) / RestoreLock(key, owner)
//	                    distributed-lock primitives. Manager.Lock type-
//	                    asserts the active store to drivers.Locker and
//	                    returns nil when the store does not support
//	                    locking; callers can also errors.Is against
//	                    ErrLockNotSupported when a driver explicitly
//	                    refuses (rather than silently returning nil).
//
// # Optional Manager extensions
//
//	RememberEable          Minimal surface for RememberT.
//	RememberEContextable   Ctx-aware counterpart for RememberTWithContext.
//
// # Lifecycle hooks
//
// Cross-cutting lifecycle hooks (contract.ShutdownAware) are defined in
// the contract package and apply uniformly to every Velocity manager
// that holds background resources; they are not duplicated in each
// package's capability table. Manager.Shutdown type-asserts every
// registered store against contract.ShutdownAware and threads the
// caller's context through to honour the deadline.
//
// Cache-specific: a store MAY implement a parameterless Start() method
// to run one-off post-construction setup (e.g. background eviction
// goroutines). The Manager invokes Start() on first resolution of a
// store via Manager.StoreWithContext.
//
// Capability detection is a plain type assertion at the call site; the
// Manager performs the assertion internally for ContextStore to gate
// the Ctx-aware code path.
package cache
