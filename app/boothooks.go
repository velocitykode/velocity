package app

import "sync"

// BootHook runs once per velocity.New() call, after all core services are
// constructed, instance event dispatchers are wired, and the module
// lifecycle has completed. The hook receives the live Services and may
// attach event listeners, register components (app.Register), or read
// service handles.
//
// Boot hooks are the seam for zero-config instrumentation: a third-party
// SDK (APM, tracing, metrics) registers a hook from its package init(), so
// a consumer enables it with a blank import plus environment variables and
// no code changes. A hook that finds itself unconfigured must return nil
// and stay dormant, not error.
//
// Hooks registered here run for EVERY app constructed in the process, in
// registration order. A returned error aborts New() with that error.
type BootHook func(s *Services) error

var (
	bootHooksMu sync.Mutex
	bootHooks   []BootHook
)

// OnBoot registers a boot hook. Safe for concurrent use; intended to be
// called from package init() in blank-imported instrumentation packages.
func OnBoot(fn BootHook) {
	if fn == nil {
		return
	}
	bootHooksMu.Lock()
	defer bootHooksMu.Unlock()
	bootHooks = append(bootHooks, fn)
}

// RunBootHooks runs every registered hook against s in registration order,
// stopping at the first error. Called by velocity.New(); not intended for
// consumer use.
func RunBootHooks(s *Services) error {
	bootHooksMu.Lock()
	hooks := make([]BootHook, len(bootHooks))
	copy(hooks, bootHooks)
	bootHooksMu.Unlock()

	for _, fn := range hooks {
		if err := fn(s); err != nil {
			return err
		}
	}
	return nil
}

// ResetBootHooks removes all registered boot hooks. Test helper only:
// package-level hook registration persists across tests in the same
// process, so tests that register hooks must clean up after themselves.
func ResetBootHooks() {
	bootHooksMu.Lock()
	defer bootHooksMu.Unlock()
	bootHooks = nil
}
