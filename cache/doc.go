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
//	Lock                Distributed-lock primitives (Lock / Unlock /
//	                    Block). Stores that cannot provide atomic
//	                    cross-process locking (file) omit this; callers
//	                    should errors.Is against ErrLockNotSupported.
//
// # Optional Manager extensions
//
//	RememberEable          Minimal surface for RememberT.
//	RememberEContextable   Ctx-aware counterpart for RememberTWithContext.
//
// Capability detection is a plain type assertion at the call site; the
// Manager performs the assertion internally for ContextStore to gate
// the Ctx-aware code path.
package cache
